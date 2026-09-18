package monitor

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// DocumentNotes is the document name of a participant's notes. An SQL tab's
// document is its id (TabDocument).
const DocumentNotes = "notes"

// TabDocument is the document name of an SQL tab: its id. Not a foreign key,
// so a deleted tab keeps its history.
func TabDocument(tab uuid.UUID) string { return tab.String() }

// RevisionWindow is how long a revision keeps absorbing saves. Autosave may
// write up to forty times a minute and every write can carry 64 KiB; folding
// every save within this window of a revision's first into that revision
// keeps at most two revisions a minute per document, and the latest state
// always.
const RevisionWindow = 30 * time.Second

// MaxRevisionBodyBytes bounds a revision's body: the larger of the two
// documents' own bounds — notes at 20,000 characters of up to four bytes each
// (workspace.MaxNotesRunes) and a tab at 64 KiB (workspace.MaxTabBodyBytes).
const MaxRevisionBodyBytes = 4 * 20000

// ErrRevisionInvalid refuses a revision that names no registration, no known
// document or no time, or whose title or body is past its bound.
var ErrRevisionInvalid = errors.New("the revision is invalid")

// Revision is one save of a document, about to be folded into its history.
type Revision struct {
	Registration uuid.UUID
	// Document is DocumentNotes or TabDocument(id).
	Document string
	// Title is the tab's title at the time; empty for the notes.
	Title string
	Body  string
	// At is when the save happened.
	At time.Time
}

// Validate refuses a revision that cannot be stored.
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
// at `started` — that is, whether the revision is younger than
// RevisionWindow. Measured from the revision's start rather than its last
// save, so a participant typing without pause still gets a new revision
// every thirty seconds instead of one that never closes.
func Extends(started, at time.Time) bool {
	return at.Sub(started) < RevisionWindow
}
