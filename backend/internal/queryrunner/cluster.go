package queryrunner

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
)

// ErrNoWriter is a read-write contest reaching a runner that was given no
// writer credentials. Refused rather than run as the reader: the reader has no
// INSERT anywhere, so every permitted write would fail as "permission denied",
// which reads as a bug in the contest rather than as a gap in the deployment.
var ErrNoWriter = errors.New("this runner has no writer credentials")

// errResultBudget is what the connection reports when it has read as much as
// one answer is allowed to cost.
var errResultBudget = errors.New("the result exceeded the read budget")

// Cluster opens one connection to one participant's database.
//
// Short-lived by design (section 4.3): a connection is opened for an execution
// and closed after it, so nothing is shared between two queries — no temporary
// table, no session setting, no open transaction. In a local network the cost
// is milliseconds, which at thirty queries a minute is not worth trading for
// state that would have to be reasoned about.
//
// It is also what makes the deadline enforceable. Closing the connection is
// what ends a query the server is still running, and a pooled connection is
// one you have to hand back rather than close.
//
// Two sets of credentials, one per participant role (section 4): which one a
// query runs as is decided by the contest's policy, never by the query.
type Cluster struct {
	reader *url.URL
	// writer is nil when the deployment gave none; a read-write contest is
	// then refused with ErrNoWriter.
	writer *url.URL
}

// NewCluster reads the base connection strings. The reader's is required; the
// writer's may be empty. In both, the database name is a placeholder replaced
// per request.
func NewCluster(readerDSN, writerDSN string) (*Cluster, error) {
	reader, err := parseBase(readerDSN, "reader")
	if err != nil {
		return nil, err
	}
	c := &Cluster{reader: reader}
	if writerDSN != "" {
		if c.writer, err = parseBase(writerDSN, "writer"); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func parseBase(dsn, role string) (*url.URL, error) {
	base, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("the game cluster %s DSN is not a URL: %w", role, err)
	}
	if base.User == nil {
		return nil, fmt.Errorf("the game cluster %s DSN carries no credentials", role)
	}
	return base, nil
}

// connect opens a connection to one database as one of the two roles, and
// returns the meter that bounds how much the connection may read.
//
// The name comes from the caller, which took it from game_instances — never
// from the participant. That is the first line of section 5: the query is the
// participant's, the address is not.
func (c *Cluster) connect(ctx context.Context, database string, write bool, readBudget int64) (*pgx.Conn, *readMeter, error) {
	base := c.reader
	if write {
		if c.writer == nil {
			return nil, nil, ErrNoWriter
		}
		base = c.writer
	}

	target := *base
	target.Path = "/" + database

	config, err := pgx.ParseConfig(target.String())
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to the game database: %w", err)
	}

	// Cancel the server's work when this process abandons a query, rather than
	// only closing the socket. pgx's default handler drops the connection on a
	// cancelled context and sends nothing to the server; the backend then runs
	// on until statement_timeout, holding a memory cap's worth of the cluster
	// while the Query Runner has already freed the slot and the participant's
	// one-query-at-a-time mark — so a participant who abandons request after
	// request leaves a backend behind each time, past the semaphore's bound.
	// This handler sends a real CancelRequest, and pgx does not let the
	// abandoned query call return until the cancel has been dispatched, so the
	// slot is still held (release runs after this connection is closed) when
	// the server is told to stop. client_connection_check_interval on the role
	// is the backstop for a backend too busy to notice between the two.
	config.BuildContextWatcherHandler = func(pgConn *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{
			Conn: pgConn,
			// Send the cancel at once; the socket deadline is a backstop far
			// enough out not to pre-empt a cancel the server is honouring, and
			// bounded anyway by the runner's own closing context.
			CancelRequestDelay: 0,
			DeadlineDelay:      5 * time.Second,
		}
	}

	// The budget is enforced at the socket, which is the only place it can
	// be. The driver reads a whole row into memory before handing any of it
	// over, so a check on the values it decoded comes after the allocation it
	// was meant to prevent: one cell of half a gigabyte is half a gigabyte in
	// this process before anything can say no. Counting bytes as they arrive
	// off the wire is what makes the result limit a bound on memory rather
	// than a bound on what is passed on.
	meter := &readMeter{remaining: readBudget}
	dialer := &net.Dialer{KeepAlive: 5 * time.Minute}
	config.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &meteredConn{Conn: conn, meter: meter}, nil
	}

	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to the game database: %w", err)
	}
	return conn, meter, nil
}

// readMeter is the running balance of one connection's read budget.
type readMeter struct {
	mu        sync.Mutex
	remaining int64
	tripped   bool
}

// take spends up to n bytes of the budget and reports how many may be read.
// Zero means the budget is gone, and says so permanently.
func (m *readMeter) take(n int) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.remaining <= 0 {
		m.tripped = true
		return 0
	}
	if int64(n) > m.remaining {
		n = int(m.remaining)
	}
	return n
}

func (m *readMeter) spend(n int) {
	m.mu.Lock()
	m.remaining -= int64(n)
	m.mu.Unlock()
}

// exhausted reports whether a read was refused for want of budget.
func (m *readMeter) exhausted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tripped
}

// meteredConn is a connection that stops reading once the budget is spent.
//
// Only Read is intercepted; writes are the queries this process sends, which
// it already bounds. A refused read surfaces to the driver as a broken
// connection, which the runner then names for what it was.
type meteredConn struct {
	net.Conn
	meter *readMeter
}

func (c *meteredConn) Read(p []byte) (int, error) {
	allowed := c.meter.take(len(p))
	if allowed == 0 && len(p) > 0 {
		return 0, errResultBudget
	}
	n, err := c.Conn.Read(p[:allowed])
	c.meter.spend(n)
	return n, err
}
