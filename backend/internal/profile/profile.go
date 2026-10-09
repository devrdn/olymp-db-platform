// Package profile answers what a participant may be told about their own
// account: totals, contests and, for each contest over for them, the report,
// queries, answers and notes. A contest is over when the participation gate
// says so, so results open exactly when the play screen closes. Nothing of a
// contest still running for the caller is shown, and every refusal is
// ErrNotFound so no contest's existence leaks.
package profile

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/monitor"
)

// ErrNotFound is every refusal these reads can make: no such contest, no
// registration in it, or not ended for the caller. One error, so it does not
// reveal whether a contest exists or who is on its roster.
var ErrNotFound = errors.New("no such finished contest for this participant")

// MaxContests bounds the list one profile carries (CLAUDE.md rule 2).
const MaxContests = 50

// Summary is the profile's header, counted in one read.
type Summary struct {
	Contests int
	Finished int
	Queries  int
	Solved   int
}

// Store is the storage this package needs.
type Store interface {
	Summary(ctx context.Context, userID uuid.UUID) (Summary, error)
	// Enrolments reads the account's registrations with their contests and
	// the participant's own numbers, newest first, at most limit, in one
	// statement. The Result carries points, solved and penalty only, never a
	// place or a state.
	Enrolments(ctx context.Context, userID uuid.UUID, limit int) ([]Enrolment, error)
	Activity(ctx context.Context, registration uuid.UUID) (Activity, error)
}

// Results is the slice of leaderboard.Service this package needs.
type Results interface {
	Own(ctx context.Context, contestID, registration uuid.UUID) (leaderboard.Own, error)
}

// Attempts is the slice of monitor.WatchService the report needs: the same
// answers read a contest's staff make.
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

type Config struct {
	Store        Store
	Contests     ContestReader
	Participants ParticipantReader
	Results      Results
	Attempts     Attempts
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Gate is the participation gate shared with the console and the answer
	// route, so results open only after the deadline plus its grace.
	// Required.
	Gate *contests.Gate
}

type Service struct {
	store    Store
	contests ContestReader
	people   ParticipantReader
	results  Results
	attempts Attempts
	now      func() time.Time
	gate     *contests.Gate
}

// NewService panics without a Gate.
func NewService(cfg Config) *Service {
	if cfg.Gate == nil {
		panic("profile: NewService needs the participation gate")
	}
	s := &Service{store: cfg.Store, contests: cfg.Contests, people: cfg.Participants,
		results: cfg.Results, attempts: cfg.Attempts, now: cfg.Now, gate: cfg.Gate}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// Access is what admission resolved: the contest and the caller's own
// registration in it.
type Access struct {
	Contest     contests.Contest
	Participant contests.Participant
}

// Open admits a caller to one of their own finished contests, or refuses
// with ErrNotFound.
//
// The registration is looked up by the calling account, never by a request
// identifier, so nobody else's can be named. It is looked up first, saving a
// read on the commonest refusal. The contest must then be over for this
// participant, so the profile is not a second door into a running contest.
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
	if !s.over(contest, participant, s.now()) {
		return Access{}, ErrNotFound
	}
	return Access{Contest: contest, Participant: participant}, nil
}

// over reports whether the contest has ended for this participant. It is the
// participation gate's answer (contests.Standing.Over), so play and results
// are never open at once.
func (s *Service) over(c contests.Contest, p contests.Participant, now time.Time) bool {
	return s.gate.StandingOf(c, p, now, netip.Addr{}).Over()
}

// Summary is the profile's four numbers over the account's whole record, not
// cut at MaxContests. The read is bounded by the account's own registrations,
// each counted over its own index range.
func (s *Service) Summary(ctx context.Context, userID uuid.UUID) (Summary, error) {
	summary, err := s.store.Summary(ctx, userID)
	if err != nil {
		return Summary{}, fmt.Errorf("read the profile summary: %w", err)
	}
	return summary, nil
}
