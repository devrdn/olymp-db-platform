package queryrunner

import (
	"context"
	"fmt"
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
	// reused says the connection served an earlier query, and may have been
	// severed while idle.
	reused bool
}

// errPoolExhausted is the pool lending every connection it may. The gate
// should make this unreachable; it is refused rather than exceeded,
// because the game cluster's memory is sized for the bound.
var errPoolExhausted = fmt.Errorf("%w: every game-database connection the runner may hold is in use", ErrBusy)

type dialer func(ctx context.Context, database string, write bool, readBudget int64) (*pgx.Conn, *readMeter, error)

// pool keeps the connection a participant's read finished on, so their next
// read skips a TCP and SCRAM handshake and a backend fork (about 5 ms of
// server CPU per query).
//
// It keeps at most one idle connection per database, closed after idleTimeout:
// an instance database has CONNECTION LIMIT 2, the second slot being headroom
// for a backend still exiting. It lends at most capacity (QUERY_CONCURRENT)
// connections, because the game cluster's memory is sized for that many
// running backends, and keeps at most maxIdle idle ones on top, since a
// separate budget is what lets a connection survive until its participant's
// next query. Past maxIdle the least recently kept one is closed.
//
// Only a read that finished cleanly is kept, after DISCARD ALL. A write, a
// failure, or an abandoned query closes its connection: a reset is not trusted
// after a write, and an abandoned query may have a cancel in flight. A write
// also closes the read connection kept for its database, to stay within the
// connection limit.
//
// DISCARD ALL leaves a custom placeholder setting defined and keeps the
// setseed state. The checker refuses everything that sets either (SET,
// set_config, setseed) or reads a setting (SHOW, current_setting). Neither is
// a privilege, and a kept connection only serves its own participant's
// database.
type pool struct {
	dial        dialer
	capacity    int
	maxIdle     int
	idleTimeout time.Duration

	mu     sync.Mutex
	lent   int
	idle   map[string]*idleConn
	closed bool
}

type idleConn struct {
	session *session
	since   time.Time
	timer   *time.Timer
}

// closeTimeout bounds closing a connection against a server that stopped
// answering.
const closeTimeout = 5 * time.Second

// resetTimeout bounds the reset before keeping a connection; a slower reset
// means the connection is not kept.
const resetTimeout = 2 * time.Second

func newPool(dial dialer, capacity, maxIdle int, idleTimeout time.Duration) *pool {
	return &pool{
		dial:        dial,
		capacity:    capacity,
		maxIdle:     maxIdle,
		idleTimeout: idleTimeout,
		idle:        make(map[string]*idleConn),
	}
}

// acquire lends a connection to one execution: the kept one for this database
// when it is a read and one is kept, a new one otherwise.
func (p *pool) acquire(ctx context.Context, database string, write bool, readBudget int64) (*session, error) {
	p.mu.Lock()
	if p.lent >= p.capacity {
		p.mu.Unlock()
		return nil, errPoolExhausted
	}
	p.lent++
	kept, ok := p.idle[database]
	if ok && !write {
		delete(p.idle, database)
		kept.timer.Stop()
		p.mu.Unlock()
		kept.session.meter.reset(readBudget)
		kept.session.reused = true
		return kept.session, nil
	}
	// A write closes the read connection kept for its database before
	// dialling, to stay within the database's connection limit.
	if ok {
		delete(p.idle, database)
		kept.timer.Stop()
	}
	p.mu.Unlock()

	if ok {
		closeConn(ctx, kept.session.conn)
	}

	conn, meter, err := p.dial(ctx, database, write, readBudget)
	if err != nil {
		p.mu.Lock()
		p.lent--
		p.mu.Unlock()
		return nil, err
	}
	return &session{conn: conn, meter: meter, database: database, write: write}, nil
}

// oldestIdleLocked removes and returns the least recently kept idle
// connection, or nil. The caller holds mu.
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

// release takes a connection back. clean means the query finished without
// error and without its context ending; a clean read's connection is reset with
// DISCARD ALL and kept, anything else is closed.
func (p *pool) release(ctx context.Context, s *session, clean bool) {
	keep := clean && !s.write && p.idleTimeout > 0 && p.maxIdle > 0 && !s.conn.IsClosed() &&
		s.conn.PgConn().TxStatus() == 'I'
	if keep {
		resetting, stop := context.WithTimeout(context.WithoutCancel(ctx), resetTimeout)
		_, err := s.conn.PgConn().Exec(resetting, `DISCARD ALL`).ReadAll()
		stop()
		keep = err == nil
	}

	p.mu.Lock()
	// One idle connection per database; if one is already kept, this one is
	// closed.
	if _, taken := p.idle[s.database]; keep && !taken && !p.closed {
		p.lent--
		var evicted *session
		if len(p.idle) >= p.maxIdle {
			evicted = p.oldestIdleLocked()
		}
		kept := &idleConn{session: s, since: time.Now()}
		kept.timer = time.AfterFunc(p.idleTimeout, func() { p.expire(kept) })
		p.idle[s.database] = kept
		p.mu.Unlock()
		// The caller's answer need not wait on another participant's close.
		if evicted != nil {
			go closeConn(ctx, evicted.conn)
		}
		return
	}
	p.mu.Unlock()

	// Closed before its place is given up, so lent never undercounts.
	closeConn(ctx, s.conn)
	p.mu.Lock()
	p.lent--
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
	p.mu.Unlock()

	for _, kept := range idle {
		closeConn(context.Background(), kept.session.conn)
	}
}

// closeConn closes a connection with a context of its own, since the caller's
// may already have expired.
func closeConn(ctx context.Context, conn *pgx.Conn) {
	closing, stop := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer stop()
	_ = conn.Close(closing)
}
