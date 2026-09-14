package leaderboard_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
)

// standings is the storage double: it answers every query with the same
// entries and remembers what it was asked.
type standings struct {
	contests *conteststest.Contests
	entries  []leaderboard.Entry
	// release, when set, is waited on inside a read before it returns — a
	// test's way of holding the single flight's leader in place long enough
	// to prove what joins it and what a cancelled waiter does not disturb.
	release <-chan struct{}
	// panicOnce makes the first read panic instead of answering, to prove a
	// panicking leader does not wedge the key for the call after it.
	panicOnce bool

	mu      sync.Mutex
	queries []leaderboard.Query
	// grid is what ICPCStandings answers beside the entries; icpcReads counts
	// its calls.
	grid      leaderboard.Grid
	icpcReads int
}

func (s *standings) Standings(_ context.Context, q leaderboard.Query) ([]leaderboard.Entry, error) {
	s.record(q)
	s.maybePanic()
	s.wait()
	return s.cut(q), nil
}

func (s *standings) maybePanic() {
	s.mu.Lock()
	should := s.panicOnce
	s.panicOnce = false
	s.mu.Unlock()
	if should {
		panic("the fake repository was told to panic once")
	}
}

func (s *standings) ICPCStandings(_ context.Context, q leaderboard.Query) ([]leaderboard.Entry, leaderboard.Grid, error) {
	s.mu.Lock()
	s.icpcReads++
	s.mu.Unlock()
	s.record(q)
	s.wait()
	return s.cut(q), s.grid, nil
}

func (s *standings) record(q leaderboard.Query) {
	s.mu.Lock()
	s.queries = append(s.queries, q)
	s.mu.Unlock()
}

func (s *standings) wait() {
	if s.release != nil {
		<-s.release
	}
}

// callCount is queries read under the same lock its writers use, so a test
// synchronising through a WaitGroup or a channel (rather than through this
// lock) still reads a value the writes happened before.
func (s *standings) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queries)
}

func (s *standings) cut(q leaderboard.Query) []leaderboard.Entry {
	out := s.entries
	if !q.IncludeDisqualified {
		out = nil
		for _, e := range s.entries {
			if !e.Disqualified {
				out = append(out, e)
			}
		}
	}
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out
}

func (s *standings) MarkRevealed(ctx context.Context, contestID uuid.UUID, at time.Time) (time.Time, bool, error) {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return time.Time{}, false, err
	}
	if c.LeaderboardRevealedAt != nil {
		return *c.LeaderboardRevealedAt, false, nil
	}
	c.LeaderboardRevealedAt = &at
	s.contests.Put(c)
	return at, true, nil
}

type rig struct {
	service   *leaderboard.Service
	contests  *conteststest.Contests
	people    *conteststest.Registrations
	standings *standings
	sink      *conteststest.Sink
	now       time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	f := conteststest.NewFixture()
	r := &rig{contests: f.Contests, people: f.Registrations, sink: conteststest.NewSink(), now: start.Add(time.Hour)}
	r.standings = &standings{contests: f.Contests}
	r.service = leaderboard.NewService(leaderboard.Config{
		Contests:     f.Contests,
		Participants: f.Registrations,
		Standings:    r.standings,
		Audit:        audit.New(r.sink),
		UnitOfWork:   f.UnitOfWork,
		Now:          func() time.Time { return r.now },
		CacheTTL:     10 * time.Second,
		MaxRows:      2000,
	})
	return r
}

func (r *rig) seed(status string, freezeMin *int) contests.Contest {
	return r.contests.Put(contest(status, freezeMin, nil))
}

func TestAFrozenTableIsCutOffAtTheFreezeAndLeavesTheDisqualifiedOut(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, minutes(30))
	r.now = end.Add(-10 * time.Minute)

	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("Public() = %v", err)
	}
	if view.State != leaderboard.StateFrozen {
		t.Errorf("State = %s, want frozen", view.State)
	}
	q := r.standings.queries[0]
	if want := end.Add(-30 * time.Minute); !q.Cutoff.Equal(want) || q.IncludeDisqualified {
		t.Errorf("query = %+v, want cutoff %v without the disqualified", q, want)
	}
}

