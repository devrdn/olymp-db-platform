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

// schemaStore is the cache, held in memory so a test can say exactly what is
// in it and see exactly what was written back.
type schemaStore struct {
	mu       sync.Mutex
	schema   provisioning.Schema
	version  int
	present  bool
	saves    int
	saveFail error
	readFail error

	// gate, when set, holds the first gateLeft cache reads until all of them
	// have arrived, and then releases them together. It is what makes the
	// stampede test deterministic rather than a race the scheduler might
	// happen to win.
	gate     chan struct{}
	gateLeft int

	// park, when set, holds the very first cache read — and only that one —
	// until the test closes it, with parked closed to say it is being held.
	// It stands in for a caller the scheduler took away between missing the
	// cache and acting on the miss.
	park     chan struct{}
	parked   chan struct{}
	parkOnce sync.Once
}

func (s *schemaStore) CachedSchema(context.Context, uuid.UUID) (provisioning.Schema, int, error) {
	s.mu.Lock()
	schema, version, present, readFail := s.schema, s.version, s.present, s.readFail
	s.mu.Unlock()

	// The gate is held *after* this read's answer is decided rather than
	// before it, and the difference is the whole point. Held before, the
	// callers released together then queue on this one mutex, a save from
	// whichever of them got there first barges in between two of the queued
	// reads, and the rest are told the cache is warm — an unfixed stampede
	// reads as a cache hit and the test passes for the wrong reason. Held
	// here, every caller has seen the miss it is about to act on.
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

// arrive blocks until every caller the gate is waiting for has reached it.
// Once the gate has opened it never closes again, so reads after the first
// gateLeft of them pass straight through.
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

// parkFirst holds the first cache read — the one that has already decided it
// missed — until the test lets it go.
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

// schemaSource stands in for the game cluster: it is asked for one database's
// shape and records that it was asked.
type schemaSource struct {
	mu     sync.Mutex
	schema provisioning.Schema
	asked  []string
	fail   error

	// holdFor names the one database whose read blocks until hold is closed —
	// empty meaning every read blocks — with entered closed once such a read
	// is under way and arrivals carrying one token per read that started.
	// Together they let a test pin catalogue reads open and ask what the rest
	// of the process can still do while they are, and count how many reads a
	// stampede really produced rather than how many finished first.
	holdFor  string
	hold     chan struct{}
	entered  chan struct{}
	arrivals chan struct{}
	once     sync.Once

	// liveAtCall and deadlineAtCall describe the context the cluster read was
	// actually handed, sampled while the call is happening. The context itself
	// would be useless kept: the caller cancels it on the way out, so anything
	// asked of it afterwards says "cancelled" no matter what was true during
	// the read.
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

// contextGiven reports whether the cluster was asked at all, whether the
// context it was handed was already dead, and whether it carried a deadline.
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

// twoTables is the shape the console's panel draws: a table with a foreign
// key pointing at another one.
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

// The whole reason this cache exists: every instance of a contest is a copy
// of one template, so hundreds of participants opening the console must cost
// one catalogue read between them, not one each.
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

// The moment the cache is worth having is the moment it is empty: three
// hundred participants opening /play in the same minute, before the first
// answer has been written back. Every one of those misses opens its own fresh
// connection to its own instance database on the game cluster and then queues
// behind one row lock writing the same document back. Concurrent misses for
// one contest have to collapse into a single read.
//
// The property, not the mechanism: N callers, one call to the cluster.
//
// Two things make that a real question rather than one the test double
// answers for us. The store's gate holds every caller until all of them have
// missed, so nobody is let through on a cache the stampede itself warmed. And
// the cluster read is held open until the callers have stopped arriving at
// it, so what is counted is how many reads a stampede *starts* — count only
// the ones that finish and a fast in-memory double lets the losers find a
// warm cache and the missing collapse look like a working one.
func TestSchemaReadsTheClusterOnceForConcurrentMisses(t *testing.T) {
	t.Parallel()

	const callers = 32
	// Long enough that thirty-two goroutines with nothing to do but call one
	// method have all called it; spent only when there is nothing to wait for,
	// which is the case this test hopes to be in. A slow machine that spends
	// it early still counts more than one read and still fails.
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
			// Each participant reads their own instance database — every one
			// of them a byte-for-byte copy of the same template, which is the
			// whole reason one read can answer all of them.
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

// A caller that missed the cache and was then taken off the processor must
// not act on a miss that has since stopped being true. Its flight has already
// finished by the time it arrives, so there is no one left to dedupe with —
// and a stale miss acted on is one more connection to the game cluster asking
// a question that is now answered. The answer is looked for again inside the
// flight, which is where this caller finds it.
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

// Collapsing one contest's misses must not serialise the whole installation.
// Several contests can be running at once, and a catalogue read on a wedged
// cluster takes as long as its statement timeout allows — behind one
// process-wide lock that would be every other contest's console, closed.
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

// The shared read belongs to the contest, not to whichever request happened
// to arrive first. A browser that navigates away mid-read would otherwise
// cancel the catalogue read every other participant is waiting behind — the
// stampede fix turning one abandoned request into two hundred and ninety-nine
// broken consoles. So the read is detached from the caller's cancellation and
// carries a deadline of its own instead; the context the cluster is handed is
// the only place that difference is visible.
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

// A rebuild bumps the template's version. Serving the shape the old build had
// would show a participant tables their own database does not have.
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

// The cache is an optimisation. A participant whose console cannot open
// because writing it back failed would be paying for our convenience.
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

// Reading the cache failing is the same case: the cluster still knows.
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

// The cluster failing is the one failure that is real: there is nothing to
// show, and saying so is better than an empty panel that reads as "this game
// has no tables".
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

// CLAUDE.md rule 2: every list that reaches storage has an explicit bound.
// The init script is an organiser's own SQL, so the table count is theirs to
// choose, and this document is both stored as one jsonb value and sent to
// every participant's browser.
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
