package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

func aContest(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	author := makeUser(t, ctx, "game-"+uuid.NewString()[:8])
	return makeContest(t, ctx, author.ID)
}

func TestSavingAScriptCreatesTheGameAndSavingAgainBumpsItsVersion(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		if _, err := repo.Template(ctx, contest); !errors.Is(err, provisioning.ErrNoGame) {
			t.Fatalf("a contest with no game answered %v, want ErrNoGame", err)
		}

		first, err := repo.SaveScript(ctx, contest, "game_tpl_cabc", `CREATE TABLE a (x int);`)
		if err != nil {
			t.Fatalf("first save: %v", err)
		}
		if first.Version != 1 || first.Status != provisioning.TemplatePending {
			t.Fatalf("first save produced version %d, status %q", first.Version, first.Status)
		}

		second, err := repo.SaveScript(ctx, contest, "game_tpl_cabc", `CREATE TABLE b (x int);`)
		if err != nil {
			t.Fatalf("second save: %v", err)
		}
		// The version is the whole rebuild mechanism: every instance carries
		// the one it was copied from, and raising it is what makes the
		// existing copies stale.
		if second.Version != 2 {
			t.Fatalf("second save produced version %d, want 2", second.Version)
		}
		if second.Script != `CREATE TABLE b (x int);` {
			t.Fatalf("second save stored %q", second.Script)
		}
	})
}

// A rebuild replaces the shape, so the cached schema describes a database
// that no longer exists.
func TestSavingAScriptClearsTheCachedSchema(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		if _, err := repo.SaveScript(ctx, contest, "game_tpl_cabc", `SELECT 1`); err != nil {
			t.Fatalf("save: %v", err)
		}
		if err := repo.SaveSchema(ctx, contest, 1, aSchema()); err != nil {
			t.Fatalf("cache a schema: %v", err)
		}
		if _, err := repo.SaveScript(ctx, contest, "game_tpl_cabc", `SELECT 2`); err != nil {
			t.Fatalf("second save: %v", err)
		}

		if _, _, err := repo.CachedSchema(ctx, contest); !errors.Is(err, provisioning.ErrNoSchema) {
			t.Fatalf("the cached schema survived a rebuild: %v", err)
		}
	})
}

// aDefinition mirrors provisioning_test's own helper of the same name: a
// small, valid game a detective olympiad might actually use.
func aDefinition() provisioning.Definition {
	return provisioning.Definition{Tables: []provisioning.TableDefinition{
		{
			Name: "suspects",
			Columns: []provisioning.ColumnDefinition{
				{Name: "id", Type: provisioning.ColumnInteger},
				{Name: "name", Type: provisioning.ColumnText},
			},
			PrimaryKey: []string{"id"},
		},
	}}
}

// The third source (migration 26) round-trips through the same upsert
// SaveScript and CompleteUpload use: the definition comes back exactly as
// it was saved, source reads 'builder', and the columns the other two
// sources own — the script, the upload id — stay empty and nil.
func TestSavingADefinitionCreatesTheGameWithSourceBuilder(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		definition := aDefinition()
		template, err := repo.SaveDefinition(ctx, contest, "game_tpl_cabc", definition)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if template.Version != 1 || template.Status != provisioning.TemplatePending {
			t.Fatalf("template = %+v, want version 1, pending", template)
		}
		if template.Source != provisioning.SourceBuilder {
			t.Fatalf("source = %q, want builder", template.Source)
		}
		if template.UploadID != nil {
			t.Fatalf("upload_id = %v, want nil for a builder-sourced game", template.UploadID)
		}
		if template.Script != "" {
			t.Fatalf("script = %q, want empty for a builder-sourced game", template.Script)
		}
		if len(template.Definition.Tables) != 1 || template.Definition.Tables[0].Name != "suspects" {
			t.Fatalf("definition round-tripped as %+v, want %+v", template.Definition, definition)
		}
	})
}

