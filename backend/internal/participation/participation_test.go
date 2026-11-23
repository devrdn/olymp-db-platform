package participation_test

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/monitor"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation/participationtest"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/cache"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/sentineltest"
	"github.com/google/uuid"
)

// The module is tested here once, against the real gate, an in-memory roster
// and budgets kept on the memory cache. Its callers' tests stage what the
// roster finds and leave the protocol to these.

var errBudgetSpent = errors.New("budget spent")

// budget is a fixed-window allowance of perMinute requests per account, kept
// on the memory cache as every domain budget is.
func budget(t *testing.T, perMinute int) participation.Budget {
	t.Helper()
	store := cache.NewMemory(100)
	t.Cleanup(func() { _ = store.Close() })
	limiter := auth.NewLimiter(store)
	return participation.BudgetFunc(func(ctx context.Context, account uuid.UUID) error {
		allowed, err := limiter.Allow(ctx, "test:user:"+account.String(), perMinute, time.Minute)
		if err != nil {
			return err
		}
		if !allowed {
			return errBudgetSpent
		}
		return nil
	})
}

type watcher struct {
	mu     sync.Mutex
	visits []monitor.Visit
}

func (w *watcher) Observe(_ context.Context, visit monitor.Visit) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.visits = append(w.visits, visit)
}

func (w *watcher) seen() []monitor.Visit {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]monitor.Visit(nil), w.visits...)
}

type fixture struct {
	roster   *participationtest.Roster
	watcher  *watcher
	admitter *participation.Admitter
	contest  contests.Contest
	// participant is who every account finds, not started.
	participant contests.Participant
	now         time.Time
}

// newFixture stages a running fixed-timing contest open for a day and an
// unstarted registration in it.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{roster: participationtest.NewRoster(), watcher: &watcher{}, now: time.Now().UTC()}
	f.contest = participationtest.Running(uuid.New())
	f.participant = contests.Participant{
		ID: uuid.New(), ContestID: f.contest.ID, UserID: uuid.New(), Status: contests.RegistrationRegistered,
	}
	f.roster.Stage(f.contest, f.participant)
	f.admitter = participationtest.NewAdmitter(f.roster, f.watcher, func() time.Time { return f.now })
	return f
}

func (f *fixture) request() participation.Request {
	return participation.Request{
		Contest: f.contest.ID.String(), Account: f.participant.UserID,
		Address: netip.MustParseAddr("192.0.2.7"), Session: "session-tag", UserAgent: "test-browser/1.0",
	}
}

func TestAnAdmittedRequestCarriesThePersonAndTheContestTheLookupFound(t *testing.T) {
	f := newFixture(t)

	admission, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request())
	if err != nil {
		t.Fatalf("Admit() = %v", err)
	}
	if admission.Participant.ID != f.participant.ID || admission.Contest.ID != f.contest.ID {
		t.Fatalf("admitted %s in %s, want %s in %s",
			admission.Participant.ID, admission.Contest.ID, f.participant.ID, f.contest.ID)
	}
	if f.roster.AskedAbout() != f.contest.ID {
		t.Fatalf("the lookup was asked about %s, want the request's contest", f.roster.AskedAbout())
	}
}

// Rule 13: the budget is the first thing spent, so a refused caller costs no
// lookup and nobody can ask faster than the budget by asking about contests
// they are not in.
func TestARequestOverItsBudgetIsRefusedBeforeAnythingIsLookedUp(t *testing.T) {
	f := newFixture(t)
	spent := budget(t, 1)
	if _, err := f.admitter.Admit(t.Context(), spent, f.request()); err != nil {
		t.Fatalf("the first request = %v", err)
	}

	stranger := f.request()
	stranger.Contest = uuid.NewString()
	if _, err := f.admitter.Admit(t.Context(), spent, stranger); !errors.Is(err, errBudgetSpent) {
		t.Fatalf("the second request = %v, want the budget's own refusal", err)
	}
	if got := f.roster.Lookups(); got != 1 {
		t.Fatalf("%d lookups, want only the first request's", got)
	}
	if len(f.watcher.seen()) != 1 {
		t.Fatalf("visits = %+v, want only the admitted one", f.watcher.seen())
	}
}

