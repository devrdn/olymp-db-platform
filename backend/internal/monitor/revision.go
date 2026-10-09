package monitor

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// DocumentNotes is the document name of a participant's notes.
const DocumentNotes = "notes"

// TabDocument is an SQL tab's document name: its id, not a foreign key, so
// a deleted tab keeps its history.
func TabDocument(tab uuid.UUID) string { return tab.String() }

// RevisionWindow is how long a revision keeps absorbing saves: at most two
// revisions a minute per document however often autosave writes, and always
// the latest state.
const RevisionWindow = 30 * time.Second

// MaxRevisionBodyBytes is the larger document bound: 20,000 note characters
// of up to four bytes each.
const MaxRevisionBodyBytes = 4 * 20000

// ErrRevisionInvalid refuses a revision that names no registration, no known
// document or no time, or whose title or body is past its bound.
var ErrRevisionInvalid = errors.New("the revision is invalid")

type Revision struct {
	Registration uuid.UUID
	Document     string
	// Title is empty for the notes.
	Title string
	Body  string
	At    time.Time
}

func (r Revision) Validate() error {
	switch {
	case r.Registration == uuid.Nil:
		return fmt.Errorf("%w: no registration", ErrRevisionInvalid)
	case r.At.IsZero():
		return fmt.Errorf("%w: no time", ErrRevisionInvalid)
	case len(r.Body) > MaxRevisionBodyBytes:
		return fmt.Errorf("%w: the body is longer than %d bytes", ErrRevisionInvalid, MaxRevisionBodyBytes)
	case utf8.RuneCountInString(r.Title) > MaxTabTitleRunes:
		return fmt.Errorf("%w: the title is longer than %d characters", ErrRevisionInvalid, MaxTabTitleRunes)
	}
	if r.Document == DocumentNotes {
		if r.Title != "" {
			return fmt.Errorf("%w: the notes have no title", ErrRevisionInvalid)
		}
		return nil
	}
	if _, err := uuid.Parse(r.Document); err != nil {
		return fmt.Errorf("%w: %q is neither the notes nor a tab", ErrRevisionInvalid, r.Document)
	}
	return nil
}

// Extends reports whether a save at `at` belongs to the revision that started
// at `started`. It counts from the start, not the last save, so continuous
// typing still opens a new revision every RevisionWindow.
func Extends(started, at time.Time) bool {
	return at.Sub(started) < RevisionWindow
}
