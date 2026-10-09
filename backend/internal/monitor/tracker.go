package monitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The timing of the server-detected signals.
const (
	// ParallelWindow is how recently the tracked session must have been seen
	// for another session's request to count as parallel; past it the new
	// session takes over (the participant moved computers).
	ParallelWindow = 2 * time.Minute
	// ParallelReportEvery is how often the same pair is reported at most.
	ParallelReportEvery = 10 * time.Minute
	// SeenRefresh is how stale the tracked session's sighting may get before
	// a request moves it, avoiding a cache write per request (CLAUDE.md rule
	// 6). ParallelWindow is honoured to within it, erring towards silence.
	SeenRefresh = 10 * time.Second
	// TrailTTL is how long a trail outlives its last change; after it the
	// next request is a first sighting, which reports nothing.
	TrailTTL = 24 * time.Hour
	// ObserveTimeout bounds what tracking may add to a request, so a degraded
	// cache cannot slow every console run. The signal is best-effort.
	ObserveTimeout = 150 * time.Millisecond
	// maxReportedPairs bounds the pairs a trail remembers, so the key stays
	// small; cycling through more only repeats a report.
	maxReportedPairs = 16
)

const trailKeyPrefix = "monitor:trail:"

type TrailCache interface {
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// EventWriter stores events.
type EventWriter interface {
	InsertEvents(ctx context.Context, events []Event) error
}

// SessionLiveness answers whether the tracked session could still be in use:
// not signed out, expired or retired. auth.SessionStore implements it, keyed
// by the same digest as SessionTag.
type SessionLiveness interface {
	SessionAlive(ctx context.Context, tracked, current string) (bool, error)
}

// Visit is one admitted request of a participant.
type Visit struct {
	Contest      uuid.UUID
	Registration uuid.UUID
	// Address is the exact client address, not a rate-limit subject
	// (CLAUDE.md rule 9).
	Address   netip.Addr
	Session   string
	UserAgent string
}

// SessionTag names a session in a trail: the hex SHA-256 of its token, as the
// session store keys it, so the trail holds no credential. Empty for no token.
func SessionTag(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// trail is what the cache holds per registration.
type trail struct {
	Address  netip.Addr `json:"a"`
	Session  string     `json:"s"`
	Seen     time.Time  `json:"t"`
	Reported []reported `json:"r,omitempty"`
}

type reported struct {
	Pair string    `json:"p"`
	At   time.Time `json:"t"`
}

// Tracker detects a registration's address changing and a second session
// using it while the first lives, from admitted requests.
//
// Per request: one cache read keyed by the registration, a bounded key
// (CLAUDE.md rule 5), a write only when the trail changed or went stale, an
// insert only when there is something to report, and on a session switch
// inside ParallelWindow one liveness check. It never fails a request; errors are logged.
//
// Each pair of sessions or addresses is reported at most once per
// ParallelReportEvery, so IPv4/IPv6 flipping or a NAT with several exits does
// not flood the feed. Racing requests may report a change twice; a lock per
// request is not worth avoiding a duplicate line.
type Tracker struct {
	cache    TrailCache
	events   EventWriter
	sessions SessionLiveness
	log      *slog.Logger
	now      func() time.Time
}

func NewTracker(cache TrailCache, events EventWriter, sessions SessionLiveness, log *slog.Logger) *Tracker {
	return &Tracker{
		cache: cache, events: events, sessions: sessions, log: log,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (t *Tracker) WithClock(now func() time.Time) *Tracker {
	t.now = now
	return t
}

// Observe records one admitted request, reporting what changed since the
// previous one. A visit without an address or session is ignored.
func (t *Tracker) Observe(ctx context.Context, visit Visit) {
	if !visit.Address.IsValid() || visit.Session == "" || visit.Registration == uuid.Nil {
		return
	}
	// Detached: the request may be cancelled once answered.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ObserveTimeout)
	defer cancel()

	key := trailKeyPrefix + visit.Registration.String()
	raw, found, err := t.cache.Get(ctx, key)
	if err != nil {
		t.log.WarnContext(ctx, "could not read the participant's trail", "registration", visit.Registration, "error", err)
		return
	}
	now := t.now()

	var current trail
	if found && json.Unmarshal(raw, &current) == nil && current.Session != "" {
		// Asked only on a session switch inside the window whose pair is
		// due, so a real second device does not pay for it every request.
		trackedLive := true
		if visit.Session != current.Session && now.Sub(current.Seen) < ParallelWindow &&
			current.due(pairKey("session", current.Session, visit.Session), now) {
			alive, err := t.sessions.SessionAlive(ctx, current.Session, visit.Session)
			if err != nil {
				// Not knowing, change nothing; the next request asks again.
				t.log.WarnContext(ctx, "could not tell whether a participant's session lives", "registration", visit.Registration, "error", err)
				return
			}
			trackedLive = alive
		}
		events, changed := current.follow(visit, now, trackedLive)
		if len(events) > 0 {
			if err := t.events.InsertEvents(ctx, events); err != nil {
				// Not retried: the trail moves on regardless.
				t.log.WarnContext(ctx, "could not record a participant signal", "registration", visit.Registration, "error", err)
			}
		}
		if !changed {
			return
		}
	} else {
		// A first sighting has nothing to compare with.
		current = trail{Address: visit.Address, Session: visit.Session, Seen: now}
	}

	encoded, err := json.Marshal(current)
	if err != nil {
		t.log.ErrorContext(ctx, "could not encode the participant's trail", "error", err)
		return
	}
	if err := t.cache.Set(ctx, key, encoded, TrailTTL); err != nil {
		t.log.WarnContext(ctx, "could not store the participant's trail", "registration", visit.Registration, "error", err)
	}
}

// follow applies one visit to the trail and returns the events it reports
// and whether the trail changed. trackedLive is consulted only inside the
// window.
func (tr *trail) follow(visit Visit, now time.Time, trackedLive bool) ([]Event, bool) {
	var events []Event
	changed := false
	event := func(payload Payload) {
		events = append(events, Event{Contest: visit.Contest, Registration: visit.Registration, Payload: payload})
	}

	if visit.Session != tr.Session {
		if now.Sub(tr.Seen) < ParallelWindow && trackedLive {
			// A parallel session; the tracked session and address stay, so
			// it is not also reported as an address change.
			if tr.remember(pairKey("session", tr.Session, visit.Session), now) {
				event(ParallelSession{OtherIP: visit.Address, UserAgent: visit.UserAgent})
				return events, true
			}
			return nil, false
		}
		tr.Session, tr.Seen = visit.Session, now
		changed = true
	} else if now.Sub(tr.Seen) >= SeenRefresh {
		tr.Seen = now
		changed = true
	}

	if visit.Address != tr.Address {
		// The tracked address always follows, even when the event is damped.
		if tr.remember(pairKey("address", tr.Address.String(), visit.Address.String()), now) {
			event(IPChanged{From: tr.Address, To: visit.Address})
		}
		tr.Address = visit.Address
		changed = true
	}
	return events, changed
}

func (tr *trail) due(pair string, now time.Time) bool {
	return !slices.ContainsFunc(tr.Reported, func(r reported) bool {
		return r.Pair == pair && now.Sub(r.At) < ParallelReportEvery && !now.Before(r.At)
	})
}

// remember reports whether pair is due and, if so, notes it. Old pairs are
// forgotten and at most maxReportedPairs kept.
func (tr *trail) remember(pair string, now time.Time) bool {
	tr.Reported = slices.DeleteFunc(tr.Reported, func(r reported) bool {
		return now.Sub(r.At) >= ParallelReportEvery || now.Before(r.At)
	})
	if slices.ContainsFunc(tr.Reported, func(r reported) bool { return r.Pair == pair }) {
		return false
	}
	tr.Reported = append(tr.Reported, reported{Pair: pair, At: now})
	if extra := len(tr.Reported) - maxReportedPairs; extra > 0 {
		tr.Reported = tr.Reported[extra:]
	}
	return true
}

// pairKey names an unordered pair compactly as a short hash. A collision
// only damps one report.
func pairKey(kind, a, b string) string {
	if a > b {
		a, b = b, a
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{kind, a, b}, "|")))
	return hex.EncodeToString(sum[:8])
}
