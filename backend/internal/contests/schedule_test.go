package contests_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

// schedulerFixtureGrace is the grace newScheduler wires every Scheduler up
// with, standing in for cfg.DeadlineGrace — a fixed, recognisable value so a
// test can tell it apart from the zero value a forgotten wiring would leave
// behind.
const schedulerFixtureGrace = 5 * time.Second

// schedulerFixture is everything one Scheduler test needs, assembled so a
// test only ever has to name the pieces it actually stages.
type schedulerFixture struct {
	scheduler   *contests.Scheduler
	repo        *conteststest.Schedule
	stories     *conteststest.Stories
	questions   *conteststest.Questions
	sink        *conteststest.Sink
	uow         *conteststest.UnitOfWork
	poolTrigger *conteststest.PoolTrigger
}

// newScheduler assembles a Scheduler over the in-memory fakes, mirroring
// conteststest.NewFixture's own wiring for the pieces Scheduler actually
// uses. poolTrigger is wired by default, the same way conteststest.Fixture
// wires one for Service — a test about it asserts on
// f.poolTrigger.Triggered, and every other test simply never looks.
func newScheduler() schedulerFixture {
	repo := conteststest.NewSchedule()
	stories := conteststest.NewStories()
	questions := conteststest.NewQuestions()
	sink := conteststest.NewSink()
	uow := &conteststest.UnitOfWork{}
	poolTrigger := conteststest.NewPoolTrigger()
	return schedulerFixture{
		scheduler: contests.NewScheduler(repo, stories, questions, sink, audit.New(sink), uow, schedulerFixtureGrace).
			WithPoolTrigger(poolTrigger),
		repo: repo, stories: stories, questions: questions, sink: sink, uow: uow, poolTrigger: poolTrigger,
	}
}

// duePublishable stages a contest that passes CheckPublishable, together
// with the story and question a test's Stories/Questions fakes need to carry
// to answer that way — the same fixture publish_test.go's own publishable()
// builds, wired into the stores a Scheduler test reads from instead of
// passed straight to CheckPublishable.
func duePublishable(f schedulerFixture) contests.Contest {
	c, story, questions := publishable()
	f.stories.Save(context.Background(), c.ID, story.Bodies)
	for _, q := range questions {
		q.ContestID = c.ID
		f.questions.Put(q)
	}
	return c
}

func TestAdvanceDoesNothingWhenAnotherReplicaHoldsTheLock(t *testing.T) {
	f := newScheduler()
	f.repo.Acquired = false
	f.repo.Due = []contests.Contest{{ID: uuid.New()}}

	started, finished, err := f.scheduler.Advance(context.Background())
	if err != nil {
		t.Fatalf("Advance() = %v", err)
	}
	if started != 0 || finished != 0 {
		t.Errorf("started, finished = %d, %d, want 0, 0 when the lock was lost", started, finished)
	}
	if f.repo.DueCalls != 0 || f.repo.FinishedCalls != 0 {
		t.Error("a lost lock must stop the tick before either move ever runs")
	}
	if len(f.sink.Entries) != 0 {
		t.Errorf("audit entries = %v, want none for a tick that moved nothing", f.sink.Actions())
	}
	if f.uow.Calls != 1 {
		t.Errorf("UnitOfWork.Calls = %d, want exactly 1", f.uow.Calls)
	}
}

