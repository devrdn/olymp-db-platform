package monitor

import (
	"time"

	"github.com/google/uuid"
)

// The thresholds of the six flags (design §5). They are hints for the
// organiser, not verdicts, and constants rather than settings: a threshold
// an organiser could tune would be a threshold nobody could compare between
// two contests.
const (
	// LongAbsenceTotal: more than this much time away from the page in total
	// raises the long-absence flag.
	LongAbsenceTotal = 5 * time.Minute
	// LongAbsenceCount: more than this many absences raise it too, however
	// short each was.
	LongAbsenceCount = 10
	// LargePasteChars: a paste of more than this many characters into the SQL
	// editor or an answer raises the large-paste flag. Pastes into the notes
	// do not: copying one's own notes around is not a signal.
	LargePasteChars = 200
	// IdenticalQueryMinChars: a query is compared with other participants'
	// only when its normalised text (Fingerprint's normalisation) is at least
	// this long. Shorter ones — "select * from suspects" — are what everybody
	// types, and matching them would flag the whole roster.
	IdenticalQueryMinChars = 60
)

// MaxRosterRows bounds the participants table (CLAUDE.md rule 2), as the
// leaderboard bounds its own: a roster larger than this is truncated and says
// so.
const MaxRosterRows = 2000

// RosterRow is one participant in the organiser's table: who they are, what
// they did in counts, and the raw numbers the flags are decided on.
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
	// Addresses is how many distinct addresses the queries came from.
	Addresses int

	Correct int
	Wrong   int
	// BlindCorrect counts correct answers with no successful query since the
	// participant's previous answer (to any question), or since the start.
	BlindCorrect int

	PageLeft int
	AwayMs   int64
	Pastes   int
	// MaxPasteChars is the largest paste into the editor or an answer, in
	// characters; pastes into the notes are not measured. Storage keeps the
	// largest rather than a count of those past LargePasteChars so that the
	// threshold is applied here and nowhere else.
	MaxPasteChars    int64
	IPChanges        int
	ParallelSessions int
	// IdenticalQueries counts this participant's distinct successful queries
	// of at least IdenticalQueryMinChars that another participant of the
	// contest also ran successfully.
	IdenticalQueries int

	// LastActivity is the latest query, answer or event; nil when there is
	// none.
	LastActivity *time.Time
}

// Flags are the six hints of design §5.
type Flags struct {
	MultipleIPs          bool `json:"multiple_ips"`
	ParallelSessions     bool `json:"parallel_sessions"`
	LongAbsence          bool `json:"long_absence"`
	AnswerWithoutQueries bool `json:"answer_without_queries"`
	LargePaste           bool `json:"large_paste"`
	IdenticalQueries     bool `json:"identical_queries"`
}

// Any reports whether at least one flag is raised.
func (f Flags) Any() bool {
	return f.MultipleIPs || f.ParallelSessions || f.LongAbsence ||
		f.AnswerWithoutQueries || f.LargePaste || f.IdenticalQueries
}

// Flags decides the row's flags from its counts. Every threshold is applied
// in exactly one place: all but one of them here, and
// IdenticalQueryMinChars where the query is journalled, because a statement
// too short to compare is given no fingerprint to compare at all.
func (r RosterRow) Flags() Flags {
	return Flags{
		MultipleIPs:          r.Addresses > 1 || r.IPChanges > 0,
		ParallelSessions:     r.ParallelSessions > 0,
		LongAbsence:          r.AwayMs > LongAbsenceTotal.Milliseconds() || r.PageLeft > LongAbsenceCount,
		AnswerWithoutQueries: r.BlindCorrect > 0,
		LargePaste:           r.MaxPasteChars > LargePasteChars,
		IdenticalQueries:     r.IdenticalQueries > 0,
	}
}

// Roster is the participants table as one computation produced it.
type Roster struct {
	GeneratedAt time.Time
	Truncated   bool
	Rows        []RosterRow
}
