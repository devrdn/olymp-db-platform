package monitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"github.com/devrdn/db-contest/backend/internal/audit"
)

// The organiser's read side (design §4): what a contest's staff are shown of
// what its participants did. WatchService answers the reads; who may ask is
// the HTTP layer's decision (rbac.PermissionContestMonitor on the contest),
// and so is the read budget.

// RosterCacheTTL is how long one computation of a contest's participants
// table serves every organiser (design §4): forty organiser tabs polling
// every five seconds are one computation.
const RosterCacheTTL = 3 * time.Second

// rosterComputeTimeout bounds one shared computation once it belongs to no
// single caller (see computeOnce); the pool's statement timeout caps the
// query itself, so this guards against an acquire that never returns.
const rosterComputeTimeout = 20 * time.Second

// WatchStore is the storage the organiser's reads need. Implemented by
// internal/postgres.Watch.
type WatchStore interface {
	// Roster computes the participants table, at most limit rows.
	Roster(ctx context.Context, contest uuid.UUID, limit int) (Roster, error)
	// Participant finds a registration of the contest, or
	// ErrParticipantNotFound.
	Participant(ctx context.Context, contest, registration uuid.UUID) (Participant, error)
	Feed(ctx context.Context, q FeedQuery) (FeedPage, error)
	FeedSourceReader
	Queries(ctx context.Context, q QueriesQuery) (QueriesPage, error)
	// Answers reads every attempt with at most perAttempt of the queries
	// that led to it.
	Answers(ctx context.Context, contest, registration uuid.UUID, perAttempt int) (Answers, error)
	Workspace(ctx context.Context, registration uuid.UUID) (Workspace, error)
	// Revision reads one revision of the registration, or
	// ErrRevisionNotFound.
	Revision(ctx context.Context, registration uuid.UUID, id int64) (RevisionBody, error)
}

// ViewAuditEvery is how often one viewer's looking at one participant (or
// at one contest's table and feed) is recorded at most (design §7): a screen
// polling every five seconds would otherwise write the trail full of it.
const ViewAuditEvery = 15 * time.Minute

// viewKeyPrefix namespaces the marks of recorded views in the shared cache.
const viewKeyPrefix = "monitor:viewed:"

