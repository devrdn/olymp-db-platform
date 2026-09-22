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
		freeze := 20

		created, err := repo.Create(ctx, contests.Contest{
			Status:       contests.StatusDraft,
			Enrollment:   contests.EnrollmentOpen,
			QuestionMode: contests.QuestionModeSingle,
			Progression:  contests.ProgressionSequential,
			Scoring:      contests.ScoringWinner,
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
			LeaderboardFreezeMin: &freeze,
			LeaderboardNames:     contests.LeaderboardNamesFullName,
			ICPCPenaltyMin:       45,
			CreatedBy:            author.ID,
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
		case loaded.Progression != contests.ProgressionSequential:
			t.Errorf("progression = %q, want sequential", loaded.Progression)
		case loaded.Scoring != contests.ScoringWinner:
			t.Errorf("scoring = %q, want winner", loaded.Scoring)
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
		case loaded.LeaderboardFreezeMin == nil || *loaded.LeaderboardFreezeMin != freeze:
			t.Errorf("leaderboard freeze = %v, want %d", loaded.LeaderboardFreezeMin, freeze)
		case loaded.LeaderboardNames != contests.LeaderboardNamesFullName:
			t.Errorf("leaderboard names = %q, want full_name", loaded.LeaderboardNames)
		case loaded.ICPCPenaltyMin != 45:
			t.Errorf("icpc penalty min = %d, want 45", loaded.ICPCPenaltyMin)
		case loaded.LeaderboardRevealedAt != nil:
			t.Errorf("leaderboard revealed at = %v, want nil", loaded.LeaderboardRevealedAt)
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

// The picture a contest wears comes back with the contest, because the one
// screen that shows a picture above a story (design spec §10) already reads
// this listing and opens under a timer. A projection is the whole point: a
// second read per row would be the N+1 the languages and translations are
// already written to avoid.
func TestAListingCarriesTheCoverEachContestWears(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-listing-cover")
		dressed := makeContest(t, ctx, author.ID)
		bare := makeContest(t, ctx, author.ID)
		if err := NewCovers(testPool).Save(ctx, aCover(dressed, author.ID)); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		found, _, err := repo.List(ctx, contests.Filter{Limit: 10})
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		byID := map[uuid.UUID]contests.Contest{}
		for _, c := range found {
			byID[c.ID] = c
		}
		want := aCover(dressed, author.ID)
		if got := byID[dressed]; got.CoverHash != want.Hash {
			t.Errorf("CoverHash = %q, want %q", got.CoverHash, want.Hash)
		}
		if got := byID[dressed]; got.CoverAttribution != want.Attribution {
			t.Errorf("CoverAttribution = %q, want %q", got.CoverAttribution, want.Attribution)
		}
		// Empty, not a failed read: a contest nobody uploaded a picture for
		// wears the drawn cover, and that is an ordinary state.
		if got := byID[bare]; got.CoverHash != "" || got.CoverAttribution != "" {
			t.Errorf("a contest with no uploaded cover read back %q/%q, want both empty",
				got.CoverHash, got.CoverAttribution)
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

// Update writes icpc_penalty_min the same way Create does — a column added
// after Update's own SQL was last touched is exactly the one a future column
// gets left out of by mistake.
func TestUpdateWritesTheICPCPenalty(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-icpc-penalty")
		id := makeContest(t, ctx, author.ID)

		before, err := repo.ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if before.ICPCPenaltyMin != 20 {
			t.Fatalf("icpc penalty min = %d, want the column's own default of 20", before.ICPCPenaltyMin)
		}

		before.ICPCPenaltyMin = 50
		if err := repo.Update(ctx, before); err != nil {
			t.Fatalf("Update() = %v", err)
		}

		after, err := repo.ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if after.ICPCPenaltyMin != 50 {
			t.Errorf("icpc penalty min = %d, want 50", after.ICPCPenaltyMin)
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

// TestTryLockRefusesASecondHolderUntilTheFirstEndsItsTransaction proves the
// guarantee contests.Scheduler's whole design rests on across two real
// connections, not one: pg_try_advisory_xact_lock is a lock between database
// sessions, and a test that only ever opens one transaction could not tell
// the difference between "this call cannot get the lock" and "this call
// forgot to ask" (CLAUDE.md rule 10 — prove it on the path the deployment
// uses).
func TestTryLockRefusesASecondHolderUntilTheFirstEndsItsTransaction(t *testing.T) {
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	repo := NewContests(testPool)
	ctx := context.Background()

	holding := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)

	go func() {
		firstDone <- storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
			ok, err := repo.TryLock(ctx)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("first caller did not get an uncontested lock")
			}
			close(holding)
			<-release
			return errRollback
		})
	}()

	<-holding
	err := storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
		ok, err := repo.TryLock(ctx)
		if err != nil {
			return err
		}
		if ok {
			t.Error("a second, independent transaction acquired the lock while the first still holds it")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("second TryLock() attempt failed: %v", err)
	}

	close(release)
	if err := <-firstDone; err != nil && !errors.Is(err, errRollback) {
		t.Fatalf("first transaction failed: %v", err)
	}

	err = storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
		ok, err := repo.TryLock(ctx)
		if err != nil {
			return err
		}
		if !ok {
			t.Error("lock is still held after the transaction that acquired it ended")
		}
		return errRollback
	})
	if err != nil && !errors.Is(err, errRollback) {
		t.Fatalf("third TryLock() attempt failed: %v", err)
	}
}

