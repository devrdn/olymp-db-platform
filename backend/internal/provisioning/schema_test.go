package provisioning_test

import (
	"context"
	"errors"
	"sync"
	"testing"

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
}

func (s *schemaStore) CachedSchema(context.Context, uuid.UUID) (provisioning.Schema, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readFail != nil {
		return provisioning.Schema{}, 0, s.readFail
	}
	if !s.present {
		return provisioning.Schema{}, 0, provisioning.ErrNoSchema
	}
	return s.schema, s.version, nil
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
}

func (s *schemaSource) ReadSchema(_ context.Context, database string) (provisioning.Schema, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, database)
	if s.fail != nil {
		return provisioning.Schema{}, s.fail
	}
	return s.schema, nil
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
