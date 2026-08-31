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
