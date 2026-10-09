// Package workspace keeps a participant's notes and SQL editor tabs per
// registration, so a reload or another computer loses nothing. It owns the
// limits and the tab rules: at least one tab, untitled tabs named after the
// smallest free number, a new order naming every tab once.
//
// It does not decide whether the participant may read or write (queryproxy's
// admission does) and never runs a tab's SQL. It is not private: organisers
// see it and its history, which the repository records with every write.
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

// The limits of one workspace (CLAUDE.md rule 2).
const (
	// MaxNotesRunes counts characters, not bytes, so Cyrillic gets the same
	// room as Latin.
	MaxNotesRunes = 20000
	MaxTabs       = 10
	// MaxTitleRunes applies after trimming.
	MaxTitleRunes = 40
	// MaxTabBodyBytes matches what the console accepts, so any tab can run as
	// it stands.
	MaxTabBodyBytes = sqlpolicy.MaxQueryBytes
	// WritesPerMinute counts notes and tabs together, refused writes included.
	// Autosave after a 1.5 s pause stays under forty even while typing.
	WritesPerMinute = 60
)

const writeWindow = time.Minute

// Why a workspace call was refused (CLAUDE.md rule 1).
var (
	ErrNotesTooLong    = fmt.Errorf("the notes are longer than %d characters", MaxNotesRunes)
	ErrTooManyTabs     = fmt.Errorf("a participant may keep at most %d tabs", MaxTabs)
	ErrTitleInvalid    = fmt.Errorf("a tab title is 1 to %d characters with no control characters", MaxTitleRunes)
	ErrTabBodyTooLong  = fmt.Errorf("a tab holds at most %d bytes of SQL", MaxTabBodyBytes)
	ErrTextInvalid     = errors.New("the text contains a NUL character or is not valid UTF-8")
	ErrNothingToChange = errors.New("the change names neither a title nor a body")
	// ErrTabNotFound is also the answer for another participant's tab.
	ErrTabNotFound   = errors.New("no such tab in this workspace")
	ErrLastTab       = errors.New("the last tab cannot be deleted")
	ErrOrderMismatch = errors.New("the order must name every tab exactly once")
	ErrTooOften      = errors.New("too many workspace writes this minute")
)

// Notes is the participant's free text. UpdatedAt is nil until first saved.
type Notes struct {
	Body      string
	UpdatedAt *time.Time
}

// Tab is one SQL editor tab. Position starts at zero.
type Tab struct {
	ID        uuid.UUID
	Title     string
	Body      string
	Position  int
	UpdatedAt time.Time
}

// TabPatch is a change to one tab; a nil field is left as it is.
type TabPatch struct {
	Title *string
	Body  *string
}

type Workspace struct {
	Notes Notes
	// Tabs are in position order and never empty.
	Tabs []Tab
}

// Session is whose workspace a call works in, after admission, and the
// language for server-named tabs.
type Session struct {
	Registration uuid.UUID
	Lang         string
}

// Repository is the storage this service needs.
type Repository interface {
	// Load returns the workspace, first creating a tab titled firstTitle if it
	// has none. Two concurrent first loads create one tab, not two.
	Load(ctx context.Context, registration uuid.UUID, firstTitle string) (Notes, []Tab, error)
	// SaveNotes replaces the notes and returns when they were saved, recording
	// the save in the notes' revisions in the same transaction.
	SaveNotes(ctx context.Context, registration uuid.UUID, body string) (time.Time, error)
	// CreateTab appends a tab, refusing with ErrTooManyTabs at limit. title
	// is called with the taken titles under the lock that counts them. The
	// creation is recorded as a tab_created event in the same transaction, as
	// is the first tab Load creates.
	CreateTab(ctx context.Context, registration uuid.UUID, limit int, title func(taken []string) string) (Tab, error)
	// UpdateTab applies patch, or returns ErrTabNotFound for a tab that is not
	// this registration's. A new title is recorded as a tab_renamed event and
	// new text as a revision, in the same transaction.
	UpdateTab(ctx context.Context, registration, id uuid.UUID, patch TabPatch) (time.Time, error)
	// DeleteTab removes a tab and closes the gap in positions, recording a
	// tab_deleted event; the tab's revisions stay. ErrTabNotFound for another
	// registration's tab, ErrLastTab for the only one.
	DeleteTab(ctx context.Context, registration, id uuid.UUID) error
	// ReorderTabs gives each tab its index in ids as its position, or refuses
	// with ErrOrderMismatch when ids is not exactly the workspace's tabs.
	ReorderTabs(ctx context.Context, registration uuid.UUID, ids []uuid.UUID) error
}