// Saving a script over a builder-sourced game must clear its definition —
// otherwise the row would carry both a script and a definition that
// migration 26's own CHECK says may not coexist with 'editor' — and the
// other direction has to hold too: saving a definition over a script-sourced
// game must leave nothing of the old script behind for the wrong source to
// read.
func TestReplacingAGameClearsWhicheverOfScriptOrDefinitionTheNewSourceDoesNotOwn(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		if _, err := repo.SaveDefinition(ctx, contest, "game_tpl_cabc", aDefinition()); err != nil {
			t.Fatalf("save definition: %v", err)
		}
		afterScript, err := repo.SaveScript(ctx, contest, "game_tpl_cabc", `SELECT 1`)
		if err != nil {
			t.Fatalf("save script: %v", err)
		}
		if afterScript.Source != provisioning.SourceEditor || len(afterScript.Definition.Tables) != 0 {
			t.Fatalf("after saving a script: source=%q definition=%+v, want editor with no definition",
				afterScript.Source, afterScript.Definition)
		}

		afterDefinition, err := repo.SaveDefinition(ctx, contest, "game_tpl_cabc", aDefinition())
		if err != nil {
			t.Fatalf("save definition again: %v", err)
		}
		if afterDefinition.Source != provisioning.SourceBuilder || afterDefinition.Script != "" {
			t.Fatalf("after saving a definition: source=%q script=%q, want builder with no script",
				afterDefinition.Source, afterDefinition.Script)
		}
	})
}

// The schema's own defence, beside Definition.Validate's (CLAUDE.md rule 2's
// point that a bound the domain enforces is still worth a second, structural
// guarantee at the boundary storage owns). `source` is a closed list; migration
// 26 widened it to three values and this is the widened list, checked against
// the database rather than assumed from the Go side.
func TestTheSourceCheckConstraintAcceptsExactlyTheThreeKnownSources(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		q := storage.QuerierFrom(ctx, testPool)

		for _, source := range []string{"editor", "file", "builder"} {
			_, err := q.Exec(ctx, `DELETE FROM game_templates WHERE contest_id = $1`, contest)
			if err != nil {
				t.Fatalf("clear the row for %q: %v", source, err)
			}
			var uploadID, definitionJSON any
			if source == "file" {
				uploadID = uploadRowFor(t, ctx, contest)
			}
			if source == "builder" {
				definitionJSON = []byte(`{"tables":[{"name":"t","columns":[{"name":"c","type":"integer"}]}]}`)
			}
			if _, err := q.Exec(ctx, `
				INSERT INTO game_templates (contest_id, template_db, init_script, source, upload_id, definition_json)
				VALUES ($1, 'game_tpl_cabc', '', $2, $3, $4)`,
				contest, source, uploadID, definitionJSON); err != nil {
				t.Fatalf("source %q was refused by the widened CHECK: %v", source, err)
			}
		}

		if _, err := q.Exec(ctx, `
			INSERT INTO game_templates (contest_id, template_db, init_script, source)
			VALUES (gen_random_uuid(), 'game_tpl_cxyz', '', 'bogus')`); err == nil {
			t.Fatal("an unknown source was accepted by the CHECK")
		}
	})
}

// uploadRowFor creates a game_uploads row so a 'file'-sourced game_templates
// row under test satisfies its own foreign key, and returns its id.
func uploadRowFor(t *testing.T, ctx context.Context, contest uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `
		INSERT INTO game_uploads (contest_id, filename, declared_bytes, status)
		VALUES ($1, 'dump.sql', 10, 'complete') RETURNING id`, contest).Scan(&id); err != nil {
		t.Fatalf("create an upload row: %v", err)
	}
	return id
}

// The pairing CHECK migration 26 adds beside the one migration 24 already
// had: a 'builder' row must carry a definition, and nothing else may. Tested
// directly against the schema, not through the repository, because
// SaveDefinition and SaveScript never produce the contradictory row
// themselves — this is what stops a future change to either from being able
// to.
func TestTheSourcePairingConstraintTiesBuilderToDefinitionJSON(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		q := storage.QuerierFrom(ctx, testPool)

		if _, err := q.Exec(ctx, `
			INSERT INTO game_templates (contest_id, template_db, init_script, source, definition_json)
			VALUES ($1, 'game_tpl_cabc', '', 'builder', NULL)`, contest); err == nil {
			t.Fatal("a 'builder' row with no definition was accepted")
		}

		if _, err := q.Exec(ctx, `
			INSERT INTO game_templates (contest_id, template_db, init_script, source, definition_json)
			VALUES ($1, 'game_tpl_cabc', '', 'editor', '{"tables":[]}')`, contest); err == nil {
			t.Fatal("an 'editor' row carrying a definition was accepted")
		}
	})
}