func TestATableThatHasNotStartedReadsNothing(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusPublished, nil)

	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil || view.State != leaderboard.StateNotStarted || len(view.Rows) != 0 {
		t.Fatalf("Public() = %+v, %v, want an empty not_started table", view, err)
	}
	if len(r.standings.queries) != 0 {
		t.Errorf("standings were read %d times, want none", len(r.standings.queries))
	}
}

func TestADraftAndAMissingContestAreTheSameNotFound(t *testing.T) {
	r := newRig(t)
	draft := r.seed(contests.StatusDraft, nil)

	for _, id := range []uuid.UUID{draft.ID, uuid.New()} {
		if _, err := r.service.Public(context.Background(), id); !errors.Is(err, leaderboard.ErrNotFound) {
			t.Errorf("Public(%s) = %v, want ErrNotFound", id, err)
		}
	}
}

// However many people watch, the database computes a contest's table once
// per TTL.
func TestOneComputationServesEveryViewerWithinTheTTL(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, nil)

	for range 3 {
		if _, err := r.service.Public(context.Background(), c.ID); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.standings.queries) != 1 {
		t.Fatalf("standings were read %d times within the TTL, want 1", len(r.standings.queries))
	}

	r.now = r.now.Add(10 * time.Second)
	if _, err := r.service.Public(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.standings.queries) != 2 {
		t.Errorf("standings were read %d times after the TTL, want 2", len(r.standings.queries))
	}
}

// A live table cached a second before the freeze must not be served after it,
// or the table would still read "live" into the freeze.
func TestALiveTableIsNotServedPastTheFreeze(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, minutes(30))
	freezeAt := end.Add(-30 * time.Minute)

	r.now = freezeAt.Add(-time.Second)
	if _, err := r.service.Public(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	r.now = freezeAt
	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != leaderboard.StateFrozen || len(r.standings.queries) != 2 {
		t.Errorf("State = %s after %d reads, want frozen after 2", view.State, len(r.standings.queries))
	}
}

func TestAParticipantSeesTheSharedTableAndWhichRowIsTheirs(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, nil)
	user := uuid.New()
	me, err := r.people.Add(context.Background(), c.ID, user)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.service.Public(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	view, own, err := r.service.ForParticipant(context.Background(), c.ID, user)
	if err != nil {
		t.Fatalf("ForParticipant() = %v", err)
	}
	if own != me.ID || view.State != leaderboard.StateLive {
		t.Errorf("own = %s, state = %s; want %s, live", own, view.State, me.ID)
	}
	if len(r.standings.queries) != 1 {
		t.Errorf("standings were read %d times, want the participant to share the public computation", len(r.standings.queries))
	}

	if _, _, err := r.service.ForParticipant(context.Background(), c.ID, uuid.New()); !errors.Is(err, leaderboard.ErrNotAParticipant) {
		t.Errorf("ForParticipant(stranger) = %v, want ErrNotAParticipant", err)
	}
}

// The staff table is the one that ignores the freeze, and it says what
// everybody else is being shown.
func TestTheStaffTableIsLiveAndSaysWhatOthersSee(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, minutes(30))
	r.now = end.Add(-10 * time.Minute)

	view, err := r.service.Live(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("Live() = %v", err)
	}
	q := r.standings.queries[0]
	if !q.Cutoff.Equal(r.now) || !q.IncludeDisqualified {
		t.Errorf("query = %+v, want cutoff now including the disqualified", q)
	}
	if view.Shown.State != leaderboard.StateFrozen {
		t.Errorf("Shown.State = %s, want frozen", view.Shown.State)
	}
}

