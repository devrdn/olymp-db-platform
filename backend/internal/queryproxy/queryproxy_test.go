package queryproxy_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/sentineltest"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/rpc"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// openWindow is a contest end no test here mistakes for closed. The deadline
// formula itself is tested in internal/contests.
var openWindow = time.Now().Add(24 * time.Hour)

// closedWindow is past every deadline, grace included.
var closedWindow = time.Now().Add(-24 * time.Hour)

// fiveSecondGate uses DEADLINE_GRACE's default.
var fiveSecondGate = contests.NewGate(5 * time.Second)

// The collaborators are faked because each is tested where it lives. Under
// test here is the order of the decisions and what each refusal is called.

type people struct {
	participant contests.Participant
	err         error
	// calls counts ByUser calls, to prove an upstream check stopped the
	// request. A pointer because the receiver is a value.
	calls *int
	// starts counts Start calls; startErr makes the write fail.
	starts   *int
	startErr error
	// stored is a start already on the row, as postgres.Registrations.Start
	// returns it to a request that lost the race. nil starts it at now.
	stored *time.Time
	// startsNothing makes Start succeed but leave the clock pending: a store
	// breaking its contract.
	startsNothing bool
}

func (p people) ByUser(context.Context, uuid.UUID, uuid.UUID) (contests.Participant, error) {
	if p.calls != nil {
		*p.calls++
	}
	return p.participant, p.err
}

// Start mirrors postgres.Registrations.Start: it sets StartedAt and the
// active status together, once.
func (p people) Start(_ context.Context, _ uuid.UUID, now time.Time) (contests.Participant, error) {
	if p.starts != nil {
		*p.starts++
	}
	if p.startErr != nil {
		return contests.Participant{}, p.startErr
	}
	started := p.participant
	if p.startsNothing {
		return started, nil
	}
	started.StartedAt = &now
	if p.stored != nil {
		started.StartedAt = p.stored
	}
	started.Status = contests.RegistrationActive
	return started, nil
}

type contestStore struct {
	contest contests.Contest
	err     error
	// calls proves WithLookup replaced ByID rather than adding a call.
	calls *int
}

func (c contestStore) ByID(context.Context, uuid.UUID) (contests.Contest, error) {
	if c.calls != nil {
		*c.calls++
	}
	return c.contest, c.err
}

type games struct {
	game provisioning.Contest
	err  error
	// calls proves a check ahead of the game lookup stopped the request.
	calls *int
}

func (g games) Game(context.Context, uuid.UUID) (provisioning.Contest, error) {
	if g.calls != nil {
		*g.calls++
	}
	return g.game, g.err
}

// lookupFake stands in for the single lookup (postgres.Registrations). calls
// proves the façade used it instead of the default's separate calls.
type lookupFake struct {
	participant contests.Participant
	contest     contests.Contest
	game        provisioning.Contest
	gameErr     error
	// instance left zero means the registration has none
	// (provisioning.ErrNoInstance).
	instance provisioning.Instance
	err      error
	calls    *int
	// asked records the contest and the account ForRun was last asked about.
	asked *[2]uuid.UUID
}

func (l lookupFake) ForRun(_ context.Context, contestID, userID uuid.UUID) (queryproxy.LookupResult, error) {
	if l.calls != nil {
		*l.calls++
	}
	if l.asked != nil {
		*l.asked = [2]uuid.UUID{contestID, userID}
	}
	if l.err != nil {
		return queryproxy.LookupResult{}, l.err
	}
	result := queryproxy.LookupResult{
		Participant: l.participant, Contest: l.contest, Game: l.game, GameErr: l.gameErr,
		Instance: l.instance,
	}
	if l.instance.Database == "" {
		result.InstanceErr = provisioning.ErrNoInstance
	}
	return result, nil
}

func (l lookupFake) ForAccess(context.Context, uuid.UUID, uuid.UUID) (contests.Participant, contests.Contest, error) {
	if l.calls != nil {
		*l.calls++
	}
	return l.participant, l.contest, l.err
}

// answerable is the fake behind queryproxy.Answerable; calls proves whether
// it was asked.
type answerable struct {
	left  bool
	err   error
	calls int
}

func (a *answerable) AnswerableLeft(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	a.calls++
	return a.left, a.err
}

type databases struct {
	database string
	quota    int64
	err      error
	// asked records what Ensure was called with, and quotaAsked whether size
	// was consulted.
	asked      *provisioning.Contest
	quotaAsked bool
	lastQuota  int64
	// existing and existingErr are what EnsureFrom was told of the
	// registration's copy.
	existing    provisioning.Instance
	existingErr error
}

// Instance answers that the registration has no copy yet.
func (d *databases) Instance(context.Context, uuid.UUID) (provisioning.Instance, error) {
	return provisioning.Instance{}, provisioning.ErrNoInstance
}

func (d *databases) EnsureFrom(_ context.Context, c provisioning.Contest, _ uuid.UUID, existing provisioning.Instance, err error) (string, error) {
	d.asked = &c
	d.existing, d.existingErr = existing, err
	return d.database, d.err
}

func (d *databases) Quota(context.Context, provisioning.Contest) (int64, error) {
	d.quotaAsked = true
	return d.quota, nil
}

type runner struct {
	quotaSink *int64
	got       queryrunner.Request
	gotOrigin queryrunner.Origin
	result    *queryrunner.Result
	err       error
	// calls proves an upstream check stopped a request before the journal was
	// written.
	calls int
}

func (r *runner) Run(_ context.Context, req queryrunner.Request, origin queryrunner.Origin) (*queryrunner.Result, error) {
	r.calls++
	r.got, r.gotOrigin = req, origin
	if r.quotaSink != nil {
		*r.quotaSink = req.DiskQuotaBytes
	}
	return r.result, r.err
}

func fixture(t *testing.T) (*queryproxy.Service, *databases, *runner) {
	t.Helper()

	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	registration := contests.Participant{
		ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive,
	}
	db := &databases{database: "game_c1_u1", quota: 42 << 20}
	run := &runner{result: &queryrunner.Result{Columns: []string{"a"}}}

	return queryproxy.New(
		people{participant: registration},
		contestStore{contest: contest},
		games{game: provisioning.Contest{
			ID: contest.ID, Template: "game_tpl_c1", Version: 3, Policy: sqlpolicy.ReadOnly(),
		}},
		db, run,
		fiveSecondGate,
	), db, run
}

func command() queryproxy.Command {
	return queryproxy.Command{
		ContestID: uuid.New(), UserID: uuid.New(),
		SQL: `SELECT * FROM suspects`, RequestID: uuid.New(),
	}
}

func TestAParticipantOfARunningContestGetsAnAnswer(t *testing.T) {
	service, _, _ := fixture(t)

	result, err := service.Run(t.Context(), command())
	if err != nil {
		t.Fatalf("running: %v", err)
	}
	if len(result.Columns) != 1 {
		t.Fatalf("columns = %v", result.Columns)
	}
}

// A caller cannot name a database: it is looked up from the registration
// every time.
func TestTheDatabaseComesFromTheRegistrationAndNeverFromTheRequest(t *testing.T) {
	service, db, run := fixture(t)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("running: %v", err)
	}

	if run.got.Database != db.database {
		t.Fatalf("database = %q, want the one provisioning handed out", run.got.Database)
	}
	if db.asked == nil || db.asked.Template != "game_tpl_c1" || db.asked.Version != 3 {
		t.Fatalf("the provisioner was asked with %+v", db.asked)
	}
}

func TestTheRequestIdentifierIsCarriedThrough(t *testing.T) {
	service, _, run := fixture(t)
	cmd := command()
	cmd.Address = netip.MustParseAddr("192.0.2.44")

	if _, err := service.Run(t.Context(), cmd); err != nil {
		t.Fatalf("running: %v", err)
	}

	// RequestID ties the journal row to the technical logs.
	if run.gotOrigin.RequestID != cmd.RequestID {
		t.Fatalf("request id = %s, want %s", run.gotOrigin.RequestID, cmd.RequestID)
	}
	// The address reaches the journal row's ip column (CLAUDE.md rule 11).
	if run.gotOrigin.Address != cmd.Address {
		t.Fatalf("address = %v, want %v", run.gotOrigin.Address, cmd.Address)
	}
	if run.got.Registration == uuid.Nil {
		t.Fatal("the query was journalled against no registration")
	}
}

// Each refusal is its own error because each is a different sentence to the
// participant.
func TestWhoMayAskAndWhen(t *testing.T) {
	for name, given := range map[string]struct {
		people  people
		contest contests.Contest
		want    error
	}{
		"somebody who never registered": {
			people:  people{err: contests.ErrParticipantNotFound},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    contests.ErrNotAParticipant,
		},
		"a contest that has not started": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusPublished},
			want:    contests.ErrContestNotRunning,
		},
		"a contest that has finished": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusFinished},
			want:    contests.ErrContestEnded,
		},
		"somebody disqualified": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationDisqualified}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    contests.ErrNotAParticipant,
		},
	} {
		t.Run(name, func(t *testing.T) {
			service := queryproxy.New(given.people, contestStore{contest: given.contest},
				games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
				&databases{database: "x"}, &runner{result: &queryrunner.Result{}}, fiveSecondGate)

			if _, err := service.Run(t.Context(), command()); !errors.Is(err, given.want) {
				t.Fatalf("error = %v, want %v", err, given.want)
			}
		})
	}
}

