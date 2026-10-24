// Package clarificationstest holds an in-memory clarifications.Repository,
// the repository contract every implementation must keep, and a fixture that
// builds the real service over fakes, for the tests of internal/clarifications
// and the HTTP layer. It is not for production use.
package clarificationstest

import (
	"cmp"
	"context"
	"slices"
	"sync"

	"github.com/devrdn/db-contest/backend/internal/clarifications"
	"github.com/google/uuid"
)

var _ clarifications.Repository = (*Repository)(nil)

// Repository is clarifications.Repository in memory. The registrations,
// questions and accounts it checks against are added with Register,
// AddQuestion and AddAccount, as the real schema would hold them.
type Repository struct {
	mu            sync.Mutex
	clarifs       []clarifications.Clarification
	announcements []clarifications.Announcement
	registrations map[uuid.UUID]registration
	questions     map[uuid.UUID]uuid.UUID
	accounts      map[uuid.UUID]string
}

type registration struct {
	contest         uuid.UUID
	login, fullName string
}

func NewRepository() *Repository {
	return &Repository{
		registrations: map[uuid.UUID]registration{},
		questions:     map[uuid.UUID]uuid.UUID{},
		accounts:      map[uuid.UUID]string{},
	}
}

// Register adds a registration of contest held by an account with this login
// and name.
func (m *Repository) Register(contest, id uuid.UUID, login, fullName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registrations[id] = registration{contest: contest, login: login, fullName: fullName}
}

// AddQuestion adds a question of contest.
func (m *Repository) AddQuestion(contest, id uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.questions[id] = contest
}

// AddAccount adds a staff account, so the queue can name who answered.
func (m *Repository) AddAccount(id uuid.UUID, login string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accounts[id] = login
}

func (m *Repository) Ask(_ context.Context, c clarifications.Clarification, openLimit int) (clarifications.Clarification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.QuestionID != nil && m.questions[*c.QuestionID] != c.ContestID {
		return clarifications.Clarification{}, clarifications.ErrQuestionNotFound
	}
	open := 0
	for _, existing := range m.clarifs {
		if existing.RegistrationID == c.RegistrationID && existing.Answer == nil {
			open++
		}
	}
	if open >= openLimit {
		return clarifications.Clarification{}, clarifications.ErrTooManyOpen
	}
	c.ID = uuid.New()
	m.clarifs = append(m.clarifs, c)
	return c, nil
}

func (m *Repository) Feed(_ context.Context, contestID, registrationID uuid.UUID, before clarifications.Cursor, limit int) ([]clarifications.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var entries []clarifications.Entry
	for _, c := range m.clarifs {
		if c.ContestID != contestID || (c.RegistrationID != registrationID && c.Visibility() != clarifications.Public) {
			continue
		}
		if older(c.PostedAt().UnixMicro(), c.ID, before) {
			copied := c
			entries = append(entries, clarifications.Entry{Clarification: &copied})
		}
	}
	for _, a := range m.announcements {
		if a.ContestID == contestID && older(a.CreatedAt.UnixMicro(), a.ID, before) {
			copied := a
			entries = append(entries, clarifications.Entry{Announcement: &copied})
		}
	}
	slices.SortFunc(entries, func(a, b clarifications.Entry) int {
		at, aid := position(a)
		bt, bid := position(b)
		return -cmp.Or(cmp.Compare(at, bt), compareIDs(aid, bid))
	})
	return entries[:min(limit, len(entries))], nil
}

func position(e clarifications.Entry) (int64, uuid.UUID) {
	if e.Announcement != nil {
		return e.Announcement.CreatedAt.UnixMicro(), e.Announcement.ID
	}
	return e.Clarification.PostedAt().UnixMicro(), e.Clarification.ID
}

// older reports whether (at, id) sorts strictly before the cursor, as
// PostgreSQL compares (timestamptz, uuid) rows.
func older(at int64, id uuid.UUID, before clarifications.Cursor) bool {
	return cmp.Or(cmp.Compare(at, before.At.UnixMicro()), compareIDs(id, before.ID)) < 0
}

// compareIDs orders uuids by their bytes, as PostgreSQL does.
func compareIDs(a, b uuid.UUID) int {
	return slices.Compare(a[:], b[:])
}

