// Package workspace answers "what has this participant written down for
// themselves in this olympiad": their notes and their SQL editor tabs, kept
// per registration so a reload, a crashed browser or a different computer in
// the lab loses nothing.
//
// It owns the limits (how long, how many, how often) and the rules of the tab
// set: there is always at least one tab, a new tab without a title is named
// after the smallest free number, and a new order names every tab exactly
// once.
//
// It does not decide whether the participant may read or write at all — that
// is queryproxy's admission, asked by the HTTP layer before a Session exists,
// and the workspace closes with the contest like the rest of the play screen
// — and it never runs the SQL a tab holds: a tab is text, and running
// it is the console's business. It is not private: the contest's organisers
// see it and its history (the monitoring design, §2.4), which the repository
// records with every write — a revision per save, an event per change in a
// tab's life — and the participant is told so on the play screen. Storage is
// declared here as Repository and implemented in internal/postgres.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// The limits of one workspace (CLAUDE.md rule 2): every string and every list
// a request carries is bounded here, whatever the request body limit allows.
const (
	// MaxNotesRunes is how many characters the notes may hold. Characters,
	// not bytes: a participant writing in Cyrillic gets the same room as one
	// writing in Latin.
	MaxNotesRunes = 20000
	// MaxTabs is how many SQL tabs one participant may keep.
	MaxTabs = 10
	// MaxTitleRunes is the longest a tab title may be, after trimming.
	MaxTitleRunes = 40
	// MaxTabBodyBytes is how much SQL one tab may hold: exactly what the
	// console would accept as a query, so any tab can be run as it stands.
	MaxTabBodyBytes = sqlpolicy.MaxQueryBytes
	// WritesPerMinute is how many writes one participant may make a minute,
	// notes and tabs together, refused ones included (AdmitWrite). Autosave at a pause of
	// a second and a half stays under forty even during continuous typing.
	WritesPerMinute = 60
)

// writeWindow is the write throttle's fixed window.
const writeWindow = time.Minute

// Why a workspace call was refused. Each is a different thing to tell the
// participant, and the HTTP layer maps every one of them (CLAUDE.md rule 1).
var (
	ErrNotesTooLong    = fmt.Errorf("the notes are longer than %d characters", MaxNotesRunes)
	ErrTooManyTabs     = fmt.Errorf("a participant may keep at most %d tabs", MaxTabs)
	ErrTitleInvalid    = fmt.Errorf("a tab title is 1 to %d characters with no control characters", MaxTitleRunes)
	ErrTabBodyTooLong  = fmt.Errorf("a tab holds at most %d bytes of SQL", MaxTabBodyBytes)
	ErrTextInvalid     = errors.New("the text contains a NUL character or is not valid UTF-8")
	ErrNothingToChange = errors.New("the change names neither a title nor a body")
	// ErrTabNotFound is also the answer for another participant's tab: that
	// it exists is not this caller's business.
	ErrTabNotFound = errors.New("no such tab in this workspace")
	// ErrLastTab keeps the editor from ever having nothing to show.
	ErrLastTab = errors.New("the last tab cannot be deleted")
	// ErrOrderMismatch is a new order that does not name every tab of the
	// workspace exactly once.
	ErrOrderMismatch = errors.New("the order must name every tab exactly once")
	ErrTooOften      = errors.New("too many workspace writes this minute")
)

// Notes is the participant's free text. UpdatedAt is nil until it is first
// saved.
type Notes struct {
	Body      string
	UpdatedAt *time.Time
}

// Tab is one SQL editor tab. Position orders the tabs, starting at zero.
type Tab struct {
	ID        uuid.UUID
	Title     string
	Body      string
	Position  int
	UpdatedAt time.Time
}

// TabPatch is a change to one tab: a nil field is left as it is.
type TabPatch struct {
	Title *string
	Body  *string
}

// Workspace is everything the participant's screen restores.
type Workspace struct {
	Notes Notes
	// Tabs are in position order, and never empty.
	Tabs []Tab
}

// Session is whose workspace a call works in, once the caller's admission
// has let the participant in, and which language a tab the server names
// should be named in.
type Session struct {
	Registration uuid.UUID
	Lang         string
}

