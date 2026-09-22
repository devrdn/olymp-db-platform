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

// The title is the visitor's language when the contest has it, and the
// contest's own default when it does not: the same translations the catalogue
// reads, chosen by the caller instead of by the query.
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

// A contest's table is open once the contest is over, or once its frozen
// result was revealed: the flag says whether the page may link to it at all.
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

// The card on the showcase carries a picture, so the list that draws the
// cards has to say which contest has one and under whose name it is shown.
//
// It comes off the same statement as everything else on the row. One read for
// the page and not one per card: six covers behind a page anybody may load
// without signing in would be six round trips a minute of cache cannot spare
// anyone, and a contest without a picture is not a second query — it wears a
// drawn cover, which needs nothing from storage at all.
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

		// A contest nobody uploaded a picture for is still a row, and the
		// absence is an empty hash rather than a missing row: the drawn cover
		// is what it wears, and the page decides that from this field alone.
		plain, ok := byID(got, bare)
		if !ok {
			t.Fatalf("Recent() did not carry the contest without a cover %s", bare)
		}
		if plain.CoverHash != "" {
			t.Errorf("CoverHash = %q, want it empty for a contest with no uploaded cover", plain.CoverHash)
		}
	})
}

// The bound is the query's, not the caller's to hope for: the page asks for
// six and is never handed a seventh.
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

// The numbers are read as differences rather than as absolutes: the test
// database is shared by every test in this package, and what this read must
// prove is that each fixture row lands on the right counter.
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
		// A published contest is not a contest that was held.
		published := makeContest(t, ctx, author.ID)
		exec(t, ctx, `UPDATE contests SET status = 'published' WHERE id = $1`, published)

		registration := makeRegistration(t, ctx, finished, author.ID)
		// The counter the landing page reads: the summary the triggers keep,
		// never count(*) over query_log.
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

// Solved is the answer counter registration_activity keeps, for the reason
// queries is the query counter: the answers journal grows with every attempt
// anybody makes and carries no index that serves this, and the page asking
// for it is one nobody has to sign in to load.
//
// The two are the same number — the counter is maintained by the trigger that
// sees every insert, and both the counter's row and the answers cascade
// together when a registration is deleted — so the test proves both halves:
// an ordinary answer moves the read, and a counter raised with no answer
// behind it moves it too, which only a read of the summary can do.
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

		// The ordinary path: the answers are written and the trigger folds
		// them into the counter. Two of the three are correct.
		answered := makeRegistration(t, ctx, contest, author.ID)
		exec(t, ctx, `
			INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct, submitted_at)
			VALUES ($1, $2, 1, 'no', false, now()),
			       ($1, $2, 2, 'yes', true, now()),
			       ($1, $2, 3, 'yes again', true, now())`, answered, question)

		// A counter with answers the journal no longer has to hold. Nothing
		// writes this in production; here it is what tells the two readings
		// apart, the way the queries fixture does above.
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

// correctCounter is what registration_activity holds for correct answers right
// now, which is what Numbers has to report.
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
