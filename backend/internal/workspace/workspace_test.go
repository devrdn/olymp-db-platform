package workspace_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/workspace"
	"github.com/google/uuid"
)

// memoryRepository is the service's storage in memory: enough of the
// repository's contract (one workspace per registration, tabs scoped to it,
// the tab limit checked under the same lock that assigns positions) to test
// the service's own rules. The SQL that keeps the same contract is proven in
// internal/postgres against a real database.
type memoryRepository struct {
	mu    sync.Mutex
	notes map[uuid.UUID]workspace.Notes
	tabs  map[uuid.UUID][]workspace.Tab
	now   time.Time
	// calls counts every repository call, so a test can prove a refusal
	// happened before any storage work.
	calls int
	// firstTitles records the title Load was asked to give a first tab.
	firstTitles []string
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{
		notes: map[uuid.UUID]workspace.Notes{},
		tabs:  map[uuid.UUID][]workspace.Tab{},
		now:   time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC),
	}
}

func (m *memoryRepository) Load(_ context.Context, registration uuid.UUID, firstTitle string) (workspace.Notes, []workspace.Tab, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.firstTitles = append(m.firstTitles, firstTitle)
	if len(m.tabs[registration]) == 0 {
		m.tabs[registration] = []workspace.Tab{{ID: uuid.New(), Title: firstTitle, UpdatedAt: m.now}}
	}
	return m.notes[registration], slices.Clone(m.tabs[registration]), nil
}

func (m *memoryRepository) SaveNotes(_ context.Context, registration uuid.UUID, body string) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	at := m.now
	m.notes[registration] = workspace.Notes{Body: body, UpdatedAt: &at}
	return at, nil
}

func (m *memoryRepository) CreateTab(_ context.Context, registration uuid.UUID, limit int, title func(taken []string) string) (workspace.Tab, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	existing := m.tabs[registration]
	if len(existing) >= limit {
		return workspace.Tab{}, workspace.ErrTooManyTabs
	}
	taken := make([]string, 0, len(existing))
	for _, tab := range existing {
		taken = append(taken, tab.Title)
	}
	tab := workspace.Tab{ID: uuid.New(), Title: title(taken), Position: len(existing), UpdatedAt: m.now}
	m.tabs[registration] = append(existing, tab)
	return tab, nil
}

func (m *memoryRepository) UpdateTab(_ context.Context, registration, id uuid.UUID, patch workspace.TabPatch) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	for i, tab := range m.tabs[registration] {
		if tab.ID != id {
			continue
		}
		if patch.Title != nil {
			tab.Title = *patch.Title
		}
		if patch.Body != nil {
			tab.Body = *patch.Body
		}
		m.tabs[registration][i] = tab
		return m.now, nil
	}
	return time.Time{}, workspace.ErrTabNotFound
}

func (m *memoryRepository) DeleteTab(_ context.Context, registration, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	tabs := m.tabs[registration]
	at := slices.IndexFunc(tabs, func(tab workspace.Tab) bool { return tab.ID == id })
	if at < 0 {
		return workspace.ErrTabNotFound
	}
	if len(tabs) == 1 {
		return workspace.ErrLastTab
	}
	m.tabs[registration] = slices.Delete(tabs, at, at+1)
	return nil
}

func (m *memoryRepository) ReorderTabs(_ context.Context, registration uuid.UUID, ids []uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	tabs := m.tabs[registration]
	if len(tabs) != len(ids) {
		return workspace.ErrOrderMismatch
	}
	ordered := make([]workspace.Tab, 0, len(ids))
	for i, id := range ids {
		at := slices.IndexFunc(tabs, func(tab workspace.Tab) bool { return tab.ID == id })
		if at < 0 {
			return workspace.ErrOrderMismatch
		}
		tab := tabs[at]
		tab.Position = i
		ordered = append(ordered, tab)
	}
	m.tabs[registration] = ordered
	return nil
}

