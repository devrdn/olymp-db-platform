package monitor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeWatchStore counts the computations and can hold them until released.
type fakeWatchStore struct {
	rosters atomic.Int32
	hold    chan struct{}
}

func (f *fakeWatchStore) Roster(_ context.Context, _ uuid.UUID, limit int) (Roster, error) {
	f.rosters.Add(1)
	if f.hold != nil {
		<-f.hold
	}
	if limit != MaxRosterRows {
		return Roster{}, nil
	}
	return Roster{Rows: []RosterRow{{Login: "a"}}}, nil
}

func TestTheRosterIsCachedForItsTTLPerContest(t *testing.T) {
	store := &fakeWatchStore{}
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	service := NewWatchService(WatchConfig{Store: store, Now: func() time.Time { return now }})
	contest, other := uuid.New(), uuid.New()

	for range 3 {
		roster, err := service.Roster(t.Context(), contest)
		if err != nil || len(roster.Rows) != 1 {
			t.Fatalf("roster = %+v, %v", roster, err)
		}
	}
	if got := store.rosters.Load(); got != 1 {
		t.Fatalf("computations within the TTL = %d, want 1", got)
	}
	if _, err := service.Roster(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if got := store.rosters.Load(); got != 2 {
		t.Fatalf("another contest shares the cache: computations = %d, want 2", got)
	}

	now = now.Add(RosterCacheTTL)
	if _, err := service.Roster(t.Context(), contest); err != nil {
		t.Fatal(err)
	}
	if got := store.rosters.Load(); got != 3 {
		t.Fatalf("computations after the TTL = %d, want 3", got)
	}
}

func TestConcurrentRosterMissesShareOneComputation(t *testing.T) {
	store := &fakeWatchStore{hold: make(chan struct{})}
	service := NewWatchService(WatchConfig{Store: store})
	contest := uuid.New()

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := service.Roster(t.Context(), contest); err != nil {
				t.Error(err)
			}
		})
	}
	// Let every caller arrive at the flight before the one computation ends.
	for store.rosters.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(store.hold)
	wg.Wait()
	if got := store.rosters.Load(); got != 1 {
		t.Errorf("computations = %d, want 1", got)
	}
}
