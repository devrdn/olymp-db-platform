package monitor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
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
}

// WatchConfig assembles a WatchService.
type WatchConfig struct {
	Store WatchStore
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// RosterTTL defaults to RosterCacheTTL.
	RosterTTL time.Duration
}

// WatchService answers the organiser's reads.
type WatchService struct {
	store     WatchStore
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
	s := &WatchService{store: cfg.Store, now: cfg.Now, rosterTTL: cfg.RosterTTL,
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
