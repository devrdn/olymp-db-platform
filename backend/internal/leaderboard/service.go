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
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
)

// Defaults for Config.
const (
	// DefaultCacheTTL is how long one computation of a contest's public table
	// serves every viewer. Ten seconds is invisible on a table people glance
	// at, and it caps the database at one aggregate per contest per window
	// however many anonymous viewers the public page draws.
	DefaultCacheTTL = 10 * time.Second
	// DefaultLiveCacheTTL is the same idea for the staff table, kept far
	// shorter: it exists only to collapse several staff tabs (or one tab's
	// own retries) refreshing within the same instant into one computation,
	// never to hide anything from the people running the contest.
	DefaultLiveCacheTTL = 3 * time.Second
	// DefaultMaxRows bounds a table (CLAUDE.md rule 2). An open contest can
	// collect more registrations than anybody will scroll, and a public answer
	// without a bound is an answer whose size the caller chooses.
	DefaultMaxRows = 2000
	// computeTimeout bounds one shared computation once it no longer belongs
	// to any single caller (see computeOnce). Comfortably above how long an
	// ordinary standings query takes — the core pool's own statement timeout
	// already caps the query itself — so what this actually guards against is
	// a connection acquire that never returns, not a slow but honest query.
	computeTimeout = 20 * time.Second
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
	// Pending asks for the attempts inside a window, on questions not solved
	// before the cutoff (Cell.Pending). It is set only for a frozen ICPC table
	// whose progression is not sequential, from the freeze to the moment of
	// computing, and never for the staff's table, which is cut off now and
	// sees the results themselves.
	Pending *Window
	// Limit is how many entries to return at most, in the order Rank would
	// put them — with the winner first in winner mode — so that cutting the
	// list never cuts the top of the table.
	Limit int
}

// Window is the half-open span [From, Until).
type Window struct {
	From  time.Time
	Until time.Time
}

// Repository is the storage the table needs.
type Repository interface {
	Standings(ctx context.Context, q Query) ([]Entry, error)
	// ICPCStandings is Standings for an ICPC table. The grid — its width and
	// each question's earliest solve — comes from the same statement as the
	// entries, so the letters, the cells and the marks describe one moment
	// even while an organiser changes a question's visibility.
	ICPCStandings(ctx context.Context, q Query) ([]Entry, Grid, error)
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
	// LiveCacheTTL defaults to DefaultLiveCacheTTL.
	LiveCacheTTL time.Duration
}

// View is the table as everybody but the staff sees it.
type View struct {
	Contest contests.Contest
	Decision
	// GeneratedAt is when the table was computed — never the time of the
	// latest answer, which during a freeze would say that something changed.
	GeneratedAt time.Time
	Truncated   bool
	// Questions is how many visible questions the ICPC grid has; zero in
	// every other mode.
	Questions int
	Rows      []Row
}

// StaffView is the live table, with what everybody else is being shown.
type StaffView struct {
	Contest     contests.Contest
	Shown       Decision
	GeneratedAt time.Time
	Truncated   bool
	Questions   int
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
	liveTTL      time.Duration
	maxRows      int

	mu    sync.Mutex
	cache map[uuid.UUID]cached

	liveMu    sync.Mutex
	liveCache map[uuid.UUID]cachedLive

	// flight collapses concurrent misses of the same key — a contest's public
	// table or its staff table, computeOnce's own callers tell the two apart
	// by key — into one call to the repository. See computeOnce for why a
	// panic or a caller's cancellation cannot wedge or narrow it.
	flight singleflight.Group

	// genMu and generations guard against a computation that is still running
	// when Reveal invalidates its contest: see Public, Live and Reveal's own
	// docs for why a generation has to sit between the cache and the flight,
	// not only inside the cache.
	genMu       sync.Mutex
	generations map[uuid.UUID]uint64
}

// cached is one contest's public table and the moment it stops being true.
type cached struct {
	view    View
	expires time.Time
}

// cachedLive is cached for the staff table.
type cachedLive struct {
	view    StaffView
	expires time.Time
}

// NewService returns a Service.
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

