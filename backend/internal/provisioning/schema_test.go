package provisioning_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// schemaStore is an in-memory schema cache.
type schemaStore struct {
	mu       sync.Mutex
	schema   provisioning.Schema
	version  int
	present  bool
	saves    int
	saveFail error
	readFail error

	// gate, when set, holds the first gateLeft cache reads until all of them
	// have arrived, then releases them together.
	gate     chan struct{}
	gateLeft int

	// park, when set, holds only the first cache read until closed; parked is
	// closed once it is held. It models a caller descheduled after a miss.
	park     chan struct{}
	parked   chan struct{}
	parkOnce sync.Once
}

func (s *schemaStore) CachedSchema(context.Context, uuid.UUID) (provisioning.Schema, int, error) {
	s.mu.Lock()
	schema, version, present, readFail := s.schema, s.version, s.present, s.readFail
	s.mu.Unlock()

	// Gated after the answer is decided: gated before, an early save could
	// let later callers see a warm cache and hide an unfixed stampede.
	s.arrive()
	s.parkFirst()

	switch {
	case readFail != nil:
		return provisioning.Schema{}, 0, readFail
	case !present:
		return provisioning.Schema{}, 0, provisioning.ErrNoSchema
	}
	return schema, version, nil
}

// arrive blocks until every gated caller has arrived. Once open, the gate
// stays open.
func (s *schemaStore) arrive() {
	s.mu.Lock()
	gate := s.gate
	if gate == nil {
		s.mu.Unlock()
		return
	}
	if s.gateLeft > 0 {
		s.gateLeft--
		if s.gateLeft == 0 {
			close(gate)
		}
	}
	s.mu.Unlock()
	<-gate
}

func (s *schemaStore) parkFirst() {
	if s.park == nil {
		return
	}
	held := false
	s.parkOnce.Do(func() { held = true; close(s.parked) })
	if held {
		<-s.park
	}
}

func (s *schemaStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

func (s *schemaStore) SaveSchema(_ context.Context, _ uuid.UUID, version int, schema provisioning.Schema) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if s.saveFail != nil {
		return s.saveFail
	}
	s.schema, s.version, s.present = schema, version, true
	return nil
}

// schemaSource is a fake game cluster that records what it was asked.
type schemaSource struct {
	mu     sync.Mutex
	schema provisioning.Schema
	asked  []string
	fail   error

	// holdFor names the database whose read blocks until hold is closed
	// (empty: every read). entered is closed once such a read is under way;
	// arrivals gets one token per read started.
	holdFor  string
	hold     chan struct{}
	entered  chan struct{}
	arrivals chan struct{}
	once     sync.Once

	// liveAtCall and deadlineAtCall sample the context during the call; the
	// context itself is cancelled by the caller on the way out.
	called         bool
	liveAtCall     error
	deadlineAtCall bool
}

func (s *schemaSource) ReadSchema(ctx context.Context, database string) (provisioning.Schema, error) {
	_, deadline := ctx.Deadline()

	s.mu.Lock()
	s.asked = append(s.asked, database)
	s.called, s.liveAtCall, s.deadlineAtCall = true, ctx.Err(), deadline
	hold, holdFor, fail, schema := s.hold, s.holdFor, s.fail, s.schema
	s.mu.Unlock()

	if s.arrivals != nil {
		s.arrivals <- struct{}{}
	}
	if hold != nil && (holdFor == "" || database == holdFor) {
		if s.entered != nil {
			s.once.Do(func() { close(s.entered) })
		}
		<-hold
	}
	if fail != nil {
		return provisioning.Schema{}, fail
	}
	return schema, nil
}

func (s *schemaSource) contextGiven() (called bool, live error, deadline bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.called, s.liveAtCall, s.deadlineAtCall
}

func (s *schemaSource) timesAsked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.asked)
}

func twoTables() provisioning.Schema {
	return provisioning.Schema{Tables: []provisioning.Table{
		{Name: "guests", Columns: []provisioning.Column{
			{Name: "id", Type: "uuid"},
			{Name: "full_name", Type: "text", Nullable: true},
		}},
		{Name: "keycard_events", Columns: []provisioning.Column{
			{Name: "guest_id", Type: "uuid", References: "guests"},
			{Name: "door", Type: "text", Nullable: true},
		}},
	}}
}

func game(version int) provisioning.Contest {
	return provisioning.Contest{ID: uuid.New(), Template: "game_tpl_c1", Version: version}
}

