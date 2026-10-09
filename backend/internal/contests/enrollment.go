package contests

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// Registration statuses.
const (
	// RegistrationRegistered means signed up but not yet started.
	RegistrationRegistered = "registered"
	// RegistrationActive means the participant's session is running.
	RegistrationActive = "active"
	// RegistrationFinished means they are done, by choice or by the clock.
	RegistrationFinished = "finished"
	// RegistrationDisqualified means excluded while the record is kept.
	RegistrationDisqualified = "disqualified"
)

// Errors about taking part.
var (
	ErrAlreadyEnrolled     = errors.New("already taking part in this contest")
	ErrEnrollmentClosed    = errors.New("this contest is not accepting signups")
	ErrAddressNotAllowed   = errors.New("this contest is not available from your network")
	ErrParticipantNotFound = errors.New("participant not found")
	// ErrParticipantStarted refuses deleting somebody with a record in this
	// contest, started or not (see HasWork); excluding them is
	// disqualification. Its API code still says "started": a published code
	// is a name clients match on.
	ErrParticipantStarted = errors.New("this participant has a record in this contest; disqualify instead of removing")
	// ErrStaffCannotParticipate refuses a registration to the contest's own
	// staff, or to an account holding contest.admin_all: they read the
	// reference answers and the unfrozen leaderboard. ErrParticipantCannotBeStaff
	// is the opposite direction.
	ErrStaffCannotParticipate = errors.New("contest staff cannot also register as a participant")
)

// PoolTrigger wakes the background game-pool tender for a contest whose
// roster or status just changed, so spare databases are ready before the
// next periodic tick; otherwise a participant waits for CREATE DATABASE in
// their first request. Callers trigger after commit.
//
// Trigger must never block the caller and never fail.
type PoolTrigger interface {
	Trigger(contestID uuid.UUID)
}

// EnrollmentOpenAt reports whether a student may still sign themselves up.
func (c Contest) EnrollmentOpenAt(now time.Time) error {
	if c.Enrollment != EnrollmentOpen {
		return ErrEnrollmentClosed
	}

	switch c.Status {
	case StatusPublished:
	case StatusRunning:
		// A late joiner in a shared window would get less time; individual
		// timing starts each participant's own clock.
		if c.Timing != TimingIndividual {
			return ErrEnrollmentClosed
		}
	default:
		return ErrEnrollmentClosed
	}

	if c.Settings.EnrollmentDeadline != nil && now.After(*c.Settings.EnrollmentDeadline) {
		return ErrEnrollmentClosed
	}
	return nil
}

// Participant is one person's involvement in one contest.
type Participant struct {
	// ID is the registration, not the user.
	ID        uuid.UUID
	ContestID uuid.UUID
	UserID    uuid.UUID
	Login     string
	FullName  string
	Status    string
	// StartedAt is the participant's first action. With individual timing
	// their deadline is computed from it.
	StartedAt  *time.Time
	FinishedAt *time.Time
	TotalScore int
	CreatedAt  time.Time
}

// HasStarted reports whether the participant has begun working. StartedAt
// is the authoritative fact, the one Deadline computes from; the statuses
// agree with it because only RegistrationRepository.Start moves a
// registration to active, setting StartedAt in the same write. Under a
// shared clock nothing sets either: see HasWork.
func (p Participant) HasStarted() bool {
	return p.StartedAt != nil ||
		p.Status == RegistrationActive ||
		p.Status == RegistrationFinished
}

// ParticipantFilter selects a page of participants.
type ParticipantFilter struct {
	// Query matches a substring of the login or full name.
	Query  string
	Status string
	Limit  int
	Offset int
}