// setSchedule puts a seeded contest (already valid by makeContest's own
// column defaults) into the status and window a DueToStart or
// AdvanceFinished test needs, without fighting the enumerated CHECK
// constraints a hand-built contests.Contest would have to satisfy on every
// other field.
func setSchedule(t *testing.T, ctx context.Context, id uuid.UUID, status string, startsAt, endsAt *time.Time) {
	t.Helper()
	_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`UPDATE contests SET status = $2, starts_at = $3, ends_at = $4 WHERE id = $1`,
		id, status, startsAt, endsAt)
	if err != nil {
		t.Fatalf("set contest schedule: %v", err)
	}
}

func TestDueToStartFindsOnlyPublishedContestsPastTheirStart(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-due-to-start")
		past := time.Now().Add(-time.Hour)
		future := time.Now().Add(time.Hour)

		due := makeContest(t, ctx, author.ID)
		setSchedule(t, ctx, due, contests.StatusPublished, &past, nil)
		notYet := makeContest(t, ctx, author.ID)
		setSchedule(t, ctx, notYet, contests.StatusPublished, &future, nil)
		alreadyRunning := makeContest(t, ctx, author.ID)
		setSchedule(t, ctx, alreadyRunning, contests.StatusRunning, &past, nil)

		found, err := repo.DueToStart(ctx)
		if err != nil {
			t.Fatalf("DueToStart() = %v", err)
		}

		seen := idSet(found)
		if !seen[due] {
			t.Errorf("DueToStart() = %v, want it to include the due contest %s", seen, due)
		}
		if seen[notYet] {
			t.Error("DueToStart() returned a contest whose start is still in the future")
		}
		if seen[alreadyRunning] {
			t.Error("DueToStart() returned a contest that was already running")
		}

		// DueToStart only reads: Scheduler decides whether a candidate may
		// move once it has re-run the publish gate against it (finding 1),
		// and this method has no business making that call itself.
		loaded, _ := repo.ByID(ctx, due)
		if loaded.Status != contests.StatusPublished {
			t.Errorf("DueToStart() moved a contest by itself; status = %q, want it left published", loaded.Status)
		}
	})
}

