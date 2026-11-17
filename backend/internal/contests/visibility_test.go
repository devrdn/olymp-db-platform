package contests_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests/conteststest"
)

var allStatuses = []string{
	contests.StatusDraft, contests.StatusPublished, contests.StatusRunning,
	contests.StatusFinished, contests.StatusArchived,
}

func TestListedHidesOnlyClosedContestsOnceEnded(t *testing.T) {
	for _, status := range allStatuses {
		for _, visibility := range []string{contests.VisibilityOpen, contests.VisibilityClosed} {
			want := !(visibility == contests.VisibilityClosed &&
				(status == contests.StatusFinished || status == contests.StatusArchived))
			if got := contests.Listed(status, visibility); got != want {
				t.Errorf("Listed(%q, %q) = %v, want %v", status, visibility, got, want)
			}
			c := contests.Contest{Status: status, Visibility: visibility}
			if got := c.Listed(); got != want {
				t.Errorf("Contest{%q, %q}.Listed() = %v, want %v", status, visibility, got, want)
			}
		}
	}
}

func TestInArchiveNeedsOpenAndEnded(t *testing.T) {
	for _, status := range allStatuses {
		for _, visibility := range []string{contests.VisibilityOpen, contests.VisibilityClosed} {
			want := visibility == contests.VisibilityOpen &&
				(status == contests.StatusFinished || status == contests.StatusArchived)
			c := contests.Contest{Status: status, Visibility: visibility}
			if got := c.InArchive(); got != want {
				t.Errorf("Contest{%q, %q}.InArchive() = %v, want %v", status, visibility, got, want)
			}
		}
	}
}

func TestDefaultVisibilityFollowsEnrollment(t *testing.T) {
	if got := contests.DefaultVisibility(contests.EnrollmentOpen); got != contests.VisibilityOpen {
		t.Errorf("DefaultVisibility(open) = %q, want %q", got, contests.VisibilityOpen)
	}
	if got := contests.DefaultVisibility(contests.EnrollmentInviteOnly); got != contests.VisibilityClosed {
		t.Errorf("DefaultVisibility(invite_only) = %q, want %q", got, contests.VisibilityClosed)
	}
}

func TestValidateRefusesClosedSelfRegistration(t *testing.T) {
	c := validContest()
	c.Enrollment = contests.EnrollmentOpen
	c.Visibility = contests.VisibilityClosed
	if err := c.Validate(); !errors.Is(err, contests.ErrVisibilityNeedsInviteOnly) {
		t.Errorf("Validate() = %v, want contests.ErrVisibilityNeedsInviteOnly", err)
	}
}

func TestValidateRefusesUnknownVisibility(t *testing.T) {
	for _, v := range []string{"", "public"} {
		c := validContest()
		c.Visibility = v
		if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
			t.Errorf("Validate() with visibility %q = %v, want contests.ErrInvalidContest", v, err)
		}
	}
}

func TestArchiveSettingsValidate(t *testing.T) {
	for _, v := range []string{contests.VisibilityOpen, contests.VisibilityClosed} {
		if err := (contests.ArchiveSettings{Visibility: v}).Validate(); err != nil {
			t.Errorf("Validate() with %q = %v, want nil", v, err)
		}
	}
	if err := (contests.ArchiveSettings{}).Validate(); !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("Validate() with no visibility = %v, want contests.ErrInvalidContest", err)
	}
}

func TestSetArchiveSettingsWorksInEveryStatus(t *testing.T) {
	want := contests.ArchiveSettings{Visibility: contests.VisibilityOpen, ArchiveTasks: true}
	for _, status := range allStatuses {
		f := conteststest.NewFixture()
		c := f.SeedContest(status)

		updated, err := f.Service.SetArchiveSettings(context.Background(), uuid.New(), c.ID, want)
		if err != nil {
			t.Errorf("SetArchiveSettings() on a %s contest = %v", status, err)
			continue
		}
		stored, _ := f.Contests.ByID(context.Background(), c.ID)
		if stored.Visibility != want.Visibility || !stored.ArchiveTasks ||
			updated.Visibility != want.Visibility || !updated.ArchiveTasks {
			t.Errorf("%s contest: stored %q/%v, returned %q/%v, want %+v",
				status, stored.Visibility, stored.ArchiveTasks, updated.Visibility, updated.ArchiveTasks, want)
		}
	}
}

func TestSetArchiveSettingsRefusesClosingASelfRegistrationContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)
	c.Enrollment, c.Visibility = contests.EnrollmentOpen, contests.VisibilityOpen
	f.Contests.Put(c)
	entries := len(f.Audit.Entries)

	_, err := f.Service.SetArchiveSettings(context.Background(), uuid.New(), c.ID,
		contests.ArchiveSettings{Visibility: contests.VisibilityClosed})

	if !errors.Is(err, contests.ErrVisibilityNeedsInviteOnly) {
		t.Fatalf("SetArchiveSettings() = %v, want ErrVisibilityNeedsInviteOnly", err)
	}
	stored, _ := f.Contests.ByID(context.Background(), c.ID)
	if stored.Visibility != contests.VisibilityOpen {
		t.Errorf("stored visibility = %q, want it unchanged", stored.Visibility)
	}
	if len(f.Audit.Entries) != entries {
		t.Errorf("audit entries = %d, want %d: a refusal records nothing", len(f.Audit.Entries), entries)
	}
}

func TestSetArchiveSettingsRefusesAnUnknownVisibility(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	_, err := f.Service.SetArchiveSettings(context.Background(), uuid.New(), c.ID,
		contests.ArchiveSettings{Visibility: "hidden"})

	if !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("SetArchiveSettings() = %v, want ErrInvalidContest", err)
	}
}

func TestSetArchiveSettingsOfAContestThatIsNotThereIsNotFound(t *testing.T) {
	f := conteststest.NewFixture()

	_, err := f.Service.SetArchiveSettings(context.Background(), uuid.New(), uuid.New(),
		contests.ArchiveSettings{Visibility: contests.VisibilityOpen})

	if !errors.Is(err, contests.ErrNotFound) {
		t.Errorf("SetArchiveSettings() = %v, want ErrNotFound", err)
	}
}

func TestSetArchiveSettingsIsAudited(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)

	if _, err := f.Service.SetArchiveSettings(context.Background(), uuid.New(), c.ID,
		contests.ArchiveSettings{Visibility: contests.VisibilityOpen, ArchiveTasks: true}); err != nil {
		t.Fatalf("SetArchiveSettings() = %v", err)
	}

	changes := changesIn(t, f, audit.ActionContestUpdate)
	for field, want := range map[string][2]any{
		"visibility":    {contests.VisibilityClosed, contests.VisibilityOpen},
		"archive_tasks": {false, true},
	} {
		change, ok := changes[field].(map[string]any)
		if !ok || change["from"] != want[0] || change["to"] != want[1] {
			t.Errorf("%s change = %v, want %v -> %v", field, changes[field], want[0], want[1])
		}
	}
}
