// Package workspacetest holds an in-memory workspace.Repository for the tests
// of internal/workspace and of the HTTP layer that serves it. It is not a
// second implementation of the storage rules for production use.
package workspacetest

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/devrdn/db-contest/backend/internal/workspace"
	"github.com/google/uuid"
)

var _ workspace.Repository = (*Repository)(nil)

// Repository is workspace.Repository in memory: enough of the contract (one
// workspace per registration, tabs scoped to it, the tab limit checked under
// the same lock that assigns positions) to test the service's own rules and
// the HTTP layer's mapping of them. The SQL that keeps the same contract is
// proven in internal/postgres against a real database.
type Repository struct {
	mu    sync.Mutex
	notes map[uuid.UUID]workspace.Notes
	tabs  map[uuid.UUID][]workspace.Tab
	now   time.Time
	// calls counts every repository call, so a test can prove a refusal
	// happened before any storage work.
	calls int
}

// NewRepository returns an empty in-memory workspace store.
func NewRepository() *Repository {
	return &Repository{
		notes: map[uuid.UUID]workspace.Notes{},
		tabs:  map[uuid.UUID][]workspace.Tab{},
		now:   time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC),
	}
}

// Load implements workspace.Repository.
func (m *Repository) Load(_ context.Context, registration uuid.UUID, firstTitle string) (workspace.Notes, []workspace.Tab, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if len(m.tabs[registration]) == 0 {
		m.tabs[registration] = []workspace.Tab{{ID: uuid.New(), Title: firstTitle, UpdatedAt: m.now}}
	}
	return m.notes[registration], slices.Clone(m.tabs[registration]), nil
}

// SaveNotes implements workspace.Repository.
func (m *Repository) SaveNotes(_ context.Context, registration uuid.UUID, body string) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	at := m.now
	m.notes[registration] = workspace.Notes{Body: body, UpdatedAt: &at}
	return at, nil
}

// CreateTab implements workspace.Repository.
func (m *Repository) CreateTab(_ context.Context, registration uuid.UUID, limit int, title func(taken []string) string) (workspace.Tab, error) {
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

// UpdateTab implements workspace.Repository.
func (m *Repository) UpdateTab(_ context.Context, registration, id uuid.UUID, patch workspace.TabPatch) (time.Time, error) {
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

// DeleteTab implements workspace.Repository.
func (m *Repository) DeleteTab(_ context.Context, registration, id uuid.UUID) error {
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
	tabs = slices.Delete(tabs, at, at+1)
	for i := range tabs {
		tabs[i].Position = i
	}
	m.tabs[registration] = tabs
	return nil
}

// ReorderTabs implements workspace.Repository.
func (m *Repository) ReorderTabs(_ context.Context, registration uuid.UUID, ids []uuid.UUID) error {
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

// Calls is how many times any method was called, so a test can prove a
// refusal came before any storage work.
func (m *Repository) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}
