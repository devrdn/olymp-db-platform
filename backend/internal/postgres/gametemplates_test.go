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

// anotherPackagesPendingGame commits an aged pending game outside the test's
// transaction, as a package running in parallel would. Claim tests must not
// take it: holding its lock would hide it from the package that owns it. It is
// removed when the test ends.
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

// markBuilding puts this test's game into 'building', aged by age, without an
// installation-wide claim that could take another package's row.
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
		if second.Version != 2 {
			t.Fatalf("second save produced version %d, want 2", second.Version)
		}
		if second.Script != `CREATE TABLE b (x int);` {
			t.Fatalf("second save stored %q", second.Script)
		}
	})
}

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

// source is a closed list in the schema; this checks it against the database
// rather than assuming it from the Go side.
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

// uploadRowFor creates a game_uploads row for a 'file'-sourced template's
// foreign key and returns its id.
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

// A 'builder' row must carry a definition and nothing else may (migration 26).
// Tested against the schema directly, since the repository never writes the
// contradictory row.
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

// ClaimBuild takes the oldest pending row installation-wide, so the test ages
// its own row by ten years to make it the one claimed. withTx rolls the
// change back.
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

		// The claim restarted the stale window. Checked on the row rather than
		// with a second claim, which would lock another package's pending game.
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

func TestAGameStuckBuildingIsClaimedAgainOnceItIsStale(t *testing.T) {
	anotherPackagesPendingGame(t)
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)
		if _, err := repo.SaveScript(ctx, contest, "game_tpl_c"+uuid.NewString()[:12], `SELECT 1`); err != nil {
			t.Fatalf("save: %v", err)
		}
		// Aged past any other package's row, so the claim below reaches it.
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

// markStatus forces one contest's game to status, for states only a real
// build would reach.
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

// Runs the real SQL, which the fake in support_test.go cannot check, once per
// source because each owns a different content column.
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

// Raising the version under a pending or running build would orphan that
// build's outcome.
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

// The UPDATE cannot tell a missing row from one that is not buildable.
// Games.RequestBuild checks TemplateStatus first and returns ErrNoGame, so
// service callers never see this answer.
func TestRequestBuildOnAContestWithNoGameAnswersErrBuildInProgress(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		if _, err := repo.RequestBuild(ctx, contest); !errors.Is(err, provisioning.ErrBuildInProgress) {
			t.Fatalf("RequestBuild on a contest with no game = %v, want ErrBuildInProgress", err)
		}
	})
}

// Before the first build the template is not ready, so Game has no row.
func TestThePolicyIsReadableBeforeTheGameIsEverBuilt(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)

		policy, err := repo.Policy(ctx, contest)
		if err != nil {
			t.Fatalf("read the policy of an unbuilt game: %v", err)
		}
		// With no policy row: read-only, catalogues open.
		if policy.Mode != sqlpolicy.ModeReadOnly || !policy.AllowCatalog {
			t.Fatalf("defaults came back as %+v", policy)
		}
	})
}

// If the two disagree, a running contest's game becomes replaceable, which
// drops every participant's database.
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

// A console polls the status read twice a second during a build, so it must
// not fetch the script itself.
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
		if saved.ScriptBytes != len(script) {
			t.Fatalf("the full read reports ScriptBytes = %d, want %d", saved.ScriptBytes, len(script))
		}
		if status.Version != saved.Version || status.Status != saved.Status ||
			status.Database != saved.Database || status.Source != saved.Source {
			t.Fatalf("the status read disagrees with the full one: %+v vs %+v", status, saved)
		}
	})
}

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
		full, err := repo.Template(ctx, contest)
		if err != nil {
			t.Fatalf("Template: %v", err)
		}
		if len(full.Definition.Tables) != 1 {
			t.Fatalf("the full read returned %d tables, want 1", len(full.Definition.Tables))
		}
	})
}

// claimBuildFor claims builds until it gets contest's own row. ClaimBuild is
// installation-wide, and packages tested in parallel share the database, so
// it may hand over another test's row first.
//
// A foreign row is put back to 'pending', never finished, so this helper does
// not mark another test's build done. The one-hour stale window means only a
// pending row is claimed, so 'pending' is its original status. updated_at is
// not restored; it only orders the queue.
func claimBuildFor(t *testing.T, repo *GameInstances, contest uuid.UUID) provisioning.Template {
	t.Helper()
	ctx := t.Context()

	for range 10 {
		claimed, err := repo.ClaimBuild(ctx, time.Hour)
		if err != nil {
			t.Fatalf("claim a build: %v", err)
		}
		if claimed.ContestID == contest {
			return claimed
		}
		if _, err := testPool.Exec(ctx,
			`UPDATE game_templates SET status = 'pending'
			 WHERE contest_id = $1 AND version = $2 AND status = 'building'`,
			claimed.ContestID, claimed.Version); err != nil {
			t.Fatalf("put a foreign claim back so it is not left stuck building: %v", err)
		}
	}
	t.Fatalf("claimed 10 builds without reaching contest %s's own row", contest)
	return provisioning.Template{}
}

// The build finishing and an organiser adding a row meet on one row; only the
// real database can show how the comparison settles it.
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

	// A second build, with nothing changing under it, clears the mark. Saving
	// the definition again requeues it.
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

	// A failed build leaves the mark: its data never reached a database, and
	// the mark is the only record of that.
	if err := repo.MarkTableDataChanged(t.Context(), contest); err != nil {
		t.Fatalf("mark the data changed before the failing build: %v", err)
	}
	if _, err := repo.RequestBuild(t.Context(), contest); err != nil {
		t.Fatalf("ask for a third build: %v", err)
	}
	third := claimBuildFor(t, repo, contest)
	if err := repo.FinishBuild(t.Context(), contest, third.Version,
		`ERROR: relation "suspects" does not exist`, third.UpdatedAt); err != nil {
		t.Fatalf("finish the third build as failed: %v", err)
	}
	broken, err := repo.TemplateStatus(t.Context(), contest)
	if err != nil {
		t.Fatalf("read the template after the failing build: %v", err)
	}
	if broken.Status != provisioning.TemplateFailed {
		t.Fatalf("the failing build left the template %q, want %q", broken.Status, provisioning.TemplateFailed)
	}
	if broken.DataChangedAt == nil {
		t.Fatal("a failed build cleared the data mark; the rows are unbuilt and nothing records it any more")
	}
}
