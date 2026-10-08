package profile

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
)

// The participant's own list of contests (design §2.1): every contest they
// are on, newest first, with their own result on the ones that have ended for
// them.

// Enrolment is one row of the list.
type Enrolment struct {
	Contest     contests.Contest
	Participant contests.Participant
	// Over says the contest has ended for this participant. Only then is
	// Result shown and the contest's report reachable; a contest still
	// running is a line saying so and a way back into it, nothing more.
	Over bool
	// Result is the participant's own numbers, as storage counted them, with
	// State and PlaceOpen decided here. It never carries a place — see
	// Contests.
	Result Result
}

// Contests lists the caller's contests with their own results.
//
// One read for the whole list, whatever it holds, and no second read for any
// row: the contests, the registrations and the participant's own points,
// solved and penalty all arrive from the single statement over the account's
// own registrations that Store.Enrolments sends (design §4).
//
// There is deliberately no place here. A place is a position on a table, and
// naming one means computing that whole table — up to a bounded page of them
// the first time a profile is opened, to decorate an overview. The place is
// on the report, which is where somebody goes for detail and which asks the
// leaderboard for exactly the one contest they opened. What the row does say
// is whether the table is open at all (leaderboard.Decide, a function over
// the contest already in hand and not a read of anything), so the interface
// can offer the report's place or explain that a freeze is still in force.
func (s *Service) Contests(ctx context.Context, userID uuid.UUID) ([]Enrolment, bool, error) {
	// One past the bound, so "there are more" is a fact rather than the guess
	// len == limit would be.
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
			// Nothing of a contest that is still being taken, not even the
			// state of its table: during one, the profile is a way back in.
			row.Result = Result{}
			continue
		}
		// A contest whose state cannot be decided — a draft, which over
		// already refuses — leaves the row's own numbers and no state, rather
		// than failing a list for one row.
		if decision, err := leaderboard.Decide(row.Contest, now); err == nil {
			row.Result.State = decision.State
			row.Result.PlaceOpen = decision.State == leaderboard.StateFinal
		}
	}
	return rows, truncated, nil
}
