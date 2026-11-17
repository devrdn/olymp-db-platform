package contests_test

import (
	"errors"
	"testing"

	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
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