// The identifier is the caller's to write, so a malformed one is refused only
// after the budget is charged, and costs no lookup.
func TestAMalformedContestIdentifierStillSpendsTheBudget(t *testing.T) {
	f := newFixture(t)
	once := budget(t, 1)
	malformed := f.request()
	malformed.Contest = "not-a-uuid"

	if _, err := f.admitter.Admit(t.Context(), once, malformed); !errors.Is(err, participation.ErrInvalidContest) {
		t.Fatalf("Admit() = %v, want ErrInvalidContest", err)
	}
	if f.roster.Lookups() != 0 {
		t.Fatal("a malformed identifier reached the lookup")
	}
	if _, err := f.admitter.Admit(t.Context(), once, malformed); !errors.Is(err, errBudgetSpent) {
		t.Fatalf("the second malformed request = %v, want the budget spent by the first", err)
	}
}

// A request the gate refuses still spends its budget.
func TestARefusedRequestStillSpendsItsBudget(t *testing.T) {
	f := newFixture(t)
	f.roster.Refuse(contests.ErrContestEnded)
	once := budget(t, 1)

	if _, err := f.admitter.Admit(t.Context(), once, f.request()); !errors.Is(err, contests.ErrContestEnded) {
		t.Fatalf("the first request = %v, want ErrContestEnded", err)
	}
	if _, err := f.admitter.Admit(t.Context(), once, f.request()); !errors.Is(err, errBudgetSpent) {
		t.Fatalf("the second request = %v, want the budget spent by the refused one", err)
	}
}

func TestAnAdmittedRequestIsVisitedAndARefusedOneIsNot(t *testing.T) {
	f := newFixture(t)

	if _, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request()); err != nil {
		t.Fatalf("Admit() = %v", err)
	}
	want := monitor.Visit{
		Contest: f.contest.ID, Registration: f.participant.ID,
		Address: netip.MustParseAddr("192.0.2.7"), Session: "session-tag", UserAgent: "test-browser/1.0",
	}
	if got := f.watcher.seen(); len(got) != 1 || got[0] != want {
		t.Fatalf("visits = %+v, want exactly %+v", got, want)
	}

	f.roster.Refuse(contests.ErrDeadlinePassed)
	if _, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request()); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("Admit() = %v, want ErrDeadlinePassed", err)
	}
	if got := f.watcher.seen(); len(got) != 1 {
		t.Fatalf("visits = %+v, want the refused request unrecorded", got)
	}
}

// The events channel is not observed: an admission without a visit records
// none, and admits exactly as one with it.
func TestAnAdmissionWithoutAVisitRecordsNone(t *testing.T) {
	f := newFixture(t)

	if _, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request(), participation.MayWait(), participation.WithoutVisit()); err != nil {
		t.Fatalf("Admit(WithoutVisit) = %v", err)
	}
	if got := f.watcher.seen(); len(got) != 0 {
		t.Fatalf("visits = %+v, want none", got)
	}
}

// A route that reads its body after admission holds the visit back and
// records it once the body is read.
func TestAVisitHeldBackIsRecordedWhenTheCallerSays(t *testing.T) {
	f := newFixture(t)
	admission, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request(), participation.WithoutVisit())
	if err != nil {
		t.Fatalf("Admit(WithoutVisit) = %v", err)
	}
	if got := f.watcher.seen(); len(got) != 0 {
		t.Fatalf("visits = %+v before Visit, want none", got)
	}
	admission.Visit(t.Context())
	if got := f.watcher.seen(); len(got) != 1 || got[0].Registration != f.participant.ID {
		t.Fatalf("visits = %+v, want the admitted registration once", got)
	}
}

// The grace the gate carries reaches every request: a working participant is
// admitted until the deadline plus the grace, and refused at that instant.
func TestTheAdmitterHonoursTheGatesGrace(t *testing.T) {
	f := newFixture(t)
	deadline := f.now.Add(time.Hour)
	contest := f.contest
	contest.EndsAt = &deadline
	f.roster.SetContest(contest)
	at := deadline
	admitter := participation.New(participation.Config{
		Lookup: f.roster, People: f.roster, Gate: contests.NewGate(5 * time.Second),
		Audit: audit.New(participationtest.Discard{}), UnitOfWork: participationtest.Discard{},
		Now: func() time.Time { return at },
	})

	at = deadline.Add(5*time.Second - time.Nanosecond)
	if _, err := admitter.Admit(t.Context(), budget(t, 10), f.request()); err != nil {
		t.Fatalf("Admit() inside the grace = %v", err)
	}
	at = deadline.Add(5 * time.Second)
	if _, err := admitter.Admit(t.Context(), budget(t, 10), f.request()); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("Admit() at deadline plus grace = %v, want ErrDeadlinePassed", err)
	}
}

