package leaderboard

import (
	"context"

	"github.com/google/uuid"
)

// Own is what a participant may be told about their own standing, read from
// the same computation as the contest's table.
type Own struct {
	// Row is the registration's own row; its Place is always zero.
	Row   Row
	State string
	// Open says the table is final or revealed. Place and Participants mean
	// nothing otherwise.
	Open bool
	// Place and Participants are zero while the table is not open.
	Place        int
	Participants int
	// Truncated says the open table was cut at the row bound.
	Truncated bool
	Scoring   string
	Questions int
}

// Own returns one registration's own result, from at most two cached
// computations the contest pages already use:
//
//   - the public table when it is open, which gives the row, its place and
//     the row count, so profile and table never disagree;
//   - otherwise, or when the registration is not on the open table (e.g.
//     disqualified), the live table, taking only the caller's own row without
//     a place.
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