// Public returns the table anybody may see, from the shared cache when it is
// still true.
//
// Only a contest that exists and is past its draft is ever cached, so the
// number of entries is bounded by the number of real contests, never by how
// many identifiers a caller can invent; a refusal is not cached and costs
// what the HTTP layer's per-address limit lets it cost.
//
// A miss is shared rather than repeated: however many viewers arrive in the
// same instant — an audience refreshing together right on the cache's own
// boundary — computeOnce collapses them into the one computation the first of
// them started (see its own doc for what that guarantees each caller).
//
// The generation read below is what keeps a Reveal racing this call honest:
// see the package doc on generations for what it buys and why the cache's
// own delete is not enough by itself.
func (s *Service) Public(ctx context.Context, contestID uuid.UUID) (View, error) {
	now := s.now()
	if view, ok := s.fromCache(contestID, now); ok {
		return view, nil
	}

	gen := s.generationOf(contestID)
	result, err := s.computeOnce(ctx, fmt.Sprintf("public:%s@%d", contestID, gen), func(ctx context.Context) (any, error) {
		return s.computePublic(ctx, contestID, now)
	})
	if err != nil {
		return View{}, err
	}
	view := result.(View)
	// The expiry is anchored to when the view was actually generated, not to
	// this caller's own clock read: a follower that joined the flight late
	// reads its own, later now here, and basing the expiry on that would
	// stretch the cache past what the leader's own TTL promised.
	s.storeIfCurrent(contestID, gen, view, s.expiry(view.Contest, view.Decision, view.GeneratedAt))
	return view, nil
}

// computePublic is Public's actual computation, run at most once per miss
// (see computeOnce) rather than once per caller.
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
		// The one thing a frozen ICPC table tells about the time since the
		// freeze: how many attempts there were, never what came of them.
		//
		// Not under sequential progression. There a question opens only once
		// the one before it is closed — solved, or every attempt spent — so
		// an attempt on B after the freeze says A was closed after it, and A
		// pending with attempts still left says A was solved. Whether an
		// attempt exists is itself the result there, and the table shows only
		// what was true at the freeze.
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
// disqualified on it.
//
// Cached for DefaultLiveCacheTTL — a handful of staff refreshing their
// dashboard together should not each recompute the same heavy query, but the
// window has to stay short: a stale staff table is the one place a freeze
// could hide something from the people who run the contest. Reveal clears
// this cache explicitly (see its own doc) rather than waiting the TTL out,
// because that is the one moment a staler answer would say the wrong thing
// about whether the result is out yet.
func (s *Service) Live(ctx context.Context, contestID uuid.UUID) (StaffView, error) {
	now := s.now()
	if view, ok := s.fromLiveCache(contestID, now); ok {
		return view, nil
	}

	gen := s.generationOf(contestID)
	result, err := s.computeOnce(ctx, fmt.Sprintf("live:%s@%d", contestID, gen), func(ctx context.Context) (any, error) {
		return s.computeLive(ctx, contestID, now)
	})
	if err != nil {
		return StaffView{}, err
	}
	view := result.(StaffView)
	// Anchored to the view's own GeneratedAt, not this caller's now — see
	// Public's identical comment.
	s.storeLiveIfCurrent(contestID, gen, view, view.GeneratedAt.Add(s.liveTTL))
	return view, nil
}

// computeLive is Live's actual computation, run at most once per miss (see
// computeOnce) rather than once per staff request.
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
		// A draft: nobody else sees anything, and there is nothing to rank.
		return StaffView{Contest: c, Shown: Decision{State: StateNotStarted, Cutoff: now}, GeneratedAt: now, Rows: []Row{}}, nil
	}
	// Never Pending: cut off now, the staff table shows the results themselves.
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

// Reveal opens a frozen table's final state to everybody, once.
//
// Refused before the finish, because revealing a running contest's table is
// turning the freeze off, and refused for a contest that was never frozen,
// because it has nothing to reveal. A second reveal is not an error: it
// returns the moment already in force and records nothing, so two organisers
// pressing the button together see the same result.
//
// bumpGeneration below drops both of the contest's cached tables, not only
// the public one: the staff table's own Shown field reports the same
// decision the public table does, so a frozen copy left in the live cache
// would tell staff the reveal has not happened for as long as that cache's
// TTL, right when the two must agree. Nothing else invalidates either cache
// — a settings change (moving the freeze, say) is not an event this package
// hooks, because both caches are short enough that such a change is visible
// again within their own TTL regardless, the same guarantee they already
// give every other caller.
//
// Deleting the cache entries is not enough by itself: a computation that
// started before this call — it already read the old, frozen contest and is
// simply slow to finish, most likely inside the repository — can still
// return afterwards and store that stale answer right back into the cache
// this call just cleared, and a request that arrives after this call but
// before that slow one finishes could join its in-flight singleflight call
// (keyed, until now, by nothing but the contest id) and be handed the same
// stale answer. bumpGeneration closes both holes: Public and Live fold the
// current generation into their singleflight key, so a request arriving
// after this call starts a flight of its own rather than joining a stale
// one, and each stores its result only if the generation it captured before
// starting is still current — so the slow computation above finds the
// generation has moved and discards its own answer instead of caching it.
// Any other event that should invalidate a contest's tables the same way
// belongs on the same call.
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

	s.bumpGeneration(contestID)
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