func TestSchemaReadsTheClusterOnceForEveryoneAfterIt(t *testing.T) {
	t.Parallel()

	store, source := &schemaStore{}, &schemaSource{schema: twoTables()}
	reader := provisioning.NewSchemaReader(store, source)
	contest := game(1)

	first, err := reader.Schema(context.Background(), contest, "game_c1_r1")
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if got := len(first.Tables); got != 2 {
		t.Fatalf("first read returned %d tables, want 2", got)
	}

	for i := 0; i < 5; i++ {
		again, err := reader.Schema(context.Background(), contest, "game_c1_r2")
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if len(again.Tables) != 2 {
			t.Fatalf("read %d returned %d tables, want 2", i, len(again.Tables))
		}
	}

	if got := source.timesAsked(); got != 1 {
		t.Fatalf("asked the cluster %d times, want 1 — the cache is not being used", got)
	}
	if store.saves != 1 {
		t.Fatalf("wrote the cache %d times, want 1", store.saves)
	}
}

// The gate holds every caller until all have missed, and the cluster read is
// held open while callers arrive, so the test counts reads started, not reads
// finished before a winner warmed the cache.
func TestSchemaReadsTheClusterOnceForConcurrentMisses(t *testing.T) {
	t.Parallel()

	const callers = 32
	// Spent in full only when the collapse works; a slow machine that stops
	// early still fails on a second read.
	const settle = 250 * time.Millisecond

	store := &schemaStore{gate: make(chan struct{}), gateLeft: callers}
	source := &schemaSource{
		schema:   twoTables(),
		hold:     make(chan struct{}),
		arrivals: make(chan struct{}, callers),
	}
	reader := provisioning.NewSchemaReader(store, source)
	contest := game(1)

	var wg sync.WaitGroup
	failures := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each participant reads their own copy of the same template.
			got, err := reader.Schema(context.Background(), contest, fmt.Sprintf("game_c1_r%d", i))
			switch {
			case err != nil:
				failures <- fmt.Errorf("caller %d: %w", i, err)
			case len(got.Tables) != 2:
				failures <- fmt.Errorf("caller %d saw %d tables, want 2", i, len(got.Tables))
			}
		}(i)
	}

	arriving := time.After(settle)
counting:
	for i := 0; i < callers; i++ {
		select {
		case <-source.arrivals:
		case <-arriving:
			break counting
		}
	}
	close(source.hold)

	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	if got := source.timesAsked(); got != 1 {
		t.Fatalf("%d concurrent misses read the cluster %d times, want 1", callers, got)
	}
	if got := store.saveCount(); got != 1 {
		t.Fatalf("%d concurrent misses wrote the cache %d times, want 1", callers, got)
	}
}

// The parked caller's flight starts after the other one finished, so only the
// cache check inside the flight saves a second read.
func TestSchemaDoesNotActOnAMissThatWasOvertaken(t *testing.T) {
	t.Parallel()

	store := &schemaStore{park: make(chan struct{}), parked: make(chan struct{})}
	source := &schemaSource{schema: twoTables()}
	reader := provisioning.NewSchemaReader(store, source)
	contest := game(1)

	overtaken := make(chan error, 1)
	go func() {
		_, err := reader.Schema(context.Background(), contest, "game_c1_r1")
		overtaken <- err
	}()
	<-store.parked // it has missed the cache and is holding that miss

	// Somebody else reads the cluster and fills the cache in the meantime.
	if _, err := reader.Schema(context.Background(), contest, "game_c1_r2"); err != nil {
		t.Fatalf("the caller that got there first: %v", err)
	}

	close(store.park)
	if err := <-overtaken; err != nil {
		t.Fatalf("the overtaken caller: %v", err)
	}

	if got := source.timesAsked(); got != 1 {
		t.Fatalf("read the cluster %d times, want 1 — a stale miss was acted on", got)
	}
}

