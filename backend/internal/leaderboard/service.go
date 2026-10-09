package leaderboard

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/flight"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
)

const (
	// DefaultCacheTTL caps the database at one public-table aggregate per
	// contest per window, however many anonymous viewers there are.
	DefaultCacheTTL = 10 * time.Second
	// DefaultLiveCacheTTL is far shorter: it only collapses staff tabs
	// refreshing at once, never hiding anything from staff.
	DefaultLiveCacheTTL = 3 * time.Second
	// DefaultMaxRows bounds a table (CLAUDE.md rule 2).
	DefaultMaxRows = 2000
	// computeTimeout bounds one shared computation (see flight.Do). The
	// pool's statement timeout caps the query, so this guards a connection
	// acquire that never returns.
	computeTimeout = 20 * time.Second
)

type ContestReader interface {
	ByID(ctx context.Context, id uuid.UUID) (contests.Contest, error)
}

type ParticipantReader interface {
	ByUser(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, error)
}

type Query struct {
	ContestID uuid.UUID
	// Cutoff is exclusive for submissions and registrations alike, so a
	// frozen table does not show that somebody joined after the freeze.
	Cutoff              time.Time
	Scoring             string
	IncludeDisqualified bool
	// Pending asks for attempts inside a window on questions unsolved at the
	// cutoff (Cell.Pending). Only a frozen, non-sequential ICPC public table
	// sets it.
	Pending *Window
	// Limit is the most entries to return, in Rank's order (winner first in
	// winner mode), so a cut never removes the top.
	Limit int
}

// Window is the half-open span [From, Until).
type Window struct {
	From  time.Time
	Until time.Time
}

type Repository interface {
	Standings(ctx context.Context, q Query) ([]Entry, error)
	// ICPCStandings is Standings for an ICPC table. The grid comes from the
	// same statement as the entries, so both describe one moment even while
	// a question's visibility changes.
	ICPCStandings(ctx context.Context, q Query) ([]Entry, Grid, error)
	// MarkRevealed records the reveal once, returning the moment in force
	// and whether this call set it.
	MarkRevealed(ctx context.Context, contestID uuid.UUID, at time.Time) (time.Time, bool, error)
}

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
	// LiveCacheTTL defaults to DefaultLiveCacheTTL.
	LiveCacheTTL time.Duration
}

type View struct {
	Contest contests.Contest
	Decision
	// GeneratedAt is when the table was computed, never the latest answer's
	// time, which during a freeze would leak that something changed.
	GeneratedAt time.Time
	Truncated   bool
	// Questions is the ICPC grid's width; zero in other modes.
	Questions int
	Rows      []Row
}

// StaffView is the live table, with what everybody else is shown.
type StaffView struct {
	Contest     contests.Contest
	Shown       Decision
	GeneratedAt time.Time
	Truncated   bool
	Questions   int
	Rows        []Row
}

type Service struct {
	contests     ContestReader
	participants ParticipantReader
	standings    Repository
	audit        *audit.Recorder
	uow          storage.UnitOfWork
	now          func() time.Time
	ttl          time.Duration
	liveTTL      time.Duration
	maxRows      int

	mu    sync.Mutex
	cache map[uuid.UUID]cached

	liveMu    sync.Mutex
	liveCache map[uuid.UUID]cachedLive

	flight singleflight.Group

	// genMu and generations stop a computation still running across a
	// Reveal from caching its stale answer (see Reveal).
	genMu       sync.Mutex
	generations map[uuid.UUID]uint64
}

type cached struct {
	view    View
	expires time.Time
}

type cachedLive struct {
	view    StaffView
	expires time.Time
}