// ViewMarks is the slice of the platform cache the view audit needs: the
// mark that a view of a pair was recorded within ViewAuditEvery.
type ViewMarks interface {
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// WatchConfig assembles a WatchService.
type WatchConfig struct {
	Store WatchStore
	// Audit records views and exports; Marks remembers which views were
	// recorded lately. Both are required for RecordView and RecordExport.
	Audit *audit.Recorder
	Marks ViewMarks
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// RosterTTL defaults to RosterCacheTTL.
	RosterTTL time.Duration
}

// WatchService answers the organiser's reads.
type WatchService struct {
	store     WatchStore
	audit     *audit.Recorder
	marks     ViewMarks
	now       func() time.Time
	rosterTTL time.Duration

	mu      sync.Mutex
	rosters map[uuid.UUID]cachedRoster
	flight  singleflight.Group
}

type cachedRoster struct {
	roster  Roster
	expires time.Time
}

// NewWatchService returns the organiser's read side.
func NewWatchService(cfg WatchConfig) *WatchService {
	s := &WatchService{store: cfg.Store, audit: cfg.Audit, marks: cfg.Marks, now: cfg.Now, rosterTTL: cfg.RosterTTL,
		rosters: make(map[uuid.UUID]cachedRoster)}
	if s.now == nil {
		s.now = time.Now
	}
	if s.rosterTTL <= 0 {
		s.rosterTTL = RosterCacheTTL
	}
	return s
}

// Roster returns the contest's participants table, from the per-contest
// cache while it is younger than the TTL. A miss is shared: however many
// organisers ask in the same instant, the store is asked once
// (computeOnce).
//
// The number of cached entries is bounded by the contests somebody holding
// contest.monitor on them asked about — the HTTP layer authorises before this
// is called — and expired entries are dropped on every store.
func (s *WatchService) Roster(ctx context.Context, contest uuid.UUID) (Roster, error) {
	now := s.now()
	if roster, ok := s.cachedRoster(contest, now); ok {
		return roster, nil
	}
	result, err := s.computeOnce(ctx, "roster:"+contest.String(), func(ctx context.Context) (any, error) {
		roster, err := s.store.Roster(ctx, contest, MaxRosterRows)
		if err != nil {
			return nil, err
		}
		roster.GeneratedAt = s.now()
		return roster, nil
	})
	if err != nil {
		return Roster{}, err
	}
	roster := result.(Roster)
	// Anchored to when the table was computed, not to this caller's clock: a
	// caller that joined the flight late must not stretch the cache.
	s.storeRoster(contest, roster, roster.GeneratedAt.Add(s.rosterTTL))
	return roster, nil
}

func (s *WatchService) cachedRoster(contest uuid.UUID, now time.Time) (Roster, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.rosters[contest]
	if !ok || !now.Before(entry.expires) {
		return Roster{}, false
	}
	return entry.roster, true
}

func (s *WatchService) storeRoster(contest uuid.UUID, roster Roster, expires time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for key, entry := range s.rosters {
		if !now.Before(entry.expires) {
			delete(s.rosters, key)
		}
	}
	s.rosters[contest] = cachedRoster{roster: roster, expires: expires}
}

// computeOnce collapses concurrent misses of one key into one call to fn,
// the way leaderboard.Service does: fn runs detached from any one caller
// (context.WithoutCancel, bounded by rosterComputeTimeout) because the
// computation belongs to every caller waiting on it, each caller still stops
// waiting when its own context ends, and a panic becomes an error instead of
// taking the process down. A failed computation is never cached.
func (s *WatchService) computeOnce(ctx context.Context, key string, fn func(ctx context.Context) (any, error)) (any, error) {
	ch := s.flight.DoChan(key, func() (result any, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("compute %s: %v", key, r)
			}
		}()
		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rosterComputeTimeout)
		defer cancel()
		return fn(flightCtx)
	})
	select {
	case res := <-ch:
		return res.Val, res.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Participant finds a registration of the contest: another contest's is
// ErrParticipantNotFound, the same as one that does not exist. Every
// per-participant read below asks it first.
func (s *WatchService) Participant(ctx context.Context, contest, registration uuid.UUID) (Participant, error) {
	return s.store.Participant(ctx, contest, registration)
}

// Feed reads one page of the contest's feed, or of one participant's
// timeline when q names a registration — which must be the contest's.
func (s *WatchService) Feed(ctx context.Context, q FeedQuery) (FeedPage, error) {
	q, err := q.Normalize()
	if err != nil {
		return FeedPage{}, err
	}
	if q.Registration != uuid.Nil {
		if _, err := s.store.Participant(ctx, q.Contest, q.Registration); err != nil {
			return FeedPage{}, err
		}
	}
	return s.store.Feed(ctx, q)
}

// StreamFeed hands the whole feed of the contest, or of one of its
// participants, to yield, oldest first (monitor.StreamFeed).
func (s *WatchService) StreamFeed(ctx context.Context, q FeedQuery, yield func(FeedItem) error) error {
	if q.Registration != uuid.Nil {
		if _, err := s.store.Participant(ctx, q.Contest, q.Registration); err != nil {
			return err
		}
	}
	return StreamFeed(ctx, s.store, q, yield)
}

// Queries reads one page of a participant's queries.
func (s *WatchService) Queries(ctx context.Context, q QueriesQuery) (QueriesPage, error) {
	q, err := q.Normalize()
	if err != nil {
		return QueriesPage{}, err
	}
	if _, err := s.store.Participant(ctx, q.Contest, q.Registration); err != nil {
		return QueriesPage{}, err
	}
	return s.store.Queries(ctx, q)
}

// Answers reads a participant's attempts with the queries that led to each.
func (s *WatchService) Answers(ctx context.Context, contest, registration uuid.UUID) (Answers, error) {
	if _, err := s.store.Participant(ctx, contest, registration); err != nil {
		return Answers{}, err
	}
	return s.store.Answers(ctx, contest, registration, MaxAttemptQueries)
}

// Workspace reads a participant's notes and tabs and their revision list.
func (s *WatchService) Workspace(ctx context.Context, contest, registration uuid.UUID) (Workspace, error) {
	if _, err := s.store.Participant(ctx, contest, registration); err != nil {
		return Workspace{}, err
	}
	return s.store.Workspace(ctx, registration)
}

// Revision reads one revision of a participant, whole.
func (s *WatchService) Revision(ctx context.Context, contest, registration uuid.UUID, id int64) (RevisionBody, error) {
	if _, err := s.store.Participant(ctx, contest, registration); err != nil {
		return RevisionBody{}, err
	}
	return s.store.Revision(ctx, registration, id)
}

// RecordView records that viewer looked at a participant of the contest —
// or, with registration uuid.Nil, at the contest's table or feed — unless
// the same view was recorded within ViewAuditEvery (design §7).
//
// One cache read per request, and a write only when a view is recorded
// (CLAUDE.md rule 6). The mark is set after the entry is written, so a
// failed write is retried by the next view instead of being forgotten; a
// cache that cannot be read counts as no mark, which records more rather
// than less. Two views racing past an empty mark may both be recorded — a
// duplicate line, never a missing one.
func (s *WatchService) RecordView(ctx context.Context, viewer, contest, registration uuid.UUID) error {
	subject := contest.String()
	if registration != uuid.Nil {
		subject = registration.String()
	}
	key := viewKeyPrefix + viewer.String() + ":" + subject
	if _, found, err := s.marks.Get(ctx, key); err == nil && found {
		return nil
	}
	if err := s.record(ctx, audit.ActionContestMonitorView, viewer, contest, registration); err != nil {
		return err
	}
	// A mark that cannot be set costs a second entry for the next view, not
	// a view without one.
	_ = s.marks.Set(ctx, key, []byte{1}, ViewAuditEvery)
	return nil
}

// RecordExport records a CSV export of a participant's feed, or with
// registration uuid.Nil of the contest's, every time (design §7).
func (s *WatchService) RecordExport(ctx context.Context, viewer, contest, registration uuid.UUID) error {
	return s.record(ctx, audit.ActionContestMonitorExport, viewer, contest, registration)
}

// record writes one entry about the contest; the participant, when there
// is one, is in the payload, so the contest's own trail lists it.
func (s *WatchService) record(ctx context.Context, action string, viewer, contest, registration uuid.UUID) error {
	payload := map[string]any{}
	if registration != uuid.Nil {
		payload["registration_id"] = registration.String()
	}
	actor := viewer
	if err := s.audit.Record(ctx, audit.Entry{
		ActorID: &actor, Action: action, Entity: "contest", EntityID: contest.String(), Payload: payload,
	}); err != nil {
		return fmt.Errorf("record %s: %w", action, err)
	}
	return nil
}
