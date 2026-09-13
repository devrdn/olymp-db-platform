package leaderboard

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
)

// Defaults for Config.
const (
	// DefaultCacheTTL is how long one computation of a contest's public table
	// serves every viewer. Ten seconds is invisible on a table people glance
	// at, and it caps the database at one aggregate per contest per window
	// however many anonymous viewers the public page draws.
	DefaultCacheTTL = 10 * time.Second
	// DefaultMaxRows bounds a table (CLAUDE.md rule 2). An open contest can
	// collect more registrations than anybody will scroll, and a public answer
	// without a bound is an answer whose size the caller chooses.
	DefaultMaxRows = 2000
)

// ContestReader is the one contest lookup this package needs.
type ContestReader interface {
	ByID(ctx context.Context, id uuid.UUID) (contests.Contest, error)
}

// ParticipantReader finds the caller's own registration.
type ParticipantReader interface {
	ByUser(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, error)
}

// Query asks storage for a contest's entries up to a cutoff.
type Query struct {
	ContestID uuid.UUID
	// Cutoff is exclusive, for submissions and for registrations alike: a
	// registration created after the freeze is not on a frozen table either,
	// or its row would appear with a zero and say that somebody joined.
	Cutoff  time.Time
	Scoring string
	// IncludeDisqualified is true only for the staff table.
	IncludeDisqualified bool
	// Limit is how many entries to return at most, in the order Rank would
	// put them — with the winner first in winner mode — so that cutting the
	// list never cuts the top of the table.
	Limit int
}

// Repository is the storage the table needs.
type Repository interface {
	Standings(ctx context.Context, q Query) ([]Entry, error)
	// MarkRevealed records the reveal once. It returns the moment in force
	// and whether this call was the one that set it.
	MarkRevealed(ctx context.Context, contestID uuid.UUID, at time.Time) (time.Time, bool, error)
}

// Config assembles a Service.
type Config struct {
	Contests     ContestReader
	Participants ParticipantReader
	Standings    Repository
	Audit        *audit.Recorder
	UnitOfWork   storage.UnitOfWork
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// CacheTTL and MaxRows default to DefaultCacheTTL and DefaultMaxRows.
	CacheTTL time.Duration
	MaxRows  int
}

// View is the table as everybody but the staff sees it.
type View struct {
	Contest contests.Contest
	Decision
	// GeneratedAt is when the table was computed — never the time of the
	// latest answer, which during a freeze would say that something changed.
	GeneratedAt time.Time
	Truncated   bool
	Rows        []Row
}

// StaffView is the live table, with what everybody else is being shown.
type StaffView struct {
	Contest     contests.Contest
	Shown       Decision
	GeneratedAt time.Time
	Truncated   bool
	Rows        []Row
}

// Service computes tables and reveals results.
type Service struct {
	contests     ContestReader
	participants ParticipantReader
	standings    Repository
	audit        *audit.Recorder
	uow          storage.UnitOfWork
	now          func() time.Time
	ttl          time.Duration
	maxRows      int

	mu    sync.Mutex
	cache map[uuid.UUID]cached
}

// cached is one contest's public table and the moment it stops being true.
type cached struct {
	view    View
	expires time.Time
}