func TestAdvanceStartsAContestThatPassesThePublishGateAndFinishesAnOverdueOne(t *testing.T) {
	f := newScheduler()
	due := duePublishable(f)
	f.repo.Due = []contests.Contest{due}
	finished := uuid.New()
	f.repo.Finished = []uuid.UUID{finished}

	gotStarted, gotFinished, err := f.scheduler.Advance(context.Background())
	if err != nil {
		t.Fatalf("Advance() = %v", err)
	}
	if gotStarted != 1 || gotFinished != 1 {
		t.Errorf("started, finished = %d, %d, want 1, 1", gotStarted, gotFinished)
	}
	if len(f.repo.Moved) != 1 || f.repo.Moved[0] != due.ID {
		t.Errorf("SetStatus moved %v, want exactly [%s]", f.repo.Moved, due.ID)
	}
	if f.uow.Calls != 1 {
		t.Errorf("UnitOfWork.Calls = %d, want exactly 1 — one tick, one transaction", f.uow.Calls)
	}

	if len(f.sink.Entries) != 2 {
		t.Fatalf("audit entries = %d, want 2 (one per moved contest)", len(f.sink.Entries))
	}
	for _, e := range f.sink.Entries {
		if e.Action != audit.ActionContestStatusChange {
			t.Errorf("action = %q, want %q", e.Action, audit.ActionContestStatusChange)
		}
		if e.ActorID != nil {
			t.Errorf("actor = %v, want nil — nobody asked for an automatic transition", e.ActorID)
		}
		if e.Entity != "contest" {
			t.Errorf("entity = %q, want contest", e.Entity)
		}
	}

	byEntity := map[string]audit.Entry{}
	for _, e := range f.sink.Entries {
		byEntity[e.EntityID] = e
	}
	startedChange, ok := byEntity[due.ID.String()].Payload["changes"].(map[string]any)["status"]
	if !ok {
		t.Fatalf("no status change recorded for the started contest: %v", f.sink.Entries)
	}
	if from, ok := startedChange.(map[string]any)["from"]; !ok || from != contests.StatusPublished {
		t.Errorf("started contest's status change from = %v, want %q", from, contests.StatusPublished)
	}
	if to, ok := startedChange.(map[string]any)["to"]; !ok || to != contests.StatusRunning {
		t.Errorf("started contest's status change to = %v, want %q", to, contests.StatusRunning)
	}

	finishedChange := byEntity[finished.String()].Payload["changes"].(map[string]any)["status"].(map[string]any)
	if finishedChange["from"] != contests.StatusRunning || finishedChange["to"] != contests.StatusFinished {
		t.Errorf("finished contest's status change = %v, want running -> finished", finishedChange)
	}
}

// TestAdvanceBlocksAContestThatFailsThePublishGate is finding 1's own test:
// the scheduler is the one door into a contest that admitted no gate before
// this change, and a contest whose story disappeared after publication (an
// organizer deleted it, or never wrote one at all) must not be let through
// just because starts_at arrived.
func TestAdvanceBlocksAContestThatFailsThePublishGate(t *testing.T) {
	f := newScheduler()
	c, _, questions := publishable()
	// Every question is staged, but the story never is — CheckPublishable's
	// ProblemNoStory, the same refusal a manual Transition would hit.
	for _, q := range questions {
		q.ContestID = c.ID
		f.questions.Put(q)
	}
	f.repo.Due = []contests.Contest{c}

	started, finished, err := f.scheduler.Advance(context.Background())
	if err != nil {
		t.Fatalf("Advance() = %v", err)
	}
	if started != 0 || finished != 0 {
		t.Errorf("started, finished = %d, %d, want 0, 0 — the gate refused this contest", started, finished)
	}
	if f.repo.SetStatusCalls != 0 {
		t.Error("a contest that failed the publish gate must never reach SetStatus")
	}

	if len(f.sink.Entries) != 1 {
		t.Fatalf("audit entries = %d, want exactly 1 — the block must still be visible", len(f.sink.Entries))
	}
	entry := f.sink.Entries[0]
	if entry.Action != audit.ActionContestStartBlocked {
		t.Errorf("action = %q, want %q", entry.Action, audit.ActionContestStartBlocked)
	}
	if entry.EntityID != c.ID.String() {
		t.Errorf("entity id = %q, want %q", entry.EntityID, c.ID.String())
	}
	problems, ok := entry.Payload["problems"].([]string)
	if !ok {
		t.Fatalf("payload problems = %v, want a []string", entry.Payload["problems"])
	}
	found := false
	for _, p := range problems {
		if p == contests.ProblemNoStory {
			found = true
		}
	}
	if !found {
		t.Errorf("problems = %v, want it to include %q", problems, contests.ProblemNoStory)
	}
}