// Closing does not wait for the scheduler: a fixed contest past ends_at stops
// taking queries while its status still says running.
func TestAFixedContestStopsAcceptingQueriesAtItsEndEvenIfStatusLagsBehind(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Minute)
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &past,
	}
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("error = %v, want ErrDeadlinePassed", err)
	}
}

// An individual participant's own deadline (started_at + duration_min) binds
// while the contest's window is open and its status is running.
func TestAnIndividualParticipantsOwnDeadlinePassesEvenThoughTheContestWindowHasNot(t *testing.T) {
	now := time.Now()
	startedAnHourAgo := now.Add(-time.Hour)
	farFuture := now.Add(24 * time.Hour)
	duration := 10
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, EndsAt: &farFuture,
	}
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive, StartedAt: &startedAnHourAgo,
		}},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("error = %v, want ErrDeadlinePassed (the participant's own 10 minutes are long over)", err)
	}
}

func TestAnIndividualParticipantStillWithinTheirOwnWindowIsUnaffected(t *testing.T) {
	now := time.Now()
	startedAMinuteAgo := now.Add(-time.Minute)
	farFuture := now.Add(24 * time.Hour)
	duration := 30
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, EndsAt: &farFuture,
	}
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive, StartedAt: &startedAMinuteAgo,
		}},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("a participant well within their own window was refused: %v", err)
	}
}

// A never-started individual participant's first query starts their clock
// and is answered, not refused.
func TestAnIndividualParticipantsFirstQueryStartsTheirClock(t *testing.T) {
	farFuture := time.Now().Add(24 * time.Hour)
	duration := 30
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, EndsAt: &farFuture,
	}
	starts := 0
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered,
		}, starts: &starts},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("the participant's first query: %v", err)
	}
	if starts != 1 {
		t.Fatalf("Start was called %d times, want 1", starts)
	}
}

// The façade calls Start only when the participant it read has no StartedAt.
func TestASecondQueryDoesNotRestartAnAlreadyStartedParticipant(t *testing.T) {
	farFuture := time.Now().Add(24 * time.Hour)
	started := time.Now().Add(-time.Minute)
	duration := 30
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, EndsAt: &farFuture,
	}
	starts := 0
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive, StartedAt: &started,
		}, starts: &starts},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("running: %v", err)
	}
	if starts != 0 {
		t.Fatalf("Start was called %d times for a participant who had already started, want 0", starts)
	}
}

// Fixed timing shares one window; Deadline never consults StartedAt for it.
func TestAFixedTimingParticipantsClockIsNeverStarted(t *testing.T) {
	starts := 0
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), Status: contests.RegistrationActive,
		}, starts: &starts},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("running: %v", err)
	}
	if starts != 0 {
		t.Fatalf("Start was called %d times for a fixed-timing participant, want 0", starts)
	}
}

func TestAFailureToStartTheClockIsMarkedAsOurs(t *testing.T) {
	farFuture := time.Now().Add(24 * time.Hour)
	duration := 30
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, EndsAt: &farFuture,
	}
	broken := errors.New("dial tcp 172.28.0.5:5432: connection refused")
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered,
		}, startErr: broken},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

// An organiser may flip status to running hours before starts_at. A first
// query then must not start the clock: the duration would burn before the
// contest opens, and started_at cannot be cleared once written.
func TestAFirstQueryBeforeStartsAtStartsNoClock(t *testing.T) {
	now := time.Now()
	opensTomorrowMorning := now.Add(13 * time.Hour)
	farFuture := now.Add(48 * time.Hour)
	duration := 120
	contest := contests.Contest{
		// Status already running, starts_at hours away.
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, StartsAt: &opensTomorrowMorning, EndsAt: &farFuture,
	}
	starts := 0
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered,
		}, starts: &starts},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, contests.ErrContestNotRunning) {
		t.Fatalf("a first query before starts_at: error = %v, want ErrContestNotRunning", err)
	}
	if starts != 0 {
		t.Fatalf("Start was called %d times for a query before starts_at, want 0 — nothing may brick this participant", starts)
	}
}

// The other edge: a status stuck at running must not let a first query start
// a clock past ends_at.
func TestAFirstQueryAfterEndsAtStartsNoClock(t *testing.T) {
	now := time.Now()
	opened := now.Add(-2 * time.Hour)
	closed := now.Add(-time.Minute)
	duration := 30
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, StartsAt: &opened, EndsAt: &closed,
	}
	starts := 0
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered,
		}, starts: &starts},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("a first query after ends_at: error = %v, want ErrDeadlinePassed", err)
	}
	if starts != 0 {
		t.Fatalf("Start was called %d times for a query after ends_at, want 0", starts)
	}
}

// Starting has no grace: the grace covers a request already in flight from
// somebody working, not time to begin.
func TestAFirstQueryAtExactlyEndsAtIsTooLateToStart(t *testing.T) {
	opened := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	closes := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	duration := 30
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, StartsAt: &opened, EndsAt: &closes,
	}
	starts := 0
	run := &runner{result: &queryrunner.Result{}}
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered,
		}, starts: &starts},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, run,
		contests.NewGate(5*time.Second),
	).WithClock(func() time.Time { return closes })

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("a first query at exactly ends_at: error = %v, want ErrDeadlinePassed", err)
	}
	if starts != 0 || run.calls != 0 {
		t.Fatalf("Start called %d times and the runner %d times at ends_at, want 0 and 0", starts, run.calls)
	}
}

// Start may hand back another request's start whose time is already up; the
// query is refused and never run.
func TestAFirstQueryWhoseStartFindsTheTimeAlreadyUpIsRefused(t *testing.T) {
	now := time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC)
	opened := now.Add(-3 * time.Hour)
	closes := now.Add(3 * time.Hour)
	longAgo := now.Add(-2 * time.Hour)
	duration := 30
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, StartsAt: &opened, EndsAt: &closes,
	}
	starts := 0
	run := &runner{result: &queryrunner.Result{}}
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered,
		}, stored: &longAgo, starts: &starts},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, run,
		fiveSecondGate,
	).WithClock(func() time.Time { return now })

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("error = %v, want ErrDeadlinePassed", err)
	}
	// Start was reached: the refusal comes from the check after it.
	if starts != 1 {
		t.Fatalf("Start called %d times, want 1", starts)
	}
	if run.calls != 0 {
		t.Fatalf("the runner was reached %d times by a query whose time was up, want 0", run.calls)
	}
}

// A Start that succeeds but leaves the clock pending is a broken store: the
// query is refused as ours and never run.
func TestAFirstQueryWhoseStartLeavesTheClockPendingIsRefusedAsOurs(t *testing.T) {
	contest := individualContest()
	starts := 0
	run := &runner{result: &queryrunner.Result{}}
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered,
		}, startsNothing: true, starts: &starts},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, run,
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if starts != 1 || run.calls != 0 {
		t.Fatalf("Start called %d times and the runner %d times, want 1 and 0", starts, run.calls)
	}
}

// The clock starts only once the request is otherwise admitted, so a query
// from outside the contest's network costs the participant no time.
func TestADisallowedAddressDoesNotStartTheClock(t *testing.T) {
	farFuture := time.Now().Add(24 * time.Hour)
	duration := 30
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, EndsAt: &farFuture,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")},
	}
	starts := 0
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered,
		}, starts: &starts},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	fromHome := command()
	fromHome.Address = netip.MustParseAddr("203.0.113.7")
	if _, err := service.Run(t.Context(), fromHome); !errors.Is(err, contests.ErrAddressNotAllowed) {
		t.Fatalf("error = %v, want ErrAddressNotAllowed", err)
	}
	if starts != 0 {
		t.Fatalf("Start was called %d times for a query from a disallowed address, want 0", starts)
	}
}

// The same finding, for an oversized first query.
func TestAnOversizedFirstQueryDoesNotStartTheClock(t *testing.T) {
	farFuture := time.Now().Add(24 * time.Hour)
	duration := 30
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, EndsAt: &farFuture,
	}
	starts := 0
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered,
		}, starts: &starts},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	cmd := command()
	cmd.SQL = "SELECT " + strings.Repeat("a", sqlpolicy.MaxQueryBytes+1)

	var refusal *sqlpolicy.Refusal
	if _, err := service.Run(t.Context(), cmd); !errors.As(err, &refusal) || refusal.Code != sqlpolicy.CodeTooLong {
		t.Fatalf("error = %v, want CodeTooLong", err)
	}
	if starts != 0 {
		t.Fatalf("Start was called %d times for an oversized first query, want 0", starts)
	}
}

// And for a contest whose game was never provisioned.
func TestANotYetProvisionedContestDoesNotStartTheClock(t *testing.T) {
	farFuture := time.Now().Add(24 * time.Hour)
	duration := 30
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, EndsAt: &farFuture,
	}
	starts := 0
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered,
		}, starts: &starts},
		contestStore{contest: contest},
		games{err: provisioning.ErrNoGame},
		&databases{}, &runner{},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrNoGameYet) {
		t.Fatalf("error = %v, want ErrNoGameYet", err)
	}
	if starts != 0 {
		t.Fatalf("Start was called %d times for a contest with no game yet, want 0", starts)
	}
}