// Two workers ticking at the same moment must not both run CREATE DATABASE
// against one name.
//
// ClaimBuild is installation-wide by design: it takes the oldest pending row
// anywhere, with no contest to scope it. This used to skip when that row
// belonged to somebody else — and since any pending game in a developer's
// own database, or left by an earlier test in the same run, makes that the
// case, the test reported PASS with a note nobody reads instead of proving
// anything. A skip that a real installation triggers is not a skip, it is
// silence.
//
// So the row is aged first, which is what makes it the one ClaimBuild
// reaches: `ORDER BY updated_at LIMIT 1` takes the oldest, and ten years is
// older than anything an installation holds. Nothing outlives the test — the
// whole body runs in a transaction withTx rolls back, this UPDATE included.
func TestOnlyOneClaimOfAGameSucceedsAndTheRestFindNothing(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)
		if _, err := repo.SaveScript(ctx, contest, "game_tpl_c"+uuid.NewString()[:12], `SELECT 1`); err != nil {
			t.Fatalf("save: %v", err)
		}
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE game_templates SET updated_at = now() - interval '10 years' WHERE contest_id = $1`,
			contest); err != nil {
			t.Fatalf("age the row so it is the oldest pending one: %v", err)
		}

		claimed, err := repo.ClaimBuild(ctx, time.Hour)
		if err != nil {
			t.Fatalf("first claim: %v", err)
		}
		if claimed.Status != provisioning.TemplateBuilding {
			t.Fatalf("claimed as %q, want building", claimed.Status)
		}
		if claimed.ContestID != contest {
			t.Fatalf("claimed %s, want this test's own game (%s) — it is the oldest pending row",
				claimed.ContestID, contest)
		}

		// Claimed once, it is no longer pending — and the stale window has
		// not passed, so nothing may take it again. The second claim may
		// legitimately find some other installation row; what it must never
		// find is this one.
		again, err := repo.ClaimBuild(ctx, time.Hour)
		if err == nil && again.ContestID == contest {
			t.Fatal("the same game was claimed twice")
		}
	})
}

// An API that died mid-build must not leave an organiser watching a spinner
// that will never stop.
func TestAGameStuckBuildingIsClaimedAgainOnceItIsStale(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)
		if _, err := repo.SaveScript(ctx, contest, "game_tpl_c"+uuid.NewString()[:12], `SELECT 1`); err != nil {
			t.Fatalf("save: %v", err)
		}
		if _, err := repo.ClaimBuild(ctx, time.Hour); err != nil {
			t.Fatalf("claim: %v", err)
		}
		// Aged deliberately rather than waited for.
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE game_templates SET updated_at = now() - interval '2 hours' WHERE contest_id = $1`, contest); err != nil {
			t.Fatalf("age the row: %v", err)
		}

		again, err := repo.ClaimBuild(ctx, time.Hour)
		if err != nil {
			t.Fatalf("re-claim: %v", err)
		}
		if again.ContestID != contest {
			t.Fatalf("claimed %s, want the stuck game", again.ContestID)
		}
	})
}

func TestFinishingABuildRecordsReadyOrTheErrorItFailedWith(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)
		saved, err := repo.SaveScript(ctx, contest, "game_tpl_c"+uuid.NewString()[:12], `SELECT 1`)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if _, err := repo.ClaimBuild(ctx, time.Hour); err != nil {
			t.Fatalf("claim: %v", err)
		}

		if err := repo.FinishBuild(ctx, contest, saved.Version, `ERROR: type "nosuchtype" does not exist`); err != nil {
			t.Fatalf("finish: %v", err)
		}
		failed, err := repo.Template(ctx, contest)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if failed.Status != provisioning.TemplateFailed || failed.BuildError == "" {
			t.Fatalf("recorded %q / %q", failed.Status, failed.BuildError)
		}
	})
}

// A script saved while a build ran already bumped the version. The older
// build's outcome speaks for a script nobody is waiting on any more, and must
// not mark the new one ready — or fail it with the old one's error.
func TestABuildCannotFinishAVersionThatHasAlreadyBeenReplaced(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)
		name := "game_tpl_c" + uuid.NewString()[:12]
		first, err := repo.SaveScript(ctx, contest, name, `SELECT 1`)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if _, err := repo.ClaimBuild(ctx, time.Hour); err != nil {
			t.Fatalf("claim: %v", err)
		}
		// The organiser saves a correction while the build is still running.
		if _, err := repo.SaveScript(ctx, contest, name, `SELECT 2`); err != nil {
			t.Fatalf("second save: %v", err)
		}

		if err := repo.FinishBuild(ctx, contest, first.Version, ""); err != nil {
			t.Fatalf("finish: %v", err)
		}
		now, err := repo.Template(ctx, contest)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if now.Status != provisioning.TemplatePending {
			t.Fatalf("the stale build marked the new script %q; it must still be pending", now.Status)
		}
	})
}