// TestAdvanceRecordsTheBlockOnceAcrossManyConsecutiveTicks is finding 1's own
// regression test: a contest whose window opened but whose story disappeared
// stays published, so DueToStart keeps matching it every tick until somebody
// fixes it. Before this change each of those ticks appended its own
// start_blocked entry — the trail growing without bound for exactly the
// contest an organizer most needs to be able to find in it.
func TestAdvanceRecordsTheBlockOnceAcrossManyConsecutiveTicks(t *testing.T) {
	f := newScheduler()
	c, _, questions := publishable()
	for _, q := range questions {
		q.ContestID = c.ID
		f.questions.Put(q)
	}
	f.repo.Due = []contests.Contest{c}

	const ticks = 5
	for i := 0; i < ticks; i++ {
		if _, _, err := f.scheduler.Advance(context.Background()); err != nil {
			t.Fatalf("Advance() tick %d = %v", i, err)
		}
	}

	if len(f.sink.Entries) != 1 {
		t.Fatalf("audit entries after %d identical ticks = %d, want exactly 1", ticks, len(f.sink.Entries))
	}
	if f.sink.Entries[0].Action != audit.ActionContestStartBlocked {
		t.Errorf("action = %q, want %q", f.sink.Entries[0].Action, audit.ActionContestStartBlocked)
	}
}

// TestAdvanceRecordsASecondEntryWhenABlockedContestIsFixedAndBrokenAgain
// proves the point of the dedup above is to stop repetition, not to stop
// reporting: once anything else has been recorded for the contest since the
// last block — here, the gate actually passing and the contest starting —
// the very next refusal must get its own entry again, even carrying the same
// problem codes as the first one did.
func TestAdvanceRecordsASecondEntryWhenABlockedContestIsFixedAndBrokenAgain(t *testing.T) {
	f := newScheduler()
	c, _, questions := publishable()
	for _, q := range questions {
		q.ContestID = c.ID
		f.questions.Put(q)
	}
	f.repo.Due = []contests.Contest{c}

	// First tick: no story yet, blocked and recorded.
	if _, _, err := f.scheduler.Advance(context.Background()); err != nil {
		t.Fatalf("Advance() first tick = %v", err)
	}
	if len(f.sink.Entries) != 1 {
		t.Fatalf("audit entries after the first block = %d, want 1", len(f.sink.Entries))
	}

	// A repeat of the identical problem must still be swallowed.
	if _, _, err := f.scheduler.Advance(context.Background()); err != nil {
		t.Fatalf("Advance() repeat tick = %v", err)
	}
	if len(f.sink.Entries) != 1 {
		t.Fatalf("audit entries after a repeat of the same block = %d, want still 1", len(f.sink.Entries))
	}

	// The organizer fixes it: the story arrives, and this tick's gate passes
	// and starts the contest — a different entry, standing for "something
	// changed since the last block".
	_, story, _ := publishable()
	f.stories.Save(context.Background(), c.ID, story.Bodies)
	if _, _, err := f.scheduler.Advance(context.Background()); err != nil {
		t.Fatalf("Advance() fix tick = %v", err)
	}

	// The organizer breaks it again — the manual equivalent of Transition
	// putting the contest back to published with the story removed a second
	// time — and it becomes due once more.
	if err := f.stories.Delete(context.Background(), c.ID); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	f.repo.Due = []contests.Contest{c}

	if _, _, err := f.scheduler.Advance(context.Background()); err != nil {
		t.Fatalf("Advance() second break tick = %v", err)
	}

	var blocked []audit.Entry
	for _, e := range f.sink.Entries {
		if e.Action == audit.ActionContestStartBlocked {
			blocked = append(blocked, e)
		}
	}
	if len(blocked) != 2 {
		t.Fatalf("start_blocked entries = %d, want 2 — the second break is a fresh refusal, not a repeat", len(blocked))
	}
}

// TestAdvanceFinishesWithTheSchedulersOwnGrace is a service-level test:
// Scheduler must hand AdvanceFinished the exact grace it was constructed
// with (cfg.DeadlineGrace in production), not compare ends_at bare — the
// repository is what turns that into "ends_at + grace <= now()", but only
// if the value actually arrives.
func TestAdvanceFinishesWithTheSchedulersOwnGrace(t *testing.T) {
	f := newScheduler()

	if _, _, err := f.scheduler.Advance(context.Background()); err != nil {
		t.Fatalf("Advance() = %v", err)
	}

	if f.repo.GraceSeen != schedulerFixtureGrace {
		t.Errorf("AdvanceFinished() was called with grace = %v, want %v", f.repo.GraceSeen, schedulerFixtureGrace)
	}
}

