package leaderboard

import (
	"context"

	"github.com/google/uuid"
)

// One registration's own result, for the participant's own profile (the
// participant profile design, §4).
//
// It exists so that the profile has no second formula of its own: points,
// solved, penalty and place are the numbers this package already computes for
// the contest's table, read back for one row rather than derived again from
// submissions somewhere else.

// Own is what a participant may be told about their own standing.
type Own struct {
	// Row is the registration's own row. Place is always zero on it; the
	// place is Place below, and only when Open says there is one.
	Row Row
	// State is the table's state for everybody but the staff (Decide).
	State string
	// Open says the table is open — final, or a freeze an organiser has
	// revealed. Place and Participants mean nothing unless it is true.
	Open bool
	// Place is the registration's place on the open table, and Participants
	// how many rows it has. Both zero while the table is not open: the
	// profile does not walk round a freeze (design §1).
	Place        int
	Participants int
	// Truncated says the open table was cut at the row bound, so Participants
	// counts what the table carries rather than everybody on the contest.
	Truncated bool
	// Scoring is the contest's mode, so a caller knows which of Points and
	// Solved/Penalty is the result.
	Scoring string
	// Questions is the ICPC grid's width, zero in every other mode.
	Questions int
}

// Own returns one registration's own result in a contest.
//
// Two reads at most, both of them cached computations this package already
// serves to somebody else, so a profile costs the database nothing a contest
// page does not already cost it:
//
//   - the public table first. When it is open (StateFinal) it is cut off now
//     and leaves the disqualified out, which is exactly the table a place is
//     a place on — so the row, its place and the number of rows all come from
//     the one computation the contest's own page is served from, and the two
//     can never disagree.
//   - otherwise the live computation, which is cut off now whatever the
//     freeze and carries the disqualified. Only the registration's own row is
//     taken from it, with its place dropped: a frozen table's numbers about
//     other people are not this caller's business, and neither is a place
//     that counts the disqualified in.
//
// A registration missing from an open table is a disqualified one (or one
// made after the cutoff), so the live computation answers it too: a
// disqualified participant sees their own result, never a place.
func (s *Service) Own(ctx context.Context, contestID, registration uuid.UUID) (Own, error) {
	view, err := s.Public(ctx, contestID)
	if err != nil {
		return Own{}, err
	}
	own := Own{State: view.State, Scoring: view.Contest.Scoring, Questions: view.Questions}
	if view.State == StateFinal {
		for _, row := range view.Rows {
			if row.Registration != registration {
				continue
			}
			own.Open, own.Place, own.Participants = true, row.Place, len(view.Rows)
			own.Truncated = view.Truncated
			row.Place = 0
			own.Row = row
			return own, nil
		}
	}

	live, err := s.Live(ctx, contestID)
	if err != nil {
		return Own{}, err
	}
	own.Questions = live.Questions
	for _, row := range live.Rows {
		if row.Registration != registration {
			continue
		}
		row.Place, row.Winner = 0, false
		own.Row = row
		return own, nil
	}
	return Own{}, ErrNotAParticipant
}
