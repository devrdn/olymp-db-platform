package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/devrdn/olymp-db-platform/backend/internal/archive"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
)

// makeArchived stores a contest in the given state with one title.
func makeArchived(t *testing.T, ctx context.Context, author uuid.UUID, status, visibility, title string) uuid.UUID {
	t.Helper()
	id := makeContest(t, ctx, author)
	exec(t, ctx, `UPDATE contests SET status = $2, visibility = $3, ends_at = now() WHERE id = $1`, id, status, visibility)
	if title != "" {
		exec(t, ctx, `INSERT INTO contest_translations (contest_id, lang, title) VALUES ($1, 'en', $2)`, id, title)
	}
	return id
}

func archiveIDs(p archive.Page) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(p.Items))
	for _, c := range p.Items {
		out = append(out, c.ID)
	}
	return out
}

func TestArchiveListsOnlyOpenEndedContests(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-archive-scope")
		openFinished := makeArchived(t, ctx, author.ID, "finished", "open", "a")
		openArchived := makeArchived(t, ctx, author.ID, "archived", "open", "b")
		hidden := []uuid.UUID{
			makeArchived(t, ctx, author.ID, "finished", "closed", "c"),
			makeArchived(t, ctx, author.ID, "archived", "closed", "d"),
			makeArchived(t, ctx, author.ID, "running", "open", "e"),
			makeArchived(t, ctx, author.ID, "published", "open", "f"),
			makeArchived(t, ctx, author.ID, "draft", "open", "g"),
		}
		repo := NewArchive(testPool)

		got, err := repo.List(ctx, archive.Filter{Limit: 50})
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		ids := archiveIDs(got)
		if !slices.Contains(ids, openFinished) || !slices.Contains(ids, openArchived) {
			t.Errorf("List() = %v, want both open ended contests", ids)
		}
		for _, id := range hidden {
			if slices.Contains(ids, id) {
				t.Errorf("List() carried %s, which is not in the archive", id)
			}
			if _, err := repo.ByID(ctx, id); !errors.Is(err, archive.ErrNotFound) {
				t.Errorf("ByID(%s) = %v, want ErrNotFound", id, err)
			}
		}
		if got, err := repo.ByID(ctx, openFinished); err != nil || got.Visibility != "open" {
			t.Errorf("ByID(open finished) = visibility %q, %v; want open", got.Visibility, err)
		}
	})
}

func TestArchiveOrdersByEndThenIDAcrossPages(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-archive-order")
		// A title only this test's contests carry isolates them from any other
		// committed contest and makes the search run in the count as well.
		title := "order-" + uuid.NewString()
		var ids []uuid.UUID
		for i := 0; i < 3; i++ {
			ids = append(ids, makeArchived(t, ctx, author.ID, "finished", "open", title))
		}
		nullEnd := makeArchived(t, ctx, author.ID, "archived", "open", title)
		exec(t, ctx, `UPDATE contests SET ends_at = NULL WHERE id = $1`, nullEnd)
		exec(t, ctx, `UPDATE contests SET ends_at = '2026-01-01T00:00:00Z' WHERE id = ANY($1)`, ids)
		// Decoys the count must not take: another title, and the same title closed.
		makeArchived(t, ctx, author.ID, "finished", "open", "other-"+uuid.NewString())
		makeArchived(t, ctx, author.ID, "finished", "closed", title)
		repo := NewArchive(testPool)

		all := mustList(t, ctx, repo, archive.Filter{Limit: 50, Query: title})
		want := slices.Clone(ids)
		slices.SortFunc(want, func(a, b uuid.UUID) int { return strings.Compare(b.String(), a.String()) })
		want = append(want, nullEnd)
		if got := archiveIDs(all); !slices.Equal(got, want) {
			t.Errorf("order = %v, want ties by id descending then the NULL end last: %v", got, want)
		}
		if all.Total != 4 {
			t.Errorf("Total = %d, want 4", all.Total)
		}

		var paged []uuid.UUID
		for off := 0; ; off += 2 {
			p := mustList(t, ctx, repo, archive.Filter{Limit: 2, Offset: off, Query: title})
			if len(p.Items) == 0 {
				break
			}
			if p.Total != 4 {
				t.Errorf("Total at offset %d = %d, want 4", off, p.Total)
			}
			paged = append(paged, archiveIDs(p)...)
		}
		if !slices.Equal(paged, want) {
			t.Errorf("pages of 2 = %v, want one page of 50: %v", paged, want)
		}

		past := mustList(t, ctx, repo, archive.Filter{Limit: 2, Offset: 10, Query: title})
		if len(past.Items) != 0 || past.Total != 4 {
			t.Errorf("past the end = %d items, total %d; want no items and total 4", len(past.Items), past.Total)
		}
	})
}