func TestAnAdmitterWithoutAWatcherStillAdmits(t *testing.T) {
	f := newFixture(t)
	admitter := participationtest.NewAdmitter(f.roster, nil, nil)
	if _, err := admitter.Admit(t.Context(), budget(t, 10), f.request()); err != nil {
		t.Fatalf("Admit() = %v", err)
	}
}

// Every refusal of the gate comes back as the gate named it, and carries the
// Standing it was decided from.
func TestTheGatesRefusalComesBackWithItsStanding(t *testing.T) {
	for _, refusal := range []struct {
		err  error
		over bool
	}{
		{contests.ErrNotAParticipant, true},
		{contests.ErrParticipantFinished, true},
		{contests.ErrContestEnded, true},
		{contests.ErrDeadlinePassed, true},
		{contests.ErrContestNotRunning, false},
		{contests.ErrAddressNotAllowed, false},
	} {
		t.Run(refusal.err.Error(), func(t *testing.T) {
			f := newFixture(t)
			f.roster.Refuse(refusal.err)

			_, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request())
			if !errors.Is(err, refusal.err) {
				t.Fatalf("Admit() = %v, want %v", err, refusal.err)
			}
			var refused *participation.Refused
			if !errors.As(err, &refused) {
				t.Fatalf("Admit() = %#v, want a *participation.Refused", err)
			}
			if refused.Standing.Over() != refusal.over {
				t.Fatalf("Over() = %v, want %v", refused.Standing.Over(), refusal.over)
			}
		})
	}
}

// A stranger and a contest that does not exist read as the gate's refusal of a
// disqualified participant, so a probe learns nothing about the roster.
func TestNobodyOnTheRosterIsNotAParticipant(t *testing.T) {
	f := newFixture(t)
	elsewhere := f.request()
	elsewhere.Contest = uuid.NewString()

	_, err := f.admitter.Admit(t.Context(), budget(t, 10), elsewhere)
	if !errors.Is(err, contests.ErrNotAParticipant) {
		t.Fatalf("Admit() = %v, want ErrNotAParticipant", err)
	}
	var refused *participation.Refused
	if errors.As(err, &refused) {
		t.Fatalf("a stranger came back with a Standing (%+v); nobody's standing was decided", refused.Standing)
	}
}

func TestALookupThatFailsIsMarkedAsOurs(t *testing.T) {
	f := newFixture(t)
	f.roster.Fail(errors.New("connection reset"))

	if _, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request()); !errors.Is(err, participation.ErrUnavailable) {
		t.Fatalf("Admit() = %v, want ErrUnavailable", err)
	}
}

func TestANilBudgetIsRefusedAsOurs(t *testing.T) {
	f := newFixture(t)
	if _, err := f.admitter.Admit(t.Context(), nil, f.request()); !errors.Is(err, participation.ErrUnavailable) {
		t.Fatalf("Admit() = %v, want ErrUnavailable", err)
	}
	if f.roster.Lookups() != 0 {
		t.Fatal("a request with no budget reached the lookup")
	}
}

// registrationBudget is a budget with a second key space, the registration's,
// charged once admission has found it.
type registrationBudget struct {
	participation.Budget
	charged []uuid.UUID
	err     error
}

func (b *registrationBudget) ChargeRegistration(_ context.Context, p contests.Participant, _ contests.Contest) error {
	b.charged = append(b.charged, p.ID)
	return b.err
}