// Normalize clamps the page size. The ceiling is higher than for contests so
// a 400-person olympiad fits on one page.
func (f ParticipantFilter) Normalize() ParticipantFilter {
	const (
		defaultLimit = 50
		maxLimit     = 500
	)
	if f.Limit <= 0 {
		f.Limit = defaultLimit
	}
	if f.Limit > maxLimit {
		f.Limit = maxLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return f
}

// RegistrationRepository stores who takes part.
type RegistrationRepository interface {
	// List returns a page of the contest's participants and the total count.
	List(ctx context.Context, contestID uuid.UUID, f ParticipantFilter) ([]Participant, int, error)
	// ByUser returns one participation, or ErrParticipantNotFound.
	ByUser(ctx context.Context, contestID, userID uuid.UUID) (Participant, error)
	// Add registers a user for a contest.
	Add(ctx context.Context, contestID, userID uuid.UUID) (Participant, error)
	// Remove deletes a registration outright.
	Remove(ctx context.Context, contestID, userID uuid.UUID) error
	// SetStatus changes a status that carries no timestamp (disqualification).
	// Moves to active go through Start, so status and StartedAt never diverge.
	SetStatus(ctx context.Context, registrationID uuid.UUID, status string) error
	// Start records now as the participant's first action if they have not
	// begun, setting StartedAt and the active status together. Concurrent
	// first actions must agree on one start time and an existing one must
	// never move: a single conditional UPDATE on "started_at IS NULL", never
	// a read then a write.
	Start(ctx context.Context, registrationID uuid.UUID, now time.Time) (Participant, error)
	// EnrolledIn reports which of these contests the user is registered for,
	// in one query.
	EnrolledIn(ctx context.Context, userID uuid.UUID, contestIDs []uuid.UUID) (map[uuid.UUID]bool, error)
	// AddScore adds delta to total_score as an atomic SQL increment, never a
	// read-modify-write, so concurrent scoring answers cannot overwrite each
	// other.
	AddScore(ctx context.Context, registrationID uuid.UUID, delta int) error
	// RegisteredWithPermission returns, ordered by login and at most
	// MaxReportedStaff, the logins of registered participants whose account
	// currently holds permission. One query; the permission is read through
	// the account's roles, since the point is a grant made after registering.
	RegisteredWithPermission(ctx context.Context, contestID uuid.UUID, permission string) ([]string, error)
	// HasWork reports whether a query, answer, note or signal is recorded
	// against this registration. Under a shared clock nothing sets
	// started_at, so this, not HasStarted, guards a cascading delete.
	HasWork(ctx context.Context, registrationID uuid.UUID) (bool, error)
}

// Why an entry of an import produced no registration.
const (
	SkipUnknownAccount  = "unknown_account"
	SkipAlreadyEnrolled = "already_enrolled"
	// SkipAccountBlocked is distinct from SkipUnknownAccount: a blocked
	// account is a real person who cannot sign in, not a typo. A deleted
	// one reads as unknown.
	SkipAccountBlocked = "account_blocked"
	// SkipStaffMember is an entry naming the contest's staff (see
	// ErrStaffCannotParticipate), skipped rather than failing the import.
	SkipStaffMember = "staff_member"
)

// maxRosterEntries bounds one import (CLAUDE.md rule 2). Every entry is a
// lookup, an insert and an audit line inside one transaction holding its
// locks; a faculty year is a few hundred people.
const maxRosterEntries = 1000

// ErrRosterTooLarge reports an import above that bound.
var ErrRosterTooLarge = errors.New("too many entries in one roster")

// MaxReportedStaff bounds RegisteredWithPermission's answer, a join nothing
// else bounds that ends up in a refusal a person reads and in the audit trail.
const MaxReportedStaff = 20

// AddParticipantsCommand adds people to a contest on behalf of its staff.
// Logins are accepted because rosters are pasted from spreadsheets.
type AddParticipantsCommand struct {
	ActorID   uuid.UUID
	ContestID uuid.UUID
	UserIDs   []uuid.UUID
	Logins    []string
}

// AddParticipantsResult reports what an import did. Partial success: one
// mistyped login must not reject the other rows.
type AddParticipantsResult struct {
	Added   int
	Skipped []SkippedParticipant
}

// SkippedParticipant is one entry that produced no registration.
type SkippedParticipant struct {
	// Ref is the login or identifier as it was given.
	Ref    string
	Reason string
}

// EnrollCommand is a student signing themselves up.
type EnrollCommand struct {
	UserID    uuid.UUID
	ContestID uuid.UUID
	// Address is the resolved client address, checked against the contest's
	// network restriction.
	Address netip.Addr
}

// EnrolledIn reports which of these contests the user is registered for.
// userID must be the authenticated caller, never a value from the request.
func (s *Service) EnrolledIn(ctx context.Context, userID uuid.UUID, contestIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	if userID == uuid.Nil || len(contestIDs) == 0 {
		return map[uuid.UUID]bool{}, nil
	}
	return s.registrations.EnrolledIn(ctx, userID, contestIDs)
}

// Participants returns a page of the contest's participants.
func (s *Service) Participants(ctx context.Context, contestID uuid.UUID, f ParticipantFilter) ([]Participant, int, error) {
	return s.registrations.List(ctx, contestID, f.Normalize())
}

// AddParticipants registers people on behalf of the contest's staff. The
// network restriction does not apply: it governs where participants work.
func (s *Service) AddParticipants(ctx context.Context, cmd AddParticipantsCommand) (AddParticipantsResult, error) {
	if entries := len(cmd.UserIDs) + len(cmd.Logins); entries > maxRosterEntries {
		return AddParticipantsResult{}, fmt.Errorf("%w: %d entries, at most %d",
			ErrRosterTooLarge, entries, maxRosterEntries)
	}

	c, err := s.contests.ByID(ctx, cmd.ContestID)
	if err != nil {
		return AddParticipantsResult{}, err
	}
	if !s.acceptsRegistrations(c) {
		return AddParticipantsResult{}, fmt.Errorf("%w: it is %s", ErrNotEditable, c.Status)
	}

	var result AddParticipantsResult
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		// The contest lock keeps a concurrent GrantManager out until commit,
		// so the staff list is read once and checked in memory per row.
		if err := s.contests.LockContest(ctx, c.ID); err != nil {
			return err
		}
		staff, err := s.managers.List(ctx, c.ID)
		if err != nil {
			return err
		}
		staffIDs := make(map[uuid.UUID]struct{}, len(staff))
		for _, m := range staff {
			staffIDs[m.UserID] = struct{}{}
		}
		for _, id := range cmd.UserIDs {
			user, err := s.users.ByID(ctx, id)
			if err != nil {
				if errors.Is(err, users.ErrNotFound) {
					result.skip(id.String(), SkipUnknownAccount)
					continue
				}
				return err
			}
			if err := s.addOne(ctx, cmd, c, staffIDs, user, &result); err != nil {
				return err
			}
		}

		for _, login := range cmd.Logins {
			login = strings.TrimSpace(login)
			if login == "" {
				continue
			}
			user, err := s.users.ByLogin(ctx, login)
			if err != nil {
				if errors.Is(err, users.ErrNotFound) {
					result.skip(login, SkipUnknownAccount)
					continue
				}
				return err
			}
			if err := s.addOne(ctx, cmd, c, staffIDs, user, &result); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return AddParticipantsResult{}, err
	}

	// A draft has nobody querying yet; the periodic tick serves it.
	if result.Added > 0 && s.poolTrigger != nil && (c.Status == StatusPublished || c.Status == StatusRunning) {
		s.poolTrigger.Trigger(c.ID)
	}
	return result, nil
}

// addOne registers one resolved account, or records why it was skipped.
//
// ByLogin still returns a deleted account whose login nobody reclaimed, so a
// deleted account is skipped as unknown. Whether the person is already
// enrolled is decided by the write's unique index, not a lookup, so two
// overlapping imports cannot both pass. staff is the contest's staff, read
// under the contest lock.
func (s *Service) addOne(ctx context.Context, cmd AddParticipantsCommand, c Contest, staff map[uuid.UUID]struct{}, user users.User, result *AddParticipantsResult) error {
	if user.Status == users.StatusDeleted {
		result.skip(user.Login, SkipUnknownAccount)
		return nil
	}
	if user.Status == users.StatusBlocked {
		result.skip(user.Login, SkipAccountBlocked)
		return nil
	}
	if _, isStaff := staff[user.ID]; isStaff {
		result.skip(user.Login, SkipStaffMember)
		return nil
	}
	if user.Has(rbac.PermissionContestAdminAll) {
		result.skip(user.Login, SkipStaffMember)
		return nil
	}

	if _, err := s.registrations.Add(ctx, c.ID, user.ID); err != nil {
		if errors.Is(err, ErrAlreadyEnrolled) {
			result.skip(user.Login, SkipAlreadyEnrolled)
			return nil
		}
		return err
	}
	result.Added++

	return s.record(ctx, cmd.ActorID, audit.ActionParticipantAdd, c.ID, map[string]any{
		"user_id": user.ID.String(),
		"login":   user.Login,
	})
}

func (r *AddParticipantsResult) skip(ref, reason string) {
	r.Skipped = append(r.Skipped, SkippedParticipant{Ref: ref, Reason: reason})
}

// Enroll signs a student up for a contest that invites self-signup.
func (s *Service) Enroll(ctx context.Context, cmd EnrollCommand) (Participant, error) {
	c, err := s.contests.ByID(ctx, cmd.ContestID)
	if err != nil {
		return Participant{}, err
	}
	if err := c.EnrollmentOpenAt(s.now()); err != nil {
		return Participant{}, err
	}

	// After the signup check, so the trail records only real attempts. A
	// refusal has nothing to be atomic with, so no unit of work.
	if !c.AllowsAddress(cmd.Address) {
		if err := s.recordDenied(ctx, cmd); err != nil {
			return Participant{}, err
		}
		return Participant{}, ErrAddressNotAllowed
	}

	// A double signup is refused by the write, not a lookup.
	var enrolled Participant
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		// The staff checks run under the contest lock, which serialises
		// against a concurrent GrantManager, so they cannot go stale before
		// the write.
		if err := s.contests.LockContest(ctx, c.ID); err != nil {
			return err
		}
		if _, err := s.managers.Get(ctx, c.ID, cmd.UserID); err == nil {
			return ErrStaffCannotParticipate
		} else if !errors.Is(err, ErrManagerNotFound) {
			return err
		}
		self, err := s.users.ByID(ctx, cmd.UserID)
		if err != nil {
			return err
		}
		if self.Has(rbac.PermissionContestAdminAll) {
			return ErrStaffCannotParticipate
		}
		if enrolled, err = s.registrations.Add(ctx, c.ID, cmd.UserID); err != nil {
			return err
		}
		return s.record(ctx, cmd.UserID, audit.ActionParticipantEnroll, c.ID, nil)
	})
	if err != nil {
		return Participant{}, err
	}

	// EnrollmentOpenAt admits only published or running contests.
	if s.poolTrigger != nil {
		s.poolTrigger.Trigger(c.ID)
	}
	return enrolled, nil
}