// Limiter is the slice of auth.Limiter the write throttle needs.
type Limiter interface {
	Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error)
}

// Service applies the workspace's rules over a Repository.
//
// Every write method assumes the caller already spent the write through
// AdmitWrite, which must run before the lookups that produce a Session.
type Service struct {
	repo    Repository
	limiter Limiter
}

func NewService(repo Repository, limiter Limiter) *Service {
	return &Service{repo: repo, limiter: limiter}
}

// Get returns the participant's workspace, creating the first tab if there is
// none. It is not throttled: read admission already charged for it, and a
// reload must not spend the budget autosave needs.
func (s *Service) Get(ctx context.Context, session Session) (Workspace, error) {
	notes, tabs, err := s.repo.Load(ctx, session.Registration, defaultTitle(session.Lang, 1))
	if err != nil {
		return Workspace{}, fmt.Errorf("load the workspace: %w", err)
	}
	return Workspace{Notes: notes, Tabs: tabs}, nil
}

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

// CreateTab appends an empty tab. A nil title names it after the smallest free
// number.
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

func (s *Service) DeleteTab(ctx context.Context, session Session, id uuid.UUID) error {
	if err := s.repo.DeleteTab(ctx, session.Registration, id); err != nil {
		if errors.Is(err, ErrTabNotFound) || errors.Is(err, ErrLastTab) {
			return err
		}
		return fmt.Errorf("delete a tab: %w", err)
	}
	return nil
}

// ReorderTabs puts the tabs in the order ids names them.
func (s *Service) ReorderTabs(ctx context.Context, session Session, ids []uuid.UUID) error {
	// The shape is checked before a transaction opens; the set itself can only
	// be compared under the repository's lock.
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

// AdmitWrite spends one write of the account's budget, refusing with
// ErrTooOften once this minute's budget is spent. The caller asks it before
// any lookup or body read, so every attempt counts (CLAUDE.md rule 13).
//
// It is a budget separate from the read budget, which the SQL console shares,
// so autosave cannot starve the participant's queries. It is keyed by the
// account, known before the lookups and never named by the request (CLAUDE.md
// rule 5).
func (s *Service) AdmitWrite(ctx context.Context, account uuid.UUID) error {
	allowed, err := s.limiter.Allow(ctx, "workspace:user:"+account.String(), WritesPerMinute, writeWindow)
	if err != nil {
		// A counter that cannot be kept refuses, but not with ErrTooOften.
		return fmt.Errorf("check the workspace write rate: %w", err)
	}
	if !allowed {
		return ErrTooOften
	}
	return nil
}

// RetryAfter is the longest a caller refused with ErrTooOften should wait:
// the whole window, since its start is not known here.
func RetryAfter() time.Duration { return writeWindow }

// checkText refuses what PostgreSQL's text type cannot store: a NUL, which
// JSON can carry, and bytes that are not UTF-8.
func checkText(text string) error {
	if strings.ContainsRune(text, 0) || !utf8.ValidString(text) {
		return ErrTextInvalid
	}
	return nil
}

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

// titleWords names server-named tabs per language. It is stored data, not
// interface text: the tab keeps its name whatever language is shown later.
var titleWords = map[string]string{
	"en": "Query",
	"ru": "Запрос",
	"ro": "Interogare",
}

// defaultTitle names the n-th server-named tab in lang, falling back to
// English.
func defaultTitle(lang string, n int) string {
	word, ok := titleWords[lang]
	if !ok {
		word = titleWords["en"]
	}
	return word + " " + strconv.Itoa(n)
}

// freeTitle is the default title with the smallest number not in taken. At
// most MaxTabs titles are taken, so the loop ends within MaxTabs+1 steps.
func freeTitle(lang string, taken []string) string {
	for n := 1; ; n++ {
		candidate := defaultTitle(lang, n)
		if !slices.Contains(taken, candidate) {
			return candidate
		}
	}
}
