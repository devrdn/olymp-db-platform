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
	"github.com/jackc/pgx/v5"
)

// ErrTimeout is a query that ran longer than it was allowed to.
var ErrTimeout = errors.New("the query took too long")

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
	}
}

// Request is one participant asking one question.
type Request struct {
	// Participant identifies who is asking, for the one-at-a-time rule. It is
	// a registration rather than an account: the same person in two contests
	// is two participants.
	Participant string
	// Database is the participant's own game database, taken from
	// game_instances by the caller. It never comes from the client.
	Database string
	SQL      string
	Policy   sqlpolicy.Policy
}

// Runner executes participants' queries.
type Runner struct {
	cluster *Cluster
	checker *sqlpolicy.Checker
	limits  Limits
	gate    *gate
}

// New assembles a runner.
func New(cluster *Cluster, checker *sqlpolicy.Checker, limits Limits) *Runner {
	return &Runner{
		cluster: cluster,
		checker: checker,
		limits:  limits,
		gate:    newGate(limits.Concurrent, limits.QueueDepth),
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

	release, err := r.gate.enter(ctx, req.Participant)
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

	text := req.SQL
	if !statement.Explain {
		text = limited(req.SQL, r.limits.MaxRows)
	}

	result, err := collect(ctx, conn, text, r.limits)
	if err != nil {
		return nil, timeoutOr(ctx, err)
	}
	if statement.Explain && len(result.Rows) > r.limits.MaxRows {
		result.Rows = result.Rows[:r.limits.MaxRows]
		result.Truncated = true
	}
	return result, nil
}

func accessMode(p sqlpolicy.Policy) pgx.TxAccessMode {
	if p.Mode == sqlpolicy.ModeReadWrite {
		return pgx.ReadWrite
	}
	return pgx.ReadOnly
}

// timeoutOr reports a deadline as a timeout rather than as whatever the driver
// happened to notice first.
//
// A cancelled query surfaces differently depending on where it was when the
// deadline fired — a context error, a closed connection, a server-side
// cancellation. They are one outcome as far as the participant and the journal
// are concerned, and collapsing them here is what keeps that true.
func timeoutOr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %w", ErrTimeout, err)
	}
	return err
}