// The first build is the one most likely to get this wrong: the template is
// not 'ready' yet, so Game() has no row to answer from.
func TestThePolicyIsReadableBeforeTheGameIsEverBuilt(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		policy, err := repo.Policy(ctx, contest)
		if err != nil {
			t.Fatalf("read the policy of an unbuilt game: %v", err)
		}
		// A contest with no policy row of its own is read-only with the
		// catalogues open, the same defaults Game() coalesces to.
		if policy.Mode != sqlpolicy.ModeReadOnly || !policy.AllowCatalog {
			t.Fatalf("defaults came back as %+v", policy)
		}
	})
}

// The one rule this file spells as SQL rather than asking the contests
// package for. If the two ever disagree, the game of a running olympiad
// becomes replaceable — which drops every participant's database at once.
func TestGameEditableAgreesWithContentEditableForEveryStatus(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		for _, status := range []string{
			contests.StatusDraft, contests.StatusPublished, contests.StatusRunning,
			contests.StatusFinished, contests.StatusArchived,
		} {
			if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
				`UPDATE contests SET status = $2 WHERE id = $1`, contest, status); err != nil {
				t.Fatalf("set status %s: %v", status, err)
			}

			got, err := repo.GameEditable(ctx, contest)
			if err != nil {
				t.Fatalf("status %s: %v", status, err)
			}
			want := contests.Contest{Status: status}.ContentEditable()
			if got != want {
				t.Fatalf("status %s: the SQL says editable=%v, contests.ContentEditable says %v", status, got, want)
			}
		}
	})
}

// The status read is what a console polls twice a second while a build runs,
// and the only thing it wants from the script is how long it is. It must
// therefore answer with the length and never with the bytes — a twenty-minute
// build with two organisers watching is 1200 polls, and a half-mebibyte script
// read on each of them is six hundred megabytes pulled out of this database,
// turned into Go strings and thrown away.
func TestTheStatusReadCarriesTheScriptsLengthAndNotItsBytes(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		if _, err := repo.TemplateStatus(ctx, contest); !errors.Is(err, provisioning.ErrNoGame) {
			t.Fatalf("a contest with no game answered %v, want ErrNoGame", err)
		}

		script := `CREATE TABLE a (x int); -- ăîș, so the length is bytes and not runes`
		saved, err := repo.SaveScript(ctx, contest, "game_tpl_cabc", script)
		if err != nil {
			t.Fatalf("save: %v", err)
		}

		status, err := repo.TemplateStatus(ctx, contest)
		if err != nil {
			t.Fatalf("TemplateStatus: %v", err)
		}
		if status.Script != "" {
			t.Fatalf("the status read returned %d bytes of script", len(status.Script))
		}
		if status.ScriptBytes != len(script) {
			t.Fatalf("ScriptBytes = %d, want %d", status.ScriptBytes, len(script))
		}
		// And the full read agrees about the length, so a caller never has to
		// know which of the two produced the row it is holding.
		if saved.ScriptBytes != len(script) {
			t.Fatalf("the full read reports ScriptBytes = %d, want %d", saved.ScriptBytes, len(script))
		}
		if status.Version != saved.Version || status.Status != saved.Status ||
			status.Database != saved.Database || status.Source != saved.Source {
			t.Fatalf("the status read disagrees with the full one: %+v vs %+v", status, saved)
		}
	})
}

// A builder-sourced game's definition is the other column the status read
// leaves behind — and the one that also cost a json.Unmarshal on every poll.
func TestTheStatusReadDoesNotDecodeTheBuilderDefinition(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		definition := provisioning.Definition{Tables: []provisioning.TableDefinition{{
			Name:       "suspects",
			Columns:    []provisioning.ColumnDefinition{{Name: "id", Type: provisioning.ColumnInteger}},
			PrimaryKey: []string{"id"},
		}}}
		if _, err := repo.SaveDefinition(ctx, contest, "game_tpl_cabc", definition); err != nil {
			t.Fatalf("save the definition: %v", err)
		}

		status, err := repo.TemplateStatus(ctx, contest)
		if err != nil {
			t.Fatalf("TemplateStatus: %v", err)
		}
		if len(status.Definition.Tables) != 0 {
			t.Fatalf("the status read decoded %d tables of definition", len(status.Definition.Tables))
		}
		if status.Source != provisioning.SourceBuilder {
			t.Fatalf("source = %q, want builder", status.Source)
		}
		// The full read still has it, which is what makes the status read a
		// narrower query rather than a lost column.
		full, err := repo.Template(ctx, contest)
		if err != nil {
			t.Fatalf("Template: %v", err)
		}
		if len(full.Definition.Tables) != 1 {
			t.Fatalf("the full read returned %d tables, want 1", len(full.Definition.Tables))
		}
	})
}