func TestRevealRefusesWhatHasNothingToReveal(t *testing.T) {
	r := newRig(t)
	running := r.seed(contests.StatusRunning, minutes(30))
	unfrozen := r.seed(contests.StatusFinished, nil)

	for _, id := range []uuid.UUID{running.ID, unfrozen.ID} {
		if _, err := r.service.Reveal(context.Background(), uuid.New(), id); !errors.Is(err, leaderboard.ErrNotRevealable) {
			t.Errorf("Reveal(%s) = %v, want ErrNotRevealable", id, err)
		}
	}
	if len(r.sink.Entries) != 0 {
		t.Errorf("audit entries = %d, want none for a refusal", len(r.sink.Entries))
	}
}

func TestRevealOpensTheFinalTableAndIsRecordedOnce(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusFinished, minutes(30))
	r.now = end.Add(time.Hour)

	// Warm the cache with the frozen table, which the reveal must replace.
	if view, _ := r.service.Public(context.Background(), c.ID); view.State != leaderboard.StateFrozen {
		t.Fatalf("setup: State = %s, want frozen", view.State)
	}

	first, err := r.service.Reveal(context.Background(), uuid.New(), c.ID)
	if err != nil {
		t.Fatalf("Reveal() = %v", err)
	}
	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil || view.State != leaderboard.StateFinal {
		t.Fatalf("after reveal Public() = %s, %v, want final", view.State, err)
	}

	r.now = r.now.Add(time.Minute)
	second, err := r.service.Reveal(context.Background(), uuid.New(), c.ID)
	if err != nil || !second.Equal(first) {
		t.Errorf("second Reveal() = %v, %v, want the first moment %v", second, err, first)
	}
	if len(r.sink.Entries) != 1 || r.sink.Entries[0].Action != audit.ActionContestLeaderboardReveal {
		t.Fatalf("audit entries = %+v, want exactly one reveal", r.sink.Entries)
	}
	if len(r.sink.Loose) != 0 {
		t.Errorf("the reveal was recorded outside its transaction")
	}
}

func TestATableLongerThanTheBoundSaysItWasCut(t *testing.T) {
	r := newRig(t)
	r.service = leaderboard.NewService(leaderboard.Config{
		Contests: r.contests, Participants: r.people, Standings: r.standings,
		Audit: audit.New(r.sink), UnitOfWork: &conteststest.UnitOfWork{},
		Now: func() time.Time { return r.now }, CacheTTL: time.Second, MaxRows: 2,
	})
	c := r.seed(contests.StatusRunning, nil)
	r.standings.entries = []leaderboard.Entry{entry("a", 3, at(1)), entry("b", 2, at(2)), entry("c", 1, at(3))}

	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Truncated || len(view.Rows) != 2 {
		t.Errorf("Truncated = %v with %d rows, want true with 2", view.Truncated, len(view.Rows))
	}
}

func (r *rig) seedICPC(status string, freezeMin *int) contests.Contest {
	c := contest(status, freezeMin, nil)
	c.Scoring = contests.ScoringICPC
	return r.contests.Put(c)
}

// Pending attempts are the one thing a frozen ICPC table tells after the
// freeze, so they are asked for exactly there: from the freeze to the moment
// of computing, by the public table and the participant's copy of it.
func TestAFrozenICPCTableAsksForTheAttemptsSinceTheFreeze(t *testing.T) {
	r := newRig(t)
	r.standings.grid = leaderboard.Grid{Questions: 3, FirstSolves: make([]*time.Time, 3)}
	c := r.seedICPC(contests.StatusRunning, minutes(30))
	user := uuid.New()
	if _, err := r.people.Add(context.Background(), c.ID, user); err != nil {
		t.Fatal(err)
	}
	r.now = end.Add(-10 * time.Minute)
	freezeAt := end.Add(-30 * time.Minute)

	view, _, err := r.service.ForParticipant(context.Background(), c.ID, user)
	if err != nil {
		t.Fatalf("ForParticipant() = %v", err)
	}
	q := r.standings.queries[0]
	if q.Pending == nil || !q.Pending.From.Equal(freezeAt) || !q.Pending.Until.Equal(r.now) || !q.Cutoff.Equal(freezeAt) {
		t.Fatalf("query = %+v, want the cutoff and pending window from %v to %v", q, freezeAt, r.now)
	}
	if view.Questions != 3 {
		t.Errorf("Questions = %d, want 3", view.Questions)
	}
}

