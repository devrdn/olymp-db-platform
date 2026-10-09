// Package queryrunner executes one participant's SQL against their own game
// database, under a deadline, admission control and a result budget; it is the
// only thing that connects to one. It does not decide what SQL is allowed
// (sqlpolicy), choose the database (the caller, from game_instances) or write
// the journal (Journalled).
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

// ErrTimeout is a query that ran past its deadline. ErrCanceled is a caller
// that stopped waiting. They are kept apart because only a timeout says
// anything about load.
var (
	ErrTimeout  = errors.New("the query took too long")
	ErrCanceled = errors.New("the caller stopped waiting")
	// ErrDiskFull is a growing write refused because the participant's
	// database is at its quota. Statements that only free space still run.
	ErrDiskFull = errors.New("the database is at its size limit")
	// ErrResultTooLarge is an answer that could not be read within the read
	// budget at all, so there is no prefix to show. A truncated result is
	// different: it is an answer.
	ErrResultTooLarge = errors.New("the result is too large to read")
	errBadDatabase    = errors.New("the database name is not a plain identifier")
)

// readSlack is what a connection may read beyond the result budget, for the
// handshake, row description, wire framing and encoding differences.
const readSlack = 1 << 20

// Outcomes lists every ending this package reports itself, as opposed to
// errors passed on from the database. The layer above must tell the two apart:
// a contest that hides its schema withholds the database's words, not ours.
func Outcomes() []error {
	return []error{
		ErrTimeout, ErrCanceled, ErrBusy, ErrAlreadyRunning,
		ErrTooManyQueries, ErrDiskFull, ErrResultTooLarge,
	}
}

// DatabaseError is PostgreSQL's own message about the participant's query
// (missing relation, type error, denied privilege). Only these words are safe
// to show a participant. Any error not marked this way is treated as ours, so
// an unanticipated failure never leaks internals such as a connection string.
type DatabaseError struct {
	// Message is the database's text, with nothing added by this process.
	Message string
}

func (e *DatabaseError) Error() string { return e.Message }

// Limits bound one execution and the instance as a whole.
type Limits struct {
	// Deadline bounds one query, and holds even against SQL the validator
	// did not stop. It lives here because statement_timeout is USERSET and SQL
	// can turn it off; a deadline on the connection's context cannot be. When
	// it fires the server is sent a cancel and the connection is closed, not
	// kept.
	Deadline time.Duration
	// MaxRows and MaxBytes bound the answer; rows alone do not bound size.
	MaxRows  int
	MaxBytes int
	// Concurrent queries may run and QueueDepth may wait; past both a
	// request is refused rather than queued indefinitely.
	Concurrent int
	QueueDepth int
	// PerMinute bounds how often one participant may ask. Zero means no limit.
	PerMinute int
	// IdleTimeout is how long a finished read's connection is kept for that
	// database's next read. Zero keeps none.
	IdleTimeout time.Duration
}

// DefaultLimits are the architecture's figures (sections 4.3 and 5).
// Concurrent is a placeholder the composition root sets from the core count.
// Concurrent + QueueDepth covers the expected forty participants, so a round's
// simultaneous Run waits rather than being refused.
func DefaultLimits() Limits {
	return Limits{
		Deadline:   5 * time.Second,
		MaxRows:    1000,
		MaxBytes:   5 << 20,
		Concurrent: 8,
		QueueDepth: 32,
		PerMinute:  30,
		// Spans reading one answer and editing the next query, and is short
		// enough that the reclaim sweep's plain DROP DATABASE finds a
		// finished contest's databases already released.
		IdleTimeout: 30 * time.Second,
	}
}

// Request is one participant asking one question.
type Request struct {
	// Registration identifies who is asking. The same person in two contests
	// is two participants.
	Registration uuid.UUID
	// Database comes from game_instances, never from the client.
	Database string
	SQL      string
	Policy   sqlpolicy.Policy
	// DiskQuotaBytes is how large the database may grow; zero means
	// unbounded. Checked before a write, never before a read.
	DiskQuotaBytes int64
}

