// Package queryrunner executes one participant's SQL against their own game
// database, under a deadline and under admission control.
//
// It is the only thing that ever connects to a participant's database. What it
// answers is narrow: given a query that a policy has already been consulted
// about, run it without letting it cost more than its share.
//
// # Why the deadline lives here
//
// The database layer bounds a great deal (see internal/gamedb) but not time.
// `statement_timeout` is a USERSET parameter: SQL that reached the server
// unchecked turns it off in one statement. The bound that holds regardless is
// a deadline on the connection's own context, held by the process that opened
// it — which is this one. When it fires, the server is sent a cancel for the
// query it is still running, and the connection is closed rather than kept for
// another query.
//
// # What it deliberately does not do
//
// It does not decide what SQL is allowed — that is internal/sqlpolicy, which
// it calls. It does not know which database belongs to which participant; the
// name arrives in the request, having come from game_instances rather than
// from the client. And it does not write the journal, so that a failure to
// record cannot become a failure to answer.
package queryrunner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrTimeout is a query that ran longer than it was allowed to. ErrCanceled is
// a caller that stopped waiting for one.
//
// Separate, because they are separate events and only one of them is about
// load. A participant who navigates away cancels the request, and counting
// that as a timeout inflates the number capacity decisions are made from — the
// two look alike only because both arrive as a cancelled context.
var (
	ErrTimeout  = errors.New("the query took too long")
	ErrCanceled = errors.New("the caller stopped waiting")
	// ErrDiskFull is a write refused because the participant's database has
	// grown past what the contest allows it.
	ErrDiskFull = errors.New("the database is at its size limit")
	// ErrResultTooLarge is an answer that could not be read within the result
	// budget at all: one row on its own outweighed the whole allowance, so
	// there is no prefix of it to show. A truncated result is an answer; this
	// is the absence of one.
	ErrResultTooLarge = errors.New("the result is too large to read")
	// errBadDatabase is a database name that is not a plain identifier. The
	// name is the caller's and is already checked upstream; this is the
	// runner declining to build a connection string out of anything else.
	errBadDatabase = errors.New("the database name is not a plain identifier")
)

// readSlack is what a connection may read beyond the result budget: the
// handshake, the row description, the framing around every value, and the
// difference between a value's size on the wire and its size in memory. A
// mebibyte covers all of that for any legitimate answer, while keeping one
// oversized cell from costing this process more than a few times the budget.
const readSlack = 1 << 20

// Outcomes lists every ending this package reports for itself, as opposed to
// passing on from the database.
//
// Enumerable because the layer above has to tell the two apart — a contest
// that hides its schema withholds the database's words and must not withhold
// ours — and a list kept by hand up there would fall behind a sentinel added
// here. This is the list that can be complete; the shapes a driver can produce
// are not.
func Outcomes() []error {
	return []error{
		ErrTimeout, ErrCanceled, ErrBusy, ErrAlreadyRunning,
		ErrTooManyQueries, ErrDiskFull, ErrResultTooLarge,
	}
}

// DatabaseError is PostgreSQL speaking for itself about this query: a
// relation that does not exist, a type error, a privilege the participant's
// role does not have.
//
// A type of its own rather than a plain error, because "these words came from
// the database, about the query that was asked" is a fact every layer above
// has to act on and none of them can infer. They are the one words in this
// system that are safe to repeat to a participant — "relation \"guests\" does
// not exist" is the most useful sentence there is — and a failure of ours
// wearing the same shape is a connection string handed to whoever asked.
//
// The counterpart of Outcomes(): that names what is ours, this names what is
// the database's, and everything else is ours by default rather than the
// database's by default. The default is the direction that matters, because
// it is what an error nobody anticipated falls into.
type DatabaseError struct {
	// Message is the database's own text, and nothing this process wrote
	// around it.
	Message string
}

func (e *DatabaseError) Error() string { return e.Message }

// Limits bound one execution and the instance as a whole.
type Limits struct {
	// Deadline bounds one query, and is the bound that holds against SQL the
	// validator did not stop.
	Deadline time.Duration
	// MaxRows and MaxBytes bound the answer. Both are needed: a thousand rows
	// is a small answer unless each one is a megabyte.
	MaxRows  int
	MaxBytes int
	// Concurrent is how many queries may run at once on this instance, and
	// QueueDepth how many may wait. Past both, a request is refused rather
	// than queued indefinitely.
	Concurrent int
	QueueDepth int
	// PerMinute bounds how often one participant may ask, which the semaphore
	// cannot: a thousand cheap queries in a minute pass it one at a time.
	// Zero means no limit.
	PerMinute int
	// IdleTimeout is how long the connection a read finished on is kept for
	// that database's next read (see pool). Zero keeps none: every query
	// opens and closes its own connection.
	IdleTimeout time.Duration
}

