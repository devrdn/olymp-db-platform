package postgres

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/workspace"
	"github.com/google/uuid"
)

// workspaceRegistration enrols somebody inside the test transaction.
func workspaceRegistration(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	user := makeUser(t, ctx, "workspace-"+uuid.NewString()[:8])
	return makeRegistration(t, ctx, makeContest(t, ctx, user.ID), user.ID)
}

// numbered names a new tab "Tab N" after how many are already taken, so a test
// can see the callback was handed the workspace's titles.
func numbered(taken []string) string { return "Tab " + strconv.Itoa(len(taken)+1) }

func tabTitles(tabs []workspace.Tab) []string {
	titles := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		titles = append(titles, tab.Title)
	}
	return titles
}

func TestWorkspaceLoadCreatesTheFirstTabOnce(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewWorkspace(testPool)
		registration := workspaceRegistration(t, ctx)

		notes, tabs, err := repo.Load(ctx, registration, "Query 1")
		if err != nil {
			t.Fatalf("Load() = %v", err)
		}
		if notes.Body != "" || notes.UpdatedAt != nil {
			t.Fatalf("notes before any save = %+v, want empty and never saved", notes)
		}
		if len(tabs) != 1 || tabs[0].Title != "Query 1" || tabs[0].Position != 0 || tabs[0].Body != "" {
			t.Fatalf("tabs = %+v, want one empty tab titled Query 1 at 0", tabs)
		}

		// The second load finds that tab and names nothing new, whatever
		// language it asks in.
		_, again, err := repo.Load(ctx, registration, "Запрос 1")
		if err != nil {
			t.Fatalf("Load() again = %v", err)
		}
		if len(again) != 1 || again[0].ID != tabs[0].ID || again[0].Title != "Query 1" {
			t.Fatalf("tabs on the second load = %+v, want the first tab unchanged", again)
		}
	})
}