// Repository is the storage this service needs. Implemented in
// internal/postgres.
type Repository interface {
	// Load returns the workspace, first creating a tab titled firstTitle if it
	// has none. Two concurrent first loads create one tab, not two.
	Load(ctx context.Context, registration uuid.UUID, firstTitle string) (Notes, []Tab, error)
	// SaveNotes replaces the notes and returns when they were saved, recording
	// the save in the notes' revisions in the same transaction.
	SaveNotes(ctx context.Context, registration uuid.UUID, body string) (time.Time, error)
	// CreateTab appends a tab, refusing with ErrTooManyTabs when the
	// workspace already holds limit of them. title is called with the titles
	// already taken, under the same lock that counts them. The creation is
	// recorded as a tab_created event in the same transaction, as is the
	// first tab Load creates.
	CreateTab(ctx context.Context, registration uuid.UUID, limit int, title func(taken []string) string) (Tab, error)
	// UpdateTab applies patch and returns when it was applied, or
	// ErrTabNotFound for a tab that is not this registration's. A changed
	// title is recorded as a tab_renamed event and new text as a revision of
	// the tab, in the same transaction.
	UpdateTab(ctx context.Context, registration, id uuid.UUID, patch TabPatch) (time.Time, error)
	// DeleteTab removes a tab and closes the gap in positions. ErrTabNotFound
	// for a tab that is not this registration's, ErrLastTab for the only one.
	// The deletion is recorded as a tab_deleted event; the tab's revisions
	// stay.
	DeleteTab(ctx context.Context, registration, id uuid.UUID) error
	// ReorderTabs gives each tab its index in ids as its position, in one
	// transaction, or refuses with ErrOrderMismatch when ids is not exactly
	// the workspace's set of tabs.
	ReorderTabs(ctx context.Context, registration uuid.UUID, ids []uuid.UUID) error
}

// Limiter is the slice of auth.Limiter the write throttle needs.
type Limiter interface {
	Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error)
}

// Service applies the workspace's rules over a Repository.
//
// Every write method assumes its caller has already spent the write through
// AdmitWrite: the throttle has to run before the caller's own lookups, which
// happen before a Session exists to hand to a write.
type Service struct {
	repo    Repository
	limiter Limiter
}

// NewService returns a workspace service.
func NewService(repo Repository, limiter Limiter) *Service {
	return &Service{repo: repo, limiter: limiter}
}

// Get returns the participant's workspace, creating the first tab — named in
// the session's language — if there is none yet.
//
// Not throttled here: the caller's read admission already charged for it,
// and a reload must not spend the budget autosave needs.
func (s *Service) Get(ctx context.Context, session Session) (Workspace, error) {
	notes, tabs, err := s.repo.Load(ctx, session.Registration, defaultTitle(session.Lang, 1))
	if err != nil {
		return Workspace{}, fmt.Errorf("load the workspace: %w", err)
	}
	return Workspace{Notes: notes, Tabs: tabs}, nil
}

// SaveNotes replaces the notes.
func (s *Service) SaveNotes(ctx context.Context, session Session, body string) (time.Time, error) {
	if err := checkText(body); err != nil {
		return time.Time{}, err
	}
	if utf8.RuneCountInString(body) > MaxNotesRunes {
		return time.Time{}, ErrNotesTooLong
	}
	at, err := s.repo.SaveNotes(ctx, session.Registration, body)
	if err != nil {
		return time.Time{}, fmt.Errorf("save the notes: %w", err)
	}
	return at, nil
}

// CreateTab appends an empty tab. A nil title names it after the smallest
// number no tab of the workspace is already named after.
func (s *Service) CreateTab(ctx context.Context, session Session, title *string) (Tab, error) {
	name := func(taken []string) string { return freeTitle(session.Lang, taken) }
	if title != nil {
		clean, err := cleanTitle(*title)
		if err != nil {
			return Tab{}, err
		}
		name = func([]string) string { return clean }
	}
	tab, err := s.repo.CreateTab(ctx, session.Registration, MaxTabs, name)
	if err != nil {
		if errors.Is(err, ErrTooManyTabs) {
			return Tab{}, err
		}
		return Tab{}, fmt.Errorf("create a tab: %w", err)
	}
	return tab, nil
}

// UpdateTab renames a tab, replaces its text, or both.
func (s *Service) UpdateTab(ctx context.Context, session Session, id uuid.UUID, patch TabPatch) (time.Time, error) {
	if patch.Title == nil && patch.Body == nil {
		return time.Time{}, ErrNothingToChange
	}
	if patch.Title != nil {
		clean, err := cleanTitle(*patch.Title)
		if err != nil {
			return time.Time{}, err
		}
		patch.Title = &clean
	}
	if patch.Body != nil {
		if err := checkText(*patch.Body); err != nil {
			return time.Time{}, err
		}
		if len(*patch.Body) > MaxTabBodyBytes {
			return time.Time{}, ErrTabBodyTooLong
		}
	}
	at, err := s.repo.UpdateTab(ctx, session.Registration, id, patch)
	if err != nil {
		if errors.Is(err, ErrTabNotFound) {
			return time.Time{}, err
		}
		return time.Time{}, fmt.Errorf("update a tab: %w", err)
	}
	return at, nil
}

