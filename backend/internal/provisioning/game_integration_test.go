package provisioning_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// The whole chain, across both clusters, on the path a deployment actually
// uses (CLAUDE.md rule 10).
//
// Every other test of this feature stops at a boundary: the domain sees a
// fake cluster, the repository sees no cluster at all, and the handler sees
// neither. What none of them can show is the one thing that matters before a
// deploy — that a script an organiser saves in the core database ends up as a
// real database on the game cluster with their tables in it. `BuildTemplate`
// sat in this codebase for months with no caller precisely because nothing
// ever went end to end.
func TestAScriptSavedInTheCoreDatabaseBecomesARealDatabaseOnTheGameCluster(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-game-build`")
	}
	if os.Getenv("GAME_DB_DSN") == "" {
		t.Skip("GAME_DB_DSN is not set; run `make test-game-build`")
	}

	contest, _ := contestFor(t, 0)
	repo := postgres.NewGameInstances(testPool)

	user, password := gamedbtest.AdminCredentials(t)
	cluster, err := gamedb.NewProvisioner(gamedbtest.Admin(t), gamedbtest.DSN(t, user, password, "postgres"))
	if err != nil {
		t.Fatalf("open the game cluster: %v", err)
	}

	games := provisioning.NewGames(repo, cluster, editableContest{})

	saved, err := games.SetScript(t.Context(), uuid.New(), contest.ID, `
		CREATE TABLE guests (id uuid PRIMARY KEY, full_name text NOT NULL);
		CREATE TABLE keycard_events (guest_id uuid REFERENCES guests, door text);
		INSERT INTO guests VALUES (gen_random_uuid(), 'Margot Feilhaber');
	`)
	if err != nil {
		t.Fatalf("save the script: %v", err)
	}
	t.Cleanup(func() { gamedbtest.Drop(saved.Database) })

	built, err := games.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.Status != provisioning.TemplateReady {
		t.Fatalf("the build finished as %q: %s", built.Status, built.BuildError)
	}

	// The row says ready...
	stored, err := repo.Template(t.Context(), contest.ID)
	if err != nil {
		t.Fatalf("read the game back: %v", err)
	}
	if stored.Status != provisioning.TemplateReady {
		t.Fatalf("the stored game is %q, want ready", stored.Status)
	}

	// ...and the database it names really exists, with the author's tables and
	// their row in it. This is the assertion the whole test is for.
	conn := gamedbtest.Connect(t, user, password, built.Database)
	defer func() { _ = conn.Close(context.Background()) }()

	var name string
	if err := conn.QueryRow(t.Context(), `SELECT full_name FROM guests`).Scan(&name); err != nil {
		t.Fatalf("read the built game: %v", err)
	}
	if name != "Margot Feilhaber" {
		t.Fatalf("the built game holds %q", name)
	}

	// And the schema panel can describe it, which is the other half of what a
	// participant sees — read here through the same reader the API uses.
	schema, err := provisioning.NewSchemaReader(repo, cluster).
		Schema(t.Context(), provisioning.Contest{ID: contest.ID, Version: stored.Version}, built.Database)
	if err != nil {
		t.Fatalf("read the schema of the built game: %v", err)
	}
	var sawForeignKey bool
	for _, table := range schema.Tables {
		for _, column := range table.Columns {
			if table.Name == "keycard_events" && column.Name == "guest_id" && column.References == "guests" {
				sawForeignKey = true
			}
		}
	}
	if len(schema.Tables) != 2 || !sawForeignKey {
		t.Fatalf("the schema of the built game came back as %+v", schema.Tables)
	}
}

// editableContest stands for a draft contest: the gate this test is not about.
type editableContest struct{}

func (editableContest) GameEditable(context.Context, uuid.UUID) (bool, error) { return true, nil }
