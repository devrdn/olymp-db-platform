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

// The report of one finished contest (design §2.2, the "result" tab): the
// participant's own result, what their session cost them in queries and
// time, and question by question what they did — all of it things they
// already know about themselves, gathered in one place.

// Result is the participant's own standing, as the profile may show it.
type Result struct {
	// Scoring is the contest's mode, so a reader knows whether Points or
	// Solved and Penalty is the result.
	Scoring string
	Points  int
	Solved  int
	// Penalty is ICPC's penalty minutes, and zero in every other mode.
	Penalty int
	// State is the table's state (leaderboard.Decide), so the interface can
	// say why a place is missing rather than merely leaving a gap.
	State string
	// PlaceOpen says the table is open, so a place is a thing that exists to
	// be shown. While a freeze is in force it is false and everything below
	// is zero: the profile does not walk round it (design §1).
	PlaceOpen bool
	// Place is zero for a row the table gives no place to — which is not the
	// same as a place of nought. In winner mode only the winner has one
	// (leaderboard.Rank), so everybody else is on an open table, with their
	// own numbers, unplaced.
	Place        int
	Participants int
	// Winner marks the one registration that won a winner-mode contest. It
	// is a result, so it travels only with an open table.
	Winner bool
	// Truncated says the table was cut at the leaderboard's row bound, so
	// Participants counts its rows rather than everybody on the contest.
	Truncated bool
}

// resultOf is the leaderboard's answer as the profile carries it. The place
// travels only with PlaceOpen, so there is one place in this package that
// can decide to show one.
func resultOf(own leaderboard.Own) Result {
	r := Result{Scoring: own.Scoring, Points: own.Row.Points, Solved: own.Row.Solved,
		Penalty: own.Row.Penalty, State: own.State}
	if own.Open {
		r.PlaceOpen, r.Place, r.Participants, r.Truncated = true, own.Place, own.Participants, own.Truncated
		r.Winner = own.Row.Winner
	}
	return r
}

// Activity is what the journals say about one registration's session.
type Activity struct {
	// Queries is every query the registration ran, Successful those that
	// finished without a refusal or an error.
	Queries    int
	Successful int
	// LastAnswerAt is the registration's last submission, which is where the
	// time worked stops.
	LastAnswerAt *time.Time
}

// QuestionResult is one question on the report.
type QuestionResult struct {
	QuestionID uuid.UUID
	// Ord is the question's place in the contest; zero for a question that
	// has since been deleted.
	Ord      int
	Attempts int
	Solved   bool
	// SolvedAt is the first correct answer, absent when there was none.
	SolvedAt *time.Time
	// Points is what the attempts earned, the penalty for wrong ones
	// included: the number the contest recorded, never one derived again.
	Points int
}

// Report is the result tab of one contest.
type Report struct {
	Contest     contests.Contest
	Participant contests.Participant
	Result      Result
	Activity    Activity
	Questions   []QuestionResult
	// Truncated says the participant made more attempts than one read of the
	// answers tab carries, so the questions below describe the first of them.
	Truncated bool
}

// Worked is how long the participant was at it: from their clock starting to
// their last answer. ok is false when either end is missing — a participant
// who never started, or never answered, worked for no stretch of time this
// can name, which is a different thing from having worked for none.
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

// Report gathers one contest's report for an admitted caller.
//
// Three reads, none of them a formula of this package's own: the standing
// from the leaderboard, the attempts from the same answers tab a contest's
// staff read, and the counters from the journals. A caller the leaderboard
// has no row for — disqualified before it was computed, say — still gets
// their report, with no result on it.
func (s *Service) Report(ctx context.Context, access Access) (Report, error) {
	report := Report{Contest: access.Contest, Participant: access.Participant}

	own, err := s.results.Own(ctx, access.Contest.ID, access.Participant.ID)
	switch {
	case errors.Is(err, leaderboard.ErrNotAParticipant), errors.Is(err, leaderboard.ErrNotFound):
	case err != nil:
		return Report{}, fmt.Errorf("read the standing: %w", err)
	default:
		report.Result = resultOf(own)
	}

	answers, err := s.attempts.Answers(ctx, access.Contest.ID, access.Participant.ID)
	if err != nil {
		return Report{}, fmt.Errorf("read the answers: %w", err)
	}
	report.Truncated = answers.Truncated
	report.Questions = make([]QuestionResult, 0, len(answers.Questions))
	for _, group := range answers.Questions {
		question := QuestionResult{QuestionID: group.QuestionID, Ord: group.QuestionOrd,
			Attempts: len(group.Attempts)}
		for _, attempt := range group.Attempts {
			question.Points += attempt.PointsAwarded
			if !attempt.Correct || question.Solved {
				continue
			}
			// The first correct answer, which is the one that counts: a
			// question is solved once, and anything after it is not a solve.
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
