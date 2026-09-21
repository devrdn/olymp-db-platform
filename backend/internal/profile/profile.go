// Package profile answers what a participant may be told about their own
// account: the four numbers of their profile, the contests they are on, and,
// for each one that has ended for them, their report, their queries, their
// answers and their notes.
//
// It answers only "is this the caller's own, and is it over for them". It
// does not compute a result — points, solved, penalty and place come from
// internal/leaderboard, and the queries, answers and workspace are read by
// internal/monitor, by the same methods a contest's staff read them with.
// It contains no SQL and no HTTP: the storage it needs is declared here and
// implemented in internal/postgres, and who may call it — authentication,
// the read budget — is internal/api's.
//
// What it deliberately does not do: show anything of a contest that is still
// running for the caller. Everything needed during a contest is on the
// contest's own screen, under that screen's rules (the window, the network
// restriction, the individual timer), and a second way to the same data past
// those rules is exactly what this package refuses to be. It also never
// tells a caller whether a contest they are not on exists: every refusal is
// ErrNotFound.
//
// The design is
// docs/superpowers/specs/2026-09-21-participant-profile-design.md.
package profile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/monitor"
)

// ErrNotFound is every refusal these reads can make: a contest that does not
// exist, one the caller has no registration in, and one that has not ended
// for them. One error, because telling them apart would say whether a
// contest exists and whether an account is on its roster (design §3).
var ErrNotFound = errors.New("no such finished contest for this participant")

// MaxContests bounds the list one profile carries (CLAUDE.md rule 2). An
// account's registrations are unbounded — a student may be enrolled in every
// olympiad an installation ever ran — and a page whose size the data chooses
// is a page nobody sized.
const MaxContests = 50

// Summary is the profile's header (design §2.1), counted in one read.
type Summary struct {
	// Contests is how many contests the account is registered in, and
	// Finished how many of those it finished.
	Contests int
	Finished int
	// Queries is every query the account ever ran, across every contest, and
	// Solved every question it ever answered correctly.
	Queries int
	Solved  int
}

// Store is the storage this package needs, implemented by
// internal/postgres.Profile.
type Store interface {
	// Summary counts the profile's four numbers in one statement.
	Summary(ctx context.Context, userID uuid.UUID) (Summary, error)
	// Enrolments reads the account's registrations with their contests and
	// the participant's own numbers in each, newest first, at most limit of
	// them — one statement over every registration of the account, never one
	// per contest (design §4).
	//
	// The Result it fills is the row's own points, solved and penalty, cut
	// off at nothing: what this participant did, which they may always see
	// once their contest has ended. It carries no place and no state; the
	// place is the report's (Service.Report, through the leaderboard), and
	// the state Service.Contests decides from the contest itself.
	Enrolments(ctx context.Context, userID uuid.UUID, limit int) ([]Enrolment, error)
	// Activity counts one registration's queries and how many of them
	// succeeded, and finds its last answer.
	Activity(ctx context.Context, registration uuid.UUID) (Activity, error)
}

// Results is the slice of leaderboard.Service this package needs: the
// caller's own standing, which is the only source of a result there is.
type Results interface {
	Own(ctx context.Context, contestID, registration uuid.UUID) (leaderboard.Own, error)
}

// Attempts is the slice of monitor.WatchService the report needs. The
// report's question table is the answers tab, grouped: the same read a
// contest's staff make of the same registration, never a second query over
// submissions.
type Attempts interface {
	Answers(ctx context.Context, contest, registration uuid.UUID) (monitor.Answers, error)
}

// ContestReader and ParticipantReader are the two lookups admission makes.
type ContestReader interface {
	ByID(ctx context.Context, id uuid.UUID) (contests.Contest, error)
}

type ParticipantReader interface {
	ByUser(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, error)
}

// Config assembles a Service.
type Config struct {
	Store        Store
	Contests     ContestReader
	Participants ParticipantReader
	Results      Results
	Attempts     Attempts
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Service answers a participant's reads of their own account.
type Service struct {
	store    Store
	contests ContestReader
	people   ParticipantReader
	results  Results
	attempts Attempts
	now      func() time.Time
}

// NewService returns the service.
func NewService(cfg Config) *Service {
	s := &Service{store: cfg.Store, contests: cfg.Contests, people: cfg.Participants,
		results: cfg.Results, attempts: cfg.Attempts, now: cfg.Now}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// Access is what admission resolved: the contest, and the caller's own
// registration in it.
type Access struct {
	Contest     contests.Contest
	Participant contests.Participant
}

// Open admits a caller to one of their own finished contests, or refuses
// with ErrNotFound.
//
// The registration is looked up by the account asking, never by an
// identifier in a request: there is no way to name somebody else's
// registration here, because nothing carries one. The contest must then be
// over for this participant (Over), so the profile is not a second door into
// a contest that is still being taken.
//
// The order is the registration first: a caller with no registration is
// refused before the contest is read at all, which is one lookup rather than
// two for the commonest refusal, and it is the lookup bounded by the
// account's own rows.
func (s *Service) Open(ctx context.Context, contestID, userID uuid.UUID) (Access, error) {
	participant, err := s.people.ByUser(ctx, contestID, userID)
	switch {
	case errors.Is(err, contests.ErrParticipantNotFound):
		return Access{}, ErrNotFound
	case err != nil:
		return Access{}, fmt.Errorf("look up the registration: %w", err)
	}

	contest, err := s.contests.ByID(ctx, contestID)
	switch {
	case errors.Is(err, contests.ErrNotFound):
		return Access{}, ErrNotFound
	case err != nil:
		return Access{}, fmt.Errorf("look up the contest: %w", err)
	}
	if !Over(contest, participant, s.now()) {
		return Access{}, ErrNotFound
	}
	return Access{Contest: contest, Participant: participant}, nil
}

// Over reports whether the contest has ended for this participant — the one
// rule the whole package turns on (design §3).
//
// Three ways it ends, any one of them enough:
//
//   - the registration is finished or disqualified. A disqualified
//     participant sees their own work and the fact of it; that is not a
//     reason to withhold what they did.
//   - the contest itself is finished or archived.
//   - the participant's own deadline has passed (contests.Deadline) — which
//     covers a fixed contest whose end has been reached while its status has
//     not caught up, and an individual participant whose hour is up while the
//     contest runs on for everybody else.
//
// A draft is over for nobody: its status is neither finished nor archived and
// it has no window anybody could have played inside. An individual
// participant who never started has no deadline for the same reason
// contests.Deadline reports none — and so nothing has passed.
func Over(c contests.Contest, p contests.Participant, now time.Time) bool {
	if c.Status == contests.StatusDraft {
		return false
	}
	if p.Status == contests.RegistrationFinished || p.Status == contests.RegistrationDisqualified {
		return true
	}
	if c.Status == contests.StatusFinished || c.Status == contests.StatusArchived {
		return true
	}
	deadline, ok := contests.Deadline(c, p)
	return ok && !now.Before(deadline)
}

// Summary is the profile's four numbers.
func (s *Service) Summary(ctx context.Context, userID uuid.UUID) (Summary, error) {
	summary, err := s.store.Summary(ctx, userID)
	if err != nil {
		return Summary{}, fmt.Errorf("read the profile summary: %w", err)
	}
	return summary, nil
}