func TestAdvanceRecordsNothingWhenTheLockIsWonAndNothingMoved(t *testing.T) {
	f := newScheduler()

	started, finished, err := f.scheduler.Advance(context.Background())
	if err != nil {
		t.Fatalf("Advance() = %v", err)
	}
	if started != 0 || finished != 0 {
		t.Errorf("started, finished = %d, %d, want 0, 0", started, finished)
	}
	if len(f.sink.Entries) != 0 {
		t.Errorf("audit entries = %v, want none — an empty tick is not an event", f.sink.Actions())
	}
	if f.uow.Calls != 1 {
		t.Errorf("UnitOfWork.Calls = %d, want exactly 1", f.uow.Calls)
	}
}

func TestAdvanceFailsWithoutRecordingWhenTheAuditSinkFails(t *testing.T) {
	f := newScheduler()
	f.repo.Finished = []uuid.UUID{uuid.New()}
	failure := errors.New("audit sink is down")
	f.sink.AppendManyErr = failure

	_, _, err := f.scheduler.Advance(context.Background())
	if !errors.Is(err, failure) {
		t.Errorf("Advance() = %v, want an error wrapping %v", err, failure)
	}
}

func TestAdvanceFailsWhenFindingDueContestsFails(t *testing.T) {
	f := newScheduler()
	failure := errors.New("database is away")
	f.repo.DueErr = failure

	_, _, err := f.scheduler.Advance(context.Background())
	if !errors.Is(err, failure) {
		t.Errorf("Advance() = %v, want an error wrapping %v", err, failure)
	}
	if f.repo.FinishedCalls != 0 {
		t.Error("a failed search for due contests must not still attempt the move to finished")
	}
	if len(f.sink.Entries) != 0 {
		t.Error("nothing should be audited when the search itself failed")
	}
}

func TestAdvanceFailsWhenAdvancingToFinishedFails(t *testing.T) {
	f := newScheduler()
	failure := errors.New("database is away")
	f.repo.FinishedErr = failure

	_, _, err := f.scheduler.Advance(context.Background())
	if !errors.Is(err, failure) {
		t.Errorf("Advance() = %v, want an error wrapping %v", err, failure)
	}
	if len(f.sink.Entries) != 0 {
		t.Error("nothing should be audited when the finishing move failed")
	}
}

// TestAdvanceFailsWhenThePublishGateItselfFails proves the gate's own
// database reads are not mistaken for the gate's verdict: a story store that
// is away must abort the tick (CLAUDE.md rule 8 — only a row-level sentinel
// becomes "skipped"; anything else surfaces), not be read as "no story" and
// audited as a blocked contest.
func TestAdvanceFailsWhenThePublishGateItselfFails(t *testing.T) {
	f := newScheduler()
	c, _, _ := publishable()
	f.repo.Due = []contests.Contest{c}
	failure := errors.New("story store is away")
	f.stories.Err = failure

	_, _, err := f.scheduler.Advance(context.Background())
	if !errors.Is(err, failure) {
		t.Errorf("Advance() = %v, want an error wrapping %v", err, failure)
	}
	if f.repo.SetStatusCalls != 0 {
		t.Error("a gate that could not even be evaluated must not still move the contest")
	}
	if len(f.sink.Entries) != 0 {
		t.Error("nothing should be audited when the gate itself failed to run")
	}
}

// TestAdvanceSkipsAContestThatRacedWithAManualTransition covers the other
// reason SetStatus can fail beyond a real error: a concurrent manual
// Transition (or a delete) moved the contest between DueToStart's read and
// this tick's write. That is somebody else's decision and somebody else's
// audit entry, not this tick's failure.
func TestAdvanceSkipsAContestThatRacedWithAManualTransition(t *testing.T) {
	f := newScheduler()
	due := duePublishable(f)
	f.repo.Due = []contests.Contest{due}
	f.repo.Raced = map[uuid.UUID]error{due.ID: contests.ErrStatusChanged}

	started, _, err := f.scheduler.Advance(context.Background())
	if err != nil {
		t.Fatalf("Advance() = %v, want the race to be skipped rather than fail the tick", err)
	}
	if started != 0 {
		t.Errorf("started = %d, want 0 — a raced contest was not this tick's to move", started)
	}
	if len(f.sink.Entries) != 0 {
		t.Error("a raced contest must not get this tick's own audit entry")
	}
}

