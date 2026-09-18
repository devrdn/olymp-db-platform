package monitor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
)

// fakeWatchStore counts the computations and can hold them until released.
type fakeWatchStore struct {
	WatchStore
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

// auditSink keeps what the recorder writes, and can refuse.
type auditSink struct {
	mu      sync.Mutex
	entries []audit.Entry
	fail    bool
}

func (s *auditSink) Append(_ context.Context, e audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("the trail is down")
	}
	s.entries = append(s.entries, e)
	return nil
}

func (s *auditSink) AppendMany(ctx context.Context, entries []audit.Entry) error {
	for _, e := range entries {
		if err := s.Append(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

func (s *auditSink) count(action string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.entries {
		if e.Action == action {
			n++
		}
	}
	return n
}

func TestAViewIsRecordedOncePerPairPerWindow(t *testing.T) {
	sink := &auditSink{}
	marks := cache.NewMemory(100)
	t.Cleanup(func() { _ = marks.Close() })
	service := NewWatchService(WatchConfig{Store: &fakeWatchStore{}, Audit: audit.New(sink), Marks: marks})
	staff, other, contest, reg := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ctx := t.Context()

	for range 5 {
		if err := service.RecordView(ctx, staff, contest, reg); err != nil {
			t.Fatal(err)
		}
	}
	if got := sink.count(audit.ActionContestMonitorView); got != 1 {
		t.Fatalf("five views of one participant: %d entries, want 1", got)
	}
	// Another participant, the contest itself, and another viewer are other pairs.
	for _, view := range [][3]uuid.UUID{{staff, contest, uuid.New()}, {staff, contest, uuid.Nil}, {other, contest, reg}} {
		if err := service.RecordView(ctx, view[0], view[1], view[2]); err != nil {
			t.Fatal(err)
		}
	}
	if got := sink.count(audit.ActionContestMonitorView); got != 4 {
		t.Fatalf("four pairs: %d entries, want 4", got)
	}
	first := sink.entries[0]
	if *first.ActorID != staff || first.Entity != "contest" || first.EntityID != contest.String() ||
		first.Payload["registration_id"] != reg.String() {
		t.Errorf("entry = %+v", first)
	}
}

func TestAViewThatCannotBeRecordedIsRefusedAndTriedAgain(t *testing.T) {
	sink := &auditSink{fail: true}
	marks := cache.NewMemory(100)
	t.Cleanup(func() { _ = marks.Close() })
	service := NewWatchService(WatchConfig{Store: &fakeWatchStore{}, Audit: audit.New(sink), Marks: marks})
	staff, contest, reg := uuid.New(), uuid.New(), uuid.New()

	if err := service.RecordView(t.Context(), staff, contest, reg); err == nil {
		t.Fatal("a view the trail refused was let through")
	}
	sink.fail = false
	if err := service.RecordView(t.Context(), staff, contest, reg); err != nil {
		t.Fatal(err)
	}
	if got := sink.count(audit.ActionContestMonitorView); got != 1 {
		t.Errorf("after the trail came back: %d entries, want 1", got)
	}
}

func TestEveryExportIsRecorded(t *testing.T) {
	sink := &auditSink{}
	marks := cache.NewMemory(100)
	t.Cleanup(func() { _ = marks.Close() })
	service := NewWatchService(WatchConfig{Store: &fakeWatchStore{}, Audit: audit.New(sink), Marks: marks})
	staff, contest := uuid.New(), uuid.New()
	for range 3 {
		if err := service.RecordExport(t.Context(), staff, contest, uuid.Nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := sink.count(audit.ActionContestMonitorExport); got != 3 {
		t.Errorf("three exports: %d entries, want 3", got)
	}
}