// Sequential progression opens a question only once the one before it is
// closed, so a pending attempt on a later question would say that the earlier
// one was closed after the freeze. A frozen sequential ICPC table asks for no
// pending attempts; the same table under free progression still does.
func TestAFrozenSequentialICPCTableAsksForNoPendingAttempts(t *testing.T) {
	r := newRig(t)
	r.standings.grid = leaderboard.Grid{Questions: 2, FirstSolves: make([]*time.Time, 2)}
	sequential := contest(contests.StatusRunning, minutes(30), nil)
	sequential.Scoring = contests.ScoringICPC
	sequential.QuestionMode, sequential.Progression = contests.QuestionModeMulti, contests.ProgressionSequential
	sequential = r.contests.Put(sequential)
	free := contest(contests.StatusRunning, minutes(30), nil)
	free.Scoring = contests.ScoringICPC
	free.QuestionMode, free.Progression = contests.QuestionModeMulti, contests.ProgressionFree
	free = r.contests.Put(free)
	r.now = end.Add(-10 * time.Minute)

	if _, err := r.service.Public(context.Background(), sequential.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.Public(context.Background(), free.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.standings.queries) != 2 {
		t.Fatalf("queries = %d, want 2", len(r.standings.queries))
	}
	if q := r.standings.queries[0]; q.Pending != nil {
		t.Errorf("the sequential table asks for pending attempts: %+v", q.Pending)
	}
	if q := r.standings.queries[1]; q.Pending == nil {
		t.Error("the free-progression table asks for no pending attempts, want the window since the freeze")
	}
}

// Nothing else asks for pending attempts: not a live or a final table, where
// the cutoff is now and a result is simply shown, and never the staff table,
// which is cut off now whatever the freeze and sees the result itself.
func TestOnlyAFrozenPublicTableAsksForPendingAttempts(t *testing.T) {
	r := newRig(t)
	live := r.seedICPC(contests.StatusRunning, nil)
	final := r.seedICPC(contests.StatusFinished, nil)
	frozen := r.seedICPC(contests.StatusRunning, minutes(30))
	r.now = end.Add(-10 * time.Minute)

	for _, id := range []uuid.UUID{live.ID, final.ID} {
		if _, err := r.service.Public(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.service.Live(context.Background(), frozen.ID); err != nil {
		t.Fatal(err)
	}
	for _, q := range r.standings.queries {
		if q.Pending != nil {
			t.Errorf("query %+v asks for pending attempts", q)
		}
	}
}

// The points table does not read the ICPC standings: its response has no grid.
func TestAPointsTableReadsNoGrid(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, minutes(30))
	r.now = end.Add(-10 * time.Minute)

	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.standings.icpcReads != 0 || view.Questions != 0 || r.standings.queries[0].Pending != nil {
		t.Errorf("ICPC read %d times, Questions = %d, Pending = %v; want none of them",
			r.standings.icpcReads, view.Questions, r.standings.queries[0].Pending)
	}
}

// The first-solver mark survives the row bound: the question's first solver
// is cut off the table, and nobody left on it is marked in their place.
func TestAnICPCFirstSolverMarkSurvivesTheRowBound(t *testing.T) {
	r := newRig(t)
	r.service = leaderboard.NewService(leaderboard.Config{
		Contests: r.contests, Participants: r.people, Standings: r.standings,
		Audit: audit.New(r.sink), UnitOfWork: &conteststest.UnitOfWork{},
		Now: func() time.Time { return r.now }, CacheTTL: time.Second, MaxRows: 2,
	})
	c := r.seedICPC(contests.StatusRunning, nil)
	r.standings.entries = []leaderboard.Entry{
		icpcEntry("top", 2, 40, at(30), solvedCell(at(10)), solvedCell(at(30))),
		icpcEntry("second", 2, 60, at(40), solvedCell(at(20)), solvedCell(at(40))),
		icpcEntry("cut", 1, 5, at(5), solvedCell(at(5)), leaderboard.Cell{}),
	}
	r.standings.grid = grid(at(5), at(30))

	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Truncated || len(view.Rows) != 2 || view.Questions != 2 {
		t.Fatalf("Truncated = %v, rows = %d, Questions = %d; want true, 2, 2", view.Truncated, len(view.Rows), view.Questions)
	}
	want := map[string][]bool{"top": {false, true}, "second": {false, false}}
	got := firstMarks(view.Rows)
	for login, marks := range want {
		if !sameMarks(got[login], marks) {
			t.Errorf("%s first marks = %v, want %v", login, got[login], marks)
		}
	}
}

// The question letters and every row's cells come from one computation and
// must agree; a grid that does not is refused rather than served misaligned.
func TestAnICPCGridWhoseRowsDoNotMatchItsWidthIsRefused(t *testing.T) {
	r := newRig(t)
	c := r.seedICPC(contests.StatusRunning, nil)
	r.standings.entries = []leaderboard.Entry{icpcEntry("short", 0, 0, nil, leaderboard.Cell{})}
	r.standings.grid = grid(nil, nil)

	if _, err := r.service.Public(context.Background(), c.ID); err == nil {
		t.Error("Public() served a row of 1 cell on a grid of 2 questions")
	}
}

// waitForCallCount polls until the fake has recorded n reads, or fails the
// test. It exists because a repository call the test means to hold open is
// started on a goroutine the test does not otherwise synchronise with —
// spinning on the fake's own counter is the one thing that tells the test the
// call has actually begun (and, since fn's registration with the single
// flight happens before it is ever called, that any waiter started from this
// point on is joining rather than racing it).
func waitForCallCount(t *testing.T, s *standings, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if s.callCount() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the repository was not called %d time(s) within a second (got %d)", n, s.callCount())
}

// Concurrent misses on the same contest must not each recompute the table:
// the whole point of the shared cache is one aggregate per contest per
// window, however many people are asking at once.
func TestConcurrentMissesOnTheSameContestShareOneComputation(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, nil)
	release := make(chan struct{})
	r.standings.release = release

	// The leader: started first and alone, so it is certainly the one that
	// reaches the repository below.
	leaderDone := make(chan struct{})
	var leaderView leaderboard.View
	var leaderErr error
	go func() {
		leaderView, leaderErr = r.service.Public(context.Background(), c.ID)
		close(leaderDone)
	}()
	waitForCallCount(t, r.standings, 1)

	// Every one of these joins the same in-flight computation rather than
	// starting its own, because the repository has not answered yet and the
	// leader's key is still in flight.
	const followers = 4
	var wg sync.WaitGroup
	results := make([]leaderboard.View, followers)
	errs := make([]error, followers)
	wg.Add(followers)
	for i := range followers {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = r.service.Public(context.Background(), c.ID)
		}(i)
	}
	// x/sync/singleflight's own tests use the same allowance to let
	// concurrently started goroutines reach Do before the flight is released
	// (singleflight_test.go, TestDoDupSuppress) — joining a flight is a
	// couple of uncontended mutex operations, so this is generous rather than
	// exact.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	<-leaderDone

	if leaderErr != nil {
		t.Fatalf("the leader's Public() = %v", leaderErr)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("follower %d Public() = %v", i, err)
		}
		if results[i].State != leaderboard.StateLive || results[i].GeneratedAt != leaderView.GeneratedAt {
			t.Errorf("follower %d view = %+v, want the leader's own %+v", i, results[i], leaderView)
		}
	}
	if got := r.standings.callCount(); got != 1 {
		t.Errorf("the repository was called %d times, want 1", got)
	}
}