func TestAdvanceFinishedMovesOnlyRunningContestsPastTheirEnd(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-advance-finished")
		past := time.Now().Add(-time.Hour)
		future := time.Now().Add(time.Hour)

		over := makeContest(t, ctx, author.ID)
		setSchedule(t, ctx, over, contests.StatusRunning, nil, &past)
		stillOpen := makeContest(t, ctx, author.ID)
		setSchedule(t, ctx, stillOpen, contests.StatusRunning, nil, &future)
		// Individual timing needs no ends_at to publish (CheckPublishable): a
		// contest left with none must never be swept up here, or an
		// organizer's still-open olympiad closes itself for no reason anybody
		// set.
		noEnd := makeContest(t, ctx, author.ID)
		setSchedule(t, ctx, noEnd, contests.StatusRunning, nil, nil)

		moved, err := repo.AdvanceFinished(ctx, 0)
		if err != nil {
			t.Fatalf("AdvanceFinished() = %v", err)
		}

		if !idInList(moved, over) {
			t.Errorf("AdvanceFinished() = %v, want it to include the overdue contest %s", moved, over)
		}
		if idInList(moved, stillOpen) {
			t.Errorf("AdvanceFinished() moved a contest whose end is still in the future")
		}
		if idInList(moved, noEnd) {
			t.Errorf("AdvanceFinished() moved a contest with no ends_at at all")
		}

		loaded, _ := repo.ByID(ctx, over)
		if loaded.Status != contests.StatusFinished {
			t.Errorf("overdue contest's status = %q, want finished", loaded.Status)
		}
	})
}

// TestAdvanceFinishedRespectsTheGracePeriod is a regression test: the
// scheduler used to compare only ends_at against now(), so a contest whose
// deadline (§8's own formula) still carried its network-latency grace was
// closed a tick early — the same request a fixed-timing participant's answer
// or query is admitted for (deadline.go, queryproxy.Admitted) was refused by
// the scheduler's own comparison, at random depending on which tick won the
// race. Passing the grace through to the WHERE clause keeps both doors
// agreeing on one deadline.
func TestAdvanceFinishedRespectsTheGracePeriod(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-advance-grace")
		const grace = 5 * time.Second

		// Ended two seconds ago: still inside a five-second grace, so a
		// participant's late answer or query is still admitted (the same
		// window submission.go and queryproxy.Admitted check), and the
		// scheduler must not close the contest out from under them.
		insideGrace := makeContest(t, ctx, author.ID)
		endedRecently := time.Now().Add(-2 * time.Second)
		setSchedule(t, ctx, insideGrace, contests.StatusRunning, nil, &endedRecently)

		// Ended ten seconds ago: past even the grace, so this one must finish.
		pastGrace := makeContest(t, ctx, author.ID)
		endedAWhileAgo := time.Now().Add(-10 * time.Second)
		setSchedule(t, ctx, pastGrace, contests.StatusRunning, nil, &endedAWhileAgo)

		moved, err := repo.AdvanceFinished(ctx, grace)
		if err != nil {
			t.Fatalf("AdvanceFinished() = %v", err)
		}

		if idInList(moved, insideGrace) {
			t.Errorf("AdvanceFinished() finished a contest still inside its grace period")
		}
		if !idInList(moved, pastGrace) {
			t.Errorf("AdvanceFinished() = %v, want it to include the contest past its grace %s", moved, pastGrace)
		}

		loaded, _ := repo.ByID(ctx, insideGrace)
		if loaded.Status != contests.StatusRunning {
			t.Errorf("contest still inside its grace period: status = %q, want running", loaded.Status)
		}
	})
}

// idInList reports whether id is among moved, for the AdvanceFinished test
// above.
func idInList(moved []uuid.UUID, id uuid.UUID) bool {
	for _, m := range moved {
		if m == id {
			return true
		}
	}
	return false
}

// idSet indexes a listing by identifier, for readable assertions.
func idSet(found []contests.Contest) map[uuid.UUID]bool {
	seen := make(map[uuid.UUID]bool, len(found))
	for _, c := range found {
		seen[c.ID] = true
	}
	return seen
}