func NewService(cfg Config) *Service {
	s := &Service{
		contests: cfg.Contests, participants: cfg.Participants, standings: cfg.Standings,
		audit: cfg.Audit, uow: cfg.UnitOfWork, now: cfg.Now, ttl: cfg.CacheTTL, liveTTL: cfg.LiveCacheTTL, maxRows: cfg.MaxRows,
		cache: make(map[uuid.UUID]cached), liveCache: make(map[uuid.UUID]cachedLive),
		generations: make(map[uuid.UUID]uint64),
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.ttl <= 0 {
		s.ttl = DefaultCacheTTL
	}
	if s.liveTTL <= 0 {
		s.liveTTL = DefaultLiveCacheTTL
	}
	if s.maxRows <= 0 {
		s.maxRows = DefaultMaxRows
	}
	return s
}

// Public returns the table anybody may see, from the shared cache while it is
// still true. Only real, non-draft contests are cached, so the cache is
// bounded by the number of contests, not by identifiers a caller invents.
// Concurrent misses share one computation.
func (s *Service) Public(ctx context.Context, contestID uuid.UUID) (View, error) {
	now := s.now()
	if view, ok := s.fromCache(contestID, now); ok {
		return view, nil
	}

	gen := s.generationOf(contestID)
	result, err := flight.Do(ctx, &s.flight, fmt.Sprintf("public:%s@%d", contestID, gen), computeTimeout, func(ctx context.Context) (any, error) {
		return s.computePublic(ctx, contestID, now)
	})
	if err != nil {
		return View{}, err
	}
	view := result.(View)
	// Expiry counts from GeneratedAt, not this caller's clock: a follower
	// that joined late would otherwise stretch the TTL.
	s.storeIfCurrent(contestID, gen, view, s.expiry(view.Contest, view.Decision, view.GeneratedAt))
	return view, nil
}

func (s *Service) computePublic(ctx context.Context, contestID uuid.UUID, now time.Time) (View, error) {
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
		q := Query{ContestID: c.ID, Cutoff: decision.Cutoff, Scoring: c.Scoring}
		// A frozen ICPC table shows how many attempts came after the freeze.
		// Not under sequential progression: there an attempt on B reveals
		// that A was closed, so the attempt's existence is itself a result.
		if decision.State == StateFrozen && c.Scoring == contests.ScoringICPC && !c.SequentialActive() {
			q.Pending = &Window{From: *decision.FrozenAt, Until: now}
		}
		t, err := s.rank(ctx, q)
		if err != nil {
			return View{}, err
		}
		view.Rows, view.Truncated, view.Questions = t.rows, t.truncated, t.questions
	}
	return view, nil
}

// ForParticipant returns the public table and the caller's own registration,
// so the caller can find their row; nothing else differs from the public view.
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
// disqualified on it. It is cached only briefly, and Reveal clears it so staff
// see the reveal at once.
func (s *Service) Live(ctx context.Context, contestID uuid.UUID) (StaffView, error) {
	now := s.now()
	if view, ok := s.fromLiveCache(contestID, now); ok {
		return view, nil
	}

	gen := s.generationOf(contestID)
	result, err := flight.Do(ctx, &s.flight, fmt.Sprintf("live:%s@%d", contestID, gen), computeTimeout, func(ctx context.Context) (any, error) {
		return s.computeLive(ctx, contestID, now)
	})
	if err != nil {
		return StaffView{}, err
	}
	view := result.(StaffView)
	// Expiry counts from GeneratedAt, as in Public.
	s.storeLiveIfCurrent(contestID, gen, view, view.GeneratedAt.Add(s.liveTTL))
	return view, nil
}

func (s *Service) computeLive(ctx context.Context, contestID uuid.UUID, now time.Time) (StaffView, error) {
	c, err := s.contests.ByID(ctx, contestID)
	if errors.Is(err, contests.ErrNotFound) {
		return StaffView{}, ErrNotFound
	}
	if err != nil {
		return StaffView{}, fmt.Errorf("look up the contest: %w", err)
	}

	shown, err := Decide(c, now)
	if err != nil {
		// A draft: nothing to rank.
		return StaffView{Contest: c, Shown: Decision{State: StateNotStarted, Cutoff: now}, GeneratedAt: now, Rows: []Row{}}, nil
	}
	// Never Pending: the staff table shows the results themselves.
	t, err := s.rank(ctx, Query{
		ContestID: c.ID, Cutoff: now, Scoring: c.Scoring, IncludeDisqualified: true,
	})
	if err != nil {
		return StaffView{}, err
	}
	return StaffView{
		Contest: c, Shown: shown, GeneratedAt: now, Truncated: t.truncated, Questions: t.questions, Rows: t.rows,
	}, nil
}

