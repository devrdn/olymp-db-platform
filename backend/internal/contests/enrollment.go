package contests

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
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
	// ErrParticipantStarted reports an attempt to delete somebody who has
	// already worked on the contest. Their queries and answers are part of the
	// record; excluding them is disqualification, not deletion.
	ErrParticipantStarted = errors.New("this participant has already started; disqualify instead of removing")
)

// EnrollmentOpenAt reports whether a student may still sign themselves up.
//
// Three things have to hold: the contest invites self-signup, it is in a state
// that accepts registrations, and the deadline (if any) has not passed.
func (c Contest) EnrollmentOpenAt(now time.Time) error {
	if c.Enrollment != EnrollmentOpen {
		return ErrEnrollmentClosed
	}

	switch c.Status {
	case StatusPublished:
	case StatusRunning:
		// A late joiner in a shared window would get less time than everybody
		// else. Individual timing measures from each participant's own start,
		// so joining late costs the joiner nothing and takes nothing from
		// anybody else.
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
	// ID is the registration identifier: what submissions, game instances and
	// the query journal all hang off.
	ID        uuid.UUID
	ContestID uuid.UUID
	UserID    uuid.UUID
	Login     string
	FullName  string
	Status    string
	// StartedAt is when the participant opened the contest. With individual
	// timing it is what their deadline is computed from.
	StartedAt  *time.Time
	FinishedAt *time.Time
	TotalScore int
	CreatedAt  time.Time
}

// HasStarted reports whether the participant has begun working.
//
// StartedAt is the authoritative fact: it is the one value Deadline
// (deadline.go) can compute an individual participant's window from, so
// anything this reports as "started" without it would tell queryproxy one
// thing and the deadline formula another. The status is still consulted
// because it is a legitimate second reading of the same fact, not a
// competing one — RegistrationRepository.Start is the only place that ever
// moves a registration to RegistrationActive, and it always sets StartedAt
// in that same write (see postgres.Registrations.Start). Nothing in this
// codebase may set the status alone: a hypothetical caller that did would
// make this method disagree with Deadline about a participant who has
// nothing for the formula to add duration_min to, which is exactly the bug
// finding 1 fixed. Kept as an OR rather than collapsed to the timestamp alone
// so a registration touched only through SetStatus (RegistrationFinished, by
// a future path with its own timestamp field) still reads as started here.
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

// Normalize clamps the page size. The ceiling is higher than for contests: a
// staff list of a 400-person olympiad is a legitimate single page.
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
	// SetStatus changes a registration's status.
	//
	// Reserved for transitions that carry no timestamp of their own —
	// disqualification, today. A move to RegistrationActive or
	// RegistrationFinished must go through Start (or its future finishing
	// counterpart) instead, so the status and the timestamp that HasStarted
	// and Deadline both read are never set one without the other.
	SetStatus(ctx context.Context, registrationID uuid.UUID, status string) error
	// Start records now as the participant's first deliberate action against
	// the game — a SQL query today, an answer submission once that path
	// exists — if they have not already begun. It is the one seam both paths
	// share, and the only place that ever sets StartedAt or moves a
	// registration to RegistrationActive: it does both together, atomically,
	// so HasStarted and Deadline can never be told two different stories.
	//
	// Two concurrent first actions must agree on one start time, and a
	// participant who has already started must never have it moved — an
	// implementation does this with a single conditional UPDATE keyed on
	// "started_at IS NULL", never a read followed by a write.
	Start(ctx context.Context, registrationID uuid.UUID, now time.Time) (Participant, error)
	// EnrolledIn reports which of these contests the user is registered for.
	//
	// One question, one query: a catalogue of twenty rows must not become
	// twenty lookups. It lives on the registrations repository rather than
	// becoming a field on Contest, because "am I on this" is a fact about a
	// viewer and a contest together, not a property of the contest — put on
	// the domain type it would have to be filled, or left wrong, everywhere a
	// contest is loaded.
	EnrolledIn(ctx context.Context, userID uuid.UUID, contestIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}

// Why an entry of an import produced no registration.
const (
	SkipUnknownAccount  = "unknown_account"
	SkipAlreadyEnrolled = "already_enrolled"
	// SkipAccountBlocked is a distinct reason from SkipUnknownAccount on
	// purpose. A deleted account's login is not usably an account at all
	// from the roster's point of view — the login might as well not exist —
	// but a blocked one still is somebody with a name, just one who cannot
	// currently sign in, and that is worth telling the person pasting the
	// list apart from a plain typo. All three locale dictionaries already
	// carry workspace.people.import.reason.account_blocked for it.
	SkipAccountBlocked = "account_blocked"
)

// maxRosterEntries bounds one import of participants.
//
// Every entry is an account lookup, an insert and an audit line, all inside
// one transaction that holds its locks until the last row. The request body
// alone would allow tens of thousands of identifiers, which is a way for
// somebody who legitimately holds participant.manage to pin a database
// connection for as long as the statement timeout allows. A whole faculty
// year is a few hundred people, so the bound is far above honest use and far
// below what turns an import into a lever.
const maxRosterEntries = 1000

// ErrRosterTooLarge reports an import above that bound.
var ErrRosterTooLarge = errors.New("too many entries in one roster")

// AddParticipantsCommand adds people to a contest on behalf of its staff.
//
// Logins as well as identifiers because the practical input is a roster pasted
// out of a spreadsheet, where what an organizer has is student numbers.
type AddParticipantsCommand struct {
	ActorID   uuid.UUID
	ContestID uuid.UUID
	UserIDs   []uuid.UUID
	Logins    []string
}

// AddParticipantsResult reports what an import did.
//
// Partial success is the honest outcome: one mistyped login must not reject
// the other three hundred rows, and the person importing has to see which
// ones did not go in and why.
type AddParticipantsResult struct {
	Added   int
	Skipped []SkippedParticipant
}

// SkippedParticipant is one entry that produced no registration.
type SkippedParticipant struct {
	// Ref is the login or identifier exactly as it was given, so the row can
	// be found again in the spreadsheet it came from.
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
//
// The user is always the caller: the HTTP layer passes the identity it
// authenticated, never a value from the request, so this cannot answer "who
// else takes part in what".
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

// AddParticipants registers people on behalf of the contest's staff.
//
// The network restriction is not applied here: it governs where a participant
// may work from, not where the organizer sits while preparing the roster.
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
		for _, id := range cmd.UserIDs {
			user, err := s.users.ByID(ctx, id)
			if err != nil {
				if errors.Is(err, users.ErrNotFound) {
					result.skip(id.String(), SkipUnknownAccount)
					continue
				}
				return err
			}
			if err := s.addOne(ctx, cmd, c, user, &result); err != nil {
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
			if err := s.addOne(ctx, cmd, c, user, &result); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return AddParticipantsResult{}, err
	}
	return result, nil
}

// addOne registers one resolved account, recording why it was skipped when it
// was.
//
// A deleted account is skipped under the same reason as one that was never
// found: users.Repository.ByLogin deliberately still returns a deleted row
// when nothing live has reclaimed its login, so a roster entry for a former
// student would otherwise resolve to an account that can never sign in and
// be reported as added. From the roster's point of view the login does not
// resolve to a usable account either way, so it reads the same to whoever
// pasted it in, and needs no reason of its own in three locales.
//
// A blocked account is skipped too, but under its own reason
// (SkipAccountBlocked) rather than folded into the deleted case above: unlike
// a deleted account it is still somebody real, and auth.Service.Login and
// auth.Middleware both refuse it for the same reason this does — enrolling it
// would put on the roster a participant who can never sign in to sit the
// contest.
//
// Whether the person is already taking part is decided by the write, not by a
// lookup first: the guarantee is a unique index, and two organizers importing
// overlapping rosters at the same moment would both pass a lookup. Treating
// that verdict as an ordinary skip is what keeps one such row from failing the
// other three hundred.
func (s *Service) addOne(ctx context.Context, cmd AddParticipantsCommand, c Contest, user users.User, result *AddParticipantsResult) error {
	if user.Status == users.StatusDeleted {
		result.skip(user.Login, SkipUnknownAccount)
		return nil
	}
	if user.Status == users.StatusBlocked {
		result.skip(user.Login, SkipAccountBlocked)
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

	// Checked after the contest is known to accept signups, so the trail
	// carries real attempts rather than noise about closed contests.
	if !c.AllowsAddress(cmd.Address) {
		// Recorded outside a unit of work: this is a refusal, there is nothing
		// to be atomic with, and the attempt is exactly what an administrator
		// wants to see afterwards.
		if err := s.recordDenied(ctx, cmd); err != nil {
			return Participant{}, err
		}
		return Participant{}, ErrAddressNotAllowed
	}

	// Signing up twice is likewise decided by the write: a double-clicked
	// button sends two requests, and a lookup would let both through.
	var enrolled Participant
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		if enrolled, err = s.registrations.Add(ctx, c.ID, cmd.UserID); err != nil {
			return err
		}
		return s.record(ctx, cmd.UserID, audit.ActionParticipantEnroll, c.ID, nil)
	})
	if err != nil {
		return Participant{}, err
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

// RemoveParticipant deletes a registration that never turned into work.
//
// Somebody who has already started is refused: their queries and answers are
// part of the record, and excluding them is disqualification.
func (s *Service) RemoveParticipant(ctx context.Context, actorID, contestID, userID uuid.UUID) error {
	if _, err := s.mutableContest(ctx, contestID); err != nil {
		return err
	}
	p, err := s.registrations.ByUser(ctx, contestID, userID)
	if err != nil {
		return err
	}
	if p.HasStarted() {
		return ErrParticipantStarted
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
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

// acceptsRegistrations reports whether staff may still add people.
//
// Up to and including a running contest: adding a latecomer who was left off
// the roster is a normal thing to have to do at the start of an olympiad.
func (s *Service) acceptsRegistrations(c Contest) bool {
	return c.Status == StatusDraft || c.Status == StatusPublished || c.Status == StatusRunning
}