// A waiter that gives up must not wait for the flight it joined, and must
// not take the flight down with it: the computation belongs to the key, not
// to whichever caller happened to start it.
func TestACancelledWaiterDoesNotDisturbTheFlightItJoined(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, nil)
	release := make(chan struct{})
	r.standings.release = release

	leaderDone := make(chan struct{})
	var leaderView leaderboard.View
	var leaderErr error
	go func() {
		leaderView, leaderErr = r.service.Public(context.Background(), c.ID)
		close(leaderDone)
	}()
	waitForCallCount(t, r.standings, 1)

	waiterCtx, cancel := context.WithCancel(context.Background())
	waiterErr := make(chan error, 1)
	go func() {
		_, err := r.service.Public(waiterCtx, c.ID)
		waiterErr <- err
	}()
	time.Sleep(20 * time.Millisecond) // let the waiter join the leader's flight.
	cancel()

	select {
	case err := <-waiterErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the cancelled waiter's Public() = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the cancelled waiter never returned")
	}

	// The cancellation must not have reached the leader: it is still blocked
	// in the repository, exactly as if the waiter had never joined.
	select {
	case <-leaderDone:
		t.Fatal("the leader finished before being released — the waiter's cancellation reached it")
	default:
	}

	close(release)
	select {
	case <-leaderDone:
	case <-time.After(time.Second):
		t.Fatal("the leader never finished once released")
	}
	if leaderErr != nil || leaderView.State != leaderboard.StateLive {
		t.Fatalf("the leader's Public() = %+v, %v, want a live view", leaderView, leaderErr)
	}
	if got := r.standings.callCount(); got != 1 {
		t.Errorf("the repository was called %d times, want 1", got)
	}
}