// The grace covers network latency: a query a few seconds past the deadline
// is honoured, one past the grace is not.
func TestTheGraceWindowAcceptsAQueryArrivingJustAfterTheDeadlineAndNoLater(t *testing.T) {
	deadline := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &deadline,
	}
	build := func(now time.Time) *queryproxy.Service {
		return queryproxy.New(
			people{participant: contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}},
			contestStore{contest: contest},
			games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
			&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
			contests.NewGate(5*time.Second),
		).WithClock(func() time.Time { return now })
	}

	withinGrace := build(deadline.Add(3 * time.Second))
	if _, err := withinGrace.Run(t.Context(), command()); err != nil {
		t.Fatalf("a query 3s after the deadline, within a 5s grace, was refused: %v", err)
	}

	lastInstant := build(deadline.Add(5*time.Second - time.Nanosecond))
	if _, err := lastInstant.Run(t.Context(), command()); err != nil {
		t.Fatalf("a query one nanosecond before the grace ends was refused: %v", err)
	}

	// At exactly deadline plus grace the core database refuses an answer
	// (now() >= deadline), and the console closes at the same instant.
	atGrace := build(deadline.Add(5 * time.Second))
	if _, err := atGrace.Run(t.Context(), command()); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("error = %v, want ErrDeadlinePassed for a query at exactly the deadline plus the grace", err)
	}

	pastGrace := build(deadline.Add(6 * time.Second))
	if _, err := pastGrace.Run(t.Context(), command()); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("error = %v, want ErrDeadlinePassed for a query past the grace too", err)
	}
}

// A contest that has not started, opened from a network it is not held on,
// names the network: waiting will not help the caller, moving will.
func TestAPublishedContestFromADisallowedAddressNamesTheAddress(t *testing.T) {
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusPublished,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")},
	}
	p := people{participant: contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered}}
	service := queryproxy.New(p, contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}}, fiveSecondGate)

	fromHome := command()
	fromHome.Address = netip.MustParseAddr("203.0.113.7")
	if _, err := service.Run(t.Context(), fromHome); !errors.Is(err, contests.ErrAddressNotAllowed) {
		t.Fatalf("Run() = %v, want ErrAddressNotAllowed", err)
	}
	if _, _, err := service.Access(t.Context(), contest.ID, uuid.New(), netip.MustParseAddr("203.0.113.7")); !errors.Is(err, contests.ErrAddressNotAllowed) {
		t.Fatalf("Access() = %v, want ErrAddressNotAllowed", err)
	}
	// From the room it is still a contest that has not started.
	if _, _, err := service.Access(t.Context(), contest.ID, uuid.New(), netip.MustParseAddr("10.20.3.4")); !errors.Is(err, contests.ErrContestNotRunning) {
		t.Fatalf("Access() from the room = %v, want ErrContestNotRunning", err)
	}
}

func TestAContestWithNoGameYet(t *testing.T) {
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{err: provisioning.ErrNoGame},
		&databases{}, &runner{},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrNoGameYet) {
		t.Fatalf("error = %v, want ErrNoGameYet", err)
	}
}

// A validator refusal arrives unwrapped: the interface translates its code.
func TestARefusalPassesThroughUntouched(t *testing.T) {
	service, _, run := fixture(t)
	run.err = &sqlpolicy.Refusal{Code: sqlpolicy.CodeFunctionNotSupported, Subject: "pg_sleep"}
	run.result = nil

	_, err := service.Run(t.Context(), command())

	var refusal *sqlpolicy.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a refusal", err)
	}
	if refusal.Subject != "pg_sleep" {
		t.Fatalf("refusal = %+v", refusal)
	}
}

// The network restriction is checked on every query, not only at enrolment.
func TestTheContestsNetworkIsCheckedOnEveryQuery(t *testing.T) {
	inRoom := netip.MustParsePrefix("10.20.0.0/16")
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow, AllowedCIDRs: []netip.Prefix{inRoom},
	}
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	fromRoom := command()
	fromRoom.Address = netip.MustParseAddr("10.20.3.4")
	if _, err := service.Run(t.Context(), fromRoom); err != nil {
		t.Fatalf("a query from the contest's own network was refused: %v", err)
	}

	fromHome := command()
	fromHome.Address = netip.MustParseAddr("203.0.113.7")
	if _, err := service.Run(t.Context(), fromHome); !errors.Is(err, contests.ErrAddressNotAllowed) {
		t.Fatalf("error = %v, want ErrAddressNotAllowed", err)
	}

	// An unresolved address fails the restriction too.
	unknown := command()
	if _, err := service.Run(t.Context(), unknown); !errors.Is(err, contests.ErrAddressNotAllowed) {
		t.Fatalf("error = %v, want ErrAddressNotAllowed", err)
	}
}

// Finishing closes the console. Their answers are in, and letting them carry
// on querying is letting them keep working after the bell.
func TestAParticipantWhoHasFinishedIsDone(t *testing.T) {
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationFinished}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, contests.ErrParticipantFinished) {
		t.Fatalf("error = %v, want ErrParticipantFinished", err)
	}
}

// Where the catalogues are closed, "relation does not exist" would turn
// guessing names into enumeration, so the database's words are withheld.
func TestWhereTheSchemaIsHiddenTheDatabaseDoesNotSpellItOut(t *testing.T) {
	closed := sqlpolicy.ReadOnly()
	closed.AllowCatalog = false

	build := func(policy sqlpolicy.Policy, failure error) *queryproxy.Service {
		return queryproxy.New(
			people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
			games{game: provisioning.Contest{Policy: policy}},
			&databases{database: "x"},
			&runner{err: failure},
			fiveSecondGate,
		)
	}

	// The database's words as rpc.errorFor hands them over, the only shape
	// this acts on.
	probe := &queryrunner.DatabaseError{Message: `ERROR: relation "salaries" does not exist (SQLSTATE 42P01)`}

	_, err := build(closed, probe).Run(t.Context(), command())
	if !errors.Is(err, queryproxy.ErrDatabaseDeclined) {
		t.Fatalf("error = %v, want ErrDatabaseDeclined", err)
	}
	if strings.Contains(err.Error(), "salaries") {
		t.Fatalf("the name leaked anyway: %v", err)
	}

	// With the catalogues open the message passes through.
	_, err = build(sqlpolicy.ReadOnly(), probe).Run(t.Context(), command())
	if !strings.Contains(err.Error(), "salaries") {
		t.Fatalf("the database's own words were withheld from an open contest: %v", err)
	}
}

// accessFixture fakes only what Access touches; games, databases and the
// runner are zero values Access must never use.
func accessFixture(people queryproxy.People, contest contestStore) *queryproxy.Service {
	return queryproxy.New(people, contest, games{}, &databases{}, &runner{}, fiveSecondGate)
}

// Access drives the same table as TestWhoMayAskAndWhen, so the two admissions
// are proven to agree.
func TestAccessAgreesWithRunAboutWhoMayAskAndWhen(t *testing.T) {
	for name, given := range map[string]struct {
		people  people
		contest contests.Contest
		want    error
	}{
		"somebody who never registered": {
			people:  people{err: contests.ErrParticipantNotFound},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    contests.ErrNotAParticipant,
		},
		"a contest that has not started": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusPublished},
			want:    contests.ErrContestNotRunning,
		},
		"a contest that has finished": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusFinished},
			want:    contests.ErrContestEnded,
		},
		"somebody disqualified": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationDisqualified}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    contests.ErrNotAParticipant,
		},
		"somebody who has finished": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationFinished}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    contests.ErrParticipantFinished,
		},
	} {
		t.Run(name, func(t *testing.T) {
			service := accessFixture(given.people, contestStore{contest: given.contest})

			_, _, err := service.Access(t.Context(), uuid.New(), uuid.New(), netip.Addr{})
			if !errors.Is(err, given.want) {
				t.Fatalf("error = %v, want %v", err, given.want)
			}
		})
	}
}

// A read past a fixed contest's ends_at is refused like a query: one deadline
// formula for both.
func TestAccessRefusesAFixedContestPastItsDeadline(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	contest := contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &past}
	service := accessFixture(people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}}, contestStore{contest: contest})

	if _, _, err := service.Access(t.Context(), uuid.New(), uuid.New(), netip.Addr{}); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("error = %v, want ErrDeadlinePassed", err)
	}
}

// A read is admitted on the same instant a query is: a working participant
// up to one nanosecond before their deadline plus the grace, and not at it.
func TestAccessClosesAtExactlyTheDeadlinePlusTheGrace(t *testing.T) {
	deadline := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	contest := contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &deadline}
	p := people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}}
	at := func(now time.Time) *queryproxy.Service {
		return queryproxy.New(p, contestStore{contest: contest}, games{}, &databases{}, &runner{}, contests.NewGate(5*time.Second)).
			WithClock(func() time.Time { return now })
	}

	if _, _, err := at(deadline.Add(5*time.Second-time.Nanosecond)).Access(t.Context(), uuid.New(), uuid.New(), netip.Addr{}); err != nil {
		t.Fatalf("Access() one nanosecond before the grace ends = %v, want nil", err)
	}
	if _, _, err := at(deadline.Add(5*time.Second)).Access(t.Context(), uuid.New(), uuid.New(), netip.Addr{}); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("Access() at exactly the deadline plus the grace = %v, want ErrDeadlinePassed", err)
	}
}

