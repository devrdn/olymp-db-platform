// Package leaderboard answers "who is where" in a contest, as each audience
// is allowed to see it: the table's state (live, frozen, final), the moment
// its answers are cut off at, and the order and places of the people on it.
//
// It does not record results — a submission's points are decided and fixed
// by internal/contests at the moment of answering, and are never recomputed —
// and it does not decide who may call it: the HTTP layer applies permissions,
// rate limits and the network boundary. It contains no SQL; the storage it
// needs is declared here and implemented in internal/postgres.
//
// The design is docs/superpowers/specs/2026-09-13-leaderboard-design.md.
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
	// StateNotStarted is a published contest whose window has not opened:
	// there is nothing to rank, and not even the roster is shown.
	StateNotStarted = "not_started"
	// StateLive is a running contest before any freeze: the table as of now.
	StateLive = "live"
	// StateFrozen is the table as it stood at the freeze, which lasts past the
	// finish until an organiser reveals the result.
	StateFrozen = "frozen"
	// StateFinal is a finished contest that was never frozen, or one whose
	// result has been revealed.
	StateFinal = "final"
)

// Errors the package reports.
var (
	// ErrNotFound is a contest that does not exist or has no table for anybody
	// outside its staff yet (a draft). One error for both, so the table is not
	// a way to learn which contests exist.
	ErrNotFound = errors.New("leaderboard not found")
	// ErrNotAParticipant is a signed-in account with no registration in the
	// contest asking for the participant's own view of the table.
	ErrNotAParticipant = errors.New("not a participant of this contest")
	// ErrNotRevealable is a reveal the contest cannot take: it has not
	// finished, or it was never frozen and so has nothing to reveal.
	ErrNotRevealable = errors.New("the leaderboard cannot be revealed")
)

// Decision is what the table shows at a moment and where it is cut off.
type Decision struct {
	State string
	// Cutoff is the moment answers are counted up to, exclusive: an answer
	// submitted exactly at the cutoff is not on the table.
	Cutoff time.Time
	// FrozenAt is set while the table is frozen.
	FrozenAt *time.Time
}

// Decide reports the table's state for everybody but the contest's staff.
//
// The cutoff for a final table is now rather than ends_at: an answer sent in
// the last second is accepted with the network allowance (DEADLINE_GRACE) and
// can carry a submitted_at just past ends_at. Nothing is accepted after that,
// so now is the result.
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

// Entry is one registration's aggregate up to a cutoff, as storage returns it.
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
	// LastScoredAt is the latest answer that earned points: when the current
	// score was reached, and the tie-break between equal scores.
	LastScoredAt *time.Time
	// FinalAt is the first correct answer to a final question, which decides
	// the winner in winner mode.
	FinalAt *time.Time

	// The rest is ICPC's (contests.ScoringICPC) and zero in every other mode,
	// where Solved still counts every question but here counts only the
	// visible ones the grid shows.
	//
	// Penalty is the minutes the solved questions cost: each one's solving
	// minute plus the contest's penalty for every wrong attempt before it.
	Penalty int
	// LastSolvedAt is the latest solve, which orders rows sharing a place.
	LastSolvedAt *time.Time
	// Cells hold one cell per visible question, in the questions' order.
	Cells []Cell
}

// Grid is what one ICPC computation knows beside its rows, taken from the
// same statement so that the two always agree.
type Grid struct {
	// Questions is the number of visible questions: the grid's width, and the
	// length of every row's Cells.
	Questions int
	// FirstSolves holds, per visible question in the questions' order, the
	// earliest solve before the cutoff among the registrations that are not
	// disqualified and were made before the cutoff — over the whole contest,
	// not only the rows returned. Nil where nobody has solved the question.
	FirstSolves []*time.Time
}

// The states of a cell on the ICPC grid.
const (
	// CellSolved is a question answered correctly before the cutoff.
	CellSolved = "solved"
	// CellFailed is a question with only wrong attempts before the cutoff.
	CellFailed = "failed"
	// CellPending is a frozen table's question that was not solved before the
	// freeze and has been attempted since. It tells how many attempts there
	// were and nothing about them.
	CellPending = "pending"
	// CellUntried is a question with nothing to show.
	CellUntried = "untried"
)

// Cell is one registration's record on one visible question.
type Cell struct {
	// SolvedAt is the first correct answer before the cutoff.
	SolvedAt *time.Time
	// Minute is the whole minutes, rounded down, from the registration's start
	// to SolvedAt. Zero when the question is not solved.
	Minute int
	// Wrong counts the wrong attempts before the cutoff — only those before
	// SolvedAt when the question is solved, since nothing after a solve costs.
	Wrong int
	// Pending counts the attempts inside Query.Pending. Storage fills it only
	// when the query asked, and only for a question not solved before the
	// cutoff.
	Pending int
	// First marks the earliest solve of this question among the rows that are
	// not disqualified. Rank sets it; storage never does.
	First bool
}

// State names what the cell shows.
//
// A solve is checked first because a cell cut off at the freeze carries a
// pending count only when the question was not solved by then; pending comes
// before failed because a frozen table must not say that attempts made since
// the freeze were wrong — the wrong count before the freeze travels with it.
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

// SolvedOnAttempt is the attempt a solved question was solved with: every
// attempt before the first correct one was wrong.
func (c Cell) SolvedOnAttempt() int {
	return c.Wrong + 1
}

// QuestionLetter names a visible question by its zero-based position among
// the visible questions: A, B, … Z, then AA, AB, … as a spreadsheet names
// its columns. A question on the table is never named by its identifier.
func QuestionLetter(position int) string {
	var name []byte
	for n := position + 1; n > 0; n = (n - 1) / 26 {
		name = append([]byte{byte('A' + (n-1)%26)}, name...)
	}
	return string(name)
}

// Row is an entry with its place.
type Row struct {
	Entry
	// Place is 1-based and shared by equal results. Zero means unplaced: in
	// winner mode only the winner has a place.
	Place  int
	Winner bool
}

// Label is how the row is named under the contest's choice. A deleted
// account has no label: its login is freed and may belong to somebody else by
// now, and the old result must not read as theirs. A full name falls back to
// the login when the account never had one.
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