func TestArchiveListsTheNewestEndFirst(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-archive-newest")
		title := "newest-" + uuid.NewString()
		older := makeArchived(t, ctx, author.ID, "finished", "open", title)
		newer := makeArchived(t, ctx, author.ID, "finished", "open", title)
		middle := makeArchived(t, ctx, author.ID, "archived", "open", title)
		exec(t, ctx, `UPDATE contests SET ends_at = '2026-01-01T00:00:00Z' WHERE id = $1`, older)
		exec(t, ctx, `UPDATE contests SET ends_at = '2026-03-01T00:00:00Z' WHERE id = $1`, newer)
		exec(t, ctx, `UPDATE contests SET ends_at = '2026-02-01T00:00:00Z' WHERE id = $1`, middle)

		got := archiveIDs(mustList(t, ctx, NewArchive(testPool), archive.Filter{Limit: 50, Query: title}))
		if want := []uuid.UUID{newer, middle, older}; !slices.Equal(got, want) {
			t.Errorf("order = %v, want the latest end first: %v", got, want)
		}
	})
}

func mustList(t *testing.T, ctx context.Context, repo *Archive, f archive.Filter) archive.Page {
	t.Helper()
	p, err := repo.List(ctx, f)
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	return p
}

func TestArchiveSearchTreatsWildcardsLiterally(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-archive-search")
		percent := makeArchived(t, ctx, author.ID, "finished", "open", "100% SQL")
		joins := makeArchived(t, ctx, author.ID, "finished", "open", "Joins")
		repo := NewArchive(testPool)

		got := archiveIDs(mustList(t, ctx, repo, archive.Filter{Query: "%", Limit: 50}))
		if !slices.Contains(got, percent) || slices.Contains(got, joins) {
			t.Errorf(`Query "%%" = %v, want only the title with a percent sign`, got)
		}
		if got := mustList(t, ctx, repo, archive.Filter{Query: `\`, Limit: 50}); len(got.Items) != 0 {
			t.Errorf(`Query "\" matched %d contests, want none`, len(got.Items))
		}
		got = archiveIDs(mustList(t, ctx, repo, archive.Filter{Query: "joi", Limit: 50}))
		if !slices.Contains(got, joins) || slices.Contains(got, percent) {
			t.Errorf(`Query "joi" = %v, want the Joins contest`, got)
		}
	})
}

func TestArchiveCountsParticipantsWithoutTheDisqualified(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-archive-count")
		id := makeArchived(t, ctx, author.ID, "finished", "open", "Counted")
		for _, login := range []string{"arch-p1", "arch-p2", "arch-p3"} {
			makeRegistration(t, ctx, id, makeUser(t, ctx, login).ID)
		}
		exec(t, ctx, `UPDATE registrations SET status = 'disqualified'
			WHERE id = (SELECT id FROM registrations WHERE contest_id = $1 LIMIT 1)`, id)

		got, err := NewArchive(testPool).ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if got.Participants != 2 {
			t.Errorf("Participants = %d, want 2", got.Participants)
		}
	})
}

func TestArchiveListLeavesDescriptionsToTheSingleRead(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-archive-descriptions")
		title := "described-" + uuid.NewString()
		id := makeArchived(t, ctx, author.ID, "finished", "open", "")
		exec(t, ctx, `INSERT INTO contest_translations (contest_id, lang, title, description)
			VALUES ($1, 'en', $2, 'A long story')`, id, title)
		repo := NewArchive(testPool)

		page := mustList(t, ctx, repo, archive.Filter{Limit: 50, Query: title})
		if len(page.Items) != 1 {
			t.Fatalf("List() = %d items, want 1", len(page.Items))
		}
		if got := page.Items[0]; got.Titles["en"] != title || len(got.Descriptions) != 0 {
			t.Errorf("List() item titles %v, descriptions %v; want the title and no descriptions",
				got.Titles, got.Descriptions)
		}
		one, err := repo.ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if one.Descriptions["en"] != "A long story" {
			t.Errorf("ByID() Descriptions = %v, want the description", one.Descriptions)
		}
	})
}