// An individual participant who has not started may begin up to ends_at and
// not at it: no grace for starting, on a read as on a query.
func TestAccessRefusesAnUnstartedIndividualParticipantAtExactlyEndsAt(t *testing.T) {
	opened := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	closes := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	duration := 30
	contest := contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, StartsAt: &opened, EndsAt: &closes,
	}
	p := people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}}
	at := func(now time.Time) *queryproxy.Service {
		return queryproxy.New(p, contestStore{contest: contest}, games{}, &databases{}, &runner{}, contests.NewGate(5*time.Second)).
			WithClock(func() time.Time { return now })
	}

	if _, _, err := at(closes.Add(-time.Nanosecond)).Access(t.Context(), uuid.New(), uuid.New(), netip.Addr{}); err != nil {
		t.Fatalf("Access() one nanosecond before ends_at = %v, want nil", err)
	}
	if _, _, err := at(closes).Access(t.Context(), uuid.New(), uuid.New(), netip.Addr{}); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("Access() at exactly ends_at = %v, want ErrDeadlinePassed", err)
	}
}

// The gate admits a never-started individual participant inside the contest
// window, and Access must not start their clock: it also admits the answer
// endpoint and the query log. StartOnRead starts it after a successful read.
func TestAccessLetsAnIndividualParticipantReadBeforeTheyHaveStartedAndNeverStartsTheirClock(t *testing.T) {
	future := time.Now().Add(time.Hour)
	duration := 30
	contest := contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, EndsAt: &future,
	}
	starts := 0
	p := people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}, starts: &starts}
	service := accessFixture(p, contestStore{contest: contest})

	participant, gotContest, err := service.Access(t.Context(), uuid.New(), uuid.New(), netip.Addr{})
	if err != nil {
		t.Fatalf("Access() = %v, want nil (their window is open even though they have not started)", err)
	}
	if participant.StartedAt != nil {
		t.Fatalf("Access started the participant's clock, which is Run's job on a deliberate action, not a read's")
	}
	if starts != 0 {
		t.Fatalf("Start was called %d times by a read, want 0", starts)
	}
	if gotContest.Status != contests.StatusRunning {
		t.Fatalf("contest returned = %+v", gotContest)
	}
}

// individualContest is a running individual-timing contest whose window is
// open around the test's wall clock.
func individualContest() contests.Contest {
	opened := time.Now().Add(-time.Hour)
	closes := time.Now().Add(time.Hour)
	duration := 30
	return contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, StartsAt: &opened, EndsAt: &closes,
	}
}

// Under individual timing reading the content is the contest; otherwise the
// whole window becomes free preparation time.
func TestStartOnReadStartsAnIndividualParticipantsClock(t *testing.T) {
	contest := individualContest()
	starts := 0
	registered := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered}
	service := accessFixture(people{participant: registered, starts: &starts}, contestStore{contest: contest})

	started, err := service.StartOnRead(t.Context(), contest, registered, netip.Addr{})
	if err != nil {
		t.Fatalf("StartOnRead() = %v", err)
	}
	if starts != 1 || started.StartedAt == nil {
		t.Fatalf("Start called %d times, StartedAt = %v; want the clock started once", starts, started.StartedAt)
	}
}

// A second read costs no write and moves nothing.
func TestStartOnReadDoesNotMoveAClockAlreadyRunning(t *testing.T) {
	contest := individualContest()
	starts := 0
	began := time.Now().Add(-10 * time.Minute)
	running := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive, StartedAt: &began}
	service := accessFixture(people{participant: running, starts: &starts}, contestStore{contest: contest})

	got, err := service.StartOnRead(t.Context(), contest, running, netip.Addr{})
	if err != nil {
		t.Fatalf("StartOnRead() = %v", err)
	}
	if starts != 0 {
		t.Fatalf("Start called %d times for a participant already started, want 0", starts)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(began) {
		t.Fatalf("StartedAt = %v, want %v unchanged", got.StartedAt, began)
	}
}

func TestStartOnReadNeverStartsAFixedTimingClock(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	starts := 0
	p := contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}
	service := accessFixture(people{participant: p, starts: &starts}, contestStore{contest: contest})

	if _, err := service.StartOnRead(t.Context(), contest, p, netip.Addr{}); err != nil {
		t.Fatalf("StartOnRead() = %v", err)
	}
	if starts != 0 {
		t.Fatalf("Start called %d times under fixed timing, want 0", starts)
	}
}

// Outside the contest's window the read is refused as a first query would be,
// and nothing is written.
func TestStartOnReadOutsideTheWindowStartsNoClock(t *testing.T) {
	for name, given := range map[string]struct {
		shift func(*contests.Contest)
		want  error
	}{
		"before starts_at": {func(c *contests.Contest) { later := time.Now().Add(time.Hour); c.StartsAt = &later }, contests.ErrContestNotRunning},
		"after ends_at":    {func(c *contests.Contest) { earlier := time.Now().Add(-time.Minute); c.EndsAt = &earlier }, contests.ErrDeadlinePassed},
		"finished":         {func(c *contests.Contest) { c.Status = contests.StatusFinished }, contests.ErrContestEnded},
	} {
		t.Run(name, func(t *testing.T) {
			contest := individualContest()
			given.shift(&contest)
			starts := 0
			p := contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}
			service := accessFixture(people{participant: p, starts: &starts}, contestStore{contest: contest})

			if _, err := service.StartOnRead(t.Context(), contest, p, netip.Addr{}); !errors.Is(err, given.want) {
				t.Fatalf("StartOnRead() = %v, want %v", err, given.want)
			}
			if starts != 0 {
				t.Fatalf("Start called %d times outside the window, want 0", starts)
			}
		})
	}
}

// StartOnRead asks the gate itself, address included, rather than trust that
// Access ran first.
func TestStartOnReadFromADisallowedAddressStartsNoClock(t *testing.T) {
	contest := individualContest()
	contest.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}
	starts := 0
	p := contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}
	service := accessFixture(people{participant: p, starts: &starts}, contestStore{contest: contest})

	if _, err := service.StartOnRead(t.Context(), contest, p, netip.MustParseAddr("203.0.113.7")); !errors.Is(err, contests.ErrAddressNotAllowed) {
		t.Fatalf("StartOnRead() = %v, want ErrAddressNotAllowed", err)
	}
	if starts != 0 {
		t.Fatalf("Start called %d times from a disallowed address, want 0", starts)
	}
	if _, err := service.StartOnRead(t.Context(), contest, p, netip.MustParseAddr("10.20.3.4")); err != nil {
		t.Fatalf("StartOnRead() from the room = %v, want nil", err)
	}
	if starts != 1 {
		t.Fatalf("Start called %d times from the room, want 1", starts)
	}
}

// At exactly ends_at it is too late to begin: no grace for starting.
func TestStartOnReadAtExactlyEndsAtStartsNoClock(t *testing.T) {
	opened := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	closes := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	duration := 30
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, StartsAt: &opened, EndsAt: &closes,
	}
	starts := 0
	p := contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}
	service := queryproxy.New(people{participant: p, starts: &starts}, contestStore{contest: contest},
		games{}, &databases{}, &runner{}, contests.NewGate(5*time.Second)).
		WithClock(func() time.Time { return closes })

	if _, err := service.StartOnRead(t.Context(), contest, p, netip.Addr{}); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("StartOnRead() = %v, want ErrDeadlinePassed", err)
	}
	if starts != 0 {
		t.Fatalf("Start called %d times at ends_at, want 0", starts)
	}
}

// The gate is asked again after Start: a request that lost the race is handed
// the earlier start, whose time may be up.
func TestStartOnReadRefusesAStartWhoseTimeIsAlreadyUp(t *testing.T) {
	now := time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC)
	opened := now.Add(-3 * time.Hour)
	closes := now.Add(3 * time.Hour)
	longAgo := now.Add(-2 * time.Hour)
	duration := 30
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		DurationMin: &duration, StartsAt: &opened, EndsAt: &closes,
	}
	starts := 0
	p := contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}
	service := accessFixture(people{participant: p, stored: &longAgo, starts: &starts}, contestStore{contest: contest}).
		WithClock(func() time.Time { return now })

	if _, err := service.StartOnRead(t.Context(), contest, p, netip.Addr{}); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("StartOnRead() = %v, want ErrDeadlinePassed", err)
	}
	// Start was reached: the refusal comes from the check after it.
	if starts != 1 {
		t.Fatalf("Start called %d times, want 1", starts)
	}
}

// A Start that leaves the clock pending would hand out content with no
// deadline running; it is refused as ours.
func TestStartOnReadRefusesAStartThatLeftTheClockPending(t *testing.T) {
	contest := individualContest()
	starts := 0
	p := contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}
	service := accessFixture(people{participant: p, startsNothing: true, starts: &starts}, contestStore{contest: contest})

	if _, err := service.StartOnRead(t.Context(), contest, p, netip.Addr{}); !errors.Is(err, queryproxy.ErrUnavailable) {
		t.Fatalf("StartOnRead() = %v, want ErrUnavailable", err)
	}
	if starts != 1 {
		t.Fatalf("Start called %d times, want 1", starts)
	}
}

// A registration disqualified before Start stays pending: that is the gate's
// refusal, not a broken store.
func TestStartOnReadRefusesARegistrationDisqualifiedBeforeItsClockStarted(t *testing.T) {
	contest := individualContest()
	starts := 0
	p := contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}
	disqualified := p
	disqualified.Status = contests.RegistrationDisqualified
	service := accessFixture(people{participant: disqualified, startsNothing: true, starts: &starts}, contestStore{contest: contest})

	if _, err := service.StartOnRead(t.Context(), contest, p, netip.Addr{}); !errors.Is(err, contests.ErrNotAParticipant) {
		t.Fatalf("StartOnRead() = %v, want ErrNotAParticipant", err)
	}
	if starts != 1 {
		t.Fatalf("Start called %d times, want 1", starts)
	}
}

