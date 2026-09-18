// Package monitor answers what is recorded about a participant beyond their
// queries and answers, and in what shape: the kinds of participant event
// (design §2.1) with the bound on every field of each, the rule that folds
// frequent saves of the notes and SQL tabs into revisions (§2.4), and the
// fingerprint that tells two participants' identical queries apart from
// merely similar ones (§5). Its Tracker detects the two signals the server
// sees for itself (§2.3): a registration's address changing, and a second
// session using it while the first is live. Its Signals throttles and cleans
// the batches the browser reports (§2.2): one bad signal is dropped rather
// than costing the batch it came in.
//
// It deliberately does not store anything itself (internal/postgres stores
// events and revisions, and the Tracker keeps its per-registration trail in
// the platform cache, each through an interface its consumer declares),
// decide when a request is a participant's (participant admission does, in
// queryproxy and the /play handlers, and hands the Tracker only admitted
// requests), serve anything over HTTP (internal/api does), decide who may
// watch (rbac.PermissionContestMonitor does), or judge a participant: a
// browser signal is what the participant's own browser chose to report, and
// nothing here treats it as proof.
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

// Kind names what happened. The values are stored in participant_events.kind
// and are part of the API the organiser's screen reads.
type Kind string

const (
	// KindPageLeft: the participant's page was hidden or lost focus, reported
	// by the browser when they came back.
	KindPageLeft Kind = "page_left"
	// KindPaste: the participant pasted text into the SQL editor, an answer
	// or their notes, reported by the browser.
	KindPaste Kind = "paste"
	// KindIPChanged: a request of this registration came from a different
	// address than the previous one.
	KindIPChanged Kind = "ip_changed"
	// KindParallelSession: a second session used this registration while the
	// first was still active.
	KindParallelSession Kind = "parallel_session"
	// KindTabCreated, KindTabRenamed and KindTabDeleted are the life of an
	// SQL tab.
	KindTabCreated Kind = "tab_created"
	KindTabRenamed Kind = "tab_renamed"
	KindTabDeleted Kind = "tab_deleted"
)

// FromBrowser reports whether events of this kind come from the
// participant's browser rather than from the server. Only those may be
// posted by a client, and only those keep the time the browser claimed.
func (k Kind) FromBrowser() bool {
	return k == KindPageLeft || k == KindPaste
}

// The bounds on every field (CLAUDE.md rule 2). The design's numbers,
// verbatim, except where noted.
const (
	// MinAway is the shortest absence worth recording: shorter ones are a
	// click on another window and back, and are not written at all.
	MinAway = time.Second
	// MaxAway caps a reported absence. A browser can claim anything; a day is
	// longer than any olympiad.
	MaxAway = 24 * time.Hour
	// MaxPasteTextRunes is how much of a paste is kept: its beginning.
	MaxPasteTextRunes = 500
	// MaxPasteChars caps the reported size of a paste. Not the design's
	// number — the design bounds the text, not the count — but far above
	// what any paste target holds (the largest, an SQL tab, is 64 KiB), so it
	// only ever cuts a lie.
	MaxPasteChars = 1 << 20
	// MaxUserAgentRunes is how much of a browser's User-Agent is kept.
	MaxUserAgentRunes = 200
	// MaxTabTitleRunes is the longest a tab title is: the workspace's own
	// bound (workspace.MaxTitleRunes), restated because the workspace
	// package is a consumer of this one and cannot be imported by it.
	MaxTabTitleRunes = 40
	// MaxBatchEvents is the most events one batch from a browser may carry.
	MaxBatchEvents = 50
)

// The refusals. Each is a sentinel so an HTTP layer can name it
// (CLAUDE.md rule 1).
var (
	// ErrEventInvalid: an event without a contest, a registration or a
	// payload, or a server event missing a field it cannot do without.
	ErrEventInvalid = errors.New("the event is incomplete")
	// ErrAwayTooShort: an absence shorter than MinAway, which is not
	// recorded. A caller filtering a batch drops the event rather than the
	// batch.
	ErrAwayTooShort = fmt.Errorf("an absence shorter than %s is not recorded", MinAway)
	// ErrPasteTarget: a paste into something that is not one of the three
	// places a paste is watched.
	ErrPasteTarget = errors.New("a paste target is editor, answer or notes")
	// ErrBatchTooLarge: more than MaxBatchEvents events in one batch.
	ErrBatchTooLarge = fmt.Errorf("a batch holds at most %d events", MaxBatchEvents)
)

// Payload is what one kind of event carries. Each kind has its own type, so
// a payload cannot carry another kind's fields; the JSON field names are the
// ones participant_events.payload stores.
type Payload interface {
	// Kind is the kind this payload belongs to.
	Kind() Kind
	// normalize bounds every field, or refuses the payload.
	normalize() (Payload, error)
}

// Event is one thing that happened, about to be stored.
type Event struct {
	Contest      uuid.UUID
	Registration uuid.UUID
	Payload      Payload
	// ClientAt is the time the browser claimed for a browser signal. It is a
	// claim and nothing more; the server's own time is the column's default.
	ClientAt *time.Time
}

// Kind is the kind of the event's payload, or "" when it has none.
func (e Event) Kind() Kind {
	if e.Payload == nil {
		return ""
	}
	return e.Payload.Kind()
}