// Collapsing one contest's misses must not serialise other contests.
func TestSchemaLetsAnotherContestThroughWhileOneIsBeingRead(t *testing.T) {
	t.Parallel()

	source := &schemaSource{
		schema:  twoTables(),
		holdFor: "game_slow_r1",
		hold:    make(chan struct{}),
		entered: make(chan struct{}),
	}
	reader := provisioning.NewSchemaReader(&schemaStore{}, source)

	slow := make(chan error, 1)
	go func() {
		_, err := reader.Schema(context.Background(), game(1), "game_slow_r1")
		slow <- err
	}()
	<-source.entered // the slow contest is inside its cluster read and stays there

	quick := make(chan error, 1)
	go func() {
		_, err := reader.Schema(context.Background(), game(1), "game_quick_r1")
		quick <- err
	}()

	select {
	case err := <-quick:
		if err != nil {
			t.Fatalf("the second contest: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a contest waited for an unrelated contest's cluster read to finish")
	}

	close(source.hold)
	if err := <-slow; err != nil {
		t.Fatalf("the first contest: %v", err)
	}
}

// The shared read drops the caller's cancellation and carries its own
// deadline, so one abandoned request does not fail everyone waiting on it.
func TestSchemaReadIsNotCancelledByTheCallerThatStartedIt(t *testing.T) {
	t.Parallel()

	store, source := &schemaStore{}, &schemaSource{schema: twoTables()}
	reader := provisioning.NewSchemaReader(store, source)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := reader.Schema(ctx, game(1), "game_c1_r1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got.Tables) != 2 {
		t.Fatalf("returned %d tables, want 2", len(got.Tables))
	}

	called, live, deadline := source.contextGiven()
	if !called {
		t.Fatal("the cluster was never asked")
	}
	if live != nil {
		t.Fatalf("the cluster read carried the caller's cancellation: %v", live)
	}
	if !deadline {
		t.Fatal("the cluster read was detached from the caller without a deadline of its own")
	}
}

func TestSchemaIsReReadWhenTheTemplateWasRebuilt(t *testing.T) {
	t.Parallel()

	store := &schemaStore{present: true, version: 1, schema: provisioning.Schema{
		Tables: []provisioning.Table{{Name: "the_old_shape"}},
	}}
	source := &schemaSource{schema: twoTables()}
	reader := provisioning.NewSchemaReader(store, source)

	got, err := reader.Schema(context.Background(), game(2), "game_c1_r1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got.Tables) != 2 || got.Tables[0].Name != "guests" {
		t.Fatalf("served the cache from version 1 for a version 2 template: %+v", got.Tables)
	}
	if source.timesAsked() != 1 {
		t.Fatal("a rebuilt template did not re-read the cluster")
	}
	if store.version != 2 {
		t.Fatalf("cached the fresh read as version %d, want 2", store.version)
	}
}

func TestSchemaIsAnsweredEvenWhenTheCacheCannotBeWritten(t *testing.T) {
	t.Parallel()

	store := &schemaStore{saveFail: errors.New("the core database is down")}
	source := &schemaSource{schema: twoTables()}
	reader := provisioning.NewSchemaReader(store, source)

	got, err := reader.Schema(context.Background(), game(1), "game_c1_r1")
	if err != nil {
		t.Fatalf("a failed cache write refused the answer: %v", err)
	}
	if len(got.Tables) != 2 {
		t.Fatalf("returned %d tables, want 2", len(got.Tables))
	}
}

func TestSchemaFallsBackToTheClusterWhenTheCacheCannotBeRead(t *testing.T) {
	t.Parallel()

	store := &schemaStore{readFail: errors.New("the core database is down")}
	source := &schemaSource{schema: twoTables()}
	reader := provisioning.NewSchemaReader(store, source)

	got, err := reader.Schema(context.Background(), game(1), "game_c1_r1")
	if err != nil {
		t.Fatalf("a failed cache read refused the answer: %v", err)
	}
	if len(got.Tables) != 2 {
		t.Fatalf("returned %d tables, want 2", len(got.Tables))
	}
}

func TestSchemaReportsAClusterThatCannotBeRead(t *testing.T) {
	t.Parallel()

	store := &schemaStore{}
	source := &schemaSource{fail: errors.New("connection refused")}
	reader := provisioning.NewSchemaReader(store, source)

	if _, err := reader.Schema(context.Background(), game(1), "game_c1_r1"); err == nil {
		t.Fatal("a cluster that could not be read answered with a schema")
	}
	if store.saves != 0 {
		t.Fatal("cached a schema that was never read")
	}
}

// CLAUDE.md rule 2.
func TestSchemaIsBounded(t *testing.T) {
	t.Parallel()

	huge := provisioning.Schema{}
	for i := 0; i < provisioning.MaxSchemaTables+10; i++ {
		columns := make([]provisioning.Column, provisioning.MaxSchemaColumns+5)
		for c := range columns {
			columns[c] = provisioning.Column{Name: "c", Type: "text"}
		}
		huge.Tables = append(huge.Tables, provisioning.Table{Name: "t", Columns: columns})
	}

	store, source := &schemaStore{}, &schemaSource{schema: huge}
	reader := provisioning.NewSchemaReader(store, source)

	got, err := reader.Schema(context.Background(), game(1), "game_c1_r1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got.Tables) != provisioning.MaxSchemaTables {
		t.Fatalf("kept %d tables, want the bound of %d", len(got.Tables), provisioning.MaxSchemaTables)
	}
	if !got.Truncated {
		t.Fatal("cut the schema down without saying so")
	}
	for _, table := range got.Tables {
		if len(table.Columns) != provisioning.MaxSchemaColumns {
			t.Fatalf("table kept %d columns, want the bound of %d", len(table.Columns), provisioning.MaxSchemaColumns)
		}
	}
}
