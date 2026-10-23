package contests

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// ErrBonusNeedsPoints refuses a bonus question in a contest that does not
// score points, and moving a contest that has one to another scoring mode.
// Only points scoring has penalty points for a bonus to remove.
var ErrBonusNeedsPoints = errors.New("a bonus question needs points scoring")

// checkBonus refuses q as a question of c when it is a bonus and c does not
// score points.
func checkBonus(c Contest, q Question) error {
	if q.IsBonus && c.Scoring != ScoringPoints {
		return fmt.Errorf("%w: the contest scores by %s", ErrBonusNeedsPoints, c.Scoring)
	}
	return nil
}

// checkScoringKeepsBonus refuses moving a contest off points scoring while
// any of its questions is a bonus. A save that leaves the scoring alone is
// never refused for it.
func (s *Service) checkScoringKeepsBonus(ctx context.Context, current, updated Contest) error {
	if updated.Scoring == ScoringPoints || updated.Scoring == current.Scoring {
		return nil
	}
	questions, err := s.questions.List(ctx, current.ID)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(questions, func(q Question) bool { return q.IsBonus }) {
		return fmt.Errorf("%w: untick the bonus questions before scoring by %s", ErrBonusNeedsPoints, updated.Scoring)
	}
	return nil
}
