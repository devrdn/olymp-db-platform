// Package userstest provides an in-memory users.Repository for tests, so
// account and authentication rules run against real behaviour without a
// database. It is not for production use.
package userstest

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

type Repository struct {
	mu          sync.Mutex
	byID        map[uuid.UUID]users.User
	permissions map[string][]string
	// Err, when set, is returned by every method.
	Err                      error
	CountActiveWithRoleCalls int
}

func New() *Repository {
	return &Repository{
		byID:        map[uuid.UUID]users.User{},
		permissions: map[string][]string{},
	}
}

func (r *Repository) GrantRole(roleCode string, permissions ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.permissions[roleCode] = permissions
}

func (r *Repository) Add(u users.User) users.User {
	r.mu.Lock()
	defer r.mu.Unlock()

	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	if u.Status == "" {
		u.Status = users.StatusActive
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	r.byID[u.ID] = u
	return u
}

func (r *Repository) Get(id uuid.UUID) (users.User, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.byID[id]
	return u, ok
}

func (r *Repository) ByLogin(_ context.Context, login string) (users.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return users.User{}, r.Err
	}
	// Prefer the live row explicitly; map order is undefined.
	for _, u := range r.byID {
		if u.Status != users.StatusDeleted && strings.EqualFold(u.Login, login) {
			return r.withPermissions(u), nil
		}
	}
	for _, u := range r.byID {
		if strings.EqualFold(u.Login, login) {
			return r.withPermissions(u), nil
		}
	}
	return users.User{}, users.ErrNotFound
}

func (r *Repository) ByID(_ context.Context, id uuid.UUID) (users.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return users.User{}, r.Err
	}
	u, ok := r.byID[id]
	if !ok {
		return users.User{}, users.ErrNotFound
	}
	return r.withPermissions(u), nil
}

// ByIDs mirrors ANY($1): a repeated id returns once, a missing one is absent.
func (r *Repository) ByIDs(_ context.Context, ids []uuid.UUID) ([]users.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return nil, r.Err
	}
	want := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var found []users.User
	for id := range want {
		if u, ok := r.byID[id]; ok {
			found = append(found, r.withPermissions(u))
		}
	}
	return found, nil
}

func (r *Repository) Create(_ context.Context, u users.User) (users.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return users.User{}, r.Err
	}
	// Mirrors the partial unique index: a deleted account releases its login.
	for _, existing := range r.byID {
		if existing.Status != users.StatusDeleted && strings.EqualFold(existing.Login, u.Login) {
			return users.User{}, users.ErrLoginTaken
		}
	}

	u.ID = uuid.New()
	u.CreatedAt = time.Now().UTC()
	u.UpdatedAt = u.CreatedAt
	if u.Status == "" {
		u.Status = users.StatusActive
	}
	r.byID[u.ID] = u
	return u, nil
}

func (r *Repository) List(_ context.Context, f users.Filter) ([]users.User, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return nil, 0, r.Err
	}

	var matched []users.User
	for _, u := range r.byID {
		// An empty status excludes deleted accounts, as the real query does.
		if f.Status == "" {
			if u.Status == users.StatusDeleted {
				continue
			}
		} else if u.Status != f.Status {
			continue
		}
		if f.Query != "" && !containsFold(u.Login, f.Query) && !containsFold(u.FullName, f.Query) {
			continue
		}
		matched = append(matched, u)
	}

	total := len(matched)
	f = f.Normalize()
	if f.Offset >= total {
		return nil, total, nil
	}
	end := min(f.Offset+f.Limit, total)
	return matched[f.Offset:end], total, nil
}

// Search mirrors the picker query: active accounts whose login, full name or
// email contains query (case-insensitively), ordered by login, cut to limit.
func (r *Repository) Search(_ context.Context, query string, limit int) ([]users.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return nil, r.Err
	}

	var found []users.User
	for _, u := range r.byID {
		if u.Status != users.StatusActive {
			continue
		}
		if !containsFold(u.Login, query) && !containsFold(u.FullName, query) && !containsFold(u.Email, query) {
			continue
		}
		found = append(found, u)
	}
	slices.SortFunc(found, func(a, b users.User) int { return strings.Compare(a.Login, b.Login) })
	if limit > 0 && len(found) > limit {
		found = found[:limit]
	}
	return found, nil
}

func (r *Repository) UpdateProfile(_ context.Context, id uuid.UUID, fullName, email string) error {
	return r.mutate(id, func(u *users.User) {
		u.FullName = fullName
		u.Email = email
		u.UpdatedAt = time.Now().UTC()
	})
}

