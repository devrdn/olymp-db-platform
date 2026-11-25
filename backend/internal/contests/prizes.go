package contests

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
)

// MaxPrizePlaces bounds each prize count (CLAUDE.md rule 2), matching the
// migration's CHECK.
const MaxPrizePlaces = 1000

// PrizePlaces is how many places down the standings earn a first, second and
// third prize. The zero value awards none.
type PrizePlaces struct {
	First  int
	Second int
	Third  int
}

// Validate checks each count against 0..MaxPrizePlaces.
func (p PrizePlaces) Validate() error {
	for _, n := range []int{p.First, p.Second, p.Third} {
		if n < 0 || n > MaxPrizePlaces {
			return fmt.Errorf("%w: a prize count of %d is outside 0..%d", ErrInvalidContest, n, MaxPrizePlaces)
		}
	}
	return nil
}

// Tier is the prize a 1-based place earns: 1, 2 or 3, or 0 for none. A place
// zero (unplaced) earns nothing. Ties share a place, so a tie straddling a
// boundary takes the better tier.
func (p PrizePlaces) Tier(place int) int {
	switch {
	case place < 1:
		return 0
	case place <= p.First:
		return 1
	case place <= p.First+p.Second:
		return 2
	case place <= p.First+p.Second+p.Third:
		return 3
	default:
		return 0
	}
}

// PrizePlacesEditable reports whether the prize counts may change. They change
// only what the standings display, so they stay open while the contest runs
// and after it finishes, until it is archived.
func (c Contest) PrizePlacesEditable() bool {
	return c.Status != StatusArchived
}

// SetPrizePlaces changes how many prizes the standings show. It is a second
// door beside Update, not a widening of it, and is refused once the contest
// is archived.
func (s *Service) SetPrizePlaces(ctx context.Context, actorID, contestID uuid.UUID, guard *VersionGuard, places PrizePlaces) (Contest, error) {
	if err := places.Validate(); err != nil {
		return Contest{}, err
	}

	var written Contest
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		current, err := s.contests.ByID(ctx, contestID)
		if err != nil {
			return err
		}
		if err := guard.check(current); err != nil {
			return err
		}
		if !current.PrizePlacesEditable() {
			return fmt.Errorf("%w: it is %s", ErrNotEditable, current.Status)
		}
		updated := current
		updated.PrizePlaces = places
		if written, err = s.contests.SetPrizePlaces(ctx, updated); err != nil {
			return err
		}
		guard.stored(written)
		changes := audit.Between(prizeAuditFields(current.PrizePlaces), prizeAuditFields(places))
		return s.record(ctx, actorID, audit.ActionContestUpdate, contestID, changes.Payload())
	})
	if err != nil {
		return Contest{}, err
	}
	return written, nil
}

// prizeAuditFields is the counts as the audit trail names them.
func prizeAuditFields(p PrizePlaces) map[string]any {
	return map[string]any{"prize_first": p.First, "prize_second": p.Second, "prize_third": p.Third}
}
