// Package monitor answers what is recorded about a participant beyond queries
// and answers: event kinds and bounds, revisions, query fingerprints,
// server-observed signals (Tracker), browser signal batches (Signals) and the
// organiser's reads (WatchService). It stores nothing, serves no HTTP, does not
// decide who may watch, and judges nobody: a browser signal is only what the
// browser chose to report.
package monitor

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Kind names what happened. The values are stored and are part of the API.
type Kind string

const (
	// KindPageLeft: the page was hidden or lost focus, reported on return.
	KindPageLeft        Kind = "page_left"
	KindPaste           Kind = "paste"
	KindIPChanged       Kind = "ip_changed"
	KindParallelSession Kind = "parallel_session"
	KindTabCreated      Kind = "tab_created"
	KindTabRenamed      Kind = "tab_renamed"
	KindTabDeleted      Kind = "tab_deleted"
)

// FromBrowser reports whether events of this kind come from the browser.
// Only those may be posted by a client and keep the browser's claimed time.
func (k Kind) FromBrowser() bool {
	return k == KindPageLeft || k == KindPaste
}

// The bounds on every field (CLAUDE.md rule 2).
const (
	// MinAway is the shortest absence recorded.
	MinAway = time.Second
	// MaxAway caps a reported absence; a day is longer than any olympiad.
	MaxAway = 24 * time.Hour
	// MaxPasteTextRunes is how much of a paste's beginning is kept.
	MaxPasteTextRunes = 500
	// MaxPasteChars caps the reported size of a paste, far above what any
	// paste target holds, so it only cuts a lie.
	MaxPasteChars     = 1 << 20
	MaxUserAgentRunes = 200
	// MaxTabTitleRunes restates workspace.MaxTitleRunes, which this package
	// cannot import.
	MaxTabTitleRunes = 40
	MaxBatchEvents   = 50
	// MaxBatchPastes is the most paste events one batch stores after folding
	// repeats (CleanBatch), so a few participants cannot flood the live feed.
	MaxBatchPastes = 10
	// MaxStoredEvents is the most browser-posted events one registration
	// stores, about ten times a very busy participant's. Past it signals are
	// refused, not silently dropped. Server-observed events are always
	// stored, or a participant could fill the budget and then change address
	// unrecorded.
	MaxStoredEvents = 20000
)

// The refusals (CLAUDE.md rule 1).
var (
	// ErrEventInvalid: an event without a contest, a registration or a
	// payload, or a server event missing a field it cannot do without.
	ErrEventInvalid = errors.New("the event is incomplete")
	// ErrAwayTooShort: an absence shorter than MinAway. A batch drops the
	// event, not itself.
	ErrAwayTooShort  = fmt.Errorf("an absence shorter than %s is not recorded", MinAway)
	ErrPasteTarget   = errors.New("a paste target is editor, answer or notes")
	ErrBatchTooLarge = fmt.Errorf("a batch holds at most %d events", MaxBatchEvents)
	ErrTooManyEvents = fmt.Errorf("a participant stores at most %d events", MaxStoredEvents)
)

// Payload is what one kind of event carries, one type per kind. The JSON
// field names are what participant_events.payload stores.
type Payload interface {
	Kind() Kind
	normalize() (Payload, error)
}

type Event struct {
	Contest      uuid.UUID
	Registration uuid.UUID
	Payload      Payload
	// ClientAt is the browser's claimed time, only a claim; the server's
	// time is the column default.
	ClientAt *time.Time
}

func (e Event) Kind() Kind {
	if e.Payload == nil {
		return ""
	}
	return e.Payload.Kind()
}

// earliestClaim and latestClaim bound a browser's claimed time. A claim
// outside is dropped, not the event.
var (
	earliestClaim = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	latestClaim   = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
)

// Normalize returns the event with every field inside its bound, or a
// refusal when it cannot be stored. internal/postgres calls it again rather
// than trust that it happened.
func (e Event) Normalize() (Event, error) {
	if e.Contest == uuid.Nil || e.Registration == uuid.Nil || e.Payload == nil {
		return Event{}, ErrEventInvalid
	}
	payload, err := e.Payload.normalize()
	if err != nil {
		return Event{}, err
	}
	e.Payload = payload

	if e.ClientAt != nil {
		if !payload.Kind().FromBrowser() || e.ClientAt.Before(earliestClaim) || e.ClientAt.After(latestClaim) {
			e.ClientAt = nil
		} else {
			claimed := e.ClientAt.UTC()
			e.ClientAt = &claimed
		}
	}
	return e, nil
}

