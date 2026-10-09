package monitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/flight"
)

// RosterCacheTTL is how long one participants-table computation serves every
// organiser of a contest.
const RosterCacheTTL = 3 * time.Second

// rosterComputeTimeout bounds one shared computation (see flight.Do); it
// guards against a connection acquire that never returns.
const rosterComputeTimeout = 20 * time.Second

// WatchStore is the storage the organiser's reads need.
type WatchStore interface {
	Roster(ctx context.Context, contest uuid.UUID, limit int) (Roster, error)
	Participant(ctx context.Context, contest, registration uuid.UUID) (Participant, error)
	Feed(ctx context.Context, q FeedQuery) (FeedPage, error)
	FeedSourceReader
	Queries(ctx context.Context, q QueriesQuery) (QueriesPage, error)
	Answers(ctx context.Context, contest, registration uuid.UUID, perAttempt int) (Answers, error)
	Workspace(ctx context.Context, registration uuid.UUID) (Workspace, error)
	Revision(ctx context.Context, registration uuid.UUID, id int64) (RevisionBody, error)
}

// ViewAuditEvery is how often one viewer's view of one participant (or of the
// contest's table and feed) is recorded at most, so polling does not flood
// the trail.
const ViewAuditEvery = 15 * time.Minute

const viewKeyPrefix = "monitor:viewed:"

// ViewMarks is the slice of the platform cache the view audit needs.
type ViewMarks interface {
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

type WatchConfig struct {
	Store WatchStore
	// Audit and Marks are required for RecordView and RecordExport.
	Audit *audit.Recorder
	Marks ViewMarks
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// RosterTTL defaults to RosterCacheTTL.
	RosterTTL time.Duration
}

// WatchService answers the organiser's reads. Who may ask, and the read
// budget, are the HTTP layer's decisions.
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

// Roster returns the contest's participants table, from the per-contest cache
// while it is younger than the TTL; concurrent misses share one computation.
// Only authorised contests are cached, and expired entries are dropped on
// every store.
func (s *WatchService) Roster(ctx context.Context, contest uuid.UUID) (Roster, error) {
	now := s.now()
	if roster, ok := s.cachedRoster(contest, now); ok {
		return roster, nil
	}
	result, err := flight.Do(ctx, &s.flight, "roster:"+contest.String(), rosterComputeTimeout, func(ctx context.Context) (any, error) {
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
	// Expiry counts from GeneratedAt, so a late joiner cannot stretch it.
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

// Participant finds a registration of the contest; another contest's is
// ErrParticipantNotFound. Every per-participant read asks it first.
func (s *WatchService) Participant(ctx context.Context, contest, registration uuid.UUID) (Participant, error) {
	return s.store.Participant(ctx, contest, registration)
}

// Feed reads one page of the contest's feed, or of one participant's when q
// names a registration of the contest.
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

func (s *WatchService) StreamFeed(ctx context.Context, q FeedQuery, yield func(FeedItem) error) error {
	if q.Registration != uuid.Nil {
		if _, err := s.store.Participant(ctx, q.Contest, q.Registration); err != nil {
			return err
		}
	}
	return StreamFeed(ctx, s.store, q, s.now(), yield)
}

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

func (s *WatchService) Answers(ctx context.Context, contest, registration uuid.UUID) (Answers, error) {
	if _, err := s.store.Participant(ctx, contest, registration); err != nil {
		return Answers{}, err
	}
	return s.store.Answers(ctx, contest, registration, MaxAttemptQueries)
}

func (s *WatchService) Workspace(ctx context.Context, contest, registration uuid.UUID) (Workspace, error) {
	if _, err := s.store.Participant(ctx, contest, registration); err != nil {
		return Workspace{}, err
	}
	return s.store.Workspace(ctx, registration)
}

func (s *WatchService) Revision(ctx context.Context, contest, registration uuid.UUID, id int64) (RevisionBody, error) {
	if _, err := s.store.Participant(ctx, contest, registration); err != nil {
		return RevisionBody{}, err
	}
	return s.store.Revision(ctx, registration, id)
}

// RecordView records that viewer looked at a participant (or, with
// registration uuid.Nil, at the contest's table or feed) unless recorded
// within ViewAuditEvery.
//
// One cache read per request and a write only when recording (CLAUDE.md rule
// 6). The mark is set after the entry, so a failed write is retried; an
// unreadable cache records more, not less. Racing views may record a
// duplicate, never miss one.
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
	_ = s.marks.Set(ctx, key, []byte{1}, ViewAuditEvery)
	return nil
}

// RecordExport records every CSV export of a participant's feed, or with
// registration uuid.Nil of the contest's.
func (s *WatchService) RecordExport(ctx context.Context, viewer, contest, registration uuid.UUID) error {
	return s.record(ctx, audit.ActionContestMonitorExport, viewer, contest, registration)
}

// record writes one entry about the contest, with the participant in the
// payload, so the contest's trail lists it.
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