// The console's contest rate is charged to the registration the lookup found,
// before the gate, so a request the gate refuses still spends it.
func TestARegistrationBudgetIsChargedAfterTheLookupAndBeforeTheGate(t *testing.T) {
	f := newFixture(t)
	f.roster.Refuse(contests.ErrContestEnded)
	charged := &registrationBudget{Budget: budget(t, 10)}

	if _, err := f.admitter.Admit(t.Context(), charged, f.request()); !errors.Is(err, contests.ErrContestEnded) {
		t.Fatalf("Admit() = %v, want ErrContestEnded", err)
	}
	if len(charged.charged) != 1 || charged.charged[0] != f.participant.ID {
		t.Fatalf("charged %v, want the registration found", charged.charged)
	}

	charged.err = errBudgetSpent
	f.roster.SetContest(f.contest)
	if _, err := f.admitter.Admit(t.Context(), charged, f.request()); !errors.Is(err, errBudgetSpent) {
		t.Fatalf("Admit() = %v, want the registration's refusal", err)
	}
	if len(f.watcher.seen()) != 0 {
		t.Fatalf("visits = %+v, want none for a request its registration's budget refused", f.watcher.seen())
	}
}

func TestARegistrationBudgetIsNotChargedForSomebodyNotOnTheRoster(t *testing.T) {
	f := newFixture(t)
	charged := &registrationBudget{Budget: budget(t, 10)}
	elsewhere := f.request()
	elsewhere.Contest = uuid.NewString()

	if _, err := f.admitter.Admit(t.Context(), charged, elsewhere); !errors.Is(err, contests.ErrNotAParticipant) {
		t.Fatalf("Admit() = %v, want ErrNotAParticipant", err)
	}
	if len(charged.charged) != 0 {
		t.Fatalf("charged %v: a stranger must not create a registration counter", charged.charged)
	}
}

// otherLookup finds a participant the admitter's own lookup does not.
type otherLookup struct{ participant contests.Participant }

func (l otherLookup) ForAccess(_ context.Context, contestID, _ uuid.UUID) (contests.Participant, contests.Contest, error) {
	return l.participant, participationtest.Running(contestID), nil
}

// A caller that needs more of the row than admission does (the console's game
// and database) supplies the lookup for its request, so the row is read once.
func TestALookupNamedForTheRequestReplacesTheAdmittersOwn(t *testing.T) {
	f := newFixture(t)
	found := contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}

	admission, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request(),
		participation.LookedUpBy(otherLookup{participant: found}))
	if err != nil {
		t.Fatalf("Admit() = %v", err)
	}
	if admission.Participant.ID != found.ID {
		t.Fatalf("admitted %s, want the participant the request's own lookup found", admission.Participant.ID)
	}
	if f.roster.Lookups() != 0 {
		t.Fatalf("the admitter's own lookup ran %d times as well", f.roster.Lookups())
	}
}

// "Start only after a successful read": admission records nothing, Start
// records the start once, and the handle then holds the started participant.
func TestAdmissionStartsNothingUntilStartIsCalled(t *testing.T) {
	f := newFixture(t)

	admission, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request())
	if err != nil {
		t.Fatalf("Admit() = %v", err)
	}
	if got := f.roster.Started(); len(got) != 0 {
		t.Fatalf("admission recorded starts %v before Start", got)
	}
	if err := admission.Start(t.Context()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if got := f.roster.Started(); len(got) != 1 || got[0] != f.participant.ID {
		t.Fatalf("started %v, want the admitted registration once", got)
	}
	if admission.Participant.StartedAt == nil || !admission.Participant.StartedAt.Equal(f.now) {
		t.Fatalf("StartedAt = %v, want %v", admission.Participant.StartedAt, f.now)
	}
	if err := admission.Start(t.Context()); err != nil {
		t.Fatalf("a second Start() = %v", err)
	}
	if got := f.roster.Started(); len(got) != 1 {
		t.Fatalf("started %v, want no second start", got)
	}
}

// individual is a running individual-timing contest whose window is open
// around now, thirty minutes per participant.
func individual(id uuid.UUID, now time.Time) contests.Contest {
	opened, closes := now.Add(-3*time.Hour), now.Add(3*time.Hour)
	duration := 30
	return contests.Contest{
		ID: id, Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, StartsAt: &opened, EndsAt: &closes,
	}
}

// Reading before the clock starts is allowed, and the read starts the clock
// only through Start.
func TestAnUnstartedIndividualParticipantIsAdmittedAndStartedOnlyByStart(t *testing.T) {
	f := newFixture(t)
	f.roster.SetContest(individual(f.contest.ID, f.now))

	admission, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request())
	if err != nil {
		t.Fatalf("Admit() = %v, want the window open to somebody not yet started", err)
	}
	if admission.Participant.StartedAt != nil || len(f.roster.Started()) != 0 {
		t.Fatal("admission started the individual clock")
	}
	if err := admission.Start(t.Context()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if admission.Participant.StartedAt == nil || !admission.Participant.StartedAt.Equal(f.now) {
		t.Fatalf("after Start the clock is %v, want started at %v", admission.Participant.StartedAt, f.now)
	}
}

