package monitor

import (
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

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

var (
	// ErrParticipantNotFound is also the answer for another contest's
	// registration.
	ErrParticipantNotFound = errors.New("no such participant in this contest")
	ErrRevisionNotFound    = errors.New("no such revision")
	// ErrInvalidQueryFilter: an unknown status, a search past its bound, or
	// a cursor of another kind.
	ErrInvalidQueryFilter = errors.New("the query filter is not valid")
)

// The bounds of the queries tab (CLAUDE.md rule 2).
const (
	// MaxQueriesPage is smaller than the feed's: each query carries its
	// whole statement.
	MaxQueriesPage      = 50
	DefaultQueriesPage  = 50
	MaxQuerySearchRunes = 200
)

var queryStatuses = map[string]bool{"running": true, "ok": true, "rejected": true, "error": true, "timeout": true}

type QueriesQuery struct {
	Contest      uuid.UUID
	Registration uuid.UUID
	// Status empty is every status.
	Status string
	// Search matches statements containing this text, ignoring case.
	Search string
	Before *Cursor
	Limit  int
}

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

type LoggedQuery struct {
	At time.Time
	QueryData
}

func (q LoggedQuery) Cursor() Cursor {
	return Cursor{At: q.At, Source: SourceQuery, ID: fmt.Sprint(q.ID)}
}

type QueriesPage struct {
	Items []LoggedQuery
	More  bool
}

// The bounds of the answers tab.
const (
	MaxAnswerAttempts = 1000
	// MaxAttemptQueries bounds the queries shown before one attempt; the
	// rest are counted.
	MaxAttemptQueries = 100
)

// Attempt is one answer with the queries that led to it: those after the
// participant's previous answer to any question (or the start) and before
// this one.
type Attempt struct {
	AnswerData
	At          time.Time
	Queries     []LoggedQuery
	MoreQueries int
}

type QuestionAttempts struct {
	QuestionID  uuid.UUID
	QuestionOrd int
	Attempts    []Attempt
}

type Answers struct {
	Questions []QuestionAttempts
	Truncated bool
}

// GroupAttempts groups time-ordered attempts by question, in question order.
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
	// Stable, so questions of equal ord (a deleted one reads 0) keep the
	// order of their first attempt.
	slices.SortStableFunc(out, func(a, b QuestionAttempts) int { return a.QuestionOrd - b.QuestionOrd })
	return out
}

const MaxRevisionsListed = 2000

type Notes struct {
	Body      string     `json:"body"`
	UpdatedAt *time.Time `json:"updated_at"`
}

type Tab struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Position  int       `json:"position"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RevisionInfo struct {
	ID        int64     `json:"id"`
	Document  string    `json:"document"`
	Title     string    `json:"title"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Size is in bytes.
	Size int `json:"size"`
}

// Workspace is the participant's notes and tabs now, with their revisions
// newest first.
type Workspace struct {
	Notes     Notes
	Tabs      []Tab
	Revisions []RevisionInfo
	Truncated bool
}

type RevisionBody struct {
	RevisionInfo
	Body string `json:"body"`
}