func (m *memoryRepository) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// fixture is a service over the in-memory repository and the real
// fixed-window limiter over an in-process cache, so what is counted is what
// the deployment counts.
type fixture struct {
	repo    *memoryRepository
	service *workspace.Service
	session workspace.Session
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	repo := newMemoryRepository()
	return &fixture{
		repo:    repo,
		service: workspace.NewService(repo, auth.NewLimiter(c)),
		session: workspace.Session{Registration: uuid.New(), Writable: true, Lang: "en"},
	}
}

// load reads the workspace, which also creates the first tab.
func (f *fixture) load(t *testing.T) workspace.Workspace {
	t.Helper()
	got, err := f.service.Get(t.Context(), f.session)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	return got
}

func ptr(s string) *string { return &s }

func TestTheFirstReadGivesTheWorkspaceATabTitledInTheRequestsLanguage(t *testing.T) {
	for lang, want := range map[string]string{
		"en": "Query 1",
		"ru": "Запрос 1",
		"ro": "Interogare 1",
		// A language the platform has no word for falls back to English
		// rather than to an empty title the validator would refuse.
		"de": "Query 1",
	} {
		t.Run(lang, func(t *testing.T) {
			f := newFixture(t)
			f.session.Lang = lang

			got := f.load(t)
			if len(got.Tabs) != 1 || got.Tabs[0].Title != want {
				t.Fatalf("tabs = %+v, want one titled %q", got.Tabs, want)
			}
		})
	}
}

func TestReadOnlyFollowsTheSession(t *testing.T) {
	f := newFixture(t)
	if f.load(t).ReadOnly {
		t.Fatal("a writable session was reported read-only")
	}
	f.session.Writable = false
	if !f.load(t).ReadOnly {
		t.Fatal("a session that may not write was not reported read-only")
	}
}

func TestNotesAreBoundedInCharactersNotBytes(t *testing.T) {
	f := newFixture(t)

	// Exactly the limit, in a script where a character is two bytes: taken.
	atLimit := strings.Repeat("ж", workspace.MaxNotesRunes)
	if _, err := f.service.SaveNotes(t.Context(), f.session, atLimit); err != nil {
		t.Fatalf("SaveNotes(at the limit) = %v", err)
	}
	if _, err := f.service.SaveNotes(t.Context(), f.session, atLimit+"ж"); !errors.Is(err, workspace.ErrNotesTooLong) {
		t.Fatalf("SaveNotes(one past the limit) = %v, want ErrNotesTooLong", err)
	}
}

func TestATabBodyIsBoundedLikeAQuery(t *testing.T) {
	f := newFixture(t)
	tab := f.load(t).Tabs[0]

	atLimit := strings.Repeat("x", sqlpolicy.MaxQueryBytes)
	if _, err := f.service.UpdateTab(t.Context(), f.session, tab.ID, workspace.TabPatch{Body: &atLimit}); err != nil {
		t.Fatalf("UpdateTab(at the limit) = %v", err)
	}
	over := atLimit + "x"
	if _, err := f.service.UpdateTab(t.Context(), f.session, tab.ID, workspace.TabPatch{Body: &over}); !errors.Is(err, workspace.ErrTabBodyTooLong) {
		t.Fatalf("UpdateTab(one byte past) = %v, want ErrTabBodyTooLong", err)
	}
}

// PostgreSQL's text cannot hold a NUL, which JSON can carry as an escaped U+0000: stored
// unchecked it is a 500 rather than the caller's own mistake.
func TestTextWithANulCharacterIsRefused(t *testing.T) {
	f := newFixture(t)
	tab := f.load(t).Tabs[0]

	if _, err := f.service.SaveNotes(t.Context(), f.session, "a\x00b"); !errors.Is(err, workspace.ErrTextInvalid) {
		t.Fatalf("SaveNotes(NUL) = %v, want ErrTextInvalid", err)
	}
	body := "SELECT 1\x00"
	if _, err := f.service.UpdateTab(t.Context(), f.session, tab.ID, workspace.TabPatch{Body: &body}); !errors.Is(err, workspace.ErrTextInvalid) {
		t.Fatalf("UpdateTab(NUL) = %v, want ErrTextInvalid", err)
	}
}