func TestStartOnReadMarksAFailureToStartAsOurs(t *testing.T) {
	contest := individualContest()
	p := contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}
	service := accessFixture(people{participant: p, startErr: errors.New("connection reset")}, contestStore{contest: contest})

	if _, err := service.StartOnRead(t.Context(), contest, p, netip.Addr{}); !errors.Is(err, queryproxy.ErrUnavailable) {
		t.Fatalf("StartOnRead() = %v, want ErrUnavailable", err)
	}
}

// Holding the events channel open is not reading the contest.
func TestAccessForEventsNeverStartsTheClock(t *testing.T) {
	contest := individualContest()
	starts := 0
	p := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered}
	service := accessFixture(people{participant: p, starts: &starts}, contestStore{contest: contest})

	participant, _, _, err := service.AccessForEvents(t.Context(), contest.ID, uuid.New(), netip.Addr{})
	if err != nil {
		t.Fatalf("AccessForEvents() = %v", err)
	}
	if starts != 0 || participant.StartedAt != nil {
		t.Fatalf("Start called %d times by the events channel, want 0", starts)
	}
}

// The address restriction applies to the participant, whichever endpoint
// they call.
func TestAccessChecksTheAddressRestriction(t *testing.T) {
	inRoom := netip.MustParsePrefix("10.20.0.0/16")
	contest := contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow,
		AllowedCIDRs: []netip.Prefix{inRoom},
	}
	service := accessFixture(people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}}, contestStore{contest: contest})

	if _, _, err := service.Access(t.Context(), uuid.New(), uuid.New(), netip.MustParseAddr("203.0.113.7")); !errors.Is(err, contests.ErrAddressNotAllowed) {
		t.Fatalf("error = %v, want ErrAddressNotAllowed", err)
	}
	if _, _, err := service.Access(t.Context(), uuid.New(), uuid.New(), netip.MustParseAddr("10.20.3.4")); err != nil {
		t.Fatalf("a read from the contest's own network was refused: %v", err)
	}
}

// AccessForEvents admits one case Access refuses, a published contest not yet
// started, so the channel can wait across the start. Every other row refuses
// as Access does.
func TestAccessForEventsAdmitsExactlyOneMoreStatusThanAccess(t *testing.T) {
	for name, given := range map[string]struct {
		people  people
		contest contests.Contest
		want    error
	}{
		"a contest that is published and has not started": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}},
			contest: contests.Contest{Status: contests.StatusPublished},
			want:    nil,
		},
		"somebody who never registered": {
			people:  people{err: contests.ErrParticipantNotFound},
			contest: contests.Contest{Status: contests.StatusPublished},
			want:    contests.ErrNotAParticipant,
		},
		"somebody disqualified, even for a published contest": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationDisqualified}},
			contest: contests.Contest{Status: contests.StatusPublished},
			want:    contests.ErrNotAParticipant,
		},
		"a contest that has finished": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusFinished},
			want:    contests.ErrContestEnded,
		},
		"a contest still a draft": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}},
			contest: contests.Contest{Status: contests.StatusDraft},
			want:    contests.ErrContestNotRunning,
		},
		"a running contest, exactly as Access itself admits it": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    nil,
		},
		"a running contest whose time is up for them": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &closedWindow},
			want:    contests.ErrDeadlinePassed,
		},
	} {
		t.Run(name, func(t *testing.T) {
			service := accessFixture(given.people, contestStore{contest: given.contest})

			_, _, _, err := service.AccessForEvents(t.Context(), uuid.New(), uuid.New(), netip.Addr{})
			if !errors.Is(err, given.want) {
				t.Fatalf("error = %v, want %v", err, given.want)
			}
		})
	}
}

// The Standing is returned refused or not, so the channel can tell "over for
// them" from "not open now". A lookup that found nobody returns the zero
// Standing, over for nobody.
func TestAccessForEventsHandsBackTheStandingItDecidedOn(t *testing.T) {
	inRoom := []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}
	for name, given := range map[string]struct {
		people  people
		contest contests.Contest
		addr    netip.Addr
		want    error
		over    bool
	}{
		"a running contest, open to them": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    nil, over: false,
		},
		"a running contest whose time is up for them": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &closedWindow},
			want:    contests.ErrDeadlinePassed, over: true,
		},
		"a contest that has finished": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusFinished},
			want:    contests.ErrContestEnded, over: true,
		},
		"a registration that is finished": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationFinished}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    contests.ErrParticipantFinished, over: true,
		},
		"somebody disqualified": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationDisqualified}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    contests.ErrNotAParticipant, over: true,
		},
		"a contest taken back to draft": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusDraft},
			want:    contests.ErrContestNotRunning, over: false,
		},
		"a running contest, open to them, from outside its network": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow, AllowedCIDRs: inRoom},
			addr:    netip.MustParseAddr("203.0.113.7"),
			want:    contests.ErrAddressNotAllowed, over: false,
		},
		"somebody who never registered": {
			people:  people{err: contests.ErrParticipantNotFound},
			contest: contests.Contest{Status: contests.StatusFinished},
			want:    contests.ErrNotAParticipant, over: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			service := accessFixture(given.people, contestStore{contest: given.contest})

			_, _, standing, err := service.AccessForEvents(t.Context(), uuid.New(), uuid.New(), given.addr)
			if !errors.Is(err, given.want) {
				t.Fatalf("error = %v, want %v", err, given.want)
			}
			if standing.Over() != given.over {
				t.Fatalf("Over() = %v, want %v", standing.Over(), given.over)
			}
		})
	}
}

// The network restriction applies before the contest starts too.
func TestAccessForEventsStillChecksTheAddressRestrictionForAPublishedContest(t *testing.T) {
	inRoom := netip.MustParsePrefix("10.20.0.0/16")
	contest := contests.Contest{Status: contests.StatusPublished, AllowedCIDRs: []netip.Prefix{inRoom}}
	service := accessFixture(people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}}, contestStore{contest: contest})

	if _, _, _, err := service.AccessForEvents(t.Context(), uuid.New(), uuid.New(), netip.MustParseAddr("203.0.113.7")); !errors.Is(err, contests.ErrAddressNotAllowed) {
		t.Fatalf("error = %v, want ErrAddressNotAllowed", err)
	}
	if _, _, _, err := service.AccessForEvents(t.Context(), uuid.New(), uuid.New(), netip.MustParseAddr("10.20.3.4")); err != nil {
		t.Fatalf("a read from the contest's own network was refused for a published contest: %v", err)
	}
}

// Only the database's own words are withheld; our refusals and outcomes carry
// codes the interface translates.
func TestClosingTheCataloguesDoesNotSwallowOurOwnAnswers(t *testing.T) {
	closed := sqlpolicy.ReadOnly()
	closed.AllowCatalog = false

	for name, failure := range map[string]error{
		"a refusal":       &sqlpolicy.Refusal{Code: sqlpolicy.CodeFunctionNotSupported, Subject: "pg_sleep"},
		"a timeout":       queryrunner.ErrTimeout,
		"a full instance": queryrunner.ErrBusy,
		"asking too fast": queryrunner.ErrTooManyQueries,
		"a full disk":     queryrunner.ErrDiskFull,
		"a huge answer":   queryrunner.ErrResultTooLarge,
		// On no list of ours, and still not the database refusing the query.
		"the query service failing": fmt.Errorf("%w: connecting to the game database: dial tcp [::1]:5433: connect: connection refused",
			rpc.ErrUnreachable),
	} {
		t.Run(name, func(t *testing.T) {
			service := queryproxy.New(
				people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
				contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
				games{game: provisioning.Contest{Policy: closed}},
				&databases{database: "x"}, &runner{err: failure},
				fiveSecondGate,
			)

			_, err := service.Run(t.Context(), command())
			if errors.Is(err, queryproxy.ErrDatabaseDeclined) {
				t.Fatalf("%v was swallowed as a database refusal", failure)
			}
		})
	}
}

// An unreachable database must not read as "your request was bad": the
// client would stop retrying and the participant would fix a fine query.
func TestOurOwnFailuresAreMarkedApartFromTheQuerysOwn(t *testing.T) {
	broken := errors.New("dial tcp 172.28.0.5:5432: connection refused")

	for name, service := range map[string]*queryproxy.Service{
		"the registration cannot be read": queryproxy.New(
			people{err: broken}, contestStore{}, games{}, &databases{}, &runner{}, fiveSecondGate),
		"the contest cannot be read": queryproxy.New(
			people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contestStore{err: broken}, games{}, &databases{}, &runner{}, fiveSecondGate),
		"the database cannot be provided": queryproxy.New(
			people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
			games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
			&databases{err: broken}, &runner{}, fiveSecondGate),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.Run(t.Context(), command())
			if !errors.Is(err, queryproxy.ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
		})
	}
}