func (m *Repository) OpenCount(_ context.Context, registrationID uuid.UUID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	open := 0
	for _, c := range m.clarifs {
		if c.RegistrationID == registrationID && c.Answer == nil {
			open++
		}
	}
	return open, nil
}

func (m *Repository) Queue(_ context.Context, contestID uuid.UUID, state clarifications.State, limit, offset int) ([]clarifications.Asked, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found []clarifications.Asked
	for _, c := range m.clarifs {
		if c.ContestID != contestID || (c.Answer == nil) != (state == clarifications.StateOpen) {
			continue
		}
		reg := m.registrations[c.RegistrationID]
		asked := clarifications.Asked{Clarification: c, AskerLogin: reg.login, AskerFullName: reg.fullName}
		if c.Answer != nil && c.Answer.By != nil {
			asked.AnsweredByLogin = m.accounts[*c.Answer.By]
		}
		found = append(found, asked)
	}
	slices.SortFunc(found, func(a, b clarifications.Asked) int {
		if state == clarifications.StateOpen {
			return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), compareIDs(a.ID, b.ID))
		}
		return -cmp.Or(a.Answer.At.Compare(b.Answer.At), compareIDs(a.ID, b.ID))
	})
	if offset >= len(found) {
		return nil, nil
	}
	return found[offset:min(offset+limit, len(found))], nil
}

func (m *Repository) Counts(_ context.Context, contestID uuid.UUID) (clarifications.Counts, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var counts clarifications.Counts
	for _, c := range m.clarifs {
		switch {
		case c.ContestID != contestID:
		case c.Answer == nil:
			counts.Open++
		default:
			counts.Answered++
		}
	}
	return counts, nil
}

func (m *Repository) find(contestID, id uuid.UUID) int {
	return slices.IndexFunc(m.clarifs, func(c clarifications.Clarification) bool {
		return c.ID == id && c.ContestID == contestID
	})
}

func (m *Repository) Answer(_ context.Context, contestID, id uuid.UUID, answer clarifications.Answer) (clarifications.Clarification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.find(contestID, id)
	switch {
	case i < 0:
		return clarifications.Clarification{}, clarifications.ErrNotFound
	case m.clarifs[i].Answer != nil:
		return clarifications.Clarification{}, clarifications.ErrAlreadyAnswered
	}
	answer.EditedAt = nil
	m.clarifs[i].Answer = &answer
	return m.clarifs[i], nil
}

func (m *Repository) EditAnswer(_ context.Context, contestID, id uuid.UUID, answer clarifications.Answer) (clarifications.Clarification, clarifications.Clarification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.find(contestID, id)
	switch {
	case i < 0:
		return clarifications.Clarification{}, clarifications.Clarification{}, clarifications.ErrNotFound
	case m.clarifs[i].Answer == nil:
		return clarifications.Clarification{}, clarifications.Clarification{}, clarifications.ErrNotAnswered
	}
	before := m.clarifs[i]
	previous := *before.Answer
	before.Answer = &previous
	edited := answer.At
	m.clarifs[i].Answer = &clarifications.Answer{
		Body: answer.Body, Declined: answer.Declined, Visibility: answer.Visibility,
		By: previous.By, At: previous.At, EditedAt: &edited,
	}
	return before, m.clarifs[i], nil
}

func (m *Repository) Announce(_ context.Context, a clarifications.Announcement) (clarifications.Announcement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a.ID = uuid.New()
	m.announcements = append(m.announcements, a)
	return a, nil
}

func (m *Repository) Announcements(_ context.Context, contestID uuid.UUID, limit, offset int) ([]clarifications.Announcement, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found []clarifications.Announcement
	for _, a := range m.announcements {
		if a.ContestID == contestID {
			found = append(found, a)
		}
	}
	slices.SortFunc(found, func(a, b clarifications.Announcement) int {
		return -cmp.Or(a.CreatedAt.Compare(b.CreatedAt), compareIDs(a.ID, b.ID))
	})
	if offset >= len(found) {
		return nil, len(found), nil
	}
	return found[offset:min(offset+limit, len(found))], len(found), nil
}