func TestTitlesAreTrimmedAndBounded(t *testing.T) {
	f := newFixture(t)
	tab := f.load(t).Tabs[0]

	for _, tc := range []struct {
		name  string
		title string
		ok    bool
	}{
		{"one character", "a", true},
		{"forty characters", strings.Repeat("ж", workspace.MaxTitleRunes), true},
		{"forty characters inside spaces", "  " + strings.Repeat("ж", workspace.MaxTitleRunes) + "  ", true},
		{"forty-one characters", strings.Repeat("ж", workspace.MaxTitleRunes+1), false},
		{"empty", "", false},
		{"only spaces", "   ", false},
		{"a newline", "two\nlines", false},
		{"a tab character", "a\tb", false},
		{"a NUL", "a\x00b", false},
		{"a C1 control", "a\u0085b", false},
		{"invalid UTF-8", "a\xffb", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			title := tc.title
			_, err := f.service.UpdateTab(t.Context(), f.session, tab.ID, workspace.TabPatch{Title: &title})
			if tc.ok && err != nil {
				t.Fatalf("UpdateTab(%q) = %v, want it accepted", tc.title, err)
			}
			if !tc.ok && !errors.Is(err, workspace.ErrTitleInvalid) {
				t.Fatalf("UpdateTab(%q) = %v, want ErrTitleInvalid", tc.title, err)
			}
		})
	}

	// What is stored is the trimmed title.
	if _, err := f.service.UpdateTab(t.Context(), f.session, tab.ID, workspace.TabPatch{Title: ptr("  joins  ")}); err != nil {
		t.Fatalf("UpdateTab() = %v", err)
	}
	if got := f.load(t).Tabs[0].Title; got != "joins" {
		t.Fatalf("stored title = %q, want %q", got, "joins")
	}
}

func TestAnEmptyPatchIsRefused(t *testing.T) {
	f := newFixture(t)
	tab := f.load(t).Tabs[0]
	if _, err := f.service.UpdateTab(t.Context(), f.session, tab.ID, workspace.TabPatch{}); !errors.Is(err, workspace.ErrNothingToChange) {
		t.Fatalf("UpdateTab({}) = %v, want ErrNothingToChange", err)
	}
}

func TestANewTabWithoutATitleTakesTheSmallestFreeNumber(t *testing.T) {
	f := newFixture(t)
	f.session.Lang = "ru"
	first := f.load(t).Tabs[0] // "Запрос 1"

	second, err := f.service.CreateTab(t.Context(), f.session, nil)
	if err != nil || second.Title != "Запрос 2" {
		t.Fatalf("CreateTab() = %+v, %v; want Запрос 2", second, err)
	}
	// Renaming the first frees its number, and the next tab takes it.
	if _, err := f.service.UpdateTab(t.Context(), f.session, first.ID, workspace.TabPatch{Title: ptr("joins")}); err != nil {
		t.Fatalf("UpdateTab() = %v", err)
	}
	third, err := f.service.CreateTab(t.Context(), f.session, nil)
	if err != nil || third.Title != "Запрос 1" {
		t.Fatalf("CreateTab() = %+v, %v; want Запрос 1", third, err)
	}
}

func TestANewTabTakesItsOwnTitleValidated(t *testing.T) {
	f := newFixture(t)
	f.load(t)

	tab, err := f.service.CreateTab(t.Context(), f.session, ptr("  suspects "))
	if err != nil || tab.Title != "suspects" {
		t.Fatalf("CreateTab(title) = %+v, %v; want suspects", tab, err)
	}
	if _, err := f.service.CreateTab(t.Context(), f.session, ptr(" ")); !errors.Is(err, workspace.ErrTitleInvalid) {
		t.Fatalf("CreateTab(blank) = %v, want ErrTitleInvalid", err)
	}
}

