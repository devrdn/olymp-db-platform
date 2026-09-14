package queryrunner

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// session is one connection lent to one execution, with the meter that bounds
// what it may read.
type session struct {
	conn     *pgx.Conn
	meter    *readMeter
	database string
	write    bool
	// reused says the connection served an earlier query. A reused connection
	// may have been severed while it sat idle — a database dropped WITH
	// (FORCE) under it — which the first statement on it discovers.
	reused bool
}

// dialer opens a new connection to one database as one of the two roles.
type dialer func(ctx context.Context, database string, write bool, readBudget int64) (*pgx.Conn, *readMeter, error)

// pool keeps the connection a participant's read finished on, so that their
// next read skips the connection handshake.
//
// # Why it exists
//
// Opening a connection is a TCP handshake, a SCRAM-SHA-256 exchange (PBKDF2 on
// both ends, by design expensive) and a backend fork. Measured on the test
// cluster that is about five milliseconds of server CPU per query — as much as
// a typical participant join, and all of a trivial one — spent on the same two
// cores the queries run on.
//
// # What it keeps, and for how long
//
// At most one idle connection per database. A participant runs one query at a
// time, so one is all their next query can use, and an instance database
// carries CONNECTION LIMIT 2, of which the schema panel borrows the other.
// An idle connection is closed after idleTimeout.
//
// At most capacity connections in all, idle and in use together, where
// capacity is QUERY_CONCURRENT. The game cluster's memory is sized for that
// many participant backends at the per-process cap (config.Runner), and a kept
// backend may still hold what its last query grew to; so a new connection
// needed while the runner is at the bound first closes the least recently
// used idle one. The gate admits at most capacity executions, which is what
// makes an idle connection always available to close at that point.
//
// # What it never keeps
//
// Only a read that finished cleanly returns its connection, and only after
// the session is reset (release). A write, a query that failed, and one that
// was abandoned or ran out of time close theirs, exactly as every query did
// before there was a pool: a write can leave what a reset is not trusted to
// find, a failure leaves a session nothing here inspects, and an abandoned
// query may still have a cancel on its way to the server that would land on
// whatever the backend ran next.
type pool struct {
	dial        dialer
	capacity    int
	idleTimeout time.Duration

	mu     sync.Mutex
	open   int // connections dialled and not yet closed, idle or lent
	idle   map[string]*idleConn
	closed bool
}

// idleConn is a kept connection and the timer that closes it.
type idleConn struct {
	session *session
	since   time.Time
	timer   *time.Timer
}

// closeTimeout bounds closing a connection: a Terminate message and a socket
// close, which should take no time, against a server that has stopped
// answering.
const closeTimeout = 5 * time.Second

// resetTimeout bounds the reset that precedes keeping a connection. A reset
// that takes longer than this means the connection is not worth keeping.
const resetTimeout = 2 * time.Second

func newPool(dial dialer, capacity int, idleTimeout time.Duration) *pool {
	return &pool{
		dial:        dial,
		capacity:    capacity,
		idleTimeout: idleTimeout,
		idle:        make(map[string]*idleConn),
	}
}

// acquire lends a connection to one execution: the kept one for this database
// when it is a read and one is kept, a new one otherwise.
func (p *pool) acquire(ctx context.Context, database string, write bool, readBudget int64) (*session, error) {
	p.mu.Lock()
	if !write {
		if kept, ok := p.idle[database]; ok {
			delete(p.idle, database)
			kept.timer.Stop()
			p.mu.Unlock()
			// A fresh budget for a fresh answer: the meter belongs to the
			// connection, the allowance to the query.
			kept.session.meter.reset(readBudget)
			kept.session.reused = true
			return kept.session, nil
		}
	}

	// At the bound, a new connection takes the place of the least recently
	// used idle one, which is closed before the new one is dialled.
	var evicted *session
	if p.open >= p.capacity {
		evicted = p.oldestIdleLocked()
	}
	if evicted == nil {
		p.open++
	}
	p.mu.Unlock()

	if evicted != nil {
		closeConn(ctx, evicted.conn)
	}

	conn, meter, err := p.dial(ctx, database, write, readBudget)
	if err != nil {
		p.mu.Lock()
		p.open--
		p.mu.Unlock()
		return nil, err
	}
	return &session{conn: conn, meter: meter, database: database, write: write}, nil
}

// oldestIdleLocked removes and returns the least recently kept idle
// connection, or nil when none is idle. The caller holds mu, and the removed
// connection's place in open passes to the caller.
func (p *pool) oldestIdleLocked() *session {
	var oldest *idleConn
	for _, candidate := range p.idle {
		if oldest == nil || candidate.since.Before(oldest.since) {
			oldest = candidate
		}
	}
	if oldest == nil {
		return nil
	}
	delete(p.idle, oldest.session.database)
	oldest.timer.Stop()
	return oldest.session
}

// release takes a connection back. clean is the execution's word that its
// query finished without error and without its context ending; anything else
// closes the connection.
//
// A clean read's connection is reset and kept. DISCARD ALL is the server's own
// "make this session like a new one": it resets every setting to the role's
// and database's defaults, releases session advisory locks, drops temporary
// tables, deallocates prepared statements and cached plans, and stops
// listening. The driver keeps no statement cache of its own on these
// connections (Cluster.connect), so there is nothing on this side to forget.
func (p *pool) release(ctx context.Context, s *session, clean bool) {
	keep := clean && !s.write && p.idleTimeout > 0 && !s.conn.IsClosed() &&
		s.conn.PgConn().TxStatus() == 'I'
	if keep {
		resetting, stop := context.WithTimeout(context.WithoutCancel(ctx), resetTimeout)
		_, err := s.conn.PgConn().Exec(resetting, `DISCARD ALL`).ReadAll()
		stop()
		keep = err == nil
	}

	if keep {
		p.mu.Lock()
		// One idle connection per database. A second can only come from two
		// executions against the same database at once, which one query per
		// participant rules out; if it happens anyway, the one already kept
		// stays and this one goes.
		if _, taken := p.idle[s.database]; !taken && !p.closed {
			kept := &idleConn{session: s, since: time.Now()}
			kept.timer = time.AfterFunc(p.idleTimeout, func() { p.expire(kept) })
			p.idle[s.database] = kept
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
	}

	closeConn(ctx, s.conn)
	p.mu.Lock()
	p.open--
	p.mu.Unlock()
}

// expire closes an idle connection whose timeout ran out, unless it has been
// lent out or closed in the meantime.
func (p *pool) expire(kept *idleConn) {
	p.mu.Lock()
	if p.idle[kept.session.database] != kept {
		p.mu.Unlock()
		return
	}
	delete(p.idle, kept.session.database)
	p.open--
	p.mu.Unlock()

	closeConn(context.Background(), kept.session.conn)
}

// close closes every idle connection and keeps none from here on. Connections
// lent out are closed as they come back.
func (p *pool) close() {
	p.mu.Lock()
	p.closed = true
	idle := make([]*idleConn, 0, len(p.idle))
	for database, kept := range p.idle {
		kept.timer.Stop()
		idle = append(idle, kept)
		delete(p.idle, database)
	}
	p.open -= len(idle)
	p.mu.Unlock()

	for _, kept := range idle {
		closeConn(context.Background(), kept.session.conn)
	}
}

// closeConn closes a connection with a context of its own: the caller's may
// already be the expired one, and a close that is skipped because a deadline
// passed is a connection left to the server to notice.
func closeConn(ctx context.Context, conn *pgx.Conn) {
	closing, stop := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer stop()
	_ = conn.Close(closing)
}
