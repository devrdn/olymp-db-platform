package postgres

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestContestSurvivesARoundTrip(t *testing.T) {
	// Every column, in one test: a field that is written but not read back is
	// the failure this catches, and it is invisible from the domain tests.
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-roundtrip")
		start := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
		end := start.Add(3 * time.Hour)
		deadline := start.Add(-time.Hour)
		minutes := 90

		created, err := repo.Create(ctx, contests.Contest{
			Status:       contests.StatusDraft,
			Enrollment:   contests.EnrollmentOpen,
			QuestionMode: contests.QuestionModeSingle,
			Timing:       contests.TimingIndividual,
			DurationMin:  &minutes,
			StartsAt:     &start,
			EndsAt:       &end,
			AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")},
			Settings: contests.Settings{
				EnrollmentDeadline:   &deadline,
				QueryRateLimitPerMin: 30,
				GracePeriodMin:       15,
			},
			CreatedBy: author.ID,
		})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}

		loaded, err := repo.ByID(ctx, created.ID)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}

		switch {
		case loaded.Enrollment != contests.EnrollmentOpen:
			t.Errorf("enrollment = %q, want open", loaded.Enrollment)
		case loaded.QuestionMode != contests.QuestionModeSingle:
			t.Errorf("question mode = %q, want single", loaded.QuestionMode)
		case loaded.Timing != contests.TimingIndividual:
			t.Errorf("timing = %q, want individual", loaded.Timing)
		case loaded.DurationMin == nil || *loaded.DurationMin != minutes:
			t.Errorf("duration = %v, want %d", loaded.DurationMin, minutes)
		case loaded.StartsAt == nil || !loaded.StartsAt.Equal(start):
			t.Errorf("starts_at = %v, want %v", loaded.StartsAt, start)
		case len(loaded.AllowedCIDRs) != 1 || loaded.AllowedCIDRs[0].String() != "10.20.0.0/16":
			t.Errorf("allowed cidrs = %v, want [10.20.0.0/16]", loaded.AllowedCIDRs)
		case loaded.Settings.QueryRateLimitPerMin != 30:
			t.Errorf("query rate limit = %d, want 30", loaded.Settings.QueryRateLimitPerMin)
		case loaded.Settings.EnrollmentDeadline == nil || !loaded.Settings.EnrollmentDeadline.Equal(deadline):
			t.Errorf("enrollment deadline = %v, want %v", loaded.Settings.EnrollmentDeadline, deadline)
		case loaded.CreatedBy != author.ID:
			t.Errorf("created_by = %v, want %v", loaded.CreatedBy, author.ID)
		}
	})
}

func TestUnknownContestIsNotFound(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		if _, err := NewContests(testPool).ByID(ctx, uuid.New()); !errors.Is(err, contests.ErrNotFound) {
			t.Errorf("ByID() = %v, want ErrNotFound", err)
		}
	})
}

// checkViolation is the SQLSTATE PostgreSQL raises for a CHECK constraint.
const checkViolation = "23514"

// Finding 5: internal/contests.Contest.Validate was the only thing bounding
// duration_min before this migration — a row written by hand, or one that
// predates the check, was not, and Deadline's
// time.Duration(*DurationMin)*time.Minute arithmetic overflows and wraps to a
// deadline in the past well before an int this size otherwise would. This
// goes straight through Exec rather than the repository, the same way a
// hand-written row or a pre-migration one would have reached the table: the
// domain's own Create was never in a position to stop either.
func TestDurationMinIsBoundedAtTheDatabaseToo(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-duration-bound")
		const overTheBound = 10081 // maxDurationMin (contests.go) + 1

		_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`INSERT INTO contests (created_by, timing, duration_min) VALUES ($1, 'individual', $2)`,
			author.ID, overTheBound)

		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != checkViolation {
			t.Fatalf("insert with duration_min = %d: err = %v, want a %s check violation", overTheBound, err, checkViolation)
		}
	})
}

// A duration exactly at the bound is still accepted — this is a ceiling, not
// a tighter limit than the domain's own.
func TestDurationMinAtTheBoundIsAcceptedByTheDatabase(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-duration-at-bound")
		const atTheBound = 10080 // maxDurationMin (contests.go)

		_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`INSERT INTO contests (created_by, timing, duration_min) VALUES ($1, 'individual', $2)`,
			author.ID, atTheBound)
		if err != nil {
			t.Fatalf("insert with duration_min = %d (the bound itself): %v", atTheBound, err)
		}
	})
}