func TestTheEleventhTabIsRefused(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	for i := 1; i < workspace.MaxTabs; i++ {
		if _, err := f.service.CreateTab(t.Context(), f.session, nil); err != nil {
			t.Fatalf("CreateTab() #%d = %v", i+1, err)
		}
	}
	if _, err := f.service.CreateTab(t.Context(), f.session, nil); !errors.Is(err, workspace.ErrTooManyTabs) {
		t.Fatalf("CreateTab() #%d = %v, want ErrTooManyTabs", workspace.MaxTabs+1, err)
	}
}

func TestTheLastTabCannotBeDeleted(t *testing.T) {
	f := newFixture(t)
	first := f.load(t).Tabs[0]
	second, err := f.service.CreateTab(t.Context(), f.session, nil)
	if err != nil {
		t.Fatalf("CreateTab() = %v", err)
	}

	if err := f.service.DeleteTab(t.Context(), f.session, first.ID); err != nil {
		t.Fatalf("DeleteTab(one of two) = %v", err)
	}
	if err := f.service.DeleteTab(t.Context(), f.session, second.ID); !errors.Is(err, workspace.ErrLastTab) {
		t.Fatalf("DeleteTab(the last) = %v, want ErrLastTab", err)
	}
}

func TestAnUnknownTabIsNotFound(t *testing.T) {
	f := newFixture(t)
	f.load(t)
	if _, err := f.service.UpdateTab(t.Context(), f.session, uuid.New(), workspace.TabPatch{Body: ptr("x")}); !errors.Is(err, workspace.ErrTabNotFound) {
		t.Fatalf("UpdateTab(unknown) = %v, want ErrTabNotFound", err)
	}
	if err := f.service.DeleteTab(t.Context(), f.session, uuid.New()); !errors.Is(err, workspace.ErrTabNotFound) {
		t.Fatalf("DeleteTab(unknown) = %v, want ErrTabNotFound", err)
	}
}

func TestReorderingMustNameEveryTabExactlyOnce(t *testing.T) {
	f := newFixture(t)
	first := f.load(t).Tabs[0]
	second, err := f.service.CreateTab(t.Context(), f.session, nil)
	if err != nil {
		t.Fatalf("CreateTab() = %v", err)
	}

	tooMany := make([]uuid.UUID, workspace.MaxTabs+1)
	for i := range tooMany {
		tooMany[i] = uuid.New()
	}
	for name, ids := range map[string][]uuid.UUID{
		"empty":            nil,
		"one missing":      {first.ID},
		"a duplicate":      {first.ID, first.ID},
		"a stranger":       {first.ID, uuid.New()},
		"one extra":        {first.ID, second.ID, uuid.New()},
		"past the maximum": tooMany,
	} {
		t.Run(name, func(t *testing.T) {
			if err := f.service.ReorderTabs(t.Context(), f.session, ids); !errors.Is(err, workspace.ErrOrderMismatch) {
				t.Fatalf("ReorderTabs(%s) = %v, want ErrOrderMismatch", name, err)
			}
		})
	}

	if err := f.service.ReorderTabs(t.Context(), f.session, []uuid.UUID{second.ID, first.ID}); err != nil {
		t.Fatalf("ReorderTabs(swap) = %v", err)
	}
	got := f.load(t).Tabs
	if got[0].ID != second.ID || got[1].ID != first.ID {
		t.Fatalf("order after swap = %v, %v", got[0].ID, got[1].ID)
	}
}

// Every write is refused while the contest is not open for this participant,
// and refused before any storage work.
func TestAReadOnlySessionWritesNothing(t *testing.T) {
	f := newFixture(t)
	tab := f.load(t).Tabs[0]
	f.session.Writable = false
	before := f.repo.callCount()

	for name, write := range map[string]func() error{
		"notes":  func() error { _, err := f.service.SaveNotes(t.Context(), f.session, "x"); return err },
		"create": func() error { _, err := f.service.CreateTab(t.Context(), f.session, nil); return err },
		"update": func() error {
			_, err := f.service.UpdateTab(t.Context(), f.session, tab.ID, workspace.TabPatch{Body: ptr("x")})
			return err
		},
		"delete":  func() error { return f.service.DeleteTab(t.Context(), f.session, tab.ID) },
		"reorder": func() error { return f.service.ReorderTabs(t.Context(), f.session, []uuid.UUID{tab.ID}) },
	} {
		if err := write(); !errors.Is(err, workspace.ErrReadOnly) {
			t.Fatalf("%s: %v, want ErrReadOnly", name, err)
		}
	}
	if f.repo.callCount() != before {
		t.Fatalf("a read-only session reached the repository %d times", f.repo.callCount()-before)
	}
}

