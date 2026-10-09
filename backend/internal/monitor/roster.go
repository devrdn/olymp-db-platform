package monitor

import (
	"time"

	"github.com/google/uuid"
)

// The thresholds of the six flags: hints, not verdicts, and constants so
// contests stay comparable.
const (
	// LongAbsenceTotal: more time away in total raises the long-absence
	// flag.
	LongAbsenceTotal = 5 * time.Minute
	// LongAbsenceCount: more absences raise it too.
	LongAbsenceCount = 10
	// LargePasteChars: a larger paste into the editor or an answer raises
	// the large-paste flag; pastes into the notes do not.
	LargePasteChars = 200
	// IdenticalQueryMinChars: only normalised queries at least this long are
	// compared; shorter ones are what everybody types.
	IdenticalQueryMinChars = 60
)

// MaxRosterRows bounds the participants table (CLAUDE.md rule 2); a larger
// roster is truncated and says so.
const MaxRosterRows = 2000

// RosterRow is one participant in the organiser's table, with the raw
// numbers the flags are decided on.
type RosterRow struct {
	Registration uuid.UUID
	User         uuid.UUID
	Login        string
	FullName     string
	Status       string
	StartedAt    *time.Time
	FinishedAt   *time.Time

	// Queries counts every journalled query; QueryErrors those that failed
	// or timed out; QueryRejected those the validator refused.
	Queries       int
	QueryErrors   int
	QueryRejected int
	Addresses     int

	Correct int
	Wrong   int
	// BlindCorrect counts correct answers with no successful query since the
	// previous answer to any question, or the start.
	BlindCorrect int

	PageLeft int
	AwayMs   int64
	Pastes   int
	// LargestPasteChars is the largest paste into the editor or an answer.
	// Storage returns the maximum rather than a count, so the threshold is
	// applied only in Flags.
	LargestPasteChars int64
	IPChanges         int
	ParallelSessions  int
	// IdenticalQueries counts distinct successful comparable queries that
	// another participant also ran successfully.
	IdenticalQueries int

	// LastActivity is nil when there is none.
	LastActivity *time.Time
}

type Flags struct {
	MultipleIPs          bool `json:"multiple_ips"`
	ParallelSessions     bool `json:"parallel_sessions"`
	LongAbsence          bool `json:"long_absence"`
	AnswerWithoutQueries bool `json:"answer_without_queries"`
	LargePaste           bool `json:"large_paste"`
	IdenticalQueries     bool `json:"identical_queries"`
}

func (f Flags) Any() bool {
	return f.MultipleIPs || f.ParallelSessions || f.LongAbsence ||
		f.AnswerWithoutQueries || f.LargePaste || f.IdenticalQueries
}

// Flags decides the row's flags. Every threshold is applied here except
// IdenticalQueryMinChars, applied when a query is journalled.
func (r RosterRow) Flags() Flags {
	return Flags{
		MultipleIPs:          r.Addresses > 1 || r.IPChanges > 0,
		ParallelSessions:     r.ParallelSessions > 0,
		LongAbsence:          r.AwayMs > LongAbsenceTotal.Milliseconds() || r.PageLeft > LongAbsenceCount,
		AnswerWithoutQueries: r.BlindCorrect > 0,
		LargePaste:           r.LargestPasteChars > LargePasteChars,
		IdenticalQueries:     r.IdenticalQueries > 0,
	}
}

type Roster struct {
	GeneratedAt time.Time
	Truncated   bool
	Rows        []RosterRow
}
