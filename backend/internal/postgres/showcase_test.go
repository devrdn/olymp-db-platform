package postgres

import (
	"context"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/showcase"
)

func TestRecentContestsLeaveOutDrafts(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewShowcase(testPool)
		author := makeUser(t, ctx, "author-showcase")
		published := makeContest(t, ctx, author.ID)
		exec(t, ctx, `UPDATE contests SET status = 'published' WHERE id = $1`, published)
		draft := makeContest(t, ctx, author.ID) // makeContest leaves it a draft

		got, err := repo.Recent(ctx, 6)
		if err != nil {
			t.Fatalf("Recent() = %v", err)
		}

		var seen []uuid.UUID
		for _, c := range got {
			seen = append(seen, c.ID)
		}
		if !slices.Contains(seen, published) {
			t.Errorf("Recent() = %v, want the published contest %s", seen, published)
		}
		if slices.Contains(seen, draft) {
			t.Error("a draft contest reached the public page")
		}
	})
}

// The caller, not the query, picks the visitor's language or the default.
func TestRecentContestsCarryEveryTitleTheContestHas(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-showcase-titles")
		contest := makeContest(t, ctx, author.ID)
		exec(t, ctx, `UPDATE contests SET status = 'running' WHERE id = $1`, contest)
		exec(t, ctx, `INSERT INTO contest_languages (contest_id, lang, is_default) VALUES ($1, 'ro', true), ($1, 'en', false)`, contest)
		exec(t, ctx, `
			INSERT INTO contest_translations (contest_id, lang, title)
			VALUES ($1, 'ro', 'Olimpiada'), ($1, 'en', 'The Olympiad')`, contest)

		got, err := NewShowcase(testPool).Recent(ctx, 6)
		if err != nil {
			t.Fatalf("Recent() = %v", err)
		}

		found, ok := byID(got, contest)
		if !ok {
			t.Fatalf("Recent() did not carry the running contest %s", contest)
		}
		if found.DefaultLanguage != "ro" {
			t.Errorf("DefaultLanguage = %q, want ro", found.DefaultLanguage)
		}
		if found.Titles["ro"] != "Olimpiada" || found.Titles["en"] != "The Olympiad" {
			t.Errorf("Titles = %v, want both translations", found.Titles)
		}
	})
}

// A table is open once the contest is over or its frozen result revealed.
func TestRecentContestsSayWhoseTableIsOpen(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-showcase-tables")
		running := makeContest(t, ctx, author.ID)
		exec(t, ctx, `UPDATE contests SET status = 'running' WHERE id = $1`, running)
		revealed := makeContest(t, ctx, author.ID)
		exec(t, ctx, `UPDATE contests SET status = 'running', leaderboard_revealed_at = now() WHERE id = $1`, revealed)
		finished := makeContest(t, ctx, author.ID)
		exec(t, ctx, `UPDATE contests SET status = 'finished' WHERE id = $1`, finished)

		got, err := NewShowcase(testPool).Recent(ctx, 50)
		if err != nil {
			t.Fatalf("Recent() = %v", err)
		}

		for _, want := range []struct {
			id   uuid.UUID
			open bool
			what string
		}{
			{running, false, "a running contest with a table nobody revealed"},
			{revealed, true, "a running contest whose table was revealed"},
			{finished, true, "a finished contest"},
		} {
			found, ok := byID(got, want.id)
			if !ok {
				t.Fatalf("Recent() did not carry %s", want.what)
			}
			if found.TableOpen != want.open {
				t.Errorf("%s: TableOpen = %v, want %v", want.what, found.TableOpen, want.open)
			}
		}
	})
}

func TestRecentContestsCarryTheCoverTheContestWears(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-showcase-covers")
		withCover := makeContest(t, ctx, author.ID)
		exec(t, ctx, `UPDATE contests SET status = 'published' WHERE id = $1`, withCover)
		cover := aCover(withCover, author.ID)
		if err := NewCovers(testPool).Save(ctx, cover); err != nil {
			t.Fatalf("Save() = %v", err)
		}
		bare := makeContest(t, ctx, author.ID)
		exec(t, ctx, `UPDATE contests SET status = 'published' WHERE id = $1`, bare)

		got, err := NewShowcase(testPool).Recent(ctx, 50)
		if err != nil {
			t.Fatalf("Recent() = %v", err)
		}

		found, ok := byID(got, withCover)
		if !ok {
			t.Fatalf("Recent() did not carry the contest with a cover %s", withCover)
		}
		if found.CoverHash != cover.Hash {
			t.Errorf("CoverHash = %q, want %q", found.CoverHash, cover.Hash)
		}
		if found.CoverAttribution != cover.Attribution {
			t.Errorf("CoverAttribution = %q, want %q", found.CoverAttribution, cover.Attribution)
		}

		// No upload: still listed, with an empty hash.
		plain, ok := byID(got, bare)
		if !ok {
			t.Fatalf("Recent() did not carry the contest without a cover %s", bare)
		}
		if plain.CoverHash != "" {
			t.Errorf("CoverHash = %q, want it empty for a contest with no uploaded cover", plain.CoverHash)
		}
	})
}

