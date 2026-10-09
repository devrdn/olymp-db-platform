package profile

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
)

type Enrolment struct {
	Contest     contests.Contest
	Participant contests.Participant
	// Over says the contest has ended for this participant. Only then is
	// Result shown and the report reachable.
	Over bool
	// Result is the participant's own numbers, with State and PlaceOpen
	// decided here. It never carries a place.
	Result Result
}

// Contests lists the caller's contests with their own results, newest first,
// in one read.
//
// It names no place: that would mean computing a whole table per row. The
// place is on the report; the row says only whether the table is open
// (leaderboard.Decide, no read).
func (s *Service) Contests(ctx context.Context, userID uuid.UUID) ([]Enrolment, bool, error) {
	// One past the bound, so "there are more" is known rather than guessed.
	rows, err := s.store.Enrolments(ctx, userID, MaxContests+1)
	if err != nil {
		return nil, false, fmt.Errorf("read the profile's contests: %w", err)
	}
	truncated := false
	if len(rows) > MaxContests {
		rows, truncated = rows[:MaxContests], true
	}

	now := s.now()
	for i := range rows {
		row := &rows[i]
		row.Over = s.over(row.Contest, row.Participant, now)
		if !row.Over {
			// Nothing of a running contest, not even its table's state.
			row.Result = Result{}
			continue
		}
		// An undecidable state leaves the row without one rather than
		// failing the list.
		if decision, err := leaderboard.Decide(row.Contest, now); err == nil {
			row.Result.State = decision.State
			row.Result.PlaceOpen = decision.State == leaderboard.StateFinal
		}
	}
	return rows, truncated, nil
}
