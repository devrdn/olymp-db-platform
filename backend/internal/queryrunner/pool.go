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
	// reused says the connection served an earlier query. A reused connection
	// may have been severed while it sat idle — a database dropped WITH
	// (FORCE) under it — which the first statement on it discovers.
	reused bool
}

// errPoolExhausted is the pool at its bound with nothing idle to close.
//
// The gate admits no more executions than the pool holds connections, so this
// is never the ordinary answer to load: it means the two bounds have come
// apart. It is refused rather than exceeded, because exceeding it is a game
// cluster holding more backends than its memory is sized for; and it is
// ErrBusy to everything above, which already knows how to tell a participant
// to try again.
var errPoolExhausted = fmt.Errorf("%w: every game-database connection the runner may hold is in use", ErrBusy)

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
// time, so one is all their next query can use; an instance database carries
// CONNECTION LIMIT 2, the second slot being headroom for a backend that has
// not yet exited after its connection was closed (gamedb's
// instanceConnectionLimit). The schema panel reads as a superuser and is not
// counted against it.
// An idle connection is closed after idleTimeout.
//
// At most capacity connections in all, idle and in use together, where
// capacity is QUERY_CONCURRENT. The game cluster's memory is sized for that
// many participant backends at the per-process cap (config.Runner), and a kept
// backend may still hold what its last query grew to; so a new connection
// needed while the runner is at the bound first closes the least recently
// used idle one. The gate admits at most capacity executions, which is what
// makes an idle connection always available to close at that point; if none
// is, the bounds have come apart and the pool refuses (errPoolExhausted)
// rather than exceed its own.
//
// # What it never keeps
//
// A write to a database closes the read connection kept for it before dialling
// its own: a write never takes a kept connection (it runs as the writer), and
// the runner holding two to one instance would leave the schema panel no room
// under CONNECTION LIMIT 2.
//
// Only a read that finished cleanly returns its connection, and only after
// the session is reset (release). A write, a query that failed, and one that
// was abandoned or ran out of time close theirs, exactly as every query did
// before there was a pool: a write can leave what a reset is not trusted to
// find, a failure leaves a session nothing here inspects, and an abandoned
// query may still have a cancel on its way to the server that would land on
// whatever the backend ran next.
//
// # What the reset leaves
//
// DISCARD ALL does not make a kept backend indistinguishable from a new one.
// Two pieces of session state survive it, and both are reachable only through
// functions the checker does not admit (set_config, current_setting, setseed):
//
//   - a custom placeholder setting (a dotted name such as `x.y`) that a read set
//     with set_config(..., false): the value is rolled back with the read's
//     transaction and reset by DISCARD ALL, but the placeholder stays defined,
//     so current_setting('x.y', true) answers ” on the kept backend where a
//     new one answers NULL;
//   - the random seed set by setseed, which DISCARD ALL does not touch, so the
//     next query's random() continues that sequence.
//
// Neither carries data between participants — a kept connection only ever
// serves the database it was opened to, and a database belongs to one
// participant — and neither is a privilege. Backend-local caches (catalogue,
// relation) also survive, as they do for any pooled PostgreSQL session; they
// are not visible to SQL.
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
	kept, ok := p.idle[database]
	if ok && !write {
		delete(p.idle, database)
		kept.timer.Stop()
		p.mu.Unlock()
		// A fresh budget for a fresh answer: the meter belongs to the
		// connection, the allowance to the query.
		kept.session.meter.reset(readBudget)
		kept.session.reused = true
		return kept.session, nil
	}

	// A new connection is needed. It takes the place of an idle one when
	// there is a reason to close one: a write closes the read connection kept
	// for its own database, and at the bound the least recently used idle
	// connection goes. Either is closed before the new one is dialled, and
	// its place in open passes to the new one.
	var evicted *session
	switch {
	case ok: // a write, with a read connection kept for the same database
		delete(p.idle, database)
		kept.timer.Stop()
		evicted = kept.session
	case p.open >= p.capacity:
		if evicted = p.oldestIdleLocked(); evicted == nil {
			p.mu.Unlock()
			return nil, errPoolExhausted
		}
	default:
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
	p.mu.Unlock()

	// Closed before its place is given up, as release does: open never counts
	// fewer connections than the server holds.
	closeConn(context.Background(), kept.session.conn)
	p.mu.Lock()
	p.open--
	p.mu.Unlock()
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
		p.mu.Lock()
		p.open--
		p.mu.Unlock()
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
