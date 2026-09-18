package monitor

import (
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// What an organiser reads about one participant (design §4): who they are,
// their queries, their answers with the queries that led to each, and their
// notes and SQL tabs with their history.

// Participant is one registration of the contest being watched.
type Participant struct {
	Registration uuid.UUID
	Contest      uuid.UUID
	User         uuid.UUID
	Login        string
	FullName     string
	Status       string
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

// The per-participant refusals (CLAUDE.md rule 1).
var (
	// ErrParticipantNotFound: no such registration in this contest — one
	// answer whether it does not exist or belongs to another contest.
	ErrParticipantNotFound = errors.New("no such participant in this contest")
	// ErrRevisionNotFound: no such revision of this participant.
	ErrRevisionNotFound = errors.New("no such revision")
	// ErrInvalidQueryFilter: an unknown status, a search past its bound, or
	// a cursor of another kind.
	ErrInvalidQueryFilter = errors.New("the query filter is not valid")
)

// The bounds of the queries tab (CLAUDE.md rule 2).
const (
	// MaxQueriesPage is the most queries one page carries. Each carries its
	// whole statement, up to 64 KiB, so the page is smaller than the feed's.
	MaxQueriesPage = 50
	// DefaultQueriesPage is the page size when the caller names none.
	DefaultQueriesPage = 50
	// MaxQuerySearchRunes bounds the text searched for.
	MaxQuerySearchRunes = 200
)

// queryStatuses are the statuses a filter may name: query_log's own.
var queryStatuses = map[string]bool{"running": true, "ok": true, "rejected": true, "error": true, "timeout": true}

// QueriesQuery asks for one page of a participant's queries, newest first.
type QueriesQuery struct {
	Contest      uuid.UUID
	Registration uuid.UUID
	// Status narrows to one status; empty is every status.
	Status string
	// Search narrows to statements containing this text, without case.
	Search string
	// Before is the position of the last query of the previous page.
	Before *Cursor
	Limit  int
}

// Normalize checks the query and fills its defaults.
func (q QueriesQuery) Normalize() (QueriesQuery, error) {
	if q.Status != "" && !queryStatuses[q.Status] {
		return q, fmt.Errorf("%w: unknown status %q", ErrInvalidQueryFilter, q.Status)
	}
	if !utf8.ValidString(q.Search) || utf8.RuneCountInString(q.Search) > MaxQuerySearchRunes {
		return q, fmt.Errorf("%w: a search is at most %d characters", ErrInvalidQueryFilter, MaxQuerySearchRunes)
	}
	if q.Before != nil && q.Before.Source != SourceQuery {
		return q, fmt.Errorf("%w: the cursor is not a query's", ErrInvalidQueryFilter)
	}
	switch {
	case q.Limit <= 0:
		q.Limit = DefaultQueriesPage
	case q.Limit > MaxQueriesPage:
		q.Limit = MaxQueriesPage
	}
	return q, nil
}

// LoggedQuery is one query of the queries tab, whole.
type LoggedQuery struct {
	At time.Time
	QueryData
}

// Cursor is the query's position, for the next page.
func (q LoggedQuery) Cursor() Cursor {
	return Cursor{At: q.At, Source: SourceQuery, ID: fmt.Sprint(q.ID)}
}

// QueriesPage is one page of the queries tab, newest first.
type QueriesPage struct {
	Items []LoggedQuery
	More  bool
}

// The bounds of the answers tab.
const (
	// MaxAnswerAttempts bounds the attempts read for one participant: far
	// past questions times attempts of any contest.
	MaxAnswerAttempts = 1000
	// MaxAttemptQueries bounds the queries shown before one attempt; the
	// rest are counted (Attempt.MoreQueries).
	MaxAttemptQueries = 100
)

// Attempt is one answer with the queries that led to it (design §3): the
// participant's queries after their previous answer to any question, or
// from the beginning, and before this one.
type Attempt struct {
	AnswerData
	At      time.Time
	Queries []LoggedQuery
	// MoreQueries is how many queries of the window are not in Queries.
	MoreQueries int
}

// QuestionAttempts is every attempt on one question, in order.
type QuestionAttempts struct {
	QuestionID  uuid.UUID
	QuestionOrd int
	Attempts    []Attempt
}

// Answers is the answers tab.
type Answers struct {
	Questions []QuestionAttempts
	// Truncated says the participant made more than MaxAnswerAttempts.
	Truncated bool
}

// GroupAttempts groups attempts, in time order, by question, in the order
// of the questions.
func GroupAttempts(attempts []Attempt) []QuestionAttempts {
	index := map[uuid.UUID]int{}
	var out []QuestionAttempts
	for _, a := range attempts {
		i, ok := index[a.QuestionID]
		if !ok {
			i = len(out)
			index[a.QuestionID] = i
			out = append(out, QuestionAttempts{QuestionID: a.QuestionID, QuestionOrd: a.QuestionOrd})
		}
		out[i].Attempts = append(out[i].Attempts, a)
	}
	// Stable by ord, so questions of the same ord (a deleted one reads 0)
	// keep the order of their first attempt.
	slices.SortStableFunc(out, func(a, b QuestionAttempts) int { return a.QuestionOrd - b.QuestionOrd })
	return out
}

// The bound of the revision list.
const MaxRevisionsListed = 2000

// Notes is the participant's notes as they are now.
type Notes struct {
	Body      string     `json:"body"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// Tab is one SQL tab as it is now.
type Tab struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Position  int       `json:"position"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RevisionInfo is one revision without its body.
type RevisionInfo struct {
	ID        int64     `json:"id"`
	Document  string    `json:"document"`
	Title     string    `json:"title"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Size is the body's length in bytes.
	Size int `json:"size"`
}

// Workspace is the participant's notes and tabs now, and the list of their
// revisions, newest first.
type Workspace struct {
	Notes     Notes
	Tabs      []Tab
	Revisions []RevisionInfo
	// Truncated says there are more than MaxRevisionsListed revisions.
	Truncated bool
}

// RevisionBody is one revision whole.
type RevisionBody struct {
	RevisionInfo
	Body string `json:"body"`
}
