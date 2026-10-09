// Package leaderboard answers "who is where" in a contest, as each audience
// may see it: the table's state (live, frozen, final), its cutoff, and the
// order and places of the rows.
//
// It does not record results (internal/contests fixes a submission's points
// when it is answered) and does not decide who may call it; the HTTP layer
// applies permissions and rate limits.
package leaderboard

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

// The table's states.
const (
	// StateNotStarted is a published contest whose window has not opened;
	// not even the roster is shown.
	StateNotStarted = "not_started"
	StateLive       = "live"
	// StateFrozen is the table as it stood at the freeze, lasting past the
	// finish until an organiser reveals the result.
	StateFrozen = "frozen"
	StateFinal  = "final"
)

var (
	// ErrNotFound is a missing contest or a draft. One error for both, so
	// the table does not reveal which contests exist.
	ErrNotFound        = errors.New("leaderboard not found")
	ErrNotAParticipant = errors.New("not a participant of this contest")
	// ErrNotRevealable is a contest not finished, or never frozen.
	ErrNotRevealable = errors.New("the leaderboard cannot be revealed")
)

type Decision struct {
	State string
	// Cutoff is exclusive: an answer submitted exactly at it is not counted.
	Cutoff   time.Time
	FrozenAt *time.Time
}

// Decide reports the table's state for everybody but the contest's staff.
//
// A final table is cut off now rather than at ends_at: an answer accepted
// within DEADLINE_GRACE can carry a submitted_at just past ends_at.
func Decide(c contests.Contest, now time.Time) (Decision, error) {
	freezeAt, frozen := c.FreezeAt()

	switch c.Status {
	case contests.StatusPublished:
		return Decision{State: StateNotStarted, Cutoff: now}, nil
	case contests.StatusRunning:
		if frozen && !now.Before(freezeAt) {
			return Decision{State: StateFrozen, Cutoff: freezeAt, FrozenAt: &freezeAt}, nil
		}
		return Decision{State: StateLive, Cutoff: now}, nil
	case contests.StatusFinished, contests.StatusArchived:
		if frozen && c.LeaderboardRevealedAt == nil {
			return Decision{State: StateFrozen, Cutoff: freezeAt, FrozenAt: &freezeAt}, nil
		}
		return Decision{State: StateFinal, Cutoff: now}, nil
	default:
		return Decision{}, fmt.Errorf("%w: the contest is %s", ErrNotFound, c.Status)
	}
}

type Entry struct {
	Registration   uuid.UUID
	Login          string
	FullName       string
	AccountDeleted bool
	Disqualified   bool
	// Points is the sum of points_awarded; Solved counts distinct questions
	// answered correctly.
	Points int
	Solved int
	// LastScoredAt is when the current score was reached: the tie-break
	// between equal scores.
	LastScoredAt *time.Time
	// FinalAt is the first correct final answer, which decides winner mode.
	FinalAt *time.Time

	// The rest is ICPC's and zero in other modes. Under ICPC, Solved counts
	// only the visible questions the grid shows.
	//
	// Penalty is the sum of Cell.Penalty over solved cells.
	Penalty int
	// LastSolvedAt orders rows sharing a place.
	LastSolvedAt *time.Time
	Cells        []Cell
}

// Grid is what one ICPC computation knows beside its rows, from the same
// statement so the two agree.
type Grid struct {
	// Questions is the grid's width and the length of every row's Cells.
	Questions int
	// FirstSolves holds, per visible question, the earliest solve before the
	// cutoff among non-disqualified registrations over the whole contest,
	// not only the rows returned. Nil where nobody has solved it.
	FirstSolves []*time.Time
}

const (
	CellSolved = "solved"
	CellFailed = "failed"
	// CellPending is a question unsolved at the freeze and attempted since;
	// it tells how many attempts, nothing about them.
	CellPending = "pending"
	CellUntried = "untried"
)

type Cell struct {
	// QuestionID is for reports; the table itself reads cells by position.
	QuestionID uuid.UUID
	SolvedAt   *time.Time
	// Minute is whole minutes, rounded down, from the registration's start to
	// SolvedAt; zero when unsolved.
	Minute int
	// Wrong counts wrong attempts before the cutoff, and before SolvedAt
	// when solved.
	Wrong int
	// Pending counts attempts inside Query.Pending, only for a question not
	// solved before the cutoff.
	Pending int
	// First marks the question's earliest non-disqualified solve. Rank sets
	// it, never storage.
	First bool
}

// State names what the cell shows. Pending is checked before failed, so a
// frozen table never calls attempts made since the freeze wrong.
func (c Cell) State() string {
	switch {
	case c.SolvedAt != nil:
		return CellSolved
	case c.Pending > 0:
		return CellPending
	case c.Wrong > 0:
		return CellFailed
	default:
		return CellUntried
	}
}

func (c Cell) SolvedOnAttempt() int {
	return c.Wrong + 1
}

// Penalty is what this cell costs its row, in minutes: the solving minute
// plus perWrong per wrong attempt. An unsolved question costs nothing.
//
// It must match the standings statement's arithmetic; the test
// TestICPCCellPenaltiesAddUpToTheRowsOwn holds the two together.
func (c Cell) Penalty(perWrong int) int {
	if c.SolvedAt == nil {
		return 0
	}
	return c.Minute + perWrong*c.Wrong
}

// QuestionLetter names a visible question by its zero-based position: A, B,
// … Z, then AA, AB, … like spreadsheet columns.
func QuestionLetter(position int) string {
	var name []byte
	for n := position + 1; n > 0; n = (n - 1) / 26 {
		name = append([]byte{byte('A' + (n-1)%26)}, name...)
	}
	return string(name)
}

type Row struct {
	Entry
	// Place is 1-based and shared by equal results. Zero means unplaced (in
	// winner mode, everyone but the winner).
	Place  int
	Winner bool
}

// Label is how the row is named under the contest's choice. A deleted
// account has no label, since its login may now belong to somebody else. A
// missing full name falls back to the login.
func (r Row) Label(names string) string {
	switch {
	case r.AccountDeleted:
		return ""
	case names == contests.LeaderboardNamesFullName && r.FullName != "":
		return r.FullName
	default:
		return r.Login
	}
}