func CheckBatch(events []Event) error {
	if len(events) > MaxBatchEvents {
		return ErrBatchTooLarge
	}
	return nil
}

type PageLeft struct {
	AwayMs int64 `json:"away_ms"`
}

func (PageLeft) Kind() Kind { return KindPageLeft }

func (p PageLeft) normalize() (Payload, error) {
	if p.AwayMs < MinAway.Milliseconds() {
		return nil, ErrAwayTooShort
	}
	p.AwayMs = min(p.AwayMs, MaxAway.Milliseconds())
	return p, nil
}

type PasteTarget string

const (
	PasteEditor PasteTarget = "editor"
	PasteAnswer PasteTarget = "answer"
	PasteNotes  PasteTarget = "notes"
)

type Paste struct {
	Target PasteTarget `json:"target"`
	// Chars is as the browser counted them.
	Chars int    `json:"chars"`
	Text  string `json:"text"`
	// Count is how many identical pastes in a row CleanBatch folded into
	// this one; zero for a single paste. Never taken from a browser.
	Count int `json:"count,omitempty"`
}

func (Paste) Kind() Kind { return KindPaste }

func (p Paste) normalize() (Payload, error) {
	switch p.Target {
	case PasteEditor, PasteAnswer, PasteNotes:
	default:
		return nil, ErrPasteTarget
	}
	p.Chars = max(0, min(p.Chars, MaxPasteChars))
	p.Text = clip(p.Text, MaxPasteTextRunes)
	p.Count = min(p.Count, MaxBatchEvents)
	if p.Count < 2 {
		p.Count = 0
	}
	return p, nil
}

type IPChanged struct {
	From netip.Addr `json:"from"`
	To   netip.Addr `json:"to"`
}

func (IPChanged) Kind() Kind { return KindIPChanged }

func (p IPChanged) normalize() (Payload, error) {
	if !p.From.IsValid() || !p.To.IsValid() {
		return nil, fmt.Errorf("%w: ip_changed needs both addresses", ErrEventInvalid)
	}
	return p, nil
}

type ParallelSession struct {
	OtherIP   netip.Addr `json:"other_ip"`
	UserAgent string     `json:"user_agent"`
}

func (ParallelSession) Kind() Kind { return KindParallelSession }

func (p ParallelSession) normalize() (Payload, error) {
	if !p.OtherIP.IsValid() {
		return nil, fmt.Errorf("%w: parallel_session needs the other session's address", ErrEventInvalid)
	}
	p.UserAgent = clip(p.UserAgent, MaxUserAgentRunes)
	return p, nil
}

type TabCreated struct {
	TabID uuid.UUID `json:"tab_id"`
	Title string    `json:"title"`
}

func (TabCreated) Kind() Kind { return KindTabCreated }

func (p TabCreated) normalize() (Payload, error) {
	if p.TabID == uuid.Nil {
		return nil, fmt.Errorf("%w: a tab event needs the tab", ErrEventInvalid)
	}
	p.Title = clip(p.Title, MaxTabTitleRunes)
	return p, nil
}

type TabRenamed struct {
	TabID uuid.UUID `json:"tab_id"`
	From  string    `json:"from"`
	To    string    `json:"to"`
}

func (TabRenamed) Kind() Kind { return KindTabRenamed }

func (p TabRenamed) normalize() (Payload, error) {
	if p.TabID == uuid.Nil {
		return nil, fmt.Errorf("%w: a tab event needs the tab", ErrEventInvalid)
	}
	p.From = clip(p.From, MaxTabTitleRunes)
	p.To = clip(p.To, MaxTabTitleRunes)
	return p, nil
}

// TabDeleted is an SQL tab removed; its revisions stay.
type TabDeleted struct {
	TabID uuid.UUID `json:"tab_id"`
	Title string    `json:"title"`
}

func (TabDeleted) Kind() Kind { return KindTabDeleted }

func (p TabDeleted) normalize() (Payload, error) {
	if p.TabID == uuid.Nil {
		return nil, fmt.Errorf("%w: a tab event needs the tab", ErrEventInvalid)
	}
	p.Title = clip(p.Title, MaxTabTitleRunes)
	return p, nil
}

// clip drops NUL and invalid UTF-8, which jsonb refuses and which would fail
// a whole batch, and cuts to at most limit characters.
func clip(text string, limit int) string {
	text = strings.ToValidUTF8(text, "�")
	text = strings.ReplaceAll(text, "\x00", "")
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	cut := 0
	for i := range text {
		if cut == limit {
			return text[:i]
		}
		cut++
	}
	return text
}