// DeleteTab removes a tab, unless it is the last one.
func (s *Service) DeleteTab(ctx context.Context, session Session, id uuid.UUID) error {
	if err := s.repo.DeleteTab(ctx, session.Registration, id); err != nil {
		if errors.Is(err, ErrTabNotFound) || errors.Is(err, ErrLastTab) {
			return err
		}
		return fmt.Errorf("delete a tab: %w", err)
	}
	return nil
}

// ReorderTabs puts the tabs in the order ids names them. ids must be the
// workspace's tabs, each exactly once.
func (s *Service) ReorderTabs(ctx context.Context, session Session, ids []uuid.UUID) error {
	// The shape is checked here, before a transaction is opened for it; the
	// set itself can only be compared under the repository's lock.
	if len(ids) == 0 || len(ids) > MaxTabs {
		return ErrOrderMismatch
	}
	seen := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			return ErrOrderMismatch
		}
		seen[id] = struct{}{}
	}
	if err := s.repo.ReorderTabs(ctx, session.Registration, ids); err != nil {
		if errors.Is(err, ErrOrderMismatch) {
			return err
		}
		return fmt.Errorf("reorder the tabs: %w", err)
	}
	return nil
}

// AdmitWrite spends one write of the account's budget, and refuses with
// ErrTooOften once the budget for this minute is spent. The caller asks it
// before anything else about a write — before the participant and the
// contest are looked up, before the body is read — so every attempt is
// counted, a refused one included (CLAUDE.md rule 13).
//
// Its own budget, not the read budget queryproxy.Service.AdmitRead spends:
// that one is shared with the SQL console, and autosave during continuous
// typing would otherwise take the participant's queries away from them.
//
// Keyed by the account rather than the registration, because the account is
// what is known before those lookups; it is just as bounded — one key per
// account, assigned at sign-in and never named by the request (CLAUDE.md
// rule 5). A person taking part in two running contests at once would share
// one budget between them, which is not a case an olympiad has.
func (s *Service) AdmitWrite(ctx context.Context, account uuid.UUID) error {
	allowed, err := s.limiter.Allow(ctx, "workspace:user:"+account.String(), WritesPerMinute, writeWindow)
	if err != nil {
		// A counter that cannot be kept refuses: writing unthrottled is what
		// this exists to prevent. Not ErrTooOften — nobody asked too often.
		return fmt.Errorf("check the workspace write rate: %w", err)
	}
	if !allowed {
		return ErrTooOften
	}
	return nil
}

// RetryAfter is how long a caller refused with ErrTooOften should wait at
// most: the whole window, since the window's start is not known here.
func RetryAfter() time.Duration { return writeWindow }

// checkText refuses what PostgreSQL's text type cannot store: a NUL, which
// JSON can carry, and bytes that are not UTF-8.
func checkText(text string) error {
	if strings.ContainsRune(text, 0) || !utf8.ValidString(text) {
		return ErrTextInvalid
	}
	return nil
}

// cleanTitle trims a title and checks it against the title rules.
func cleanTitle(title string) (string, error) {
	if !utf8.ValidString(title) {
		return "", ErrTitleInvalid
	}
	clean := strings.TrimSpace(title)
	length := utf8.RuneCountInString(clean)
	if length < 1 || length > MaxTitleRunes {
		return "", ErrTitleInvalid
	}
	if strings.ContainsFunc(clean, unicode.IsControl) {
		return "", ErrTitleInvalid
	}
	return clean, nil
}

// titleWords is the word a server-named tab is named with, per language the
// platform serves. Stored data, not interface text: the tab keeps the name
// it was given in whatever language the screen later shows.
var titleWords = map[string]string{
	"en": "Query",
	"ru": "Запрос",
	"ro": "Interogare",
}

// defaultTitle is the name of the n-th server-named tab in lang, in English
// for a language the platform has no word for.
func defaultTitle(lang string, n int) string {
	word, ok := titleWords[lang]
	if !ok {
		word = titleWords["en"]
	}
	return word + " " + strconv.Itoa(n)
}

// freeTitle is the default title with the smallest number no title in taken
// already uses. At most MaxTabs titles are taken, so the loop ends within
// MaxTabs+1 steps.
func freeTitle(lang string, taken []string) string {
	for n := 1; ; n++ {
		candidate := defaultTitle(lang, n)
		if !slices.Contains(taken, candidate) {
			return candidate
		}
	}
}