// A read-only database cannot grow, so the quota, a round trip to the game
// cluster, is never asked for.
func TestAReadOnlyContestDoesNotAskTheClusterAboutSizeAtAll(t *testing.T) {
	db := &databases{database: "x", quota: 1 << 20}
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		db, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("running: %v", err)
	}
	if db.quotaAsked {
		t.Fatal("a read-only contest asked the cluster for a size limit it cannot reach")
	}

	// A writable contest still gets one: it is all that stands between a
	// participant and the cluster's disk.
	writing := &databases{database: "x", quota: 1 << 20}
	service = queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadWrite("evidence")}},
		writing, &runner{result: &queryrunner.Result{}, quotaSink: &writing.lastQuota},
		fiveSecondGate,
	)
	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("running: %v", err)
	}
	if !writing.quotaAsked {
		t.Fatal("a contest that permits writing got no size limit")
	}
	if got := writing.lastQuota; got != writing.quota {
		t.Fatalf("quota reached the runner as %d, want %d", got, writing.quota)
	}
}

// No runner outcome is mistaken for the database speaking in a contest that
// withholds the database's words, where nobody would notice.
func TestEveryOutcomeTheRunnerReportsIsRecognisedAsOurs(t *testing.T) {
	closed := sqlpolicy.ReadOnly()
	closed.AllowCatalog = false

	for _, outcome := range queryrunner.Outcomes() {
		t.Run(outcome.Error(), func(t *testing.T) {
			service := queryproxy.New(
				people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
				contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
				games{game: provisioning.Contest{Policy: closed}},
				&databases{database: "x"}, &runner{err: outcome},
				fiveSecondGate,
			)

			_, err := service.Run(t.Context(), command())
			if errors.Is(err, queryproxy.ErrDatabaseDeclined) {
				t.Fatalf("%v was swallowed as the database speaking", outcome)
			}
			if !errors.Is(err, outcome) {
				t.Fatalf("error = %v, want it to still be %v", err, outcome)
			}
		})
	}
}

// A journal failure never reached the database, so a closed catalogue must
// not turn it into ErrDatabaseDeclined.
func TestAJournalFailureIsNotSwallowedByAClosedCatalogue(t *testing.T) {
	closed := sqlpolicy.ReadOnly()
	closed.AllowCatalog = false

	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: closed}},
		&databases{database: "x"},
		&runner{err: fmt.Errorf("%w: %w", queryrunner.ErrJournalUnavailable, errors.New("dial tcp: connection refused"))},
		fiveSecondGate,
	)

	_, err := service.Run(t.Context(), command())
	if errors.Is(err, queryproxy.ErrDatabaseDeclined) {
		t.Fatalf("a journal failure was swallowed as the database refusing the query: %v", err)
	}
	if !errors.Is(err, queryrunner.ErrJournalUnavailable) {
		t.Fatalf("error = %v, want it to still be ErrJournalUnavailable", err)
	}
}

// An oversized query is refused for its length after the rate check
// (CLAUDE.md rule 13), and before provisioning or the Query Runner.
func TestAQueryOverTheLengthBoundIsRefusedAfterTheRateCheckAndBeforeAnythingElse(t *testing.T) {
	calls := 0
	db := &databases{database: "x"}
	run := &runner{result: &queryrunner.Result{}}
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}, calls: &calls},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		db, run,
		fiveSecondGate,
	)

	cmd := command()
	cmd.SQL = "SELECT " + strings.Repeat("a", sqlpolicy.MaxQueryBytes+1)

	_, err := service.Run(t.Context(), cmd)

	var refusal *sqlpolicy.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a refusal", err)
	}
	if refusal.Code != sqlpolicy.CodeTooLong {
		t.Fatalf("code = %q, want %q", refusal.Code, sqlpolicy.CodeTooLong)
	}
	if calls != 1 {
		t.Fatalf("the participant was looked up %d times, want 1 — the rate check needs it", calls)
	}
	if db.asked != nil {
		t.Fatalf("a database was provisioned for a query that was refused for its length")
	}
	if run.calls != 0 {
		t.Fatalf("the runner was reached %d times for a query that was refused for its length", run.calls)
	}
}

// An oversized query still spends its share of the rate budget.
func TestAQueryOverTheLengthBoundStillCountsAgainstTheRate(t *testing.T) {
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow,
		Settings: contests.Settings{QueryRateLimitPerMin: 1},
	}
	registration := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}
	service := queryproxy.New(
		people{participant: registration},
		contestStore{contest: contest},
		games{game: provisioning.Contest{ID: contest.ID, Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	oversized := command()
	oversized.SQL = "SELECT " + strings.Repeat("a", sqlpolicy.MaxQueryBytes+1)

	var refusal *sqlpolicy.Refusal
	if _, err := service.Run(t.Context(), oversized); !errors.As(err, &refusal) || refusal.Code != sqlpolicy.CodeTooLong {
		t.Fatalf("the oversized query: error = %v, want CodeTooLong", err)
	}

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("a legitimate query right after: error = %v, want ErrTooManyQueries", err)
	}
}

func TestAQueryWithinTheLengthBoundIsUnaffected(t *testing.T) {
	service, _, _ := fixture(t)
	cmd := command()
	cmd.SQL = "SELECT " + strings.Repeat("a", sqlpolicy.MaxQueryBytes-100)

	if _, err := service.Run(t.Context(), cmd); err != nil {
		t.Fatalf("a query within the bound was refused: %v", err)
	}
}

// A query over the rate is refused here, before the journal write in front of
// the Query Runner's own limiter (CLAUDE.md rule 13).
func TestAParticipantAskingTooFastIsRefusedBeforeTheRunnerIsReached(t *testing.T) {
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow,
		Settings: contests.Settings{QueryRateLimitPerMin: 1},
	}
	registration := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}
	run := &runner{result: &queryrunner.Result{Columns: []string{"a"}}}
	service := queryproxy.New(
		people{participant: registration},
		contestStore{contest: contest},
		games{game: provisioning.Contest{ID: contest.ID, Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, run,
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("the first query in the minute: %v", err)
	}
	if run.calls != 1 {
		t.Fatalf("the runner was reached %d times for the first query, want 1", run.calls)
	}

	_, err := service.Run(t.Context(), command())
	if !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("error = %v, want ErrTooManyQueries", err)
	}
	if run.calls != 1 {
		t.Fatalf("the runner was reached by a query that should have been refused for its rate (calls=%d)", run.calls)
	}
}

// A query refused for timing still costs the lookups, so the rate check runs
// ahead of that refusal.
func TestARequestRefusedByTheDeadlineStillCountsAgainstTheRate(t *testing.T) {
	deadline := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &deadline,
		Settings: contests.Settings{QueryRateLimitPerMin: 1},
	}
	registration := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}
	service := queryproxy.New(
		people{participant: registration},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	).WithClock(func() time.Time { return deadline.Add(time.Minute) })

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, contests.ErrDeadlinePassed) {
		t.Fatalf("the first query past the deadline: error = %v, want ErrDeadlinePassed", err)
	}

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("a second query in the same state: error = %v, want ErrTooManyQueries — the first should have counted", err)
	}
}

// The same, for a contest that has not opened yet.
func TestARequestRefusedBecauseTheContestIsNotRunningStillCountsAgainstTheRate(t *testing.T) {
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusPublished,
		Settings: contests.Settings{QueryRateLimitPerMin: 1},
	}
	registration := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered}
	service := queryproxy.New(
		people{participant: registration},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, contests.ErrContestNotRunning) {
		t.Fatalf("the first query before the contest opened: error = %v, want ErrContestNotRunning", err)
	}

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("a second query in the same state: error = %v, want ErrTooManyQueries — the first should have counted", err)
	}
}

// The first rate check is keyed by the account (cmd.UserID), which is bounded
// where the contest ID is not, and runs before the lookup (CLAUDE.md rule 5).
// This test and the next two reuse one Command, so UserID stays fixed, and
// prove the lookup is reached only while the limiter admits the request.
func TestANeverRegisteredCallerEventuallyMeetsTheLimiterBeforeTheLookup(t *testing.T) {
	calls := 0
	service := queryproxy.New(
		people{err: contests.ErrParticipantNotFound, calls: &calls},
		contestStore{}, games{}, &databases{}, &runner{},
		fiveSecondGate,
	).WithPerMinuteDefault(1)
	cmd := command()

	if _, err := service.Run(t.Context(), cmd); !errors.Is(err, contests.ErrNotAParticipant) {
		t.Fatalf("the first request: error = %v, want ErrNotAParticipant", err)
	}
	if calls != 1 {
		t.Fatalf("the participant was looked up %d times after 1 request, want 1", calls)
	}

	if _, err := service.Run(t.Context(), cmd); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("a second request from the same caller: error = %v, want ErrTooManyQueries", err)
	}
	if calls != 1 {
		t.Fatalf("the participant was looked up %d times after the rate limiter should have refused the second request, want still 1", calls)
	}
}

func TestADisqualifiedCallerEventuallyMeetsTheLimiterBeforeTheLookup(t *testing.T) {
	calls := 0
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationDisqualified}, calls: &calls},
		contestStore{}, games{}, &databases{}, &runner{},
		fiveSecondGate,
	).WithPerMinuteDefault(1)
	cmd := command()

	if _, err := service.Run(t.Context(), cmd); !errors.Is(err, contests.ErrNotAParticipant) {
		t.Fatalf("the first request: error = %v, want ErrNotAParticipant", err)
	}
	if calls != 1 {
		t.Fatalf("the participant was looked up %d times after 1 request, want 1", calls)
	}

	if _, err := service.Run(t.Context(), cmd); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("a second request from the same caller: error = %v, want ErrTooManyQueries", err)
	}
	if calls != 1 {
		t.Fatalf("the participant was looked up %d times after the rate limiter should have refused the second request, want still 1", calls)
	}
}