// A leader that panics must not wedge the key: the next call has to try
// again rather than hang behind a flight that can never finish.
func TestAPanickingLeaderDoesNotWedgeLaterCalls(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, nil)
	r.standings.entries = []leaderboard.Entry{entry("a", 1, at(1))}
	r.standings.panicOnce = true

	if _, err := r.service.Public(context.Background(), c.ID); err == nil {
		t.Fatal("Public() over a panicking repository = nil error, want one")
	}

	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("Public() after the panic = %v, want the next call to try again", err)
	}
	if len(view.Rows) != 1 {
		t.Errorf("Rows = %d, want 1 from the retried call", len(view.Rows))
	}
}

// The staff table is cached too, briefly — just long enough that several
// staff tabs refreshing together share one computation rather than each
// recomputing the same heavy query.
func TestTheLiveTableIsCachedForItsOwnShortTTL(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, nil)

	for range 3 {
		if _, err := r.service.Live(context.Background(), c.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.standings.callCount(); got != 1 {
		t.Fatalf("standings were read %d times within the live TTL, want 1", got)
	}

	r.now = r.now.Add(leaderboard.DefaultLiveCacheTTL)
	if _, err := r.service.Live(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.standings.callCount(); got != 2 {
		t.Errorf("standings were read %d times after the live TTL, want 2", got)
	}
}

// A reveal must not leave the staff table saying "frozen" for as long as its
// own cache TTL after the result is already public — the one view a reveal
// exists to change is exactly the one it must not leave stale. A settings
// change (moving the freeze, say) gets no equivalent hook: it is eventually
// reflected within the same short TTL, the same guarantee the table already
// gives everyone else, so nothing beyond that TTL is asked of it here.
func TestRevealInvalidatesTheLiveCacheTooSoStaffSeeItAtOnce(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusFinished, minutes(30))
	r.now = end.Add(time.Hour)

	live, err := r.service.Live(context.Background(), c.ID)
	if err != nil || live.Shown.State != leaderboard.StateFrozen {
		t.Fatalf("setup: Live() = %+v, %v, want frozen", live, err)
	}

	if _, err := r.service.Reveal(context.Background(), uuid.New(), c.ID); err != nil {
		t.Fatalf("Reveal() = %v", err)
	}

	live, err = r.service.Live(context.Background(), c.ID)
	if err != nil || live.Shown.State != leaderboard.StateFinal {
		t.Fatalf("Live() right after Reveal() = %+v, %v, want final, not a cached frozen copy", live, err)
	}
}
