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

// anotherPackagesPendingGame commits, outside the test's own transaction, a
// pending game belonging to nobody this test knows about — what a package
// running in parallel against the same database leaves in game_templates —
// and aged so that an installation-wide claim would reach it before any fresh
// row. The claim tests must pass with it present: a test that claims such a
// row fails in its own assertions and, while it holds the row's lock, hides
// it from the package that owns it. It is removed when the test ends.
func anotherPackagesPendingGame(t *testing.T) {
	t.Helper()
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	ctx := context.Background()
	author := makeUser(t, ctx, "game-other-"+uuid.NewString()[:8])
	contest := makeContest(t, ctx, author.ID)
	t.Cleanup(func() {
		q := storage.QuerierFrom(context.Background(), testPool)
		_, _ = q.Exec(context.Background(), `DELETE FROM contests WHERE id = $1`, contest)
		_, _ = q.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, author.ID)
	})
	repo := NewGameInstances(testPool)
	if _, err := repo.SaveScript(ctx, contest, "game_tpl_c"+uuid.NewString()[:12], `SELECT 1`); err != nil {
		t.Fatalf("save another package's game: %v", err)
	}
	if _, err := testPool.Exec(ctx,
		`UPDATE game_templates SET updated_at = now() - interval '5 years' WHERE contest_id = $1`, contest); err != nil {
		t.Fatalf("age another package's game: %v", err)
	}
}