// earliestClaim and latestClaim bound a browser's claimed time to what a
// clock could plausibly say. A claim outside is dropped, not refused: the
// event itself is still worth keeping, and the column cannot hold every
// time.Time a decoder can produce.
var (
	earliestClaim = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	latestClaim   = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
)

// Normalize returns the event with every field inside its bound — text cut,
// numbers clamped, a claimed time kept only for a browser signal — or a
// refusal when it cannot be stored at all. Storing an event that did not
// pass through here is a bug; internal/postgres calls it again rather than
// trust that it happened.
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

// CheckBatch refuses a batch larger than MaxBatchEvents.
func CheckBatch(events []Event) error {
	if len(events) > MaxBatchEvents {
		return ErrBatchTooLarge
	}
	return nil
}

// PageLeft is an absence from the page.
type PageLeft struct {
	// AwayMs is how long the participant was away, in milliseconds.
	AwayMs int64 `json:"away_ms"`
}

// Kind implements Payload.
func (PageLeft) Kind() Kind { return KindPageLeft }

func (p PageLeft) normalize() (Payload, error) {
	if p.AwayMs < MinAway.Milliseconds() {
		return nil, ErrAwayTooShort
	}
	p.AwayMs = min(p.AwayMs, MaxAway.Milliseconds())
	return p, nil
}

// PasteTarget is where a paste landed.
type PasteTarget string

const (
	PasteEditor PasteTarget = "editor"
	PasteAnswer PasteTarget = "answer"
	PasteNotes  PasteTarget = "notes"
)

// Paste is text pasted into one of the watched fields.
type Paste struct {
	Target PasteTarget `json:"target"`
	// Chars is how many characters were pasted, as the browser counted them.
	Chars int `json:"chars"`
	// Text is the beginning of what was pasted, at most MaxPasteTextRunes.
	Text string `json:"text"`
}

// Kind implements Payload.
func (Paste) Kind() Kind { return KindPaste }

func (p Paste) normalize() (Payload, error) {
	switch p.Target {
	case PasteEditor, PasteAnswer, PasteNotes:
	default:
		return nil, ErrPasteTarget
	}
	p.Chars = max(0, min(p.Chars, MaxPasteChars))
	p.Text = clip(p.Text, MaxPasteTextRunes)
	return p, nil
}

// IPChanged is a request from a different address than the previous one.
type IPChanged struct {
	From netip.Addr `json:"from"`
	To   netip.Addr `json:"to"`
}

// Kind implements Payload.
func (IPChanged) Kind() Kind { return KindIPChanged }

func (p IPChanged) normalize() (Payload, error) {
	if !p.From.IsValid() || !p.To.IsValid() {
		return nil, fmt.Errorf("%w: ip_changed needs both addresses", ErrEventInvalid)
	}
	return p, nil
}

// ParallelSession is a second session using the registration while the first
// was active.
type ParallelSession struct {
	OtherIP   netip.Addr `json:"other_ip"`
	UserAgent string     `json:"user_agent"`
}

// Kind implements Payload.
func (ParallelSession) Kind() Kind { return KindParallelSession }

func (p ParallelSession) normalize() (Payload, error) {
	if !p.OtherIP.IsValid() {
		return nil, fmt.Errorf("%w: parallel_session needs the other session's address", ErrEventInvalid)
	}
	p.UserAgent = clip(p.UserAgent, MaxUserAgentRunes)
	return p, nil
}

// TabCreated is a new SQL tab.
type TabCreated struct {
	TabID uuid.UUID `json:"tab_id"`
	Title string    `json:"title"`
}

// Kind implements Payload.
func (TabCreated) Kind() Kind { return KindTabCreated }

func (p TabCreated) normalize() (Payload, error) {
	if p.TabID == uuid.Nil {
		return nil, fmt.Errorf("%w: a tab event needs the tab", ErrEventInvalid)
	}
	p.Title = clip(p.Title, MaxTabTitleRunes)
	return p, nil
}

// TabRenamed is an SQL tab given a new title.
type TabRenamed struct {
	TabID uuid.UUID `json:"tab_id"`
	From  string    `json:"from"`
	To    string    `json:"to"`
}

// Kind implements Payload.
func (TabRenamed) Kind() Kind { return KindTabRenamed }

func (p TabRenamed) normalize() (Payload, error) {
	if p.TabID == uuid.Nil {
		return nil, fmt.Errorf("%w: a tab event needs the tab", ErrEventInvalid)
	}
	p.From = clip(p.From, MaxTabTitleRunes)
	p.To = clip(p.To, MaxTabTitleRunes)
	return p, nil
}

// TabDeleted is an SQL tab removed. Its revisions stay.
type TabDeleted struct {
	TabID uuid.UUID `json:"tab_id"`
	Title string    `json:"title"`
}

// Kind implements Payload.
func (TabDeleted) Kind() Kind { return KindTabDeleted }

func (p TabDeleted) normalize() (Payload, error) {
	if p.TabID == uuid.Nil {
		return nil, fmt.Errorf("%w: a tab event needs the tab", ErrEventInvalid)
	}
	p.Title = clip(p.Title, MaxTabTitleRunes)
	return p, nil
}

// clip makes text storable in jsonb and cuts it to at most limit characters.
//
// jsonb refuses the NUL character, and a broken UTF-8 sequence has no text
// to store; either would fail the insert of a whole batch over one field.
// Characters rather than bytes, so a cut never splits one.
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