// Validator decides whether a query is allowed, and says what it is. It is an
// interface so that importers of this package (the Core API) need not link the
// cgo parser; only the runner's own command wires in the checker.
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
		// Idle connections count against QUERY_CONCURRENT too (see pool).
		conns:   newPool(cluster.connect, limits.Concurrent, limits.IdleTimeout),
		checker: checker,
		limits:  limits,
		gate:    newGate(limits.Concurrent, limits.QueueDepth),
		rate:    newWindow(limits.PerMinute, time.Minute, nil),
	}
}

// Close closes the kept connections. Running queries finish, and their
// connections are then closed rather than kept.
func (r *Runner) Close() { r.conns.close() }

// Run checks the query, admits it, and executes it, in order of increasing
// cost. The rate comes before the parser, which is C code reading adversarial
// text, so a query the checker refuses still counts against the rate
// (CLAUDE.md rule 13). Checking needs no connection, and admission comes
// before one is opened.
func (r *Runner) Run(ctx context.Context, req Request) (*Result, error) {
	participant := req.Registration.String()
	if err := r.rate.admit(participant); err != nil {
		return nil, err
	}

	// Checked upstream too; a connection string is only ever built from a
	// plain identifier.
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

// execute runs the query under the deadline on a pooled connection.
func (r *Runner) execute(ctx context.Context, req Request, statement sqlpolicy.Statement) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.limits.Deadline)
	defer cancel()

	sess, tx, err := r.begin(ctx, req)
	if err != nil {
		return nil, timeoutOr(ctx, err)
	}
	// Rolled back unless a write commits (rollback after commit is a no-op).
	// The connection is kept only if the query answered and the context never
	// ended: an ended context may have a cancel in flight.
	clean := false
	defer func() {
		ending, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		rolledBack := tx.Rollback(ending)
		r.conns.release(ending, sess, clean && ctx.Err() == nil &&
			(rolledBack == nil || errors.Is(rolledBack, pgx.ErrTxClosed)))
	}()

	// A statement that only frees space (sqlpolicy.Statement.Frees) runs at
	// the cap, or a full database could never be emptied. TRUNCATE and DROP
	// cannot allocate, so this is no hole in the quota. A plain DELETE does
	// not count: its pages stay allocated.
	if statement.Writes && !statement.Frees && req.DiskQuotaBytes > 0 {
		if err := withinQuota(ctx, tx, req.DiskQuotaBytes); err != nil {
			return nil, err
		}
	}

	// A read is wrapped so the server stops early. A write or EXPLAIN cannot
	// sit inside a FROM; its answer is bounded while reading instead.
	text := statement.Text
	if !statement.Explain && !statement.Writes {
		text = limited(statement.Text, r.limits.MaxRows)
	}

	result, err := collect(ctx, tx, text, r.limits, statement.Writes)
	if err != nil {
		if sess.meter.exhausted() {
			// The driver reports the meter's refusal as a broken connection.
			return nil, ErrResultTooLarge
		}
		return nil, timeoutOr(ctx, err)
	}

	// Commit only once the statement completed and its answer was read
	// within the limits.
	if statement.Writes {
		if err := tx.Commit(ctx); err != nil {
			return nil, timeoutOr(ctx, err)
		}
	}
	clean = true
	return result, nil
}

// begin takes a connection and opens the query's transaction on it. It
// connects as the writer only when the policy permits writing, so a read-only
// contest is read-only by privilege as well as by the transaction's access mode.
//
// A kept connection may have been severed while idle (database dropped WITH
// (FORCE), server restarted). BEGIN finds out before anything has run, so the
// query retries once on a new connection.
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

// withinQuota refuses a write to a database already at its allowance. It runs
// before the statement because a full shared disk takes the contest down. One
// statement can overshoot, bounded by what it writes within the deadline
// (section 4.1).
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

// timeoutOr names what ended the query from the context, whatever error the
// driver surfaced first, keeping the deadline apart from the caller leaving.
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