// NewService returns a Service.
func NewService(cfg Config) *Service {
	s := &Service{
		contests: cfg.Contests, participants: cfg.Participants, standings: cfg.Standings,
		audit: cfg.Audit, uow: cfg.UnitOfWork, now: cfg.Now, ttl: cfg.CacheTTL, maxRows: cfg.MaxRows,
		cache: make(map[uuid.UUID]cached),
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.ttl <= 0 {
		s.ttl = DefaultCacheTTL
	}
	if s.maxRows <= 0 {
		s.maxRows = DefaultMaxRows
	}
	return s
}

// Public returns the table anybody may see, from the shared cache when it is
// still true.
//
// Only a contest that exists and is past its draft is ever cached, so the
// number of entries is bounded by the number of real contests, never by how
// many identifiers a caller can invent; a refusal is not cached and costs
// what the HTTP layer's per-address limit lets it cost.
func (s *Service) Public(ctx context.Context, contestID uuid.UUID) (View, error) {
	now := s.now()
	if view, ok := s.fromCache(contestID, now); ok {
		return view, nil
	}

	c, err := s.contest(ctx, contestID)
	if err != nil {
		return View{}, err
	}
	decision, err := Decide(c, now)
	if err != nil {
		return View{}, err
	}

	view := View{Contest: c, Decision: decision, GeneratedAt: now, Rows: []Row{}}
	if decision.State != StateNotStarted {
		view.Rows, view.Truncated, err = s.rank(ctx, Query{
			ContestID: c.ID, Cutoff: decision.Cutoff, Scoring: c.Scoring,
		})
		if err != nil {
			return View{}, err
		}
	}

	s.store(contestID, view, s.expiry(c, decision, now))
	return view, nil
}

// ForParticipant returns the public table and the caller's own registration,
// so the caller can find their row. The rows are the shared computation: the
// only thing a participant is told beyond an anonymous viewer is which row is
// theirs.
func (s *Service) ForParticipant(ctx context.Context, contestID, userID uuid.UUID) (View, uuid.UUID, error) {
	participant, err := s.participants.ByUser(ctx, contestID, userID)
	switch {
	case errors.Is(err, contests.ErrParticipantNotFound):
		return View{}, uuid.Nil, ErrNotAParticipant
	case err != nil:
		return View{}, uuid.Nil, fmt.Errorf("look up the participant: %w", err)
	}
	view, err := s.Public(ctx, contestID)
	if err != nil {
		return View{}, uuid.Nil, err
	}
	return view, participant.ID, nil
}

// Live returns the staff table: cut off now whatever the freeze, with the
// disqualified on it, and never cached — a handful of staff do not need it,
// and a stale staff table is the one place a freeze could hide something from
// the people who run the contest.
func (s *Service) Live(ctx context.Context, contestID uuid.UUID) (StaffView, error) {
	now := s.now()
	c, err := s.contests.ByID(ctx, contestID)
	if errors.Is(err, contests.ErrNotFound) {
		return StaffView{}, ErrNotFound
	}
	if err != nil {
		return StaffView{}, fmt.Errorf("look up the contest: %w", err)
	}

	shown, err := Decide(c, now)
	if err != nil {
		// A draft: nobody else sees anything, and there is nothing to rank.
		return StaffView{Contest: c, Shown: Decision{State: StateNotStarted, Cutoff: now}, GeneratedAt: now, Rows: []Row{}}, nil
	}
	rows, truncated, err := s.rank(ctx, Query{
		ContestID: c.ID, Cutoff: now, Scoring: c.Scoring, IncludeDisqualified: true,
	})
	if err != nil {
		return StaffView{}, err
	}
	return StaffView{Contest: c, Shown: shown, GeneratedAt: now, Truncated: truncated, Rows: rows}, nil
}

// Reveal opens a frozen table's final state to everybody, once.
//
// Refused before the finish, because revealing a running contest's table is
// turning the freeze off, and refused for a contest that was never frozen,
// because it has nothing to reveal. A second reveal is not an error: it
// returns the moment already in force and records nothing, so two organisers
// pressing the button together see the same result.
func (s *Service) Reveal(ctx context.Context, actorID, contestID uuid.UUID) (time.Time, error) {
	c, err := s.contests.ByID(ctx, contestID)
	if errors.Is(err, contests.ErrNotFound) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("look up the contest: %w", err)
	}
	if c.Status != contests.StatusFinished && c.Status != contests.StatusArchived {
		return time.Time{}, fmt.Errorf("%w: the contest is %s", ErrNotRevealable, c.Status)
	}
	freezeAt, frozen := c.FreezeAt()
	if !frozen {
		return time.Time{}, fmt.Errorf("%w: the contest was never frozen", ErrNotRevealable)
	}

	var revealedAt time.Time
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		at, newly, err := s.standings.MarkRevealed(ctx, contestID, s.now())
		if err != nil {
			return err
		}
		revealedAt = at
		if !newly {
			return nil
		}
		actor := actorID
		return s.audit.Record(ctx, audit.Entry{
			ActorID:  &actor,
			Action:   audit.ActionContestLeaderboardReveal,
			Entity:   "contest",
			EntityID: contestID.String(),
			Payload:  map[string]any{"frozen_at": freezeAt.UTC().Format(time.RFC3339)},
		})
	})
	if err != nil {
		return time.Time{}, err
	}

	s.mu.Lock()
	delete(s.cache, contestID)
	s.mu.Unlock()
	return revealedAt, nil
}

// contest looks a contest up for the public table: a draft and a missing
// contest are the same answer.
func (s *Service) contest(ctx context.Context, id uuid.UUID) (contests.Contest, error) {
	c, err := s.contests.ByID(ctx, id)
	switch {
	case errors.Is(err, contests.ErrNotFound):
		return contests.Contest{}, ErrNotFound
	case err != nil:
		return contests.Contest{}, fmt.Errorf("look up the contest: %w", err)
	case c.Status == contests.StatusDraft:
		return contests.Contest{}, ErrNotFound
	}
	return c, nil
}

// rank reads one more entry than the bound, so "there are more" is a fact
// rather than the guess len == limit would be.
func (s *Service) rank(ctx context.Context, q Query) ([]Row, bool, error) {
	q.Limit = s.maxRows + 1
	entries, err := s.standings.Standings(ctx, q)
	if err != nil {
		return nil, false, fmt.Errorf("read the standings: %w", err)
	}
	truncated := len(entries) > s.maxRows
	if truncated {
		entries = entries[:s.maxRows]
	}
	return Rank(q.Scoring, entries), truncated, nil
}

// expiry is when a cached table stops being true: the TTL, or the freeze if
// the table is live and the freeze comes sooner.
func (s *Service) expiry(c contests.Contest, d Decision, now time.Time) time.Time {
	expires := now.Add(s.ttl)
	if freezeAt, ok := c.FreezeAt(); ok && d.State == StateLive && freezeAt.Before(expires) {
		expires = freezeAt
	}
	return expires
}

func (s *Service) fromCache(id uuid.UUID, now time.Time) (View, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.cache[id]
	if !ok || !now.Before(entry.expires) {
		return View{}, false
	}
	return entry.view, true
}

// store keeps a table and drops every entry that has expired, so the map
// holds only what is still being served.
func (s *Service) store(id uuid.UUID, view View, expires time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for key, entry := range s.cache {
		if !now.Before(entry.expires) {
			delete(s.cache, key)
		}
	}
	s.cache[id] = cached{view: view, expires: expires}
}