// table is one computation of the rows.
type table struct {
	rows      []Row
	truncated bool
	// questions is the ICPC grid's width, zero in every other mode.
	questions int
}

// rank reads one more entry than the bound, so "there are more" is a fact
// rather than the guess len == limit would be.
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
	// One statement makes these agree; a grid that does not is refused rather
	// than served with letters that name the wrong columns.
	if len(grid.FirstSolves) != grid.Questions {
		return table{}, fmt.Errorf("the icpc grid has %d questions and %d first solves", grid.Questions, len(grid.FirstSolves))
	}
	for _, e := range entries {
		if len(e.Cells) != grid.Questions {
			return table{}, fmt.Errorf("an icpc row has %d cells on a grid of %d questions", len(e.Cells), grid.Questions)
		}
	}
	// The marks come from the grid, so cutting the rows cannot move them.
	entries, truncated := s.cut(entries)
	return table{rows: RankICPC(entries, grid), truncated: truncated, questions: grid.Questions}, nil
}

func (s *Service) cut(entries []Entry) ([]Entry, bool) {
	if len(entries) > s.maxRows {
		return entries[:s.maxRows], true
	}
	return entries, false
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

// storeIfCurrent is store, but only when gen — the generation Public captured
// before starting the computation — is still the contest's current one.
//
// Checking and writing are one step under genMu, not two: a check that ran,
// found itself current, and only afterwards raced a concurrent bumpGeneration
// to the actual write would be exactly the hole this exists to close, just
// narrowed rather than removed. Held across the write, genMu forces every
// call here into a total order with every bumpGeneration: either this runs
// first and stores, and the bump's own delete (also under genMu, see its
// doc) removes it right after, or the bump runs first and this call's
// generation is already stale by the time it checks. Either way nothing
// this stores can outlive the invalidation that raced it.
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

// storeLive is store for the staff table's own, shorter-lived cache.
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

// storeLiveIfCurrent is storeIfCurrent for the staff table's cache — see its
// doc for why genMu has to be held across the check and the write together.
func (s *Service) storeLiveIfCurrent(id uuid.UUID, gen uint64, view StaffView, expires time.Time) {
	s.genMu.Lock()
	defer s.genMu.Unlock()
	if s.generations[id] != gen {
		return
	}
	s.storeLive(id, view, expires)
}

// generationOf is the contest's current generation: every increment
// (bumpGeneration) invalidates whatever a computation captured before it.
func (s *Service) generationOf(id uuid.UUID) uint64 {
	s.genMu.Lock()
	defer s.genMu.Unlock()
	return s.generations[id]
}

// bumpGeneration invalidates every cached table for a contest as one step:
// held across the increment and both deletes, genMu forces every concurrent
// storeIfCurrent/storeLiveIfCurrent into a strict before-or-after order with
// this call (see their own docs) — the ordinary map deletes elsewhere in
// this file are not, by themselves, enough to make an invalidation stick
// against a computation that is still in flight.
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

// computeOnce collapses every concurrent miss for one key into a single call
// to fn, so however many callers arrive while a table is being computed, the
// repository is asked once and all of them read the same answer.
//
// fn runs detached from any one caller's context (context.WithoutCancel),
// bounded instead by computeTimeout: the computation belongs to the key, not
// to whichever caller's request happened to start it, so one caller giving up
// must not cut short an answer the callers behind it are still waiting for.
// Each caller of computeOnce still honours its own context — it stops
// waiting the moment ctx is done, without touching the flight it joined.
//
// A panic inside fn is recovered into an error rather than left to
// singleflight's own handling, which — when a call has joiners — re-panics
// on a fresh, unrecovered goroutine specifically so the crash cannot be
// swallowed. That is the right choice for a bug an operator must see, but the
// wrong one for a single bad computation to cost the whole process; recovery
// here turns it into an ordinary refusal instead. Either way, singleflight
// forgets a key the moment its call returns — before any of it is reported
// back — so a failed or recovered computation is never cached, and the very
// next call for the same key starts a fresh one rather than waiting behind a
// key that could never resolve.
func (s *Service) computeOnce(ctx context.Context, key string, fn func(ctx context.Context) (any, error)) (any, error) {
	ch := s.flight.DoChan(key, func() (result any, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("compute %s: %v", key, r)
			}
		}()
		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), computeTimeout)
		defer cancel()
		return fn(flightCtx)
	})

	select {
	case res := <-ch:
		return res.Val, res.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