func TestArchiveCarriesEveryLanguagesTitle(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-archive-titles")
		id := makeArchived(t, ctx, author.ID, "finished", "open", "")
		exec(t, ctx, `INSERT INTO contest_languages (contest_id, lang, is_default) VALUES ($1, 'ro', true), ($1, 'en', false)`, id)
		exec(t, ctx, `INSERT INTO contest_translations (contest_id, lang, title, description)
			VALUES ($1, 'ro', 'Olimpiada', 'Descriere'), ($1, 'en', 'The Olympiad', NULL)`, id)

		got, err := NewArchive(testPool).ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if got.Titles["ro"] != "Olimpiada" || got.Titles["en"] != "The Olympiad" {
			t.Errorf("Titles = %v, want both translations", got.Titles)
		}
		if got.Descriptions["ro"] != "Descriere" {
			t.Errorf("ByID() Descriptions = %v, want the Romanian one", got.Descriptions)
		}
		if got.DefaultLanguage != "ro" {
			t.Errorf("DefaultLanguage = %q, want ro", got.DefaultLanguage)
		}
		if !slices.Equal(got.Languages, []string{"en", "ro"}) {
			t.Errorf("Languages = %v, want [en ro]", got.Languages)
		}
	})
}

// enable_seqscan is off so the assertion is about what the planner can
// match, not about the size of the test table: on a handful of rows it would
// choose a sequential scan whatever the index offered.
func TestArchiveListUsesTheArchiveIndex(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		q := storage.QuerierFrom(ctx, testPool)
		exec(t, ctx, `SET LOCAL enable_seqscan = off`)

		for name, query := range map[string]string{"without a query": "", "with a query": "sql"} {
			rows, err := q.Query(ctx, `EXPLAIN `+archiveListSQL, escapeLike(query), 20, 0)
			if err != nil {
				t.Fatalf("%s: explain: %v", name, err)
			}
			var plan strings.Builder
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatalf("%s: scan: %v", name, err)
				}
				plan.WriteString(line + "\n")
			}
			rows.Close()
			if !strings.Contains(plan.String(), "contests_archive_idx") {
				t.Errorf("%s: the plan does not use contests_archive_idx:\n%s", name, plan.String())
			}
			if strings.Contains(plan.String(), "Seq Scan on contests") {
				t.Errorf("%s: the plan scans contests sequentially:\n%s", name, plan.String())
			}
		}
	})
}

type switchOn struct{}

func (switchOn) ArchiveEnabled(context.Context) (bool, error) { return true, nil }

func TestArchiveTasksNeverCarryAReferenceAnswerOrHiddenQuestion(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-archive-tasks")
		id := makeArchived(t, ctx, author.ID, "finished", "open", "Tasks")
		exec(t, ctx, `UPDATE contests SET archive_tasks = true WHERE id = $1`, id)
		exec(t, ctx, `INSERT INTO contest_schema_notes (contest_id, lang, body_md) VALUES ($1, 'en', 'the tables')`, id)

		for n, visible := range []bool{true, false, true} {
			q := makeQuestion(t, ctx, id)
			title := fmt.Sprintf("visible-%d", n)
			if !visible {
				title = "HIDDEN-TITLE"
			}
			exec(t, ctx, `UPDATE questions SET is_visible = $2 WHERE id = $1`, q, visible)
			exec(t, ctx, `INSERT INTO question_translations (question_id, lang, body_md, title) VALUES ($1, 'en', $2, $3)`,
				q, "body", title)
			exec(t, ctx, `INSERT INTO question_answers (question_id, value) VALUES ($1, $2)`,
				q, fmt.Sprintf("REF-%d-SECRET", n))
		}

		svc := archive.NewService(archive.Config{
			Repository: NewArchive(testPool), Switch: switchOn{},
			Stories: NewStories(testPool), Notes: NewSchemaNotes(testPool), Questions: NewQuestions(testPool),
		})
		c, err := svc.Contest(ctx, id)
		if err != nil {
			t.Fatalf("Contest() = %v", err)
		}
		tasks, err := svc.Tasks(ctx, c, "en")
		if err != nil || tasks == nil {
			t.Fatalf("Tasks() = %v, %v", tasks, err)
		}
		if len(tasks.Questions) != 2 || tasks.Questions[0].Position != 1 || tasks.Questions[1].Position != 2 {
			t.Fatalf("questions = %+v, want the two visible ones at positions 1 and 2", tasks.Questions)
		}
		if tasks.SchemaNotes != "the tables" {
			t.Errorf("SchemaNotes = %q", tasks.SchemaNotes)
		}
		dump := fmt.Sprintf("%#v", tasks)
		for _, banned := range []string{"SECRET", "HIDDEN-TITLE"} {
			if strings.Contains(dump, banned) {
				t.Errorf("the task view carries %q: %s", banned, dump)
			}
		}
	})
}