// TestAdvanceTriggersThePoolForEveryContestItStarts is P-C1's own claim for
// the scheduler: a contest whose window opens is exactly the moment its
// pool's roster stops being merely "published" and starts being played on,
// so the tender is woken rather than left to its own next tick.
func TestAdvanceTriggersThePoolForEveryContestItStarts(t *testing.T) {
	f := newScheduler()
	due := duePublishable(f)
	f.repo.Due = []contests.Contest{due}

	if _, _, err := f.scheduler.Advance(context.Background()); err != nil {
		t.Fatalf("Advance() = %v", err)
	}

	if len(f.poolTrigger.Triggered) != 1 || f.poolTrigger.Triggered[0] != due.ID {
		t.Fatalf("triggered = %v, want exactly [%s]", f.poolTrigger.Triggered, due.ID)
	}
}

// A tick that starts nothing — nothing was due, or the gate refused every
// contest that was — must not wake the pool tender for a move that never
// happened.
func TestAdvanceDoesNotTriggerThePoolWhenNothingStarted(t *testing.T) {
	f := newScheduler()
	c, _, questions := publishable()
	for _, q := range questions {
		q.ContestID = c.ID
		f.questions.Put(q)
	}
	// The story never staged: the gate refuses this one (see
	// TestAdvanceBlocksAContestThatFailsThePublishGate).
	f.repo.Due = []contests.Contest{c}

	if _, _, err := f.scheduler.Advance(context.Background()); err != nil {
		t.Fatalf("Advance() = %v", err)
	}
	if len(f.poolTrigger.Triggered) != 0 {
		t.Fatalf("triggered = %v, want none — the gate refused the only contest due", f.poolTrigger.Triggered)
	}
}

// A contest a concurrent manual Transition already started is not this
// tick's move to announce: it raced this tick's own write, and this tick did
// not actually start it.
func TestAdvanceDoesNotTriggerThePoolForAContestThatRacedAManualTransition(t *testing.T) {
	f := newScheduler()
	due := duePublishable(f)
	f.repo.Due = []contests.Contest{due}
	f.repo.Raced = map[uuid.UUID]error{due.ID: contests.ErrStatusChanged}

	if _, _, err := f.scheduler.Advance(context.Background()); err != nil {
		t.Fatalf("Advance() = %v", err)
	}
	if len(f.poolTrigger.Triggered) != 0 {
		t.Fatalf("triggered = %v, want none for a raced contest", f.poolTrigger.Triggered)
	}
}

// TestAdvanceDoesNotTriggerThePoolWhenTheTransactionFails is the ordering
// this whole feature depends on: a tick that started a contest but then
// failed to commit — here, the audit sink refusing the entry — must not wake
// the pool tender for a move the database itself rolled back. The trigger
// only ever fires after Advance's own transaction has actually committed.
func TestAdvanceDoesNotTriggerThePoolWhenTheTransactionFails(t *testing.T) {
	f := newScheduler()
	due := duePublishable(f)
	f.repo.Due = []contests.Contest{due}
	f.sink.AppendManyErr = errors.New("audit sink is down")

	if _, _, err := f.scheduler.Advance(context.Background()); err == nil {
		t.Fatal("Advance() succeeded despite the audit sink failing")
	}

	if len(f.poolTrigger.Triggered) != 0 {
		t.Fatalf("triggered = %v, want none — the transaction that started the contest never committed", f.poolTrigger.Triggered)
	}
}

// A Scheduler built without WithPoolTrigger — every deployment with no game
// cluster, and every test above this one — must not panic reaching for a
// trigger that was never wired.
func TestAdvanceWithNoPoolTriggerWiredStillWorks(t *testing.T) {
	repo := conteststest.NewSchedule()
	stories := conteststest.NewStories()
	questions := conteststest.NewQuestions()
	sink := conteststest.NewSink()
	uow := &conteststest.UnitOfWork{}
	scheduler := contests.NewScheduler(repo, stories, questions, sink, audit.New(sink), uow, schedulerFixtureGrace)

	f := schedulerFixture{scheduler: scheduler, repo: repo, stories: stories, questions: questions, sink: sink, uow: uow}
	due := duePublishable(f)
	f.repo.Due = []contests.Contest{due}

	if _, _, err := f.scheduler.Advance(context.Background()); err != nil {
		t.Fatalf("Advance() with no pool trigger wired = %v", err)
	}
}