// Reveal opens a frozen table's final state to everybody, once. It is refused
// before the finish and for a contest never frozen. A second reveal returns
// the moment already in force and records nothing.
//
// bumpGeneration drops both cached tables. Deleting entries alone is not
// enough: a computation that read the frozen contest before this call could
// still store its answer afterwards, and a new request could join its flight.
// Public and Live put the generation in their flight key and store only if
// their generation is still current, which closes both holes. Other changes
// (moving the freeze) are not hooked; the short TTLs pick them up.
func (s *Service) Reveal(ctx context.Context, actorID, contestID uuid.UUID) (time.Time, error) {
	c, err := s.contests.ByID(ctx, contestID)
	if errors.Is(err, contests.ErrNotFound) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("look up the contest: %w", err)
	}
	if !c.Ended() {
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

	s.bumpGeneration(contestID)
	return revealedAt, nil
}

// contest looks a contest up for the public table; a draft is ErrNotFound.
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

type table struct {
	rows      []Row
	truncated bool
	questions int
}

// rank reads one entry past the bound, so truncation is known, not guessed.
func (s *Service) rank(ctx context.Context, q Query) (table, error) {
	q.Limit = s.maxRows + 1
	if q.Scoring != contests.ScoringICPC {
		entries, err := s.standings.Standings(ctx, q)
		if err != nil {
			return table{}, fmt.Errorf("read the standings: %w", err)
		}
		entries, truncated := s.cut(entries)
		return table{rows: Rank(q.Scoring, entries), truncated: truncated}, nil
	}

	entries, grid, err := s.standings.ICPCStandings(ctx, q)
	if err != nil {
		return table{}, fmt.Errorf("read the icpc standings: %w", err)
	}
	// A grid that disagrees with its rows is refused, not served misaligned.
	if len(grid.FirstSolves) != grid.Questions {
		return table{}, fmt.Errorf("the icpc grid has %d questions and %d first solves", grid.Questions, len(grid.FirstSolves))
	}
	for _, e := range entries {
		if len(e.Cells) != grid.Questions {
			return table{}, fmt.Errorf("an icpc row has %d cells on a grid of %d questions", len(e.Cells), grid.Questions)
		}
	}
	entries, truncated := s.cut(entries)
	return table{rows: RankICPC(entries, grid), truncated: truncated, questions: grid.Questions}, nil
}

func (s *Service) cut(entries []Entry) ([]Entry, bool) {
	if len(entries) > s.maxRows {
		return entries[:s.maxRows], true
	}
	return entries, false
}

// expiry is the TTL, or the freeze if the table is live and it comes sooner.
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

// store keeps a table and drops every expired entry.
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

// storeIfCurrent is store, only when gen is still the contest's current
// generation. genMu is held across the check and the write, so every call is
// ordered with bumpGeneration: either this stores first and the bump deletes
// it, or the bump comes first and gen is already stale.
func (s *Service) storeIfCurrent(id uuid.UUID, gen uint64, view View, expires time.Time) {
	s.genMu.Lock()
	defer s.genMu.Unlock()
	if s.generations[id] != gen {
		return
	}
	s.store(id, view, expires)
}

func (s *Service) fromLiveCache(id uuid.UUID, now time.Time) (StaffView, bool) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	entry, ok := s.liveCache[id]
	if !ok || !now.Before(entry.expires) {
		return StaffView{}, false
	}
	return entry.view, true
}

func (s *Service) storeLive(id uuid.UUID, view StaffView, expires time.Time) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	now := s.now()
	for key, entry := range s.liveCache {
		if !now.Before(entry.expires) {
			delete(s.liveCache, key)
		}
	}
	s.liveCache[id] = cachedLive{view: view, expires: expires}
}

// storeLiveIfCurrent is storeIfCurrent for the staff table's cache.
func (s *Service) storeLiveIfCurrent(id uuid.UUID, gen uint64, view StaffView, expires time.Time) {
	s.genMu.Lock()
	defer s.genMu.Unlock()
	if s.generations[id] != gen {
		return
	}
	s.storeLive(id, view, expires)
}

func (s *Service) generationOf(id uuid.UUID) uint64 {
	s.genMu.Lock()
	defer s.genMu.Unlock()
	return s.generations[id]
}

// bumpGeneration invalidates every cached table for a contest, holding genMu
// across the increment and both deletes.
func (s *Service) bumpGeneration(id uuid.UUID) {
	s.genMu.Lock()
	defer s.genMu.Unlock()
	s.generations[id]++
	s.mu.Lock()
	delete(s.cache, id)
	s.mu.Unlock()
	s.liveMu.Lock()
	delete(s.liveCache, id)
	s.liveMu.Unlock()
}