func TestLanguagesAndTranslationsComeBackWithTheContest(t *testing.T) {
	// The publish gate reads both from the contest it was handed; loading them
	// separately would be an N+1 and a chance for them to disagree.
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-langs")
		id := makeContest(t, ctx, author.ID)

		if err := repo.ReplaceLanguages(ctx, id, []contests.ContestLanguage{
			{Code: "ro"}, {Code: "en", IsDefault: true},
		}); err != nil {
			t.Fatalf("ReplaceLanguages() = %v", err)
		}
		if err := repo.ReplaceTranslations(ctx, id, []contests.Translation{
			{Lang: "en", Title: "The Library Murder", Description: "A locked room."},
			{Lang: "ro", Title: "Crima din bibliotecă"},
		}); err != nil {
			t.Fatalf("ReplaceTranslations() = %v", err)
		}

		loaded, err := repo.ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if loaded.DefaultLanguage() != "en" {
			t.Errorf("default language = %q, want en", loaded.DefaultLanguage())
		}
		if loaded.Translations["ro"].Title != "Crima din bibliotecă" {
			t.Errorf("Romanian title = %q", loaded.Translations["ro"].Title)
		}
		if loaded.Translations["en"].Description != "A locked room." {
			t.Errorf("English description = %q", loaded.Translations["en"].Description)
		}
	})
}

func TestReplacingLanguagesDropsTheOnesLeftOut(t *testing.T) {
	// "Replace" has to mean replace: a language quietly left behind would keep
	// the publish gate demanding translations for something nobody offers.
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-replace")
		id := makeContest(t, ctx, author.ID)

		if err := repo.ReplaceLanguages(ctx, id, []contests.ContestLanguage{
			{Code: "en", IsDefault: true}, {Code: "ro"},
		}); err != nil {
			t.Fatalf("ReplaceLanguages() = %v", err)
		}
		if err := repo.ReplaceLanguages(ctx, id, []contests.ContestLanguage{
			{Code: "en", IsDefault: true},
		}); err != nil {
			t.Fatalf("ReplaceLanguages() = %v", err)
		}

		loaded, _ := repo.ByID(ctx, id)
		if got := loaded.LanguageCodes(); len(got) != 1 || got[0] != "en" {
			t.Errorf("languages = %v, want [en]", got)
		}
	})
}

func TestListFindsAContestByItsTranslatedTitle(t *testing.T) {
	// The title lives only in the translations, so searching has to reach
	// them — in any language, since staff search in the one they think in.
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-search")
		id := makeContest(t, ctx, author.ID)
		if err := repo.ReplaceTranslations(ctx, id, []contests.Translation{
			{Lang: "ro", Title: "Crima din bibliotecă"},
		}); err != nil {
			t.Fatalf("ReplaceTranslations() = %v", err)
		}

		found, total, err := repo.List(ctx, contests.Filter{Query: "bibliotec", Limit: 10})
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if total == 0 || len(found) == 0 {
			t.Fatalf("List() found nothing, want the contest")
		}
		if found[0].ID != id {
			t.Errorf("found %v, want %v", found[0].ID, id)
		}
	})
}

func TestListLimitsAnOrganizerToTheContestsTheyStaff(t *testing.T) {
	// This is what keeps one organizer's list their own without the repository
	// knowing anything about permissions.
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		mine := makeUser(t, ctx, "author-mine")
		theirs := makeUser(t, ctx, "author-theirs")
		myContest := makeContest(t, ctx, mine.ID)
		otherContest := makeContest(t, ctx, theirs.ID)

		managers := NewContestManagers(testPool)
		for id, owner := range map[uuid.UUID]uuid.UUID{myContest: mine.ID, otherContest: theirs.ID} {
			if err := managers.Grant(ctx, contests.Manager{
				ContestID: id, UserID: owner, Role: "owner", GrantedBy: owner,
			}); err != nil {
				t.Fatalf("Grant() = %v", err)
			}
		}

		found, _, err := repo.List(ctx, contests.Filter{ManagedBy: mine.ID, Limit: 50})
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		for _, c := range found {
			if c.ID == otherContest {
				t.Fatalf("List() returned a contest the organizer does not staff")
			}
		}
	})
}