// The start may be another request's, recorded long enough ago that the time
// is already up: the gate is asked again of what Start hands back.
func TestAStartThatFindsTheTimeAlreadyUpIsRefused(t *testing.T) {
	f := newFixture(t)
	f.roster.SetContest(individual(f.contest.ID, f.now))
	admission, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request())
	if err != nil {
		t.Fatalf("Admit() = %v", err)
	}

	longAgo := f.now.Add(-2 * time.Hour)
	earlier := f.participant
	earlier.StartedAt, earlier.Status = &longAgo, contests.RegistrationActive
	f.roster.SetParticipant(earlier)

	if err := admission.Start(t.Context()); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("Start() = %v, want ErrDeadlinePassed", err)
	}
	if len(f.roster.Started()) != 0 {
		t.Fatalf("started %v, want the earlier start left as it was", f.roster.Started())
	}
}

// Between admission and Start the window can close; Start asks the gate
// first, so it starts nobody.
func TestAStartAfterTheWindowClosedStartsNobody(t *testing.T) {
	f := newFixture(t)
	f.roster.SetContest(individual(f.contest.ID, f.now))
	admission, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request())
	if err != nil {
		t.Fatalf("Admit() = %v", err)
	}

	f.now = *admission.Contest.EndsAt
	if err := admission.Start(t.Context()); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("Start() at ends_at = %v, want ErrDeadlinePassed", err)
	}
	if len(f.roster.Started()) != 0 {
		t.Fatalf("started %v, want nobody", f.roster.Started())
	}
}

func TestAStartThatCannotBeWrittenIsMarkedAsOurs(t *testing.T) {
	f := newFixture(t)
	admission, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request())
	if err != nil {
		t.Fatalf("Admit() = %v", err)
	}
	f.roster.FailStart(errors.New("connection reset"))

	if err := admission.Start(t.Context()); !errors.Is(err, participation.ErrUnavailable) {
		t.Fatalf("Start() = %v, want ErrUnavailable", err)
	}
}

// A registration disqualified between admission and Start comes back with no
// start recorded, and the gate says why.
func TestAStartForARegistrationDisqualifiedMeanwhileIsTheGatesRefusal(t *testing.T) {
	f := newFixture(t)
	f.roster.SetContest(individual(f.contest.ID, f.now))
	admission, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request())
	if err != nil {
		t.Fatalf("Admit() = %v", err)
	}
	disqualified := f.participant
	disqualified.Status = contests.RegistrationDisqualified
	f.roster.SetParticipant(disqualified)

	if err := admission.Start(t.Context()); !errors.Is(err, contests.ErrNotAParticipant) {
		t.Fatalf("Start() = %v, want ErrNotAParticipant", err)
	}
}

// pendingStarter is a store breaking Start's contract: it reports success and
// leaves the clock pending.
type pendingStarter struct{}

func (pendingStarter) Start(_ context.Context, id uuid.UUID, _ time.Time) (contests.Participant, bool, error) {
	return contests.Participant{ID: id, Status: contests.RegistrationRegistered}, false, nil
}

// Admitting somebody with no deadline would hand out content with no clock
// running, so a start left pending is refused as ours.
func TestAStartThatLeavesTheClockPendingIsRefusedAsOurs(t *testing.T) {
	f := newFixture(t)
	f.roster.SetContest(individual(f.contest.ID, f.now))
	admitter := participation.New(participation.Config{
		Lookup: f.roster, People: pendingStarter{}, Gate: contests.NewGate(0),
		Audit: audit.New(participationtest.Discard{}), UnitOfWork: participationtest.Discard{}, Now: func() time.Time { return f.now },
	})
	admission, err := admitter.Admit(t.Context(), budget(t, 10), f.request())
	if err != nil {
		t.Fatalf("Admit() = %v", err)
	}

	if err := admission.Start(t.Context()); !errors.Is(err, participation.ErrUnavailable) {
		t.Fatalf("Start() = %v, want ErrUnavailable", err)
	}
}

