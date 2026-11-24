package contests

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
)

// Visibility of a contest once it has ended.
const (
	// VisibilityOpen puts an ended contest in the archive.
	VisibilityOpen = "open"
	// VisibilityClosed keeps an ended contest out of every list.
	VisibilityClosed = "closed"
)

// ErrVisibilityNeedsInviteOnly refuses closing a contest with self-registration;
// the contests_self_registration_open CHECK is the database's last word on it.
var ErrVisibilityNeedsInviteOnly = errors.New("a contest with self-registration is always open")

// ArchiveSettings are the two properties an organiser changes after the
// contest has ended, whatever its status.
type ArchiveSettings struct {
	Visibility   string
	ArchiveTasks bool
}

// Validate reports a visibility that is neither open nor closed.
func (s ArchiveSettings) Validate() error {
	return validVisibility(s.Visibility)
}

func validVisibility(v string) error {
	if v != VisibilityOpen && v != VisibilityClosed {
		return fmt.Errorf("%w: unknown visibility %q", ErrInvalidContest, v)
	}
	return nil
}

// checkVisibility checks the visibility and its tie to enrolment: a contest
// anybody may join cannot be closed.
func (c Contest) checkVisibility() error {
	if err := validVisibility(c.Visibility); err != nil {
		return err
	}
	if c.Enrollment == EnrollmentOpen && c.Visibility == VisibilityClosed {
		return ErrVisibilityNeedsInviteOnly
	}
	return nil
}

// DefaultVisibility is what a new contest gets: self-registration is open, an
// invite-only contest is closed.
func DefaultVisibility(enrollment string) string {
	return Patch{Enrollment: enrollment}.ApplyTo(DefaultContest()).Visibility
}

// Listed reports whether a contest appears in lists. A closed contest leaves
// every list when it ends, and stays reachable by direct link.
func Listed(status, visibility string) bool {
	ended := status == StatusFinished || status == StatusArchived
	return !(visibility == VisibilityClosed && ended)
}

// Listed is Listed for this contest.
func (c Contest) Listed() bool {
	return Listed(c.Status, c.Visibility)
}

// InArchive reports whether the contest is shown in the public archive.
func (c Contest) InArchive() bool {
	return c.Visibility == VisibilityOpen && c.Ended()
}

// SetArchiveSettings is a contest's second settings door, open in every
// status: visibility and archive tasks matter most once the contest has ended,
// when Update is closed. expectedVersion is UpdateCommand.ExpectedVersion's guard.
func (s *Service) SetArchiveSettings(ctx context.Context, actorID, contestID uuid.UUID, expectedVersion *int64, settings ArchiveSettings) (Contest, error) {
	if err := settings.Validate(); err != nil {
		return Contest{}, err
	}

	var written Contest
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		current, err := s.contests.ByID(ctx, contestID)
		if err != nil {
			return err
		}
		if err := current.checkVersion(expectedVersion); err != nil {
			return err
		}
		updated := current
		updated.Visibility, updated.ArchiveTasks = settings.Visibility, settings.ArchiveTasks
		if err := updated.checkVisibility(); err != nil {
			return err
		}
		if written, err = s.contests.SetArchiveSettings(ctx, updated); err != nil {
			return err
		}
		changes := audit.Between(
			map[string]any{"visibility": current.Visibility, "archive_tasks": current.ArchiveTasks},
			map[string]any{"visibility": settings.Visibility, "archive_tasks": settings.ArchiveTasks})
		return s.record(ctx, actorID, audit.ActionContestUpdate, contestID, changes.Payload())
	})
	if err != nil {
		return Contest{}, err
	}
	return written, nil
}
