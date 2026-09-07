package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// aGameWithNoSchemaYet is a contest whose template row exists but whose shape
// has never been worked out — the state every contest is in until the first
// participant opens their console.
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
		// The one field a JSON round trip is most likely to lose, because it
		// is the only one carrying `omitempty` on a non-obvious default.
		if got.Tables[1].Columns[1].References != "rooms" {
			t.Fatalf("the foreign key did not survive the round trip: %+v", got.Tables[1].Columns[1])
		}
		if !got.Tables[1].Columns[1].Nullable {
			t.Fatal("nullability did not survive the round trip")
		}
	})
}

// A rebuild bumps the version, and the pair has to move together — a document
// from one build labelled with another's number is worse than no cache.
func TestSavingTheSchemaAgainReplacesBoththeDocumentAndItsVersion(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aGameWithNoSchemaYet(t, ctx)
		repo := NewGameInstances(testPool)

		if err := repo.SaveSchema(ctx, contest, 1, aSchema()); err != nil {
			t.Fatalf("first save: %v", err)
		}
		second := provisioning.Schema{Tables: []provisioning.Table{{Name: "only_this"}}}
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

// The cache is derived data. A document this build cannot parse must send the
// caller back to the cluster, not fail their console.
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

// The pairing constraint from migration 22: a version with no document, or a
// document with no version, is a row nothing can reason about.
func TestTheSchemaAndItsVersionCannotBeSetApart(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aGameWithNoSchemaYet(t, ctx)
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE game_templates SET schema_version = 1 WHERE contest_id = $1`, contest); err == nil {
			t.Fatal("a version with no document was accepted")
		}
	})
}