// markBuilding puts this test's own game into 'building', aged by age, the way
// a claim would have left it — without an installation-wide claim, which
// could take another package's row instead of this one.
func markBuilding(t *testing.T, ctx context.Context, contest uuid.UUID, age string) {
	t.Helper()
	tag, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`UPDATE game_templates SET status = 'building', updated_at = now() - $2::interval WHERE contest_id = $1`,
		contest, age)
	if err != nil {
		t.Fatalf("mark the game building: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("marked %d games building, want this test's one", tag.RowsAffected())
	}
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
// older than anything an installation holds, or than the other package's row
// anotherPackagesPendingGame leaves. Nothing else outlives the test — the
// whole body runs in a transaction withTx rolls back, this UPDATE included.
func TestAClaimTakesTheOldestGameAndLeavesItOutOfReachOfTheNextClaim(t *testing.T) {
	anotherPackagesPendingGame(t)
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

		// Claimed once, it is no longer pending, and the claim restarted its
		// stale window, so no claim may take it again until that passes.
		// Asked of the row rather than of a second claim: a second
		// installation-wide claim would take whatever other package's pending
		// game comes next, and hold it locked from that package until this
		// transaction rolls back.
		var status string
		var fresh bool
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT status, updated_at = now() FROM game_templates WHERE contest_id = $1`, contest,
		).Scan(&status, &fresh); err != nil {
			t.Fatalf("read the claimed game back: %v", err)
		}
		if status != string(provisioning.TemplateBuilding) || !fresh {
			t.Fatalf("after the claim the game is %q (stale window restarted: %v); another claim could take it again", status, fresh)
		}
	})
}

// An API that died mid-build must not leave an organiser watching a spinner
// that will never stop.
func TestAGameStuckBuildingIsClaimedAgainOnceItIsStale(t *testing.T) {
	anotherPackagesPendingGame(t)
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)
		if _, err := repo.SaveScript(ctx, contest, "game_tpl_c"+uuid.NewString()[:12], `SELECT 1`); err != nil {
			t.Fatalf("save: %v", err)
		}
		// Left building by a build that never finished, and aged deliberately
		// rather than waited for: ten years is older than any other package's
		// row, which is what makes this the one the claim below reaches.
		markBuilding(t, ctx, contest, "10 years")

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
	anotherPackagesPendingGame(t)
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)
		saved, err := repo.SaveScript(ctx, contest, "game_tpl_c"+uuid.NewString()[:12], `SELECT 1`)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		markBuilding(t, ctx, contest, "0 seconds")

		if err := repo.FinishBuild(ctx, contest, saved.Version, `ERROR: type "nosuchtype" does not exist`, time.Now()); err != nil {
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
	anotherPackagesPendingGame(t)
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)
		name := "game_tpl_c" + uuid.NewString()[:12]
		first, err := repo.SaveScript(ctx, contest, name, `SELECT 1`)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		markBuilding(t, ctx, contest, "0 seconds")
		// The organiser saves a correction while the build is still running.
		if _, err := repo.SaveScript(ctx, contest, name, `SELECT 2`); err != nil {
			t.Fatalf("second save: %v", err)
		}

		if err := repo.FinishBuild(ctx, contest, first.Version, "", time.Now()); err != nil {
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

// markStatus forces one contest's game to status directly, the way a test
// that needs a 'ready' or 'failed' row to already exist has to — neither
// state is reachable through the repository's own calls without a real
// build running.
func markStatus(t *testing.T, ctx context.Context, contest uuid.UUID, status string) {
	t.Helper()
	tag, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`UPDATE game_templates SET status = $2, updated_at = now() WHERE contest_id = $1`, contest, status)
	if err != nil {
		t.Fatalf("mark the game %s: %v", status, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("marked %d games %s, want this test's one", tag.RowsAffected(), status)
	}
}

// TestRequestBuildLeavesTheStoredContentUntouched is the guarantee the fake
// in support_test.go cannot prove: it reimplements RequestBuild's own
// condition as an if/else, so a wrong column name, a wrong status literal or
// a missing AND in the real UPDATE would still pass every test that ran
// against the fake. This one runs the actual SQL.
//
// Run once per source, because each owns a different one of the three
// content columns RequestBuild must leave alone — a script's init_script, a
// definition's definition_json, an upload's upload_id — and reading them
// back after the call is what would catch somebody later adding one of them
// to the UPDATE's own SET list, rather than trusting that list to be
// complete.
func TestRequestBuildLeavesTheStoredContentUntouched(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, ctx context.Context, repo *GameInstances, contest uuid.UUID) provisioning.Template
	}{
		{
			name: "script",
			setup: func(t *testing.T, ctx context.Context, repo *GameInstances, contest uuid.UUID) provisioning.Template {
				saved, err := repo.SaveScript(ctx, contest, "game_tpl_c"+uuid.NewString()[:12], `SELECT 1`)
				if err != nil {
					t.Fatalf("save script: %v", err)
				}
				return saved
			},
		},
		{
			name: "definition",
			setup: func(t *testing.T, ctx context.Context, repo *GameInstances, contest uuid.UUID) provisioning.Template {
				saved, err := repo.SaveDefinition(ctx, contest, "game_tpl_c"+uuid.NewString()[:12], aDefinition())
				if err != nil {
					t.Fatalf("save definition: %v", err)
				}
				return saved
			},
		},
		{
			name: "upload",
			setup: func(t *testing.T, ctx context.Context, repo *GameInstances, contest uuid.UUID) provisioning.Template {
				id := uuid.New()
				if _, err := repo.BeginUpload(ctx, id, contest, "dump.sql", 10); err != nil {
					t.Fatalf("begin upload: %v", err)
				}
				saved, err := repo.CompleteUpload(ctx, contest, id, "game_tpl_c"+uuid.NewString()[:12],
					provisioning.UploadSummary{Bytes: 10, SHA256: "aaaa", Lines: 1}, nil)
				if err != nil {
					t.Fatalf("complete upload: %v", err)
				}
				return saved
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			anotherPackagesPendingGame(t)
			withTx(t, func(ctx context.Context) {
				contest := aContest(t, ctx)
				repo := NewGameInstances(testPool)

				before := tc.setup(t, ctx, repo, contest)
				markStatus(t, ctx, contest, "ready")

				asked, err := repo.RequestBuild(ctx, contest)
				if err != nil {
					t.Fatalf("RequestBuild: %v", err)
				}
				if asked.Status != provisioning.TemplatePending {
					t.Fatalf("status = %q, want pending", asked.Status)
				}
				if asked.Version != before.Version+1 {
					t.Fatalf("version = %d, want %d (one more than %d): the copies made from the old one must go stale",
						asked.Version, before.Version+1, before.Version)
				}

				after, err := repo.Template(ctx, contest)
				if err != nil {
					t.Fatalf("read back: %v", err)
				}
				if after.Script != before.Script {
					t.Fatalf("script = %q after RequestBuild, want %q (unchanged)", after.Script, before.Script)
				}
				if len(after.Definition.Tables) != len(before.Definition.Tables) {
					t.Fatalf("definition = %+v after RequestBuild, want %+v (unchanged)", after.Definition, before.Definition)
				}
				for i := range before.Definition.Tables {
					if after.Definition.Tables[i].Name != before.Definition.Tables[i].Name {
						t.Fatalf("definition = %+v after RequestBuild, want %+v (unchanged)", after.Definition, before.Definition)
					}
				}
				if (after.UploadID == nil) != (before.UploadID == nil) {
					t.Fatalf("upload_id = %v after RequestBuild, want %v (unchanged)", after.UploadID, before.UploadID)
				}
				if after.UploadID != nil && *after.UploadID != *before.UploadID {
					t.Fatalf("upload_id = %s after RequestBuild, want %s (unchanged)", *after.UploadID, *before.UploadID)
				}
			})
		})
	}
}

// TestRequestBuildAcceptsAFailedGameToo: a build that ran and did not finish
// is exactly as buildable again as one that finished cleanly — the organiser
// pressing the button does not care which of the two states got them there,
// and RequestBuild's own WHERE clause names both.
func TestRequestBuildAcceptsAFailedGameToo(t *testing.T) {
	anotherPackagesPendingGame(t)
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		saved, err := repo.SaveScript(ctx, contest, "game_tpl_c"+uuid.NewString()[:12], `SELECT 1`)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		markBuilding(t, ctx, contest, "0 seconds")
		if err := repo.FinishBuild(ctx, contest, saved.Version, `ERROR: type "nosuchtype" does not exist`, time.Now()); err != nil {
			t.Fatalf("finish (failed): %v", err)
		}

		asked, err := repo.RequestBuild(ctx, contest)
		if err != nil {
			t.Fatalf("RequestBuild on a failed game: %v", err)
		}
		if asked.Status != provisioning.TemplatePending {
			t.Fatalf("status = %q, want pending", asked.Status)
		}
		if asked.BuildError != "" {
			t.Fatalf("build_error = %q, want cleared", asked.BuildError)
		}
	})
}

// TestRequestBuildRefusesAGameNotReadyOrFailed is the race arbiter itself —
// the assertion the fake in support_test.go cannot make, because the fake's
// own if/else is what it exists to prove is not the whole story. A game
// already 'pending' (a build is waiting) or 'building' (one is running) must
// both be refused: raising the version out from under a build already under
// way would leave that build's own outcome recorded against a version
// nobody is waiting on, the same trap TestABuildCannotFinishAVersionThatHas
// AlreadyBeenReplaced covers from FinishBuild's side.
func TestRequestBuildRefusesAGameNotReadyOrFailed(t *testing.T) {
	for _, status := range []string{"pending", "building"} {
		t.Run(status, func(t *testing.T) {
			anotherPackagesPendingGame(t)
			withTx(t, func(ctx context.Context) {
				contest := aContest(t, ctx)
				repo := NewGameInstances(testPool)

				saved, err := repo.SaveScript(ctx, contest, "game_tpl_c"+uuid.NewString()[:12], `SELECT 1`)
				if err != nil {
					t.Fatalf("save: %v", err)
				}
				if status != "pending" {
					markStatus(t, ctx, contest, status)
				}

				if _, err := repo.RequestBuild(ctx, contest); !errors.Is(err, provisioning.ErrBuildInProgress) {
					t.Fatalf("RequestBuild on a %s game = %v, want ErrBuildInProgress", status, err)
				}

				now, err := repo.Template(ctx, contest)
				if err != nil {
					t.Fatalf("read back: %v", err)
				}
				if now.Status != provisioning.TemplateStatus(status) || now.Version != saved.Version {
					t.Fatalf("the refused request changed the row to %+v, want status %q and version %d unchanged",
						now, status, saved.Version)
				}
			})
		})
	}
}

// TestRequestBuildOnAContestWithNoGameAnswersErrBuildInProgress documents the
// repository's own answer for a contest_id that names no game_templates row
// at all: the conditional UPDATE matches nothing, the same as it would for a
// row that exists but is pending or building, so this repository method
// cannot itself tell "no game" apart from "not buildable right now" — both
// read back as zero rows changed.
//
// It is Games.RequestBuild, one layer up, that tells the two apart: it reads
// TemplateStatus first and returns ErrNoGame before this method is ever
// called, so a caller of the service never observes what this test asserts.
// This is deliberately the deferred minor from the branch review recorded
// against this method — not something to fix here — stated as the behaviour
// that exists, so a later change to it is a decision made on purpose.
func TestRequestBuildOnAContestWithNoGameAnswersErrBuildInProgress(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		if _, err := repo.RequestBuild(ctx, contest); !errors.Is(err, provisioning.ErrBuildInProgress) {
			t.Fatalf("RequestBuild on a contest with no game = %v, want ErrBuildInProgress", err)
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

// claimBuildFor claims builds until it gets contest's own row, finishing
// back — with the claim's own UpdatedAt, so nothing is left stuck in
// 'building' — any foreign row it picks up along the way. ClaimBuild takes
// the oldest pending template of any contest, so a package running its tests
// in parallel can hand either claim in this file another test's row before
// it hands over this one's.
func claimBuildFor(t *testing.T, repo *GameInstances, contest uuid.UUID) provisioning.Template {
	t.Helper()
	ctx := t.Context()

	for range 10 {
		claimed, err := repo.ClaimBuild(ctx, time.Minute)
		if err != nil {
			t.Fatalf("claim a build: %v", err)
		}
		if claimed.ContestID == contest {
			return claimed
		}
		if err := repo.FinishBuild(ctx, claimed.ContestID, claimed.Version, "", claimed.UpdatedAt); err != nil {
			t.Fatalf("finish a foreign claim so it is not left stuck building: %v", err)
		}
	}
	t.Fatalf("claimed 10 builds without reaching contest %s's own row", contest)
	return provisioning.Template{}
}

// TestFinishBuildClearsTheDataMarkOnlyWhenNothingChangedDuringTheBuild is the
// half of this feature that a fake cannot prove: two writers — the build
// finishing and an organiser adding a row — meeting on one row, arbitrated by
// a comparison PostgreSQL makes.
func TestFinishBuildClearsTheDataMarkOnlyWhenNothingChangedDuringTheBuild(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	repo := NewGameInstances(testPool)
	contest := contestRow(t, t.Context())

	saved, err := repo.SaveDefinition(t.Context(), contest, "game_"+contest.String()[:8], aDefinition())
	if err != nil {
		t.Fatalf("save the definition: %v", err)
	}
	if err := repo.MarkTableDataChanged(t.Context(), contest); err != nil {
		t.Fatalf("mark the data changed: %v", err)
	}

	claimed := claimBuildFor(t, repo, contest)

	// A row typed while the build runs: the mark moves past the claim.
	if err := repo.MarkTableDataChanged(t.Context(), contest); err != nil {
		t.Fatalf("mark the data changed during the build: %v", err)
	}
	if err := repo.FinishBuild(t.Context(), contest, saved.Version, "", claimed.UpdatedAt); err != nil {
		t.Fatalf("finish the build: %v", err)
	}

	after, err := repo.TemplateStatus(t.Context(), contest)
	if err != nil {
		t.Fatalf("read the template: %v", err)
	}
	if after.DataChangedAt == nil {
		t.Fatal("the build cleared a mark left by a row added while it ran; that row will never be built")
	}

	// A second build, with nothing changing under it, does clear the mark.
	//
	// FinishBuild only ever leaves a game 'ready' or 'failed', never back to
	// 'pending' — that requeue is the later task's own RequestBuild, which
	// this task does not implement — so here it is done the same way any
	// other edit does it: saving the definition again, exactly as an
	// organiser queuing a rebuild would.
	requeued, err := repo.SaveDefinition(t.Context(), contest, "game_"+contest.String()[:8], aDefinition())
	if err != nil {
		t.Fatalf("save the definition again to queue a second build: %v", err)
	}
	second := claimBuildFor(t, repo, contest)
	if second.Version != requeued.Version {
		t.Fatalf("claimed version %d, want the requeued version %d", second.Version, requeued.Version)
	}
	if err := repo.FinishBuild(t.Context(), contest, second.Version, "", second.UpdatedAt); err != nil {
		t.Fatalf("finish the second build: %v", err)
	}
	settled, err := repo.TemplateStatus(t.Context(), contest)
	if err != nil {
		t.Fatalf("read the template again: %v", err)
	}
	if settled.DataChangedAt != nil {
		t.Fatalf("the mark survived a build that saw every change: %v", settled.DataChangedAt)
	}
}
