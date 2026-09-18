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

// The clock of the server-detected signals (design §2.3). Named here so the
// numbers the design gives are written once.
const (
	// ParallelWindow is how recently the tracked session must have been seen
	// for a request from another session to count as a parallel one. Past it,
	// the other session simply takes over: a participant who moved to another
	// computer is not using two at once.
	ParallelWindow = 2 * time.Minute
	// ParallelReportEvery is how often the same pair of sessions is reported
	// at most. Two live browsers ask every few seconds; one event per pair
	// per ten minutes says the same thing without drowning the feed.
	ParallelReportEvery = 10 * time.Minute
	// SeenRefresh is how stale the tracked session's last sighting may get
	// before a request of that session moves it forward. The sighting is a
	// cache write, and one per request would be a write per request for
	// nothing (CLAUDE.md rule 6); the cost is that ParallelWindow is honoured
	// to within this interval, erring towards not reporting.
	SeenRefresh = 10 * time.Second
	// TrailTTL is how long a registration's trail outlives its last change.
	// Longer than any olympiad; after it, the next request is a first
	// sighting again, which reports nothing.
	TrailTTL = 24 * time.Hour
	// ObserveTimeout bounds what tracking may add to a participant's request:
	// the cache read, and on a change the event insert and the cache write.
	// The signal is best-effort, the request is not.
	ObserveTimeout = 500 * time.Millisecond
	// maxReportedPairs bounds the pairs a trail remembers for the ten-minute
	// rule, so the one key per registration stays small whatever a caller
	// does. A participant cycling through more sessions than this inside ten
	// minutes gets a pair reported again, which is not a cost worth more.
	maxReportedPairs = 8
)

// trailKeyPrefix namespaces the trails inside the shared cache.
const trailKeyPrefix = "monitor:trail:"

// TrailCache is the slice of the platform cache the tracker needs.
type TrailCache interface {
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// EventWriter stores events. Implemented by internal/postgres.
type EventWriter interface {
	InsertEvents(ctx context.Context, events []Event) error
}

// Visit is one admitted request of a participant: whose, from where, and
// from which session.
type Visit struct {
	Contest      uuid.UUID
	Registration uuid.UUID
	// Address is the client's own address (httpx.ClientIP), not the grouped
	// key a rate limiter uses (CLAUDE.md rule 9).
	Address netip.Addr
	// Session is SessionTag of the session token, never the token itself.
	Session   string
	UserAgent string
}

// SessionTag is how a session is named in a trail: a hash of its token, so
// the cache never holds a credential it could leak. Empty for no token.
func SessionTag(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:16])
}

// trail is what the cache holds per registration: the address and the
// session its requests last came from, and the pairs of sessions already
// reported.
type trail struct {
	Address  netip.Addr `json:"a"`
	Session  string     `json:"s"`
	Seen     time.Time  `json:"t"`
	Reported []reported `json:"r,omitempty"`
}

// reported is a pair of sessions and when it was last reported.
type reported struct {
	Pair string    `json:"p"`
	At   time.Time `json:"t"`
}

// Tracker detects the server-side signals of §2.3 — a registration's
// address changing, and a second session using it while the first is live —
// from the requests participant admission lets through.
//
// One cache read per request, keyed by the registration (one bounded key
// each, CLAUDE.md rule 5); a cache write only when the trail changed or its
// sighting went stale; an event insert only when there is something to
// report. It never refuses or fails a request: an unreadable cache or an
// event that cannot be stored is logged and the request goes on, within
// ObserveTimeout.
//
// Requests racing each other read the same trail and the later write wins,
// so two concurrent requests from a new address can report the change twice.
// A lock or a compare-and-swap on every request is not worth a duplicate
// line in a feed.
type Tracker struct {
	cache  TrailCache
	events EventWriter
	log    *slog.Logger
	now    func() time.Time
}

// NewTracker returns a tracker keeping trails in cache and writing events to
// events.
func NewTracker(cache TrailCache, events EventWriter, log *slog.Logger) *Tracker {
	return &Tracker{cache: cache, events: events, log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the wall clock, for tests.
func (t *Tracker) WithClock(now func() time.Time) *Tracker {
	t.now = now
	return t
}

// Observe records one admitted request, reporting what changed since the
// registration's previous one. A visit without an address or a session has
// nothing to compare and is ignored.
func (t *Tracker) Observe(ctx context.Context, visit Visit) {
	if !visit.Address.IsValid() || visit.Session == "" || visit.Registration == uuid.Nil {
		return
	}
	// The participant's request may be cancelled the moment it is answered;
	// what was observed is still worth recording, within the budget.
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
		events, changed := current.follow(visit, now)
		if len(events) > 0 {
			if err := t.events.InsertEvents(ctx, events); err != nil {
				// Not retried: the trail below moves on regardless, or every
				// request after this one would try the same insert again.
				t.log.WarnContext(ctx, "could not record a participant signal", "registration", visit.Registration, "error", err)
			}
		}
		if !changed {
			return
		}
	} else {
		// The first sighting of this registration, or one past TrailTTL:
		// there is nothing to compare with, so nothing to report.
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
// and whether the trail changed.
func (tr *trail) follow(visit Visit, now time.Time) ([]Event, bool) {
	var events []Event
	changed := false
	event := func(payload Payload) {
		events = append(events, Event{Contest: visit.Contest, Registration: visit.Registration, Payload: payload})
	}

	if visit.Session != tr.Session {
		if now.Sub(tr.Seen) < ParallelWindow {
			// Another session while the tracked one is live. The tracked one
			// stays tracked, and so does its address: this is reported as a
			// parallel session, not also as the address moving.
			if tr.remember(pairOf(tr.Session, visit.Session), now) {
				event(ParallelSession{OtherIP: visit.Address, UserAgent: visit.UserAgent})
				return events, true
			}
			return nil, false
		}
		// The tracked session went quiet: this one takes over.
		tr.Session, tr.Seen = visit.Session, now
		changed = true
	} else if now.Sub(tr.Seen) >= SeenRefresh {
		tr.Seen = now
		changed = true
	}

	if visit.Address != tr.Address {
		event(IPChanged{From: tr.Address, To: visit.Address})
		tr.Address = visit.Address
		changed = true
	}
	return events, changed
}

// remember reports whether pair is due to be reported at now, and if so
// notes that it was. Pairs reported longer than ParallelReportEvery ago are
// forgotten, and at most maxReportedPairs are kept.
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

// pairOf names a pair of sessions the same whichever is tracked.
func pairOf(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return strings.Join([]string{a, b}, "|")
}