// recording is an audit sink and unit of work that tells an entry written
// inside the start's transaction from one written outside it.
type recording struct {
	entries []audit.Entry
	loose   []audit.Entry
}

type inTx struct{}

func (r *recording) Do(ctx context.Context, fn func(context.Context) error) error {
	return fn(context.WithValue(ctx, inTx{}, true))
}

func (r *recording) Append(ctx context.Context, e audit.Entry) error {
	r.entries = append(r.entries, e)
	if ctx.Value(inTx{}) == nil {
		r.loose = append(r.loose, e)
	}
	return nil
}

func (r *recording) AppendMany(ctx context.Context, entries []audit.Entry) error {
	for _, e := range entries {
		_ = r.Append(ctx, e)
	}
	return nil
}

// The start is audited in its own transaction, once: a second admission that
// lost the start to the first writes nothing (CLAUDE.md rule 6).
func TestAStartIsAuditedOnceInsideItsTransaction(t *testing.T) {
	f := newFixture(t)
	recorded := &recording{}
	admitter := participation.New(participation.Config{
		Lookup: f.roster, People: f.roster, Gate: contests.NewGate(0),
		Audit: audit.New(recorded), UnitOfWork: recorded, Now: func() time.Time { return f.now },
	})
	first, err := admitter.Admit(t.Context(), budget(t, 10), f.request())
	if err != nil {
		t.Fatalf("Admit() = %v", err)
	}
	second, err := admitter.Admit(t.Context(), budget(t, 10), f.request())
	if err != nil {
		t.Fatalf("Admit() = %v", err)
	}
	for _, admission := range []*participation.Admission{first, second} {
		if err := admission.Start(t.Context()); err != nil {
			t.Fatalf("Start() = %v", err)
		}
	}

	if len(recorded.entries) != 1 {
		t.Fatalf("audit entries = %+v, want one %s", recorded.entries, audit.ActionParticipantStart)
	}
	got := recorded.entries[0]
	if got.Action != audit.ActionParticipantStart || got.Entity != "contest" || got.EntityID != f.contest.ID.String() {
		t.Errorf("entry = %s on %s %s, want %s on contest %s", got.Action, got.Entity, got.EntityID, audit.ActionParticipantStart, f.contest.ID)
	}
	if got.ActorID == nil || *got.ActorID != f.participant.UserID {
		t.Errorf("actor = %v, want the participant %s", got.ActorID, f.participant.UserID)
	}
	if len(recorded.loose) != 0 {
		t.Errorf("entries recorded outside the transaction: %+v", recorded.loose)
	}
}

// The events channel waits on MayWait: it is admitted to a published contest
// that has not started, which every other request is refused.
func TestTheEventsChannelWaitsForAPublishedContestToStart(t *testing.T) {
	f := newFixture(t)
	f.roster.SetContest(contests.Contest{ID: f.contest.ID, Status: contests.StatusPublished})

	if _, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request()); !errors.Is(err, contests.ErrContestNotRunning) {
		t.Fatalf("Admit() = %v, want ErrContestNotRunning", err)
	}
	admission, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request(), participation.MayWait())
	if err != nil {
		t.Fatalf("Admit(MayWait) = %v", err)
	}
	// Waiting is not acting: a start asked of a waiting admission is refused.
	if err := admission.Start(t.Context()); !errors.Is(err, contests.ErrContestNotRunning) {
		t.Fatalf("Start() of a waiting admission = %v, want ErrContestNotRunning", err)
	}
	if len(f.roster.Started()) != 0 {
		t.Fatalf("started %v while waiting", f.roster.Started())
	}
}

func TestWaitingStillChecksEverythingButTheStart(t *testing.T) {
	for _, refusal := range []error{
		contests.ErrNotAParticipant, contests.ErrParticipantFinished, contests.ErrContestEnded,
		contests.ErrDeadlinePassed, contests.ErrContestNotRunning, contests.ErrAddressNotAllowed,
	} {
		t.Run(refusal.Error(), func(t *testing.T) {
			f := newFixture(t)
			f.roster.Refuse(refusal)
			if _, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request(), participation.MayWait()); !errors.Is(err, refusal) {
				t.Fatalf("Admit(MayWait) = %v, want %v", err, refusal)
			}
		})
	}
}