func TestRecentContestsStopAtTheLimit(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-showcase-limit")
		for range 3 {
			contest := makeContest(t, ctx, author.ID)
			exec(t, ctx, `UPDATE contests SET status = 'published' WHERE id = $1`, contest)
		}

		got, err := NewShowcase(testPool).Recent(ctx, 2)
		if err != nil {
			t.Fatalf("Recent() = %v", err)
		}
		if len(got) != 2 {
			t.Errorf("Recent(2) returned %d contests, want 2", len(got))
		}
	})
}

// Read as differences: the test database is shared by the whole package.
func TestNumbersCountTheSummaryTableAndNotTheQueryJournal(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewShowcase(testPool)
		before, err := repo.Numbers(ctx)
		if err != nil {
			t.Fatalf("Numbers() = %v", err)
		}

		author := makeUser(t, ctx, "author-showcase-numbers")
		finished := makeContest(t, ctx, author.ID)
		exec(t, ctx, `UPDATE contests SET status = 'finished' WHERE id = $1`, finished)
		// Published is not held, so it must not count.
		published := makeContest(t, ctx, author.ID)
		exec(t, ctx, `UPDATE contests SET status = 'published' WHERE id = $1`, published)

		registration := makeRegistration(t, ctx, finished, author.ID)
		exec(t, ctx, `INSERT INTO registration_activity (registration_id, queries) VALUES ($1, 7)`, registration)
		question := makeShowcaseQuestion(t, ctx, finished)
		exec(t, ctx, `
			INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct, submitted_at)
			VALUES ($1, $2, 1, 'no', false, now()), ($1, $2, 2, 'yes', true, now())`, registration, question)

		after, err := repo.Numbers(ctx)
		if err != nil {
			t.Fatalf("Numbers() = %v", err)
		}
		for _, want := range []struct {
			name       string
			before, at int64
			delta      int64
		}{
			{"contests", before.Contests, after.Contests, 1},
			{"participants", before.Participants, after.Participants, 1},
			{"queries", before.Queries, after.Queries, 7},
			{"solved", before.Solved, after.Solved, 1},
		} {
			if got := want.at - want.before; got != want.delta {
				t.Errorf("%s rose by %d, want %d", want.name, got, want.delta)
			}
		}
	})
}

// A counter raised with no answers behind it still moves Solved, which only a
// read of registration_activity can do.
func TestSolvedCountsTheAnswerCounterAndNotTheAnswersJournal(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewShowcase(testPool)
		before, err := repo.Numbers(ctx)
		if err != nil {
			t.Fatalf("Numbers() = %v", err)
		}
		countedBefore := correctCounter(t, ctx)

		author := makeUser(t, ctx, "author-showcase-solved")
		contest := makeContest(t, ctx, author.ID)
		exec(t, ctx, `UPDATE contests SET status = 'finished' WHERE id = $1`, contest)
		question := makeShowcaseQuestion(t, ctx, contest)

		// The trigger folds these into the counter; two are correct.
		answered := makeRegistration(t, ctx, contest, author.ID)
		exec(t, ctx, `
			INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct, submitted_at)
			VALUES ($1, $2, 1, 'no', false, now()),
			       ($1, $2, 2, 'yes', true, now()),
			       ($1, $2, 3, 'yes again', true, now())`, answered, question)

		// A counter with no answers behind it; never written in production.
		other := makeUser(t, ctx, "participant-showcase-solved")
		bare := makeRegistration(t, ctx, contest, other.ID)
		exec(t, ctx, `INSERT INTO registration_activity (registration_id, correct) VALUES ($1, 5)`, bare)

		after, err := repo.Numbers(ctx)
		if err != nil {
			t.Fatalf("Numbers() = %v", err)
		}
		if got := after.Solved - before.Solved; got != 7 {
			t.Errorf("solved rose by %d, want the 7 the counters hold", got)
		}
		if got, want := after.Solved-before.Solved, correctCounter(t, ctx)-countedBefore; got != want {
			t.Errorf("solved rose by %d, want the counter's own rise of %d", got, want)
		}
	})
}

func correctCounter(t *testing.T, ctx context.Context) int64 {
	t.Helper()
	var total int64
	err := storage.QuerierFrom(ctx, testPool).
		QueryRow(ctx, `SELECT COALESCE(sum(correct), 0) FROM registration_activity`).Scan(&total)
	if err != nil {
		t.Fatalf("read the answer counter: %v", err)
	}
	return total
}

func byID(list []showcase.Contest, id uuid.UUID) (showcase.Contest, bool) {
	for _, c := range list {
		if c.ID == id {
			return c, true
		}
	}
	return showcase.Contest{}, false
}

func makeShowcaseQuestion(t *testing.T, ctx context.Context, contest uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `
		INSERT INTO questions (contest_id, ord, kind, points, is_visible)
		VALUES ($1, 1, 'text', 10, true) RETURNING id`, contest).Scan(&id)
	if err != nil {
		t.Fatalf("create question: %v", err)
	}
	return id
}
