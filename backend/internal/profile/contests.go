package profile

import (
	"context"
	"errors"
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
	// Result filled and the contest's report reachable; a contest still
	// running is a line saying so and a way back into it, nothing more.
	Over bool
	// Result is the participant's own standing, from the leaderboard. Its
	// place is filled only where the table is open.
	Result Result
}

// Contests lists the caller's contests with their own results.
//
// One read of storage for the whole list, whatever it holds: the contests and
// the registrations arrive together from a single statement over the
// account's own registrations (Store.Enrolments), never a lookup per contest.
//
// The results are then the leaderboard's, and only for the contests that have
// ended for this caller — a running contest is not asked about at all, so the
// number of standings computations is bounded by the finished contests on one
// bounded page, and each of them is the same cached computation the contest's
// own page is served from (leaderboard.Service.Own).
//
// A contest the leaderboard cannot place the caller on is listed without a
// result rather than failing the list: one missing number is not a reason to
// answer nothing at all.
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
		row.Over = Over(row.Contest, row.Participant, now)
		if !row.Over {
			continue
		}
		own, err := s.results.Own(ctx, row.Contest.ID, row.Participant.ID)
		switch {
		case errors.Is(err, leaderboard.ErrNotAParticipant), errors.Is(err, leaderboard.ErrNotFound):
			continue
		case err != nil:
			return nil, false, fmt.Errorf("read the standing in %s: %w", row.Contest.ID, err)
		}
		row.Result = resultOf(own)
	}
	return rows, truncated, nil
}