// Waiting widens the status admitted, never the network: a published contest
// held in a room refuses the channel from outside it.
func TestWaitingOnAPublishedContestStillChecksItsNetwork(t *testing.T) {
	f := newFixture(t)
	f.roster.SetContest(contests.Contest{
		ID: f.contest.ID, Status: contests.StatusPublished,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")},
	})

	fromHome := f.request()
	fromHome.Address = netip.MustParseAddr("203.0.113.7")
	if _, err := f.admitter.Admit(t.Context(), budget(t, 10), fromHome, participation.MayWait()); !errors.Is(err, contests.ErrAddressNotAllowed) {
		t.Fatalf("Admit(MayWait) from outside = %v, want ErrAddressNotAllowed", err)
	}
	fromTheRoom := f.request()
	fromTheRoom.Address = netip.MustParseAddr("10.20.3.4")
	if _, err := f.admitter.Admit(t.Context(), budget(t, 10), fromTheRoom, participation.MayWait()); err != nil {
		t.Fatalf("Admit(MayWait) from the room = %v", err)
	}
}

// A held-open channel asks again on each tick. Its opening paid the budget
// and recorded the visit, so a recheck does neither, and it sees the state as
// it now is.
func TestARecheckChargesNothingRecordsNoVisitAndSeesTheStateAsItIsNow(t *testing.T) {
	f := newFixture(t)
	once := budget(t, 1)
	admission, err := f.admitter.Admit(t.Context(), once, f.request(), participation.MayWait())
	if err != nil {
		t.Fatalf("Admit() = %v", err)
	}

	for tick := range 3 {
		if _, err := admission.Recheck(t.Context()); err != nil {
			t.Fatalf("tick %d: Recheck() = %v, want no budget spent by a tick", tick+1, err)
		}
	}
	if len(f.watcher.seen()) != 1 {
		t.Fatalf("visits = %+v, want only the opening's", f.watcher.seen())
	}

	f.roster.SetContest(contests.Contest{ID: f.contest.ID, Status: contests.StatusPublished})
	if _, err := admission.Recheck(t.Context()); err != nil {
		t.Fatalf("Recheck() of a contest taken back to published = %v, want the wait to hold", err)
	}
	f.roster.Refuse(contests.ErrContestEnded)
	_, err = admission.Recheck(t.Context())
	var refused *participation.Refused
	if !errors.As(err, &refused) || !errors.Is(err, contests.ErrContestEnded) || !refused.Standing.Over() {
		t.Fatalf("Recheck() of an ended contest = %v, want a refusal that is over", err)
	}
	if f.roster.Lookups() != 6 {
		t.Fatalf("%d lookups, want one per call", f.roster.Lookups())
	}
}

func TestARecheckThatCannotLookUpIsMarkedAsOurs(t *testing.T) {
	f := newFixture(t)
	admission, err := f.admitter.Admit(t.Context(), budget(t, 10), f.request(), participation.MayWait())
	if err != nil {
		t.Fatalf("Admit() = %v", err)
	}
	f.roster.Fail(errors.New("connection reset"))
	if _, err := admission.Recheck(t.Context()); !errors.Is(err, participation.ErrUnavailable) {
		t.Fatalf("Recheck() = %v, want ErrUnavailable", err)
	}
}

func TestNewRefusesToAssembleWithoutWhatAdmissionNeeds(t *testing.T) {
	roster := participationtest.NewRoster()
	complete := func() participation.Config {
		return participation.Config{
			Lookup: roster, People: roster, Gate: contests.NewGate(0),
			Audit: audit.New(participationtest.Discard{}), UnitOfWork: participationtest.Discard{},
		}
	}
	for name, breakIt := range map[string]func(*participation.Config){
		"no lookup":       func(c *participation.Config) { c.Lookup = nil },
		"no people":       func(c *participation.Config) { c.People = nil },
		"no gate":         func(c *participation.Config) { c.Gate = nil },
		"no audit":        func(c *participation.Config) { c.Audit = nil },
		"no unit of work": func(c *participation.Config) { c.UnitOfWork = nil },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("New did not panic")
				}
			}()
			cfg := complete()
			breakIt(&cfg)
			participation.New(cfg)
		})
	}
}

// Every exported sentinel in the package's source is listed by Errors
// (CLAUDE.md rule 1).
func TestEveryExportedErrorIsListed(t *testing.T) {
	sentineltest.AssertListed(t, ".")
}
