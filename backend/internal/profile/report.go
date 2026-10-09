package profile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
)

// Result is the participant's own standing, as the profile may show it.
type Result struct {
	Scoring string
	Points  int
	Solved  int
	// Penalty is ICPC penalty minutes, zero in other modes.
	Penalty int
	// State is the table's state (leaderboard.Decide), so the interface can
	// say why a place is missing.
	State string
	// PlaceOpen says the table is open. During a freeze it is false and
	// everything below is zero.
	PlaceOpen bool
	// Place is zero for a row the table gives no place to; in winner mode
	// only the winner has one.
	Place        int
	Participants int
	// Winner marks the winner of a winner-mode contest, only on an open
	// table.
	Winner bool
	// Truncated says the table was cut at the leaderboard's row bound, so
	// Participants counts its rows rather than everybody on the contest.
	Truncated bool
}

// resultOf is the only place in this package that decides to show a place:
// only with PlaceOpen.
func resultOf(own leaderboard.Own) *Result {
	r := Result{Scoring: own.Scoring, Points: own.Row.Points, Solved: own.Row.Solved,
		Penalty: own.Row.Penalty, State: own.State}
	if own.Open {
		r.PlaceOpen, r.Place, r.Participants, r.Truncated = true, own.Place, own.Participants, own.Truncated
		r.Winner = own.Row.Winner
	}
	return &r
}

type Activity struct {
	// Successful counts queries that finished without a refusal or error.
	Queries    int
	Successful int
	// LastAnswerAt is the last submission, where the time worked stops.
	LastAnswerAt *time.Time
}

type QuestionResult struct {
	QuestionID uuid.UUID
	// Ord is zero for a question since deleted.
	Ord      int
	Attempts int
	Solved   bool
	SolvedAt *time.Time
	// Points is what the contest recorded, wrong-answer penalties included.
	Points int
	// Penalty is this question's share of an ICPC row's penalty minutes
	// (leaderboard.Cell.Penalty), zero in other modes. ICPC writes
	// points_awarded = 0, so Points alone would show nought there.
	Penalty int
}

type Report struct {
	Contest     contests.Contest
	Participant contests.Participant
	// Result is nil, not zeroed, when the table has no row for this
	// registration: below the leaderboard's row bound, or disqualified before
	// it was computed. A zeroed Result would read as a result of nought.
	Result    *Result
	Activity  Activity
	Questions []QuestionResult
	// Truncated says the participant made more attempts than one read of the
	// answers tab carries, so Questions describe the first of them.
	Truncated bool
}

// Worked is the time from the participant's clock starting to their last
// answer. ok is false when either end is missing, which is not the same as
// zero.
func (r Report) Worked() (time.Duration, bool) {
	if r.Participant.StartedAt == nil || r.Activity.LastAnswerAt == nil {
		return 0, false
	}
	worked := r.Activity.LastAnswerAt.Sub(*r.Participant.StartedAt)
	if worked < 0 {
		return 0, false
	}
	return worked, true
}

// Report gathers one contest's report for an admitted caller from the
// leaderboard, the answers tab and the journals. A caller with no leaderboard
// row still gets a report, with Result nil.
func (s *Service) Report(ctx context.Context, access Access) (Report, error) {
	report := Report{Contest: access.Contest, Participant: access.Participant}

	var cells map[uuid.UUID]leaderboard.Cell
	own, err := s.results.Own(ctx, access.Contest.ID, access.Participant.ID)
	switch {
	case errors.Is(err, leaderboard.ErrNotAParticipant), errors.Is(err, leaderboard.ErrNotFound):
	case err != nil:
		return Report{}, fmt.Errorf("read the standing: %w", err)
	default:
		report.Result = resultOf(own)
		// ICPC only. A hidden or deleted question has no cell and costs
		// nothing.
		cells = make(map[uuid.UUID]leaderboard.Cell, len(own.Row.Cells))
		for _, cell := range own.Row.Cells {
			cells[cell.QuestionID] = cell
		}
	}

	answers, err := s.attempts.Answers(ctx, access.Contest.ID, access.Participant.ID)
	if err != nil {
		return Report{}, fmt.Errorf("read the answers: %w", err)
	}
	report.Truncated = answers.Truncated
	report.Questions = make([]QuestionResult, 0, len(answers.Questions))
	for _, group := range answers.Questions {
		question := QuestionResult{QuestionID: group.QuestionID, Ord: group.QuestionOrd,
			Attempts: len(group.Attempts),
			Penalty:  cells[group.QuestionID].Penalty(access.Contest.ICPCPenaltyMin)}
		for _, attempt := range group.Attempts {
			question.Points += attempt.PointsAwarded
			if !attempt.Correct || question.Solved {
				continue
			}
			// Only the first correct answer counts.
			at := attempt.At
			question.Solved, question.SolvedAt = true, &at
		}
		report.Questions = append(report.Questions, question)
	}

	report.Activity, err = s.store.Activity(ctx, access.Participant.ID)
	if err != nil {
		return Report{}, fmt.Errorf("read the activity: %w", err)
	}
	return report, nil
}