func TestAFinishedCallerEventuallyMeetsTheLimiterBeforeTheLookup(t *testing.T) {
	calls := 0
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationFinished}, calls: &calls},
		contestStore{}, games{}, &databases{}, &runner{},
		fiveSecondGate,
	).WithPerMinuteDefault(1)
	cmd := command()

	if _, err := service.Run(t.Context(), cmd); !errors.Is(err, contests.ErrParticipantFinished) {
		t.Fatalf("the first request: error = %v, want ErrParticipantFinished", err)
	}
	if calls != 1 {
		t.Fatalf("the participant was looked up %d times after 1 request, want 1", calls)
	}

	if _, err := service.Run(t.Context(), cmd); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("a second request from the same caller: error = %v, want ErrTooManyQueries", err)
	}
	if calls != 1 {
		t.Fatalf("the participant was looked up %d times after the rate limiter should have refused the second request, want still 1", calls)
	}
}

// The early check uses the installation's ceiling, so it never binds before a
// contest's own limit would.
func TestALegitimateParticipantIsUnaffectedByTheEarlyRateCheck(t *testing.T) {
	service, _, _ := fixture(t)
	cmd := command()

	for i := range 3 {
		if _, err := service.Run(t.Context(), cmd); err != nil {
			t.Fatalf("query %d from a legitimate participant: %v", i+1, err)
		}
	}
}

// New panics without a gate, so the console and the answer route cannot
// disagree about when time is up.
func TestNewRefusesToAssembleWithoutAGate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New(..., nil) did not panic")
		}
	}()
	queryproxy.New(people{}, contestStore{}, games{}, &databases{}, &runner{}, nil)
}

// A negative QUERY_PER_MINUTE panics, as a negative DEADLINE_GRACE does in
// contests.NewGate: config.Load never produces one.
func TestWithPerMinuteDefaultPanicsOnANegativeValue(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("WithPerMinuteDefault(-1) did not panic")
		}
	}()
	queryproxy.New(people{}, contestStore{}, games{}, &databases{}, &runner{}, fiveSecondGate).WithPerMinuteDefault(-1)
}

// The contest's query_rate_limit_per_min is enforced, and zero falls back to
// the installation's default rather than no limit.
func TestTheContestsOwnRateLimitIsEnforced(t *testing.T) {
	strict := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow, Settings: contests.Settings{QueryRateLimitPerMin: 1}}
	lenient := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow} // zero: installation default

	strictService := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: strict},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)
	lenientService := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: lenient},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	)

	if _, err := strictService.Run(t.Context(), command()); err != nil {
		t.Fatalf("the strict contest's first query: %v", err)
	}
	if _, err := strictService.Run(t.Context(), command()); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("the strict contest's second query: %v, want ErrTooManyQueries", err)
	}

	for i := range 5 {
		if _, err := lenientService.Run(t.Context(), command()); err != nil {
			t.Fatalf("the lenient contest's query %d was refused: %v", i+1, err)
		}
	}
}

// WithPerMinuteDefault keeps the pre-check in step with the Query Runner's
// QUERY_PER_MINUTE.
func TestWithPerMinuteDefaultOverridesTheFallback(t *testing.T) {
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	).WithPerMinuteDefault(1)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("the first query: %v", err)
	}
	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("error = %v, want ErrTooManyQueries", err)
	}
}

// A contest's rate above the installation's is clamped: the Query Runner would
// refuse the excess anyway, after the journal write. Installation 3, contest
// 5: only 3 pass.
func TestAContestCannotSetALooserRateThanTheInstallation(t *testing.T) {
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow,
		Settings: contests.Settings{QueryRateLimitPerMin: 5},
	}
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}},
		contestStore{contest: contest},
		games{game: provisioning.Contest{ID: contest.ID, Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	).WithPerMinuteDefault(3)

	admitted := 0
	var last error
	for range 5 {
		if _, err := service.Run(t.Context(), command()); err != nil {
			last = err
			break
		}
		admitted++
	}
	if admitted != 3 {
		t.Fatalf("admitted %d queries before a refusal, want 3 (the installation's own figure)", admitted)
	}
	if !errors.Is(last, queryrunner.ErrTooManyQueries) {
		t.Fatalf("the refusal was %v, want ErrTooManyQueries", last)
	}
}

func TestAContestMaySetAStricterRateThanTheInstallation(t *testing.T) {
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow,
		Settings: contests.Settings{QueryRateLimitPerMin: 2},
	}
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}},
		contestStore{contest: contest},
		games{game: provisioning.Contest{ID: contest.ID, Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	).WithPerMinuteDefault(30)

	for i := range 2 {
		if _, err := service.Run(t.Context(), command()); err != nil {
			t.Fatalf("query %d of the contest's own allowance: %v", i+1, err)
		}
	}
	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("error = %v, want ErrTooManyQueries", err)
	}
}

// With no installation limit (zero) the contest's own number governs, however
// high.
func TestAContestsRateIsNotClampedWhenTheInstallationHasNoLimit(t *testing.T) {
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow,
		Settings: contests.Settings{QueryRateLimitPerMin: 50},
	}
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}},
		contestStore{contest: contest},
		games{game: provisioning.Contest{ID: contest.ID, Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	).WithPerMinuteDefault(0)

	for i := range 50 {
		if _, err := service.Run(t.Context(), command()); err != nil {
			t.Fatalf("query %d of 50, within the contest's own allowance: %v", i+1, err)
		}
	}
}

// AdmitRead shares Run's limiter and keys, so alternating queries and reads
// spends one budget, not two.
func TestAdmitReadSharesRunsOwnPerAccountBudget(t *testing.T) {
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	).WithPerMinuteDefault(1)

	userID := uuid.New()
	cmd := command()
	cmd.UserID = userID

	if _, err := service.Run(t.Context(), cmd); err != nil {
		t.Fatalf("the first query from this account: %v", err)
	}

	if err := service.AdmitRead(userID); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("AdmitRead() right after this account's own query = %v, want ErrTooManyQueries — the budget must be shared with Run, not doubled", err)
	}
}

// The key is the account, so a fresh account has spent nothing however many
// contests another account tried.
func TestAdmitReadIsKeyedPerAccountNotShared(t *testing.T) {
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	).WithPerMinuteDefault(1)

	spent := uuid.New()
	cmd := command()
	cmd.UserID = spent
	if _, err := service.Run(t.Context(), cmd); err != nil {
		t.Fatalf("spending the first account's budget: %v", err)
	}
	if err := service.AdmitRead(spent); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("AdmitRead() for the spent account = %v, want ErrTooManyQueries", err)
	}

	if err := service.AdmitRead(uuid.New()); err != nil {
		t.Fatalf("AdmitRead() for an account that made no request = %v, want nil", err)
	}
}

func TestAParticipantWithNothingLeftToAnswerIsRefusedTheConsole(t *testing.T) {
	service, _, run := fixture(t)
	service.WithAnswerable(&answerable{left: false})

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrNothingLeftToAnswer) {
		t.Fatalf("error = %v, want ErrNothingLeftToAnswer", err)
	}
	if run.calls != 0 {
		t.Fatalf("the runner was reached %d times, want 0 — a refused query is never journalled or executed", run.calls)
	}
}

// Narrower than ErrParticipantFinished: no status is written and the read
// endpoints stay open.
func TestNothingLeftToAnswerDoesNotCloseTheReadEndpoints(t *testing.T) {
	service, _, _ := fixture(t)
	service.WithAnswerable(&answerable{left: false})

	cmd := command()
	if _, err := service.Run(t.Context(), cmd); !errors.Is(err, queryproxy.ErrNothingLeftToAnswer) {
		t.Fatalf("Run() = %v, want ErrNothingLeftToAnswer", err)
	}
	if _, _, err := service.Access(t.Context(), cmd.ContestID, cmd.UserID, cmd.Address); err != nil {
		t.Fatalf("Access() = %v, want nil — only the console closes, not the play screen", err)
	}
}

func TestAParticipantWithAQuestionStillOpenKeepsTheConsole(t *testing.T) {
	service, _, run := fixture(t)
	answers := &answerable{left: true}
	service.WithAnswerable(answers)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("running: %v", err)
	}
	if answers.calls != 1 {
		t.Fatalf("AnswerableLeft was asked %d times, want exactly 1", answers.calls)
	}
	if run.calls != 1 {
		t.Fatalf("the runner was reached %d times, want 1", run.calls)
	}
}

// A build without the reader fails open (see WithAnswerable).
func TestAServiceWithNoAnswerableWiredStillRunsQueries(t *testing.T) {
	service, _, run := fixture(t)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("running with no Answerable wired: %v", err)
	}
	if run.calls != 1 {
		t.Fatalf("the runner was reached %d times, want 1", run.calls)
	}
}

// A refused query must not provision a database. The game arrives with the
// same lookup before this check, so only provisioning is asserted.
func TestNothingLeftToAnswerIsRefusedBeforeAnyDatabaseIsProvisioned(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	db := &databases{database: "game_c1_u1"}
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		db, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	).WithAnswerable(&answerable{left: false})

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrNothingLeftToAnswer) {
		t.Fatalf("error = %v, want ErrNothingLeftToAnswer", err)
	}
	if db.asked != nil {
		t.Fatalf("a database was provisioned for a refused query: %+v", db.asked)
	}
}

