package contests

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/audit"
)

// maxPrizePlaces bounds each prize count (CLAUDE.md rule 2), matching the
// migration's CHECK.
const maxPrizePlaces = 1000

// PrizePlaces is how many places down the standings earn a first, second and
// third prize. The zero value awards none.
type PrizePlaces struct {
	First  int
	Second int
	Third  int
}

// Validate checks each count against 0..maxPrizePlaces.
func (p PrizePlaces) Validate() error {
	for _, n := range []int{p.First, p.Second, p.Third} {
		if n < 0 || n > maxPrizePlaces {
			return fmt.Errorf("%w: a prize count of %d is outside 0..%d", ErrInvalidContest, n, maxPrizePlaces)
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

// SetPrizePlaces changes how many prizes the standings show. It is a second
// door beside Update, not a widening of it: the counts change only what the
// table displays, so they stay open while the contest runs and after it
// finishes, until it is archived. The repository decides the archived refusal
// in the write itself, so an archive racing this save cannot be overtaken.
func (s *Service) SetPrizePlaces(ctx context.Context, actorID, contestID uuid.UUID, places PrizePlaces) (Contest, error) {
	if err := places.Validate(); err != nil {
		return Contest{}, err
	}

	var updated Contest
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		previous, err := s.contests.SetPrizePlaces(ctx, contestID, places)
		if err != nil {
			return err
		}
		if updated, err = s.contests.ByID(ctx, contestID); err != nil {
			return err
		}
		changes := audit.Between(prizeAuditFields(previous), prizeAuditFields(places))
		return s.record(ctx, actorID, audit.ActionContestUpdate, contestID, changes.Payload())
	})
	if err != nil {
		return Contest{}, err
	}
	return updated, nil
}

// prizeAuditFields is the counts as the audit trail names them.
func prizeAuditFields(p PrizePlaces) map[string]any {
	return map[string]any{"prize_first": p.First, "prize_second": p.Second, "prize_third": p.Third}
}
