// Package userstest provides an in-memory users.Repository for tests, so
// account and authentication rules run against real behaviour without a
// database. It is not for production use.
package userstest

import (
	"bytes"
	"cmp"
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/google/uuid"
)

type Repository struct {
	mu          sync.Mutex
	byID        map[uuid.UUID]users.User
	permissions map[string][]string
	// Err, when set, is returned by every method.
	Err error
	// OnLock, when set, runs as Lock takes an account, as a change another
	// transaction committed while Lock waited for the row.
	OnLock                   func(id uuid.UUID)
	CountActiveWithRoleCalls int
	identities               map[identityKey]users.LinkedIdentity
	lastUsed                 map[identityKey]time.Time
	unlinked                 map[unlinkedKey]users.UnlinkedIdentity
	// contestStaff are the accounts that manage or own a contest that is not
	// archived.
	contestStaff map[uuid.UUID]bool
}

type identityKey struct{ provider, issuer, subject string }

// unlinkedKey mirrors unlinked_identities' unique key.
type unlinkedKey struct {
	userID uuid.UUID
	identityKey
}

func New() *Repository {
	return &Repository{
		byID:         map[uuid.UUID]users.User{},
		permissions:  map[string][]string{},
		identities:   map[identityKey]users.LinkedIdentity{},
		lastUsed:     map[identityKey]time.Time{},
		unlinked:     map[unlinkedKey]users.UnlinkedIdentity{},
		contestStaff: map[uuid.UUID]bool{},
	}
}

// AppointToContest makes the account a manager of a contest that is not
// archived, which gives it no global role.
func (r *Repository) AppointToContest(id uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contestStaff[id] = true
}

func (r *Repository) ManagesContest(_ context.Context, id uuid.UUID) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return false, r.Err
	}
	return r.contestStaff[id], nil
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

// Len counts every account held, deleted ones included.
func (r *Repository) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byID)
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

// ByEmail mirrors the lower(email) lookup over live accounts.
func (r *Repository) ByEmail(_ context.Context, email string) (users.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return users.User{}, r.Err
	}
	for _, u := range r.byID {
		if u.Status != users.StatusDeleted && u.Email != "" && strings.EqualFold(u.Email, email) {
			return r.withPermissions(u), nil
		}
	}
	return users.User{}, users.ErrNotFound
}

func (r *Repository) MarkEmailVerified(_ context.Context, id uuid.UUID, at time.Time) error {
	return r.mutate(id, func(u *users.User) { u.EmailVerifiedAt = &at })
}

// Lock stands in for the row lock: it holds nothing, and runs OnLock.
func (r *Repository) Lock(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	err, hook := r.Err, r.OnLock
	_, ok := r.byID[id]
	r.mu.Unlock()
	switch {
	case err != nil:
		return err
	case !ok:
		return users.ErrNotFound
	}
	if hook != nil {
		hook(id)
	}
	return nil
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

func (r *Repository) UpdateProfile(_ context.Context, id uuid.UUID, fullName, email string, keepVerified bool) error {
	return r.mutate(id, func(u *users.User) {
		u.FullName = fullName
		u.Email = email
		if !keepVerified {
			u.EmailVerifiedAt = nil
		}
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

// UpgradePasswordHash replaces the digest only while it is still verified, as
// the real one does.
func (r *Repository) UpgradePasswordHash(_ context.Context, id uuid.UUID, verified, upgraded string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return false, r.Err
	}
	u, ok := r.byID[id]
	if !ok || u.PasswordHash != verified {
		return false, nil
	}
	u.PasswordHash = upgraded
	u.UpdatedAt = time.Now().UTC()
	r.byID[id] = u
	return true, nil
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

func (r *Repository) ByExternalIdentity(_ context.Context, provider, issuer, subject string) (users.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return users.User{}, r.Err
	}
	id, ok := r.identities[identityKey{provider, issuer, subject}]
	if !ok {
		return users.User{}, users.ErrNotFound
	}
	u, ok := r.byID[id.UserID]
	if !ok {
		return users.User{}, users.ErrNotFound
	}
	return r.withPermissions(u), nil
}

// LinkExternalIdentity mirrors the unique (provider, issuer, subject), the
// foreign key to the account and the row's defaults.
func (r *Repository) LinkExternalIdentity(_ context.Context, id users.ExternalIdentity) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return r.Err
	}
	key := identityKey{id.Provider, id.Issuer, id.Subject}
	if _, taken := r.identities[key]; taken {
		return users.ErrExternalIdentityTaken
	}
	if _, ok := r.byID[id.UserID]; !ok {
		return users.ErrNotFound
	}
	r.identities[key] = users.LinkedIdentity{ID: uuid.New(), ExternalIdentity: id, LinkedAt: time.Now().UTC()}
	return nil
}

func (r *Repository) HoldsOtherIdentityAt(_ context.Context, userID uuid.UUID, provider, issuer, subject string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return false, r.Err
	}
	for key, id := range r.identities {
		if id.UserID == userID && key.provider == provider && key.issuer == issuer && key.subject != subject {
			return true, nil
		}
	}
	return false, nil
}