// The reader failing is ours, and must not read as "nothing left to do".
func TestAFailingAnswerableReadsAsUnavailableAndNotAsARefusal(t *testing.T) {
	service, _, _ := fixture(t)
	service.WithAnswerable(&answerable{err: errors.New("the core database is down")})

	_, err := service.Run(t.Context(), command())
	if !errors.Is(err, queryproxy.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if errors.Is(err, queryproxy.ErrNothingLeftToAnswer) {
		t.Fatal("a failed read was reported as nothing left to answer")
	}
}

// New's default builds LookupResult from three calls; WithLookup replaces it
// with one, and none of the three runs.
func TestTheSingleLookupRemovesTwoCoreRoundTripsFromRun(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	participant := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}
	game := provisioning.Contest{ID: contest.ID, Template: "game_tpl_c1", Policy: sqlpolicy.ReadOnly()}

	t.Run("New's own default: three separate calls", func(t *testing.T) {
		peopleCalls, contestCalls, gameCalls := 0, 0, 0
		service := queryproxy.New(
			people{participant: participant, calls: &peopleCalls},
			contestStore{contest: contest, calls: &contestCalls},
			games{game: game, calls: &gameCalls},
			&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
			fiveSecondGate,
		)
		if _, err := service.Run(t.Context(), command()); err != nil {
			t.Fatalf("running: %v", err)
		}
		if peopleCalls != 1 || contestCalls != 1 || gameCalls != 1 {
			t.Fatalf("round trips = participant %d, contest %d, game %d, want 1, 1 and 1 — the three calls this replaces",
				peopleCalls, contestCalls, gameCalls)
		}
	})

	t.Run("with a single query wired", func(t *testing.T) {
		peopleCalls, contestCalls, gameCalls, lookupCalls := 0, 0, 0, 0
		service := queryproxy.New(
			people{participant: participant, calls: &peopleCalls},
			contestStore{contest: contest, calls: &contestCalls},
			games{game: game, calls: &gameCalls},
			&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
			fiveSecondGate,
		).WithLookup(lookupFake{participant: participant, contest: contest, game: game, calls: &lookupCalls})

		if _, err := service.Run(t.Context(), command()); err != nil {
			t.Fatalf("running: %v", err)
		}
		if lookupCalls != 1 {
			t.Fatalf("lookup calls = %d, want 1", lookupCalls)
		}
		if peopleCalls != 0 || contestCalls != 0 || gameCalls != 0 {
			t.Fatalf("the default's three separate calls still ran (%d, %d, %d) once a single query was wired",
				peopleCalls, contestCalls, gameCalls)
		}
	})
}

// The participant's copy arrives with the lookup, and provisioning is told
// what was read rather than reading it again.
func TestTheInstanceTheLookupReadReachesProvisioningUntouched(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	participant := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}
	game := provisioning.Contest{ID: contest.ID, Template: "game_tpl_c1", Version: 2, Policy: sqlpolicy.ReadOnly()}
	instance := provisioning.Instance{Database: "game_c1_u1", TemplateVersion: 2, Status: "ready"}

	for name, given := range map[string]struct {
		instance provisioning.Instance
		wantErr  error
	}{
		"a registration with a copy":   {instance: instance},
		"a registration with none yet": {wantErr: provisioning.ErrNoInstance},
	} {
		t.Run(name, func(t *testing.T) {
			db := &databases{database: "x"}
			service := queryproxy.New(people{}, contestStore{}, games{}, db, &runner{result: &queryrunner.Result{}}, fiveSecondGate).
				WithLookup(lookupFake{participant: participant, contest: contest, game: game, instance: given.instance})

			if _, err := service.Run(t.Context(), command()); err != nil {
				t.Fatalf("running: %v", err)
			}
			if db.existing != given.instance || !errors.Is(db.existingErr, given.wantErr) || (given.wantErr == nil && db.existingErr != nil) {
				t.Fatalf("EnsureFrom was told %+v, %v; want %+v, %v", db.existing, db.existingErr, given.instance, given.wantErr)
			}
		})
	}
}

// Access is paid by every participant-facing read, so it must use the one
// lookup the deployment wires rather than the default's two separate reads.
func TestAccessUsesTheSingleLookupOnceWired(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	participant := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}
	peopleCalls, contestCalls, lookupCalls := 0, 0, 0
	service := queryproxy.New(
		people{participant: participant, calls: &peopleCalls},
		contestStore{contest: contest, calls: &contestCalls},
		games{}, &databases{}, &runner{},
		fiveSecondGate,
	).WithLookup(lookupFake{participant: participant, contest: contest, calls: &lookupCalls})

	got, _, err := service.Access(t.Context(), contest.ID, uuid.New(), netip.Addr{})
	if err != nil {
		t.Fatalf("Access() = %v", err)
	}
	if got.ID != participant.ID {
		t.Fatalf("Access() answered participant %v, want %v", got.ID, participant.ID)
	}
	if lookupCalls != 1 || peopleCalls != 0 || contestCalls != 0 {
		t.Fatalf("calls = lookup %d, participant %d, contest %d; want 1, 0 and 0", lookupCalls, peopleCalls, contestCalls)
	}
}

// A failed single lookup is ours, like a failed separate read.
func TestASingleLookupsFailureIsMarkedAsOurs(t *testing.T) {
	broken := errors.New("dial tcp 172.28.0.5:5432: connection refused")
	service := queryproxy.New(
		people{}, contestStore{}, games{}, &databases{}, &runner{},
		fiveSecondGate,
	).WithLookup(lookupFake{err: broken})

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

// Reading the game in the combined lookup must not move where a missing game
// is noticed: the address, length and answerable checks still refuse first.
func TestASingleLookupsMissingGameIsStillCheckedAtItsUsualPoint(t *testing.T) {
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")},
	}
	participant := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}
	lookup := lookupFake{participant: participant, contest: contest, gameErr: provisioning.ErrNoGame}

	// The address restriction refuses before the game is consulted.
	fromHome := command()
	fromHome.Address = netip.MustParseAddr("203.0.113.7")
	service := queryproxy.New(people{}, contestStore{}, games{}, &databases{}, &runner{}, fiveSecondGate).WithLookup(lookup)
	if _, err := service.Run(t.Context(), fromHome); !errors.Is(err, contests.ErrAddressNotAllowed) {
		t.Fatalf("error = %v, want ErrAddressNotAllowed — the address check comes before the game is looked at", err)
	}

	// So does nothing left to answer, though both are true.
	fromRoom := command()
	fromRoom.Address = netip.MustParseAddr("10.20.3.4")
	service = queryproxy.New(people{}, contestStore{}, games{}, &databases{}, &runner{}, fiveSecondGate).
		WithLookup(lookup).WithAnswerable(&answerable{left: false})
	if _, err := service.Run(t.Context(), fromRoom); !errors.Is(err, queryproxy.ErrNothingLeftToAnswer) {
		t.Fatalf("error = %v, want ErrNothingLeftToAnswer — answerable comes before the game is looked at", err)
	}

	// With nothing else refusing, the missing game answers.
	service = queryproxy.New(people{}, contestStore{}, games{}, &databases{}, &runner{}, fiveSecondGate).WithLookup(lookup)
	if _, err := service.Run(t.Context(), fromRoom); !errors.Is(err, queryproxy.ErrNoGameYet) {
		t.Fatalf("error = %v, want ErrNoGameYet", err)
	}
}

// watcher is the fake behind queryproxy.Watcher: every visit Run reported.
type watcher struct{ visits []monitor.Visit }

func (w *watcher) Observe(_ context.Context, visit monitor.Visit) { w.visits = append(w.visits, visit) }

// An admitted query is reported to the watcher with its address and session.
func TestAnAdmittedQueryIsObserved(t *testing.T) {
	service, _, run := fixture(t)
	seen := &watcher{}
	service = service.WithWatcher(seen)
	cmd := command()
	cmd.Address = netip.MustParseAddr("192.0.2.44")
	cmd.Session = monitor.SessionTag("token")
	cmd.UserAgent = "Firefox"

	if _, err := service.Run(t.Context(), cmd); err != nil {
		t.Fatalf("running: %v", err)
	}
	if len(seen.visits) != 1 {
		t.Fatalf("the query was observed %d times, want once", len(seen.visits))
	}
	got := seen.visits[0]
	if got.Registration != run.got.Registration || got.Contest == uuid.Nil ||
		got.Address != cmd.Address || got.Session != cmd.Session || got.UserAgent != cmd.UserAgent {
		t.Fatalf("visit = %+v, want the registration %v, the command's address, session and browser", got, run.got.Registration)
	}
}

// A query refused at admission is not observed.
func TestARefusedQueryIsNotObserved(t *testing.T) {
	seen := &watcher{}
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusFinished}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
		fiveSecondGate,
	).WithWatcher(seen)
	cmd := command()
	cmd.Address = netip.MustParseAddr("192.0.2.44")
	cmd.Session = monitor.SessionTag("token")

	if _, err := service.Run(t.Context(), cmd); !errors.Is(err, contests.ErrContestEnded) {
		t.Fatalf("error = %v, want ErrContestEnded", err)
	}
	if len(seen.visits) != 0 {
		t.Fatalf("a refused query was observed: %+v", seen.visits)
	}
}

// Every exported sentinel in the package's source is listed by Errors
// (CLAUDE.md rule 1).
func TestEveryExportedErrorIsListed(t *testing.T) {
	sentineltest.AssertListed(t, ".")
}
