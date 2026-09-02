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
// it — which is this one. When it fires, the connection is closed, and closing
// it is what ends the query the server is still running.
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
)

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
}

// DefaultLimits are the figures section 4.3 and section 5 name.
//
// Concurrent is a placeholder for "two to three times the cores", which the
// composition root sets from the machine it is on; the rest are the
// architecture's own numbers.
func DefaultLimits() Limits {
	return Limits{
		Deadline:   5 * time.Second,
		MaxRows:    1000,
		MaxBytes:   5 << 20,
		Concurrent: 8,
		QueueDepth: 16,
		PerMinute:  30,
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

// Runner executes participants' queries.
type Runner struct {
	cluster *Cluster
	checker *sqlpolicy.Checker
	limits  Limits
	gate    *gate
	rate    *window
}

// New assembles a runner.
func New(cluster *Cluster, checker *sqlpolicy.Checker, limits Limits) *Runner {
	return &Runner{
		cluster: cluster,
		checker: checker,
		limits:  limits,
		gate:    newGate(limits.Concurrent, limits.QueueDepth),
		rate:    newWindow(limits.PerMinute, time.Minute, nil),
	}
}

// Run checks the query, admits it, and executes it.
//
// The order is the order of increasing cost. Checking is free and needs no
// connection, so a refused query never occupies a slot or reaches the
// database. Admission comes next, because a query that will not run should not
// have a connection opened for it. The database is last.
func (r *Runner) Run(ctx context.Context, req Request) (*Result, error) {
	statement, err := r.checker.Analyse(req.SQL, req.Policy)
	if err != nil {
		return nil, err
	}

	// The rate comes before the semaphore, as section 5 has it: it is about
	// who is asking rather than about what is running, and answering it costs
	// nothing.
	participant := req.Registration.String()
	if err := r.rate.admit(participant); err != nil {
		return nil, err
	}

	release, err := r.gate.enter(ctx, participant)
	if err != nil {
		return nil, err
	}
	defer release()

	return r.execute(ctx, req, statement)
}

// execute opens a connection, runs the query under the deadline, and closes it.
func (r *Runner) execute(ctx context.Context, req Request, statement sqlpolicy.Statement) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.limits.Deadline)
	defer cancel()

	conn, err := r.cluster.connect(ctx, req.Database)
	if err != nil {
		return nil, timeoutOr(ctx, err)
	}
	// Closed with a context of its own: the one above may already be the
	// expired one, and a close that is skipped because the deadline passed is
	// a connection left to the server to notice.
	defer func() {
		closing, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		_ = conn.Close(closing)
	}()

	// A transaction per query, read-only for a read-only contest. It is the
	// database's own answer to "this changes nothing", and it makes a stray
	// write fail at the first statement rather than half-way through.
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: accessMode(req.Policy)})
	if err != nil {
		return nil, timeoutOr(ctx, err)
	}
	// Always rolled back: a read has nothing to commit, and a write that got
	// this far under a read-only policy has nothing that should be kept.
	defer func() {
		ending, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		_ = tx.Rollback(ending)
	}()

	if statement.Writes && req.DiskQuotaBytes > 0 {
		if err := withinQuota(ctx, tx, req.DiskQuotaBytes); err != nil {
			return nil, err
		}
	}

	text := req.SQL
	if !statement.Explain {
		text = limited(req.SQL, r.limits.MaxRows)
	}

	// EXPLAIN is not wrapped and so arrives without the extra LIMIT; collect
	// stops at MaxRows either way, which is all a plan needs.
	return collectOr(ctx, tx, text, r.limits)
}

// collectOr reads the result and names what interrupted it, if anything.
func collectOr(ctx context.Context, tx pgx.Tx, text string, limits Limits) (*Result, error) {
	result, err := collect(ctx, tx, text, limits)
	if err != nil {
		return nil, timeoutOr(ctx, err)
	}
	return result, nil
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
