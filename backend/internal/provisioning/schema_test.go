package provisioning_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// schemaStore is an in-memory schema cache. SaveSchema is not on the reader's
// interface; it is a trap, so a reader that saved by any route would be
// counted.
type schemaStore struct {
	mu       sync.Mutex
	schema   provisioning.Schema
	version  int
	present  bool
	saves    int
	readFail error
}

func (s *schemaStore) CachedSchema(context.Context, uuid.UUID) (provisioning.Schema, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.readFail != nil:
		return provisioning.Schema{}, 0, s.readFail
	case !s.present:
		return provisioning.Schema{}, 0, provisioning.ErrNoSchema
	}
	return s.schema, s.version, nil
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
	s.schema, s.version, s.present = schema, version, true
	return nil
}

// schemaSource is a fake game cluster that records what it was asked.
type schemaSource struct {
	mu     sync.Mutex
	schema provisioning.Schema
	// byDatabase, when it names the database read, answers in place of schema:
	// one participant's copy that has drifted from another's.
	byDatabase map[string]provisioning.Schema
	asked      []string
	fail       error

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
	if own, ok := s.byDatabase[database]; ok {
		return own, nil
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

// A ready game with nothing stored (built before the build described it)
// answers each participant from their own copy, and keeps none of it.
func TestSchemaWithNothingStoredAnswersEachParticipantFromTheirOwnCopy(t *testing.T) {
	t.Parallel()

	first := twoTables()
	second := twoTables()
	second.Tables = append(second.Tables, provisioning.Table{Name: "planted_by_r2"})
	store := &schemaStore{}
	source := &schemaSource{byDatabase: map[string]provisioning.Schema{"game_c1_r1": first, "game_c1_r2": second}}
	reader := provisioning.NewSchemaReader(store, source)
	contest := game(1)

	for _, step := range []struct {
		database string
		tables   int
	}{{"game_c1_r2", 3}, {"game_c1_r1", 2}, {"game_c1_r2", 3}} {
		got, err := reader.Schema(context.Background(), contest, step.database)
		if err != nil {
			t.Fatalf("%s: %v", step.database, err)
		}
		if len(got.Tables) != step.tables {
			t.Fatalf("%s was answered with %d tables, want its own copy's %d", step.database, len(got.Tables), step.tables)
		}
	}
	if got := source.timesAsked(); got != 3 {
		t.Fatalf("read copies %d times for three requests, want 3", got)
	}
	if got := store.saveCount(); got != 0 {
		t.Fatalf("saved a participant's copy %d times; only a build writes the description", got)
	}
}

// One slow copy read must not hold up another participant's.
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

// A copy read carries a deadline of its own, so a cluster that never answers
// does not hold a request open past it.
func TestSchemaReadOfACopyHasADeadline(t *testing.T) {
	t.Parallel()

	source := &schemaSource{schema: twoTables()}
	reader := provisioning.NewSchemaReader(&schemaStore{}, source)

	if _, err := reader.Schema(context.Background(), game(1), "game_c1_r1"); err != nil {
		t.Fatalf("read: %v", err)
	}
	called, _, deadline := source.contextGiven()
	if !called || !deadline {
		t.Fatalf("asked the cluster: %v, with a deadline: %v", called, deadline)
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
	if store.saveCount() != 0 || store.version != 1 {
		t.Fatal("cached a participant's copy; only a build writes the description")
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

// The cache holds what the build read from the template. A participant's copy
// read only because that could not be read must not replace it.
func TestSchemaDoesNotCacheACopyReadBecauseTheCacheCouldNotBeRead(t *testing.T) {
	t.Parallel()

	store := &schemaStore{readFail: errors.New("the core database is down")}
	source := &schemaSource{schema: twoTables()}
	reader := provisioning.NewSchemaReader(store, source)

	if _, err := reader.Schema(context.Background(), game(1), "game_c1_r1"); err != nil {
		t.Fatalf("a failed cache read refused the answer: %v", err)
	}
	if store.saveCount() != 0 {
		t.Fatal("a participant's copy was written over the build's description")
	}
}

// A cache that cannot be read sends every participant to their own copy until
// the next build, so the operator is told, once per contest rather than once
// per request. Nothing stored is the ordinary state of a legacy game and is
// not reported.
func TestSchemaReportsAnUnreadableCacheOncePerContest(t *testing.T) {
	t.Parallel()

	var logged bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logged, nil))
	source := &schemaSource{schema: twoTables()}
	broken := provisioning.NewSchemaReader(&schemaStore{readFail: errors.New("decode the stored schema")}, source).WithLog(log)
	first, second := game(1), game(1)
	for _, contest := range []provisioning.Contest{first, first, second} {
		if _, err := broken.Schema(context.Background(), contest, "game_c1_r1"); err != nil {
			t.Fatalf("a failed cache read refused the answer: %v", err)
		}
	}
	empty := provisioning.NewSchemaReader(&schemaStore{}, source).WithLog(log)
	if _, err := empty.Schema(context.Background(), game(1), "game_c1_r1"); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("logged %d lines, want one per contest with an unreadable cache:\n%s", len(lines), logged.String())
	}
	for i, contest := range []provisioning.Contest{first, second} {
		if !strings.Contains(lines[i], `"level":"WARN"`) || !strings.Contains(lines[i], contest.ID.String()) ||
			!strings.Contains(lines[i], "decode the stored schema") {
			t.Errorf("line %d = %s, want a warning naming the contest and the error", i, lines[i])
		}
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

// A document cached before keys were described has none, and is served as
// it is: the game gains its arrows at its next build. Reading the
// participant's copy instead would cache, for everyone and for the rest of a
// running contest, whatever that copy's tables had become (ARCHITECTURE.md
// §15, item 49); reading the ready template would break the copies made from
// it.
func TestSchemaServesADocumentCachedBeforeKeysWereDescribed(t *testing.T) {
	t.Parallel()

	store := &schemaStore{present: true, version: 1, schema: twoTables()}
	withKeys := twoTables()
	withKeys.Tables[1].ForeignKeys = []provisioning.ForeignKey{{
		Name: "keycard_events_guest_id_fkey", Columns: []string{"guest_id"},
		ReferencedTable: "guests", ReferencedColumns: []string{"id"},
	}}
	source := &schemaSource{schema: withKeys}
	reader := provisioning.NewSchemaReader(store, source)

	got, err := reader.Schema(context.Background(), game(1), "game_c1_r1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if source.timesAsked() != 0 {
		t.Fatalf("read a copy %d times for a game whose description is cached", source.timesAsked())
	}
	if store.saveCount() != 0 {
		t.Fatal("replaced the build's description")
	}
	if len(got.Tables) != 2 {
		t.Fatalf("served %d tables, want the cached 2", len(got.Tables))
	}
	for _, table := range got.Tables {
		if len(table.ForeignKeys) != 0 || len(table.PrimaryKey) != 0 {
			t.Fatalf("served keys the cached document does not carry: %+v", table)
		}
	}
}

// CLAUDE.md rule 2.
func TestSchemaKeysAreBounded(t *testing.T) {
	t.Parallel()

	wide := make([]string, provisioning.MaxKeyColumns+1)
	for i := range wide {
		wide[i] = fmt.Sprintf("c%d", i)
	}
	keys := make([]provisioning.ForeignKey, provisioning.MaxSchemaForeignKeys+3)
	for i := range keys {
		keys[i] = provisioning.ForeignKey{
			Name: fmt.Sprintf("fk%d", i), Columns: []string{"a"},
			ReferencedTable: "t", ReferencedColumns: []string{"id"},
		}
	}
	tooWideKey := provisioning.ForeignKey{Name: "wide", Columns: wide, ReferencedTable: "t", ReferencedColumns: wide}
	schema := provisioning.Schema{Tables: []provisioning.Table{
		{Name: "many", Columns: []provisioning.Column{{Name: "a", Type: "int"}}, ForeignKeys: keys},
		{Name: "wide", Columns: []provisioning.Column{{Name: "a", Type: "int"}},
			PrimaryKey: wide, ForeignKeys: []provisioning.ForeignKey{tooWideKey}},
	}}

	reader := provisioning.NewSchemaReader(&schemaStore{}, &schemaSource{schema: schema})
	got, err := reader.Schema(context.Background(), game(1), "game_c1_r1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n := len(got.Tables[0].ForeignKeys); n != provisioning.MaxSchemaForeignKeys {
		t.Fatalf("kept %d foreign keys, want the bound of %d", n, provisioning.MaxSchemaForeignKeys)
	}
	// Half a key is a wrong key, so one past the bound is dropped whole.
	if len(got.Tables[1].PrimaryKey) != 0 || len(got.Tables[1].ForeignKeys) != 0 {
		t.Fatalf("kept a key wider than %d columns: %+v", provisioning.MaxKeyColumns, got.Tables[1])
	}
	if !got.Truncated {
		t.Fatal("cut the keys down without saying so")
	}
}

// CLAUDE.md rule 2: the per-table cap alone allows 40,000 keys in one
// document, read and sent on every schema request.
func TestSchemaForeignKeysAreBoundedAcrossTheDocument(t *testing.T) {
	t.Parallel()

	withKeys := func(total int) provisioning.Schema {
		var schema provisioning.Schema
		for i := 0; total > 0; i++ {
			n := min(total, provisioning.MaxSchemaForeignKeys)
			total -= n
			keys := make([]provisioning.ForeignKey, n)
			for k := range keys {
				keys[k] = provisioning.ForeignKey{
					Name: fmt.Sprintf("t%d_fk%d", i, k), Columns: []string{"a"},
					ReferencedTable: "t0", ReferencedColumns: []string{"a"},
				}
			}
			schema.Tables = append(schema.Tables, provisioning.Table{
				Name: fmt.Sprintf("t%d", i), Columns: []provisioning.Column{{Name: "a", Type: "int"}}, ForeignKeys: keys,
			})
		}
		return schema
	}
	count := func(schema provisioning.Schema) int {
		n := 0
		for _, table := range schema.Tables {
			n += len(table.ForeignKeys)
		}
		return n
	}

	for _, tc := range []struct {
		name      string
		total     int
		kept      int
		truncated bool
	}{
		{"at the bound", provisioning.MaxDocumentForeignKeys, provisioning.MaxDocumentForeignKeys, false},
		{"one past it", provisioning.MaxDocumentForeignKeys + 1, provisioning.MaxDocumentForeignKeys, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader := provisioning.NewSchemaReader(&schemaStore{}, &schemaSource{schema: withKeys(tc.total)})

			got, err := reader.Schema(context.Background(), game(1), "game_c1_r1")
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if n := count(got); n != tc.kept {
				t.Fatalf("kept %d foreign keys, want %d", n, tc.kept)
			}
			if got.Truncated != tc.truncated {
				t.Fatalf("truncated = %v, want %v", got.Truncated, tc.truncated)
			}
		})
	}
}

// Without a stored description every participant reads their own copy, so at
// a contest's start they would all open a provisioning connection at once.
func TestSchemaReadsOnlyAFewCopiesAtOnce(t *testing.T) {
	t.Parallel()

	const callers = 12
	const settle = 250 * time.Millisecond
	source := &schemaSource{schema: twoTables(), hold: make(chan struct{}), arrivals: make(chan struct{}, callers)}
	reader := provisioning.NewSchemaReader(&schemaStore{}, source)
	contest := game(1)

	var wg sync.WaitGroup
	failures := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := reader.Schema(context.Background(), contest, fmt.Sprintf("game_c1_r%d", i)); err != nil {
				failures <- fmt.Errorf("caller %d: %w", i, err)
			}
		}(i)
	}

	started := 0
	arriving := time.After(settle)
counting:
	for {
		select {
		case <-source.arrivals:
			started++
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

	if started != provisioning.MaxCopyReads {
		t.Fatalf("%d copy reads were under way at once, want the bound of %d", started, provisioning.MaxCopyReads)
	}
	if got := source.timesAsked(); got != callers {
		t.Fatalf("read %d copies for %d participants, want one each", got, callers)
	}
}
