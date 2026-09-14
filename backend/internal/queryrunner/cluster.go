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

// Cluster opens connections to participants' databases.
//
// It dials; it does not decide how long a connection lives. That is the pool's
// (pool.go): a clean read's connection is reset and kept for the participant's
// next read, and everything else is closed after its query, as every
// connection was before the pool existed.
//
// The deadline does not depend on closing. An abandoned query is stopped by the
// CancelRequest the context watcher below sends, and its connection is then
// closed rather than kept, so a cancel still on its way cannot land on another
// query.
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

	// No statement cache on either side. The driver's default names and keeps
	// a server-side prepared statement for every distinct query text, which on
	// a kept connection is one participant's past queries accumulating in a
	// backend's memory (and listed in pg_prepared_statements), and a cache on
	// this side that the reset between queries (DISCARD ALL) would silently
	// invalidate. Describe-then-execute uses the unnamed statement, which the
	// next query replaces, and costs the same two round trips a cache miss
	// did — and participants' queries are almost never repeated verbatim, so
	// the cache was missing anyway.
	config.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	config.StatementCacheCapacity = 0
	config.DescriptionCacheCapacity = 0

	// Cancel the server's work when this process abandons a query, and do it
	// as the handler's job rather than as a side effect. pgx's default handler
	// breaks the socket with a past deadline; the server is then only told to
	// stop because pgconn, on the resulting read error, happens to send a
	// CancelRequest from a background goroutine while closing (asyncClose).
	// That is an implementation detail, and it races the runner: the query call
	// returns and the slot and the participant's one-query mark are released
	// while that goroutine may not yet have sent the cancel. This handler sends the CancelRequest
	// itself, and pgx does not let the abandoned query call return until the
	// cancel has been dispatched, so the slot is still held (release runs after
	// this connection is closed) when the server is told to stop. When no cancel
	// can arrive at all — the runner killed, the network gone —
	// client_connection_check_interval on the role is what ends the backend.
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

// reset gives the meter a new allowance, for the next query on a kept
// connection. A tripped meter never reaches here: its connection is closed.
func (m *readMeter) reset(budget int64) {
	m.mu.Lock()
	m.remaining = budget
	m.tripped = false
	m.mu.Unlock()
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