func (r *Repository) SetStatus(_ context.Context, ids []uuid.UUID, status string, change users.StatusChange) error {
	for _, id := range ids {
		if err := r.mutate(id, func(u *users.User) {
			u.Status = status
			u.StatusReason = change.Reason
			at := change.At
			u.StatusChangedAt = &at
			by := change.By
			u.StatusChangedBy = &by
			u.UpdatedAt = time.Now().UTC()
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) SetPassword(_ context.Context, id uuid.UUID, hash string, mustChange bool) error {
	return r.mutate(id, func(u *users.User) {
		now := time.Now().UTC()
		u.PasswordHash = hash
		u.MustChangePassword = mustChange
		u.PasswordChangedAt = &now
		u.UpdatedAt = now
	})
}

// SetPasswordMany skips a missing account, as the real one does.
func (r *Repository) SetPasswordMany(_ context.Context, creds []users.Credential) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return r.Err
	}
	now := time.Now().UTC()
	for _, c := range creds {
		u, ok := r.byID[c.UserID]
		if !ok {
			continue
		}
		u.PasswordHash = c.Hash
		u.MustChangePassword = true
		u.PasswordChangedAt = &now
		u.UpdatedAt = now
		r.byID[c.UserID] = u
	}
	return nil
}

func (r *Repository) BumpSessionGeneration(_ context.Context, id uuid.UUID) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return 0, r.Err
	}
	u, ok := r.byID[id]
	if !ok {
		return 0, users.ErrNotFound
	}
	u.SessionGeneration++
	r.byID[id] = u
	return u.SessionGeneration, nil
}

// BumpSessionGenerationMany skips a missing account, as the real one does.
func (r *Repository) BumpSessionGenerationMany(_ context.Context, ids []uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return r.Err
	}
	for _, id := range ids {
		u, ok := r.byID[id]
		if !ok {
			continue
		}
		u.SessionGeneration++
		u.UpdatedAt = time.Now().UTC()
		r.byID[id] = u
	}
	return nil
}

func (r *Repository) RecordLogin(_ context.Context, id uuid.UUID, at time.Time) error {
	return r.mutate(id, func(u *users.User) { u.LastLoginAt = &at })
}

func (r *Repository) TakenAmong(_ context.Context, ids []uuid.UUID) ([]users.TakenConflict, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return nil, r.Err
	}
	want := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}

	var taken []users.TakenConflict
	for id := range want {
		deleted, ok := r.byID[id]
		if !ok || deleted.Status != users.StatusDeleted {
			continue
		}
		var c users.TakenConflict
		for _, live := range r.byID {
			if live.Status == users.StatusDeleted {
				continue
			}
			if strings.EqualFold(live.Login, deleted.Login) {
				c.Login = true
			}
			if deleted.Email != "" && live.Email == deleted.Email {
				c.Email = true
			}
		}
		if c.Login || c.Email {
			c.ID = id
			taken = append(taken, c)
		}
	}
	return taken, nil
}

func (r *Repository) CountActiveWithRole(_ context.Context, roleCode string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.CountActiveWithRoleCalls++
	count := 0
	for _, u := range r.byID {
		if u.Status == users.StatusActive && slices.Contains(u.Roles, roleCode) {
			count++
		}
	}
	return count, nil
}

// Roles lists the roles GrantRole defined, ordered like the real one.
func (r *Repository) Roles(_ context.Context) ([]users.Role, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	codes := make([]string, 0, len(r.permissions))
	for code := range r.permissions {
		codes = append(codes, code)
	}
	slices.Sort(codes)

	catalogue := make([]users.Role, 0, len(codes))
	for _, code := range codes {
		catalogue = append(catalogue, users.Role{Code: code, Name: code})
	}
	return catalogue, nil
}

func (r *Repository) ReplaceRoles(_ context.Context, id uuid.UUID, roleCodes []string) error {
	return r.mutate(id, func(u *users.User) {
		u.Roles = append([]string(nil), roleCodes...)
		u.UpdatedAt = time.Now().UTC()
	})
}

// ReplaceRolesMany skips a missing account, as the real one does.
func (r *Repository) ReplaceRolesMany(_ context.Context, ids []uuid.UUID, roleCodes []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return r.Err
	}
	for _, id := range ids {
		u, ok := r.byID[id]
		if !ok {
			continue
		}
		u.Roles = append([]string(nil), roleCodes...)
		u.UpdatedAt = time.Now().UTC()
		r.byID[id] = u
	}
	return nil
}

// withPermissions attaches permissions as the real repository does. Callers
// hold the lock.
func (r *Repository) withPermissions(u users.User) users.User {
	seen := map[string]struct{}{}
	u.Permissions = nil
	for _, role := range u.Roles {
		for _, permission := range r.permissions[role] {
			if _, dup := seen[permission]; dup {
				continue
			}
			seen[permission] = struct{}{}
			u.Permissions = append(u.Permissions, permission)
		}
	}
	return u
}

func (r *Repository) mutate(id uuid.UUID, fn func(*users.User)) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return r.Err
	}
	u, ok := r.byID[id]
	if !ok {
		return users.ErrNotFound
	}
	fn(&u)
	r.byID[id] = u
	return nil
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// SpyUnitOfWork is a pass-through storage.UnitOfWork that counts invocations.
type SpyUnitOfWork struct {
	Calls int
	Err   error
}

func (s *SpyUnitOfWork) Do(ctx context.Context, fn func(context.Context) error) error {
	s.Calls++
	if s.Err != nil {
		return s.Err
	}
	return fn(ctx)
}
