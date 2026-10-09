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

// ErrNoWriter is a read-write contest reaching a runner with no writer
// credentials. Running as the reader would turn every write into a misleading
// "permission denied".
var ErrNoWriter = errors.New("this runner has no writer credentials")

var errResultBudget = errors.New("the result exceeded the read budget")

// Cluster opens connections to participants' databases; the pool decides how
// long they live. It holds two sets of credentials, one per participant role
// (section 4), chosen by the contest's policy, never by the query.
type Cluster struct {
	reader *url.URL
	// writer is nil when the deployment gave none.
	writer *url.URL
}

// NewCluster reads the base connection strings. The reader's is required; the
// writer's may be empty. The database name in each is replaced per request.
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
// returns the meter that bounds how much it may read. The name comes from
// game_instances, never from the participant (section 5).
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

	// No statement cache on either side: on a kept connection it would
	// accumulate past queries as server-side prepared statements, and DISCARD
	// ALL would invalidate it silently. Participants rarely repeat a query
	// verbatim, so describe-then-execute costs nothing extra.
	config.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	config.StatementCacheCapacity = 0
	config.DescriptionCacheCapacity = 0

	// Send a CancelRequest when a query is abandoned. pgx's default handler
	// only breaks the socket, and the cancel pgconn sends while closing can
	// race the runner releasing the slot. With this handler the query call
	// returns only after the cancel is dispatched, while the slot is still
	// held. If no cancel can arrive at all, the role's
	// client_connection_check_interval ends the backend.
	config.BuildContextWatcherHandler = func(pgConn *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{
			Conn: pgConn,
			// Cancel at once; the socket deadline is only a backstop.
			CancelRequestDelay: 0,
			DeadlineDelay:      5 * time.Second,
		}
	}

	// The budget is enforced on the socket: the driver reads a whole row
	// before handing any of it over, so a check on decoded values comes after
	// the allocation (CLAUDE.md rule 12).
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

// reset gives the meter a new allowance for the next query on a kept
// connection. A tripped meter's connection is closed instead.
func (m *readMeter) reset(budget int64) {
	m.mu.Lock()
	m.remaining = budget
	m.tripped = false
	m.mu.Unlock()
}

// take reports how many of n bytes may be read. Zero means the budget is gone,
// and trips the meter for good.
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
// Only reads are metered; a refused read surfaces to the driver as a broken
// connection, which the runner renames.
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
