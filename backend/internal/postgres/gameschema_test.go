package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

func aGameWithNoSchemaYet(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	author := makeUser(t, ctx, "schema-"+uuid.NewString()[:8])
	contest := makeContest(t, ctx, author.ID)
	reclaimTemplate(t, ctx, contest, "ready")
	return contest
}

func aSchema() provisioning.Schema {
	return provisioning.Schema{Tables: []provisioning.Table{
		{Name: "rooms", Columns: []provisioning.Column{{Name: "id", Type: "uuid"}}},
		{Name: "guests", Columns: []provisioning.Column{
			{Name: "id", Type: "uuid"},
			{Name: "room_id", Type: "uuid", Nullable: true, References: "rooms"},
		}},
	}}
}

func TestTheGamesSchemaSurvivesAWriteAndARead(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aGameWithNoSchemaYet(t, ctx)
		repo := NewGameInstances(testPool)

		if _, _, err := repo.CachedSchema(ctx, contest); !errors.Is(err, provisioning.ErrNoSchema) {
			t.Fatalf("an unread game answered %v, want ErrNoSchema", err)
		}

		rebuild(t, ctx, contest, 3)
		if err := repo.SaveSchema(ctx, contest, 3, aSchema()); err != nil {
			t.Fatalf("save: %v", err)
		}

		got, version, err := repo.CachedSchema(ctx, contest)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if version != 3 {
			t.Fatalf("read back version %d, want 3", version)
		}
		if len(got.Tables) != 2 || got.Tables[1].Name != "guests" {
			t.Fatalf("read back %+v", got.Tables)
		}
		// The only field with omitempty, so the likeliest to be lost.
		if got.Tables[1].Columns[1].References != "rooms" {
			t.Fatalf("the foreign key did not survive the round trip: %+v", got.Tables[1].Columns[1])
		}
		if !got.Tables[1].Columns[1].Nullable {
			t.Fatal("nullability did not survive the round trip")
		}
	})
}

// The keys and the format are what decide whether a cached document is drawn
// or read again, so losing either in the jsonb would re-read every game on
// every request.
func TestTheGamesKeysAndFormatSurviveAWriteAndARead(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aGameWithNoSchemaYet(t, ctx)
		repo := NewGameInstances(testPool)

		schema := aSchema()
		schema.Format = provisioning.SchemaFormat
		schema.Tables[0].PrimaryKey = []string{"id"}
		schema.Tables[1].ForeignKeys = []provisioning.ForeignKey{{
			Name: "guests_room_id_fkey", Columns: []string{"room_id"},
			ReferencedTable: "rooms", ReferencedColumns: []string{"id"},
		}}
		rebuild(t, ctx, contest, 2)
		if err := repo.SaveSchema(ctx, contest, 2, schema); err != nil {
			t.Fatalf("save: %v", err)
		}

		got, _, err := repo.CachedSchema(ctx, contest)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if !reflect.DeepEqual(got, schema) {
			t.Fatalf("read back %+v, want %+v", got, schema)
		}
	})
}

func TestSavingTheSchemaAgainReplacesBoththeDocumentAndItsVersion(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aGameWithNoSchemaYet(t, ctx)
		repo := NewGameInstances(testPool)

		if err := repo.SaveSchema(ctx, contest, 1, aSchema()); err != nil {
			t.Fatalf("first save: %v", err)
		}
		second := provisioning.Schema{Tables: []provisioning.Table{{Name: "only_this"}}}
		rebuild(t, ctx, contest, 2)
		if err := repo.SaveSchema(ctx, contest, 2, second); err != nil {
			t.Fatalf("second save: %v", err)
		}

		got, version, err := repo.CachedSchema(ctx, contest)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if version != 2 || len(got.Tables) != 1 || got.Tables[0].Name != "only_this" {
			t.Fatalf("read back version %d, %+v", version, got.Tables)
		}
	})
}

// A reader still holding an older build, or a superseded build, must not
// replace the description of the build the contest now runs.
func TestASchemaSavedForAnOlderVersionChangesNothing(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aGameWithNoSchemaYet(t, ctx)
		repo := NewGameInstances(testPool)
		rebuild(t, ctx, contest, 2)

		current := provisioning.Schema{Tables: []provisioning.Table{{Name: "as_built"}}}
		if err := repo.SaveSchema(ctx, contest, 2, current); err != nil {
			t.Fatalf("save the current build's schema: %v", err)
		}
		stale := provisioning.Schema{Tables: []provisioning.Table{{Name: "from_an_old_copy"}}}
		if err := repo.SaveSchema(ctx, contest, 1, stale); err != nil {
			t.Fatalf("a stale save is a no-op, not an error: %v", err)
		}

		got, version, err := repo.CachedSchema(ctx, contest)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if version != 2 || len(got.Tables) != 1 || got.Tables[0].Name != "as_built" {
			t.Fatalf("read back version %d, %+v; the stale save replaced the current build's", version, got.Tables)
		}
	})
}

// rebuild moves the game row to version, as RequestBuild and a finished
// build would.
func rebuild(t *testing.T, ctx context.Context, contest uuid.UUID, version int) {
	t.Helper()
	if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`UPDATE game_templates SET version = $2 WHERE contest_id = $1`, contest, version); err != nil {
		t.Fatalf("move the game to version %d: %v", version, err)
	}
}

func TestAnUnreadableCachedSchemaReadsAsNotCached(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aGameWithNoSchemaYet(t, ctx)
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE game_templates SET schema_json = '"not an object"'::jsonb, schema_version = 1
			 WHERE contest_id = $1`, contest); err != nil {
			t.Fatalf("plant a bad document: %v", err)
		}

		if _, _, err := NewGameInstances(testPool).CachedSchema(ctx, contest); !errors.Is(err, provisioning.ErrNoSchema) {
			t.Fatalf("a document that cannot be parsed answered %v, want ErrNoSchema", err)
		}
	})
}

// The pairing constraint from migration 22.
func TestTheSchemaAndItsVersionCannotBeSetApart(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aGameWithNoSchemaYet(t, ctx)
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE game_templates SET schema_version = 1 WHERE contest_id = $1`, contest); err == nil {
			t.Fatal("a version with no document was accepted")
		}
	})
}