// Sixty writes a minute per registration, notes and tabs together, and a
// refused write — for any reason — spends the budget too (CLAUDE.md rule 13).
func TestWritesPastTheRateAreRefusedAndRefusalsCount(t *testing.T) {
	f := newFixture(t)
	f.load(t)

	// Half the budget on refusals: an invalid title and an overlong note are
	// refused, and still counted.
	for i := 0; i < workspace.WritesPerMinute/2; i++ {
		if _, err := f.service.CreateTab(t.Context(), f.session, ptr("")); !errors.Is(err, workspace.ErrTitleInvalid) {
			t.Fatalf("CreateTab(blank) = %v, want ErrTitleInvalid", err)
		}
	}
	for i := 0; i < workspace.WritesPerMinute/2; i++ {
		if _, err := f.service.SaveNotes(t.Context(), f.session, "note"); err != nil {
			t.Fatalf("SaveNotes() #%d = %v", i, err)
		}
	}
	before := f.repo.callCount()
	if _, err := f.service.SaveNotes(t.Context(), f.session, "note"); !errors.Is(err, workspace.ErrTooOften) {
		t.Fatalf("write #%d = %v, want ErrTooOften", workspace.WritesPerMinute+1, err)
	}
	if f.repo.callCount() != before {
		t.Fatal("a write refused for its rate still reached the repository")
	}

	// A read-only refusal counts as well: a closed contest is not a place to
	// spend requests for free.
	g := newFixture(t)
	g.session.Writable = false
	for i := 0; i < workspace.WritesPerMinute; i++ {
		if _, err := g.service.SaveNotes(t.Context(), g.session, "x"); !errors.Is(err, workspace.ErrReadOnly) {
			t.Fatalf("SaveNotes() = %v, want ErrReadOnly", err)
		}
	}
	if _, err := g.service.SaveNotes(t.Context(), g.session, "x"); !errors.Is(err, workspace.ErrTooOften) {
		t.Fatalf("SaveNotes() past the rate = %v, want ErrTooOften", err)
	}
}

func TestTheRateIsKeptPerRegistration(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < workspace.WritesPerMinute; i++ {
		if _, err := f.service.SaveNotes(t.Context(), f.session, "x"); err != nil {
			t.Fatalf("SaveNotes() = %v", err)
		}
	}
	other := f.session
	other.Registration = uuid.New()
	if _, err := f.service.SaveNotes(t.Context(), other, "x"); err != nil {
		t.Fatalf("another registration's write = %v, want it admitted", err)
	}
}

// Reading is not a write, and spends nothing of the write budget.
func TestReadingSpendsNoWriteBudget(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < workspace.WritesPerMinute+5; i++ {
		f.load(t)
	}
	if _, err := f.service.SaveNotes(t.Context(), f.session, "x"); err != nil {
		t.Fatalf("SaveNotes() after many reads = %v", err)
	}
}

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string, int, time.Duration) (bool, error) {
	return false, errors.New("cache unreachable")
}

// A counter that cannot be kept refuses (auth.Limiter's own rule), and the
// refusal is not a rate refusal: nobody asked too often.
func TestAWriteIsRefusedWhenItsRateCannotBeCounted(t *testing.T) {
	repo := newMemoryRepository()
	service := workspace.NewService(repo, failingLimiter{})
	_, err := service.SaveNotes(t.Context(), workspace.Session{Registration: uuid.New(), Writable: true}, "x")
	if err == nil || errors.Is(err, workspace.ErrTooOften) {
		t.Fatalf("SaveNotes() = %v, want an internal error", err)
	}
	if repo.callCount() != 0 {
		t.Fatal("a write whose rate could not be counted reached the repository")
	}
}