// recordDenied notes a participant turned away by the network restriction.
func (s *Service) recordDenied(ctx context.Context, cmd EnrollCommand) error {
	payload := map[string]any{"reason": "address_not_allowed"}
	if cmd.Address.IsValid() {
		payload["address"] = cmd.Address.String()
	}
	return s.record(ctx, cmd.UserID, audit.ActionContestAccessDenied, cmd.ContestID, payload)
}

// RemoveParticipant deletes a registration that never turned into work;
// anyone with a record must be disqualified instead.
//
// The checks run inside the deleting transaction because the delete cascades:
// checked before it, a first query landing in between would be lost. A query
// that starts first holds a KEY SHARE lock the delete waits behind, and one
// that starts after is refused by the foreign key.
func (s *Service) RemoveParticipant(ctx context.Context, actorID, contestID, userID uuid.UUID) error {
	if _, err := s.mutableContest(ctx, contestID); err != nil {
		return err
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		p, err := s.registrations.ByUser(ctx, contestID, userID)
		if err != nil {
			return err
		}
		if p.HasStarted() {
			return ErrParticipantStarted
		}
		// A shared clock never marks anybody started.
		work, err := s.registrations.HasWork(ctx, p.ID)
		if err != nil {
			return err
		}
		if work {
			return ErrParticipantStarted
		}

		if err := s.registrations.Remove(ctx, contestID, userID); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionParticipantRemove, contestID, map[string]any{
			"user_id": userID.String(),
		})
	})
}

// DisqualifyParticipant excludes somebody while keeping everything they did.
func (s *Service) DisqualifyParticipant(ctx context.Context, actorID, contestID, userID uuid.UUID) error {
	if _, err := s.mutableContest(ctx, contestID); err != nil {
		return err
	}
	p, err := s.registrations.ByUser(ctx, contestID, userID)
	if err != nil {
		return err
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.registrations.SetStatus(ctx, p.ID, RegistrationDisqualified); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionParticipantDisqualify, contestID, map[string]any{
			"user_id": userID.String(),
		})
	})
}

// acceptsRegistrations reports whether staff may still add people: up to and
// including a running contest, for latecomers left off the roster.
func (s *Service) acceptsRegistrations(c Contest) bool {
	return c.Status == StatusDraft || c.Status == StatusPublished || c.Status == StatusRunning
}