// Two first loads at once — two browser tabs opened together — must not give
// the participant two first tabs. Outside a rolled-back transaction, because
// the race is between two connections.
func TestConcurrentFirstLoadsCreateOneTab(t *testing.T) {
	ctx, registration := committedRegistration(t)
	repo := NewWorkspace(testPool)

	const racers = 8
	var start, done sync.WaitGroup
	start.Add(1)
	errs := make(chan error, racers)
	for range racers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			_, _, err := repo.Load(ctx, registration, "Query 1")
			errs <- err
		}()
	}
	start.Done()
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Load() = %v", err)
		}
	}

	var count int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM participant_sql_tabs WHERE registration_id = $1`, registration).Scan(&count); err != nil {
		t.Fatalf("count tabs: %v", err)
	}
	if count != 1 {
		t.Fatalf("%d concurrent first loads created %d tabs, want 1", racers, count)
	}
}

func TestWorkspaceNotesAreReplaced(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewWorkspace(testPool)
		registration := workspaceRegistration(t, ctx)

		if _, err := repo.SaveNotes(ctx, registration, "first"); err != nil {
			t.Fatalf("SaveNotes() = %v", err)
		}
		at, err := repo.SaveNotes(ctx, registration, "second")
		if err != nil {
			t.Fatalf("SaveNotes() again = %v", err)
		}

		notes, _, err := repo.Load(ctx, registration, "Query 1")
		if err != nil {
			t.Fatalf("Load() = %v", err)
		}
		if notes.Body != "second" || notes.UpdatedAt == nil || !notes.UpdatedAt.Equal(at) {
			t.Fatalf("notes = %+v, want the second save at %v", notes, at)
		}
	})
}

func TestWorkspaceTabsAreAppendedUpToTheLimit(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewWorkspace(testPool)
		registration := workspaceRegistration(t, ctx)
		if _, _, err := repo.Load(ctx, registration, "Tab 1"); err != nil {
			t.Fatalf("Load() = %v", err)
		}

		for want := 1; want < 3; want++ {
			tab, err := repo.CreateTab(ctx, registration, 3, numbered)
			if err != nil {
				t.Fatalf("CreateTab() = %v", err)
			}
			if tab.Position != want || tab.Title != "Tab "+strconv.Itoa(want+1) || tab.UpdatedAt.IsZero() {
				t.Fatalf("created %+v, want Tab %d at %d", tab, want+1, want)
			}
		}
		if _, err := repo.CreateTab(ctx, registration, 3, numbered); !errors.Is(err, workspace.ErrTooManyTabs) {
			t.Fatalf("CreateTab() at the limit = %v, want ErrTooManyTabs", err)
		}

		_, tabs, err := repo.Load(ctx, registration, "unused")
		if err != nil {
			t.Fatalf("Load() = %v", err)
		}
		if got := tabTitles(tabs); !slices.Equal(got, []string{"Tab 1", "Tab 2", "Tab 3"}) {
			t.Fatalf("titles in order = %v", got)
		}
	})
}

// The limit is counted under the lock that inserts: parallel creates cannot
// each see room for one more.
func TestConcurrentCreatesStayWithinTheLimit(t *testing.T) {
	ctx, registration := committedRegistration(t)
	repo := NewWorkspace(testPool)
	if _, _, err := repo.Load(ctx, registration, "Tab 1"); err != nil {
		t.Fatalf("Load() = %v", err)
	}

	const limit, racers = 4, 12
	var start, done sync.WaitGroup
	start.Add(1)
	errs := make(chan error, racers)
	for range racers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			_, err := repo.CreateTab(ctx, registration, limit, numbered)
			errs <- err
		}()
	}
	start.Done()
	done.Wait()
	close(errs)

	created := 0
	for err := range errs {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, workspace.ErrTooManyTabs):
			t.Fatalf("CreateTab() = %v", err)
		}
	}
	if created != limit-1 {
		t.Fatalf("created %d tabs next to the first, want %d", created, limit-1)
	}
	_, tabs, err := repo.Load(ctx, registration, "unused")
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	for i, tab := range tabs {
		if tab.Position != i {
			t.Fatalf("positions = %v, want 0..%d with no repeats", tabs, limit-1)
		}
	}
}

func TestWorkspaceTabUpdateChangesOnlyWhatItNames(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewWorkspace(testPool)
		registration := workspaceRegistration(t, ctx)
		_, tabs, err := repo.Load(ctx, registration, "Query 1")
		if err != nil {
			t.Fatalf("Load() = %v", err)
		}
		id := tabs[0].ID

		body := "SELECT * FROM suspects"
		if _, err := repo.UpdateTab(ctx, registration, id, workspace.TabPatch{Body: &body}); err != nil {
			t.Fatalf("UpdateTab(body) = %v", err)
		}
		title := "suspects"
		at, err := repo.UpdateTab(ctx, registration, id, workspace.TabPatch{Title: &title})
		if err != nil {
			t.Fatalf("UpdateTab(title) = %v", err)
		}

		_, tabs, err = repo.Load(ctx, registration, "unused")
		if err != nil {
			t.Fatalf("Load() = %v", err)
		}
		if tabs[0].Title != title || tabs[0].Body != body || !tabs[0].UpdatedAt.Equal(at) {
			t.Fatalf("tab = %+v, want title %q, body %q, updated at %v", tabs[0], title, body, at)
		}
	})
}

// Another participant's tab is invisible and untouchable: every write that
// names it answers ErrTabNotFound and changes nothing.
func TestAnotherParticipantsTabIsNotFoundAndUnchanged(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewWorkspace(testPool)
		owner := workspaceRegistration(t, ctx)
		stranger := workspaceRegistration(t, ctx)
		_, ownerTabs, err := repo.Load(ctx, owner, "Owner 1")
		if err != nil {
			t.Fatalf("Load(owner) = %v", err)
		}
		second, err := repo.CreateTab(ctx, owner, 10, numbered)
		if err != nil {
			t.Fatalf("CreateTab(owner) = %v", err)
		}
		_, strangerTabs, err := repo.Load(ctx, stranger, "Stranger 1")
		if err != nil {
			t.Fatalf("Load(stranger) = %v", err)
		}
		if _, err := repo.CreateTab(ctx, stranger, 10, numbered); err != nil {
			t.Fatalf("CreateTab(stranger) = %v", err)
		}
		target := ownerTabs[0].ID

		body := "DROP everything"
		if _, err := repo.UpdateTab(ctx, stranger, target, workspace.TabPatch{Body: &body}); !errors.Is(err, workspace.ErrTabNotFound) {
			t.Fatalf("UpdateTab(another's) = %v, want ErrTabNotFound", err)
		}
		if err := repo.DeleteTab(ctx, stranger, target); !errors.Is(err, workspace.ErrTabNotFound) {
			t.Fatalf("DeleteTab(another's) = %v, want ErrTabNotFound", err)
		}
		mixed := []uuid.UUID{strangerTabs[0].ID, target}
		if err := repo.ReorderTabs(ctx, stranger, mixed); !errors.Is(err, workspace.ErrOrderMismatch) {
			t.Fatalf("ReorderTabs(with another's) = %v, want ErrOrderMismatch", err)
		}

		_, after, err := repo.Load(ctx, owner, "unused")
		if err != nil {
			t.Fatalf("Load(owner) = %v", err)
		}
		if len(after) != 2 || after[0].ID != target || after[0].Body != "" || after[1].ID != second.ID {
			t.Fatalf("owner's tabs after the stranger's attempts = %+v", after)
		}
		_, strangerAfter, err := repo.Load(ctx, stranger, "unused")
		if err != nil {
			t.Fatalf("Load(stranger) = %v", err)
		}
		if slices.ContainsFunc(strangerAfter, func(tab workspace.Tab) bool { return tab.ID == target }) {
			t.Fatal("the stranger's workspace lists the owner's tab")
		}
	})
}

func TestWorkspaceDeleteClosesTheGapAndKeepsTheLastTab(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewWorkspace(testPool)
		registration := workspaceRegistration(t, ctx)
		_, tabs, err := repo.Load(ctx, registration, "Tab 1")
		if err != nil {
			t.Fatalf("Load() = %v", err)
		}
		first := tabs[0].ID
		second, err := repo.CreateTab(ctx, registration, 10, numbered)
		if err != nil {
			t.Fatalf("CreateTab() = %v", err)
		}
		third, err := repo.CreateTab(ctx, registration, 10, numbered)
		if err != nil {
			t.Fatalf("CreateTab() = %v", err)
		}

		if err := repo.DeleteTab(ctx, registration, second.ID); err != nil {
			t.Fatalf("DeleteTab(middle) = %v", err)
		}
		_, tabs, err = repo.Load(ctx, registration, "unused")
		if err != nil {
			t.Fatalf("Load() = %v", err)
		}
		if len(tabs) != 2 || tabs[0].ID != first || tabs[1].ID != third.ID || tabs[1].Position != 1 {
			t.Fatalf("tabs after deleting the middle one = %+v, want the first at 0 and the third at 1", tabs)
		}

		if err := repo.DeleteTab(ctx, registration, first); err != nil {
			t.Fatalf("DeleteTab(first) = %v", err)
		}
		if err := repo.DeleteTab(ctx, registration, third.ID); !errors.Is(err, workspace.ErrLastTab) {
			t.Fatalf("DeleteTab(last) = %v, want ErrLastTab", err)
		}
		if err := repo.DeleteTab(ctx, registration, first); !errors.Is(err, workspace.ErrTabNotFound) {
			t.Fatalf("DeleteTab(already deleted) = %v, want ErrTabNotFound", err)
		}
	})
}

func TestWorkspaceReorderRewritesEveryPosition(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewWorkspace(testPool)
		registration := workspaceRegistration(t, ctx)
		_, tabs, err := repo.Load(ctx, registration, "Tab 1")
		if err != nil {
			t.Fatalf("Load() = %v", err)
		}
		ids := []uuid.UUID{tabs[0].ID}
		for range 2 {
			tab, err := repo.CreateTab(ctx, registration, 10, numbered)
			if err != nil {
				t.Fatalf("CreateTab() = %v", err)
			}
			ids = append(ids, tab.ID)
		}

		if err := repo.ReorderTabs(ctx, registration, ids[:2]); !errors.Is(err, workspace.ErrOrderMismatch) {
			t.Fatalf("ReorderTabs(missing one) = %v, want ErrOrderMismatch", err)
		}
		if err := repo.ReorderTabs(ctx, registration, append(slices.Clone(ids), uuid.New())); !errors.Is(err, workspace.ErrOrderMismatch) {
			t.Fatalf("ReorderTabs(one extra) = %v, want ErrOrderMismatch", err)
		}

		reversed := []uuid.UUID{ids[2], ids[0], ids[1]}
		if err := repo.ReorderTabs(ctx, registration, reversed); err != nil {
			t.Fatalf("ReorderTabs() = %v", err)
		}
		_, tabs, err = repo.Load(ctx, registration, "unused")
		if err != nil {
			t.Fatalf("Load() = %v", err)
		}
		for i, tab := range tabs {
			if tab.ID != reversed[i] || tab.Position != i {
				t.Fatalf("tab %d = %v at %d, want %v at %d", i, tab.ID, tab.Position, reversed[i], i)
			}
		}
	})
}

// The workspace belongs to the registration and goes with it.
func TestTheWorkspaceIsDeletedWithTheRegistration(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewWorkspace(testPool)
		registration := workspaceRegistration(t, ctx)
		if _, _, err := repo.Load(ctx, registration, "Query 1"); err != nil {
			t.Fatalf("Load() = %v", err)
		}
		if _, err := repo.SaveNotes(ctx, registration, "the butler"); err != nil {
			t.Fatalf("SaveNotes() = %v", err)
		}

		q := storage.QuerierFrom(ctx, testPool)
		if _, err := q.Exec(ctx, `DELETE FROM registrations WHERE id = $1`, registration); err != nil {
			t.Fatalf("delete the registration: %v", err)
		}
		var notes, tabs int
		if err := q.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM participant_notes WHERE registration_id = $1),
			       (SELECT count(*) FROM participant_sql_tabs WHERE registration_id = $1)`,
			registration).Scan(&notes, &tabs); err != nil {
			t.Fatalf("count what is left: %v", err)
		}
		if notes != 0 || tabs != 0 {
			t.Fatalf("after the registration went, %d notes and %d tabs remain", notes, tabs)
		}
	})
}

// The path the deployment uses: no transaction around the call, so the
// repository opens its own for the writes that need one (CLAUDE.md rule 10).
func TestWorkspaceWritesWorkWithNoTransactionAroundThem(t *testing.T) {
	ctx, registration := committedRegistration(t)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	repo := NewWorkspace(testPool)

	_, tabs, err := repo.Load(ctx, registration, "Tab 1")
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	second, err := repo.CreateTab(ctx, registration, 10, numbered)
	if err != nil {
		t.Fatalf("CreateTab() = %v", err)
	}
	if err := repo.ReorderTabs(ctx, registration, []uuid.UUID{second.ID, tabs[0].ID}); err != nil {
		t.Fatalf("ReorderTabs() = %v", err)
	}
	if err := repo.DeleteTab(ctx, registration, tabs[0].ID); err != nil {
		t.Fatalf("DeleteTab() = %v", err)
	}
	_, after, err := repo.Load(ctx, registration, "unused")
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if len(after) != 1 || after[0].ID != second.ID || after[0].Position != 0 {
		t.Fatalf("tabs = %+v, want only the second at 0", after)
	}
}