func TestUpdateLeavesTheStatusAlone(t *testing.T) {
	// Status moves through Transition, which is where the lifecycle rules and
	// the publish gate are. An update path that could also move it would be a
	// way around both.
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-status")
		id := makeContest(t, ctx, author.ID)
		if err := repo.SetStatus(ctx, id, contests.StatusDraft, contests.StatusPublished); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}

		loaded, _ := repo.ByID(ctx, id)
		loaded.Status = contests.StatusArchived
		loaded.Enrollment = contests.EnrollmentOpen
		if err := repo.Update(ctx, loaded); err != nil {
			t.Fatalf("Update() = %v", err)
		}

		after, _ := repo.ByID(ctx, id)
		if after.Status != contests.StatusPublished {
			t.Errorf("status = %q, want it unchanged at published", after.Status)
		}
		if after.Enrollment != contests.EnrollmentOpen {
			t.Errorf("enrollment = %q, want the update applied", after.Enrollment)
		}
	})
}

func TestDeletingAContestTakesItsTranslationsWithIt(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-delete")
		id := makeContest(t, ctx, author.ID)
		if err := repo.ReplaceTranslations(ctx, id, []contests.Translation{
			{Lang: "en", Title: "Gone"},
		}); err != nil {
			t.Fatalf("ReplaceTranslations() = %v", err)
		}

		if err := repo.Delete(ctx, id); err != nil {
			t.Fatalf("Delete() = %v", err)
		}

		var left int
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT count(*) FROM contest_translations WHERE contest_id = $1`, id).Scan(&left); err != nil {
			t.Fatalf("count translations: %v", err)
		}
		if left != 0 {
			t.Errorf("%d translations left behind, want 0", left)
		}
	})
}

func TestTheDefaultLanguageCanBeMovedToAnotherLanguage(t *testing.T) {
	// A partial unique index allows one default per contest, and it is checked
	// row by row. Switching the default has to survive the moment when the old
	// one has not been cleared yet.
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-default-switch")
		id := makeContest(t, ctx, author.ID)
		if err := repo.ReplaceLanguages(ctx, id, []contests.ContestLanguage{
			{Code: "en", IsDefault: true}, {Code: "ro"},
		}); err != nil {
			t.Fatalf("ReplaceLanguages() = %v", err)
		}

		err := repo.ReplaceLanguages(ctx, id, []contests.ContestLanguage{
			{Code: "ro", IsDefault: true}, {Code: "en"},
		})
		if err != nil {
			t.Fatalf("moving the default language failed: %v", err)
		}

		loaded, _ := repo.ByID(ctx, id)
		if got := loaded.DefaultLanguage(); got != "ro" {
			t.Errorf("default language = %q, want ro", got)
		}
	})
}

func TestSetStatusRefusesAStatusThatMovedUnderneathIt(t *testing.T) {
	// The guard that makes the check-then-write atomic. Transition decides
	// against a status it read moments earlier; if a concurrent request has
	// since moved the contest, the second write must not land on a state
	// nobody examined — the publish gate ran against the old one.
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-cas")
		id := makeContest(t, ctx, author.ID)

		if err := repo.SetStatus(ctx, id, contests.StatusDraft, contests.StatusPublished); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}

		// A second caller still holding "draft", as a concurrent request would.
		err := repo.SetStatus(ctx, id, contests.StatusDraft, contests.StatusRunning)

		if !errors.Is(err, contests.ErrStatusChanged) {
			t.Errorf("SetStatus() = %v, want ErrStatusChanged", err)
		}
		if after, _ := repo.ByID(ctx, id); after.Status != contests.StatusPublished {
			t.Errorf("status = %q, want the first writer's value to stand", after.Status)
		}
	})
}

func TestSetStatusStillReportsAContestThatIsGone(t *testing.T) {
	// "Nothing matched" has two causes and they are different answers: the
	// row moved, or there is no row. Collapsing them would report a deleted
	// contest as a conflict somebody could retry.
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)

		err := repo.SetStatus(ctx, uuid.New(), contests.StatusDraft, contests.StatusPublished)

		if !errors.Is(err, contests.ErrNotFound) {
			t.Errorf("SetStatus() = %v, want ErrNotFound", err)
		}
	})
}

// setContest puts a seeded contest into a state the visibility rule reacts to.
func setContest(t *testing.T, ctx context.Context, id uuid.UUID, status, enrollment string) {
	t.Helper()
	_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`UPDATE contests SET status = $2, enrollment = $3 WHERE id = $1`, id, status, enrollment)
	if err != nil {
		t.Fatalf("set contest state: %v", err)
	}
}

func TestAParticipantSeesTheirOwnContestsAndWhatIsOpen(t *testing.T) {
	// The rule the two participant screens are cut from, and the one that
	// decides what a student may see at all.
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-vis")
		student := makeUser(t, ctx, "student-vis")

		mine := makeContest(t, ctx, author.ID)
		open := makeContest(t, ctx, author.ID)
		shut := makeContest(t, ctx, author.ID)
		draft := makeContest(t, ctx, author.ID)

		setContest(t, ctx, mine, contests.StatusPublished, contests.EnrollmentInviteOnly)
		setContest(t, ctx, open, contests.StatusPublished, contests.EnrollmentOpen)
		setContest(t, ctx, shut, contests.StatusPublished, contests.EnrollmentInviteOnly)
		setContest(t, ctx, draft, contests.StatusDraft, contests.EnrollmentOpen)

		if _, err := NewRegistrations(testPool).Add(ctx, mine, student.ID); err != nil {
			t.Fatalf("Add() = %v", err)
		}

		found, _, err := repo.List(ctx, contests.Filter{VisibleTo: student.ID}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		seen := idSet(found)
		if !seen[mine] {
			t.Error("the contest the student is registered for is missing")
		}
		if !seen[open] {
			t.Error("a contest open for signup is missing")
		}
		// An invitation-only contest they are not on is not their business,
		// and a draft is nobody's but its authors'.
		if seen[shut] {
			t.Error("an invitation-only contest the student is not on was listed")
		}
		if seen[draft] {
			t.Error("a draft was listed to a participant")
		}
	})
}

func TestAParticipantCanAskForOnlyTheContestsTheyAreOn(t *testing.T) {
	// What /my asks. The two lists answer different questions, and the one
	// asked under a timer on the day must not be diluted by the one browsed
	// once a term.
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-mine")
		student := makeUser(t, ctx, "student-mine")

		mine := makeContest(t, ctx, author.ID)
		open := makeContest(t, ctx, author.ID)
		setContest(t, ctx, mine, contests.StatusPublished, contests.EnrollmentInviteOnly)
		setContest(t, ctx, open, contests.StatusPublished, contests.EnrollmentOpen)
		if _, err := NewRegistrations(testPool).Add(ctx, mine, student.ID); err != nil {
			t.Fatalf("Add() = %v", err)
		}

		enrolled := true
		found, total, err := repo.List(ctx,
			contests.Filter{VisibleTo: student.ID, Enrolled: &enrolled}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if total != 1 || !idSet(found)[mine] {
			t.Errorf("List() returned %d contests, want only the one they are on", total)
		}
	})
}

func TestNarrowingByEnrolmentCannotWidenWhatIsVisible(t *testing.T) {
	// The flag narrows the visible set and must never reach outside it.
	// Asking for "not enrolled" must not turn into a listing of every
	// invitation-only contest in the installation.
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-narrow")
		student := makeUser(t, ctx, "student-narrow")

		shut := makeContest(t, ctx, author.ID)
		open := makeContest(t, ctx, author.ID)
		setContest(t, ctx, shut, contests.StatusPublished, contests.EnrollmentInviteOnly)
		setContest(t, ctx, open, contests.StatusPublished, contests.EnrollmentOpen)

		notEnrolled := false
		found, _, err := repo.List(ctx,
			contests.Filter{VisibleTo: student.ID, Enrolled: &notEnrolled}.Normalize())
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		seen := idSet(found)
		if seen[shut] {
			t.Error("an invitation-only contest leaked through the not-enrolled filter")
		}
		if !seen[open] {
			t.Error("the open contest the student could join is missing")
		}
	})
}

func TestEnrolledInReportsOnlyTheCallersOwnRegistrations(t *testing.T) {
	// The flag the catalogue marks its rows with. Reporting somebody else's
	// registration would disclose who takes part in what.
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-on")
		student := makeUser(t, ctx, "student-on")
		other := makeUser(t, ctx, "other-on")

		mine := makeContest(t, ctx, author.ID)
		theirs := makeContest(t, ctx, author.ID)
		if _, err := repo.Add(ctx, mine, student.ID); err != nil {
			t.Fatalf("Add() = %v", err)
		}
		if _, err := repo.Add(ctx, theirs, other.ID); err != nil {
			t.Fatalf("Add() = %v", err)
		}

		on, err := repo.EnrolledIn(ctx, student.ID, []uuid.UUID{mine, theirs})
		if err != nil {
			t.Fatalf("EnrolledIn() = %v", err)
		}

		if !on[mine] {
			t.Error("the student's own registration is not reported")
		}
		if on[theirs] {
			t.Error("another account's registration was reported as this student's")
		}
	})
}

// idSet indexes a listing by identifier, for readable assertions.
func idSet(found []contests.Contest) map[uuid.UUID]bool {
	seen := make(map[uuid.UUID]bool, len(found))
	for _, c := range found {
		seen[c.ID] = true
	}
	return seen
}