// DefaultLimits are the figures section 4.3 and section 5 name.
//
// Concurrent is a placeholder for "two to three times the cores", which the
// composition root sets from the machine it is on. QueueDepth is sized so
// that Concurrent + QueueDepth covers the forty participants the olympiad
// expects: each has at most one query in flight, so a queue that deep turns
// a round's simultaneous Run into a wait rather than a refusal (see
// config.Runner.QueueDepth). The rest are the architecture's own numbers.
func DefaultLimits() Limits {
	return Limits{
		Deadline:   5 * time.Second,
		MaxRows:    1000,
		MaxBytes:   5 << 20,
		Concurrent: 8,
		QueueDepth: 32,
		PerMinute:  30,
		// Long enough to span a participant reading one answer and editing
		// the next query; short enough that a participant who has stopped
		// holds nothing for long, and that the reclaim sweep's plain DROP
		// DATABASE — which refuses a database anything is connected to —
		// meets a finished contest's databases already released.
		IdleTimeout: 30 * time.Second,
	}
}

// Request is one participant asking one question.
type Request struct {
	// Registration identifies who is asking, for the one-at-a-time rule. A
	// registration rather than an account: the same person in two contests is
	// two participants, and the query log is keyed the same way.
	Registration uuid.UUID
	// Database is the participant's own game database, taken from
	// game_instances by the caller. It never comes from the client.
	Database string
	SQL      string
	Policy   sqlpolicy.Policy
	// DiskQuotaBytes is how large this participant's database may grow. Zero
	// means unbounded, which is the right answer for a contest that permits no
	// writing at all. Checked before a write and never before a read.
	DiskQuotaBytes int64
}

// Validator decides whether a query is allowed, and says what it is.
//
// An interface rather than the checker itself, and the reason is a build
// rather than a taste: the checker links PostgreSQL's parser through cgo, and
// anything importing it inherits that. The Core API journals queries and holds
// a client of this service, so it reaches this package — and would have had to
// be compiled with cgo, on a base image carrying a libc, for a parser it never
// runs. The concrete checker is wired in by the command that serves this
// service and by nothing else.
type Validator interface {
	Analyse(sql string, p sqlpolicy.Policy) (sqlpolicy.Statement, error)
}

// Runner executes participants' queries.
type Runner struct {
	conns   *pool
	checker Validator
	limits  Limits
	gate    *gate
	rate    *window
}

// New assembles a runner.
func New(cluster *Cluster, checker Validator, limits Limits) *Runner {
	return &Runner{
		// The pool's bound is the gate's: see pool for why every connection
		// the runner holds, idle ones included, counts against QUERY_CONCURRENT.
		conns:   newPool(cluster.connect, limits.Concurrent, limits.IdleTimeout),
		checker: checker,
		limits:  limits,
		gate:    newGate(limits.Concurrent, limits.QueueDepth),
		rate:    newWindow(limits.PerMinute, time.Minute, nil),
	}
}

// Close closes the connections the runner is keeping. Queries still running
// finish, and their connections are closed rather than kept.
func (r *Runner) Close() { r.conns.close() }

// Run checks the query, admits it, and executes it.
//
// The order is the order of increasing cost, and the rate comes first. It is
// about who is asking rather than about what was asked, it costs a map lookup,
// and it is the only bound on how often the parser is exercised: the parser is
// C code reading text an adversary chose, and a participant who could put
// sixty-four kilobytes through it as fast as the network allowed — refused or
// not — would have a way to spend this process's CPU that no other limit sees.
// A query refused by the checker therefore counts against the rate, which is
// also what the journal records it as: a query that was asked.
//
// Checking needs no connection, so a refused query never occupies a slot or
// reaches the database. Admission comes next, because a query that will not
// run should not have a connection opened for it. The database is last.
func (r *Runner) Run(ctx context.Context, req Request) (*Result, error) {
	participant := req.Registration.String()
	if err := r.rate.admit(participant); err != nil {
		return nil, err
	}

	// The name is the caller's, taken from game_instances and checked there;
	// this is the runner refusing to build a connection string out of anything
	// but a plain identifier, whichever caller it has.
	if !sqlpolicy.PlainIdentifier(req.Database) {
		return nil, errBadDatabase
	}

	statement, err := r.checker.Analyse(req.SQL, req.Policy)
	if err != nil {
		return nil, err
	}

	release, err := r.gate.enter(ctx, participant)
	if err != nil {
		return nil, err
	}
	defer release()

	return r.execute(ctx, req, statement)
}