func (r *Repository) TouchExternalIdentity(_ context.Context, provider, issuer, subject string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return r.Err
	}
	key := identityKey{provider, issuer, subject}
	if _, ok := r.identities[key]; !ok {
		return users.ErrNotFound
	}
	r.lastUsed[key] = at
	return nil
}

// LastUsed is the time TouchExternalIdentity last stamped, and zero if it
// never did, which LinkedIdentity.LastUsedAt cannot say: like the column, it
// starts at the link.
func (r *Repository) LastUsed(provider, issuer, subject string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastUsed[identityKey{provider, issuer, subject}]
}

func (r *Repository) UnlinkExternalIdentity(_ context.Context, userID, identityID uuid.UUID) (users.LinkedIdentity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return users.LinkedIdentity{}, r.Err
	}
	for key, id := range r.identities {
		if id.ID == identityID && id.UserID == userID {
			gone := r.linked(key)
			delete(r.identities, key)
			delete(r.lastUsed, key)
			return gone, nil
		}
	}
	return users.LinkedIdentity{}, users.ErrIdentityNotFound
}

func (r *Repository) DeleteExternalIdentities(_ context.Context, userID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return r.Err
	}
	for key, id := range r.identities {
		if id.UserID == userID {
			delete(r.identities, key)
			delete(r.lastUsed, key)
		}
	}
	return nil
}

// IdentitiesOf mirrors last_used_at, which starts at the link.
func (r *Repository) IdentitiesOf(_ context.Context, userID uuid.UUID) ([]users.LinkedIdentity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return nil, r.Err
	}
	var found []users.LinkedIdentity
	for key, id := range r.identities {
		if id.UserID == userID {
			found = append(found, r.linked(key))
		}
	}
	slices.SortFunc(found, func(a, b users.LinkedIdentity) int {
		return cmp.Or(a.LinkedAt.Compare(b.LinkedAt), bytes.Compare(a.ID[:], b.ID[:]))
	})
	return found[:min(len(found), users.MaxIdentitiesListed)], nil
}

// RecordUnlinked mirrors the upsert on the unique key, which keeps the row's
// id.
func (r *Repository) RecordUnlinked(_ context.Context, id users.ExternalIdentity) (users.UnlinkedIdentity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return users.UnlinkedIdentity{}, r.Err
	}
	key := unlinkedKey{id.UserID, identityKey{id.Provider, id.Issuer, id.Subject}}
	record, ok := r.unlinked[key]
	if !ok {
		record.ID = uuid.New()
	}
	record.ExternalIdentity, record.UnlinkedAt = id, time.Now().UTC()
	r.unlinked[key] = record
	return record, nil
}

func (r *Repository) UnlinkedIdentitiesOf(_ context.Context, userID uuid.UUID) ([]users.UnlinkedIdentity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return nil, r.Err
	}
	var found []users.UnlinkedIdentity
	for key, record := range r.unlinked {
		if key.userID == userID {
			found = append(found, record)
		}
	}
	slices.SortFunc(found, func(a, b users.UnlinkedIdentity) int {
		return cmp.Or(b.UnlinkedAt.Compare(a.UnlinkedAt), bytes.Compare(b.ID[:], a.ID[:]))
	})
	return found[:min(len(found), users.MaxIdentitiesListed)], nil
}

func (r *Repository) IsUnlinked(_ context.Context, userID uuid.UUID, provider, issuer, subject string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return false, r.Err
	}
	_, ok := r.unlinked[unlinkedKey{userID, identityKey{provider, issuer, subject}}]
	return ok, nil
}

func (r *Repository) AllowUnlinked(_ context.Context, userID, unlinkedID uuid.UUID) (users.UnlinkedIdentity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Err != nil {
		return users.UnlinkedIdentity{}, r.Err
	}
	for key, record := range r.unlinked {
		if record.ID == unlinkedID && key.userID == userID {
			delete(r.unlinked, key)
			return record, nil
		}
	}
	return users.UnlinkedIdentity{}, users.ErrUnlinkedIdentityNotFound
}

// Link links the identity as LinkExternalIdentity does, failing t when that
// refuses, and returns it as IdentitiesOf would.
func (r *Repository) Link(t testing.TB, identity users.ExternalIdentity) users.LinkedIdentity {
	t.Helper()
	if err := r.LinkExternalIdentity(context.Background(), identity); err != nil {
		t.Fatalf("link %s at %s: %v", identity.Subject, identity.Issuer, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.linked(identityKey{identity.Provider, identity.Issuer, identity.Subject})
}

// linked is the identity at key with its last use, which starts at the link.
// Callers hold the lock.
func (r *Repository) linked(key identityKey) users.LinkedIdentity {
	id := r.identities[key]
	id.LastUsedAt = id.LinkedAt
	if at, ok := r.lastUsed[key]; ok {
		id.LastUsedAt = at
	}
	return id
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
