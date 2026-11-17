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
// it mirrors the contests_self_registration_open CHECK.
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

// DefaultVisibility is what a new contest gets: self-registration is open, an
// invite-only contest is closed.
func DefaultVisibility(enrollment string) string {
	if enrollment == EnrollmentOpen {
		return VisibilityOpen
	}
	return VisibilityClosed
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
// when Update is closed.
func (s *Service) SetArchiveSettings(ctx context.Context, actorID, contestID uuid.UUID, settings ArchiveSettings) (Contest, error) {
	if err := settings.Validate(); err != nil {
		return Contest{}, err
	}

	var updated Contest
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		previous, err := s.contests.SetArchiveSettings(ctx, contestID, settings)
		if err != nil {
			return err
		}
		if updated, err = s.contests.ByID(ctx, contestID); err != nil {
			return err
		}
		changes := audit.Between(
			map[string]any{"visibility": previous.Visibility, "archive_tasks": previous.ArchiveTasks},
			map[string]any{"visibility": settings.Visibility, "archive_tasks": settings.ArchiveTasks})
		return s.record(ctx, actorID, audit.ActionContestUpdate, contestID, changes.Payload())
	})
	if err != nil {
		return Contest{}, err
	}
	return updated, nil
}
