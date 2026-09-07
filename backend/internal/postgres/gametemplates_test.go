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

// Two workers ticking at the same moment must not both run CREATE DATABASE
// against one name.
func TestOnlyOneClaimOfAGameSucceedsAndTheRestFindNothing(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := aContest(t, ctx)
		repo := NewGameInstances(testPool)
		if _, err := repo.SaveScript(ctx, contest, "game_tpl_c"+uuid.NewString()[:12], `SELECT 1`); err != nil {
			t.Fatalf("save: %v", err)
		}

		claimed, err := repo.ClaimBuild(ctx, time.Hour)
		if err != nil {
			t.Fatalf("first claim: %v", err)
		}
		if claimed.Status != provisioning.TemplateBuilding {
			t.Fatalf("claimed as %q, want building", claimed.Status)
		}
		if claimed.ContestID != contest {
			t.Skip("another test's pending game was claimed first; this run proves nothing")
		}

		// Claimed once, it is no longer pending — and the stale window has
		// not passed, so nothing may take it again.
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