// execute runs the query under the deadline on a connection from the pool,
// and hands the connection back.
func (r *Runner) execute(ctx context.Context, req Request, statement sqlpolicy.Statement) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.limits.Deadline)
	defer cancel()

	sess, tx, err := r.begin(ctx, req)
	if err != nil {
		return nil, timeoutOr(ctx, err)
	}
	// Rolled back unless a write below commits: a read has nothing to commit,
	// and a write that failed half-way has nothing that should be kept.
	// Rollback after a commit is a no-op, so this is safe unconditionally.
	// Then the connection goes back, kept only if everything here finished
	// cleanly: clean is set on the one path that returns an answer, and a
	// context that ended at any point — its watcher may have sent a cancel —
	// is not clean, however the query itself came out.
	clean := false
	defer func() {
		ending, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		rolledBack := tx.Rollback(ending)
		r.conns.release(ending, sess, clean && ctx.Err() == nil &&
			(rolledBack == nil || errors.Is(rolledBack, pgx.ErrTxClosed)))
	}()

	if statement.Writes && req.DiskQuotaBytes > 0 {
		if err := withinQuota(ctx, tx, req.DiskQuotaBytes); err != nil {
			return nil, err
		}
	}

	// A read is wrapped so the server stops early. A write is not: an INSERT
	// cannot sit inside a FROM any more than an EXPLAIN can, and what bounds
	// its answer is that RETURNING is read through the same limits below.
	text := statement.Text
	if !statement.Explain && !statement.Writes {
		text = limited(statement.Text, r.limits.MaxRows)
	}

	result, err := collect(ctx, tx, text, r.limits, statement.Writes)
	if err != nil {
		if sess.meter.exhausted() {
			// The connection stopped reading, and the driver reports that as
			// the connection failing. It did not: this process declined to
			// hold more of the answer than one is allowed to cost.
			return nil, ErrResultTooLarge
		}
		return nil, timeoutOr(ctx, err)
	}

	// The whole point of a permitted write is that it stays written. Only
	// now, with the statement complete and its answer read within the limits,
	// does the change become the participant's database.
	if statement.Writes {
		if err := tx.Commit(ctx); err != nil {
			return nil, timeoutOr(ctx, err)
		}
	}
	clean = true
	return result, nil
}

// begin takes a connection and opens the query's transaction on it.
//
// As the writer only when the policy permits writing, so that a read-only
// contest is read-only by privilege and not only by the transaction's access
// mode, which a query cannot change but a bug here could.
//
// A transaction per query, read-only for a read-only contest. It is the
// database's own answer to "this changes nothing", and it makes a stray write
// fail at the first statement rather than half-way through.
//
// A kept connection can have been severed while it sat idle: the database
// dropped WITH (FORCE) and perhaps created again under the same name, or the
// server restarted. BEGIN is the first thing to find out, and nothing has run
// yet, so the connection is closed and the query goes on a new one — which
// reaches whatever database carries the name now, never the old one, since no
// connection outlives the database it was opened to.
func (r *Runner) begin(ctx context.Context, req Request) (*session, pgx.Tx, error) {
	writes := req.Policy.Mode == sqlpolicy.ModeReadWrite
	budget := int64(r.limits.MaxBytes) + readSlack
	options := pgx.TxOptions{AccessMode: accessMode(req.Policy)}

	sess, err := r.conns.acquire(ctx, req.Database, writes, budget)
	if err != nil {
		return nil, nil, err
	}
	tx, err := sess.conn.BeginTx(ctx, options)
	if err == nil {
		return sess, tx, nil
	}
	r.conns.release(ctx, sess, false)
	if !sess.reused || ctx.Err() != nil {
		return nil, nil, err
	}

	if sess, err = r.conns.acquire(ctx, req.Database, writes, budget); err != nil {
		return nil, nil, err
	}
	if tx, err = sess.conn.BeginTx(ctx, options); err != nil {
		r.conns.release(ctx, sess, false)
		return nil, nil, err
	}
	return sess, tx, nil
}

// withinQuota refuses a write to a database that has grown past its allowance.
//
// Checked synchronously, before the statement, because afterwards is too late:
// a participant filling a shared disk takes the contest down with them. It
// cannot be exact — one INSERT … SELECT can overshoot between the check and
// the end of the statement — but the overshoot is bounded by what a statement
// can write in the runner's deadline, which is the trade section 4.1 makes
// explicitly.
func withinQuota(ctx context.Context, tx pgx.Tx, allowed int64) error {
	var used int64
	if err := tx.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&used); err != nil {
		return fmt.Errorf("read the database size: %w", err)
	}
	if used >= allowed {
		return fmt.Errorf("%w: %d bytes of %d", ErrDiskFull, used, allowed)
	}
	return nil
}

func accessMode(p sqlpolicy.Policy) pgx.TxAccessMode {
	if p.Mode == sqlpolicy.ModeReadWrite {
		return pgx.ReadWrite
	}
	return pgx.ReadOnly
}

// timeoutOr names what ended the query, rather than reporting whatever the
// driver happened to notice first.
//
// An interrupted query surfaces differently depending on where it was at the
// time — a context error, a closed connection, a server-side cancellation —
// and collapsing those is the point. What must not be collapsed is the
// difference between the deadline firing and the caller leaving: the first is
// about load and the second is not.
func timeoutOr(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("%w: %w", ErrTimeout, err)
	case errors.Is(ctx.Err(), context.Canceled):
		return fmt.Errorf("%w: %w", ErrCanceled, err)
	default:
		return err
	}
}