// TestLockContestSerialisesConcurrentWriters proves LockContest is a real
// lock between database sessions, not merely decoration: a second caller
// must block until the first transaction holding it ends, across two real
// connections rather than one (CLAUDE.md rule 10 — prove it on the path the
// deployment uses; TestTryLockRefusesASecondHolderUntilTheFirstEndsItsTransaction
// above proves the same thing for the scheduler's advisory lock). This is
// what contests.Service.GrantManager and Service.Enroll/AddParticipants rely
// on to keep a contest's staff and its participants from overlapping no
// matter how two requests for the same contest interleave: the two checks
// live in different tables, so only a lock shared by both directions can
// make one of them wait for the other to finish.
func TestLockContestSerialisesConcurrentWriters(t *testing.T) {
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	ctx := context.Background()
	author := makeUser(t, ctx, "lock-contest-"+uuid.NewString()[:8])
	contest := makeContest(t, ctx, author.ID)
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contest)
	})
	repo := NewContests(testPool)

	holding := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)

	go func() {
		firstDone <- storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
			if err := repo.LockContest(ctx, contest); err != nil {
				return err
			}
			close(holding)
			<-release
			return nil
		})
	}()

	<-holding

	secondAcquired := make(chan error, 1)
	go func() {
		secondAcquired <- storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
			return repo.LockContest(ctx, contest)
		})
	}()

	select {
	case <-secondAcquired:
		t.Fatal("a second caller acquired the lock while the first still holds it")
	case <-time.After(200 * time.Millisecond):
		// Still blocked, as expected.
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first transaction failed: %v", err)
	}

	select {
	case err := <-secondAcquired:
		if err != nil {
			t.Fatalf("second LockContest() = %v, want it to succeed once the first released", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second caller never acquired the lock after the first released it")
	}
}

// TestLockContestRefusesOutsideATransaction proves the guard: a lock that
// silently did nothing outside a transaction would be indistinguishable from
// one that worked, until two requests actually raced.
func TestLockContestRefusesOutsideATransaction(t *testing.T) {
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	repo := NewContests(testPool)

	if err := repo.LockContest(context.Background(), uuid.New()); err == nil {
		t.Error("LockContest() outside a transaction = nil, want an error")
	}
}

// TestLockContestDoesNotBlockAForeignKeyInsertReferencingTheContest proves
// LockContest takes a lock weak enough to leave the contest's other
// concurrent writers alone: inserting a row that references this contest by
// foreign key (game_instances.contest_id, here through AddSpare — the same
// insert the pool's own background top-ups make) takes a key-share lock on
// the referenced contests row, and that must not be made to wait behind
// whatever GrantManager, Enroll or AddParticipants is doing with the
// stronger lock this type serialises them on. A `FOR UPDATE` lock would
// block it — exactly what would starve the pool of capacity a participant
// is waiting on for as long as a roster import runs; `FOR NO KEY UPDATE`
// does not conflict with a key-share lock, so this insert completes
// promptly while the first transaction still holds LockContest open.
func TestLockContestDoesNotBlockAForeignKeyInsertReferencingTheContest(t *testing.T) {
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	ctx := context.Background()
	author := makeUser(t, ctx, "lock-fk-"+uuid.NewString()[:8])
	contest := makeContest(t, ctx, author.ID)
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contest)
	})
	repo := NewContests(testPool)
	instances := NewGameInstances(testPool)

	holding := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)

	go func() {
		firstDone <- storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
			if err := repo.LockContest(ctx, contest); err != nil {
				return err
			}
			close(holding)
			<-release
			return nil
		})
	}()

	<-holding

	// A tight deadline stands in for "did not wait behind the lock": if the
	// insert were blocked, it would still be waiting when this expires,
	// long before release is ever closed.
	insertCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	insertErr := instances.AddSpare(insertCtx, contest, "lockfk_"+uuid.NewString()[:12], 1)

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first transaction failed: %v", err)
	}
	if insertErr != nil {
		t.Fatalf("AddSpare() = %v, want the foreign key insert to complete without waiting on LockContest", insertErr)
	}
}
