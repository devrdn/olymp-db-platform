package queryproxy_test

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/rpc"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// openWindow is a contest end far enough in the future that no test in this
// file mistakes it for closed. Every test here is about something other than
// timing unless it says otherwise — the deadline formula itself is tested in
// internal/contests, not re-tested against every one of these fakes.
var openWindow = time.Now().Add(24 * time.Hour)

// The collaborators are faked because each is tested where it lives: the
// repositories against a real database, the provisioner against a real
// cluster, the runner against both. What is under test here is the order of
// the decisions and what each refusal is called — which is the whole of this
// package.

type people struct {
	participant contests.Participant
	err         error
	// calls counts how often ByUser was reached, when a test needs to prove a
	// check upstream of it stopped a request before it got here. A pointer so
	// the value receiver below can still record into it.
	calls *int
	// starts counts how often Start was reached, and startErr lets a test
	// simulate the write failing.
	starts   *int
	startErr error
}

func (p people) ByUser(context.Context, uuid.UUID, uuid.UUID) (contests.Participant, error) {
	if p.calls != nil {
		*p.calls++
	}
	return p.participant, p.err
}

// Start mirrors what postgres.Registrations.Start guarantees: it sets
// StartedAt and moves the status to active together, once, and reports how
// often it was actually reached so a test can prove an already-started
// participant costs no further call.
func (p people) Start(_ context.Context, _ uuid.UUID, now time.Time) (contests.Participant, error) {
	if p.starts != nil {
		*p.starts++
	}
	if p.startErr != nil {
		return contests.Participant{}, p.startErr
	}
	started := p.participant
	started.StartedAt = &now
	started.Status = contests.RegistrationActive
	return started, nil
}

type contestStore struct {
	contest contests.Contest
	err     error
	// calls counts how often ByID was reached, so a test can prove
	// WithLookup really did replace this call rather than merely adding a
	// second one beside it.
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
	// calls counts how often Game was reached, so a test can prove a check
	// placed ahead of the game lookup really did stop the request before it.
	// A pointer for the same reason people.calls is one: the receiver is a
	// value.
	calls *int
}

func (g games) Game(context.Context, uuid.UUID) (provisioning.Contest, error) {
	if g.calls != nil {
		*g.calls++
	}
	return g.game, g.err
}

// lookupFake is the fake behind queryproxy.Lookup: the single round trip
// WithLookup wires in, standing in for postgres.Registrations. calls counts
// how often either method was reached, so a test can prove the façade used
// this one call instead of its own default's separate ones.
type lookupFake struct {
	participant contests.Participant
	contest     contests.Contest
	game        provisioning.Contest
	gameErr     error
	// instance is the participant's own copy as the lookup read it; left
	// zero, the registration has none (provisioning.ErrNoInstance).
	instance provisioning.Instance
	err      error
	calls    *int
}

func (l lookupFake) ForRun(context.Context, uuid.UUID, uuid.UUID) (queryproxy.LookupResult, error) {
	if l.calls != nil {
		*l.calls++
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

// answerable is the fake behind queryproxy.Answerable: whether this contest
// still has a question this registration could get an answer out of, plus a
// counter so a test can prove the question was (or was not) asked at all.
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
	// asked records what Ensure was called with, and quotaAsked whether the
	// cluster was consulted about size at all.
	asked      *provisioning.Contest
	quotaAsked bool
	lastQuota  int64
	// existing and existingErr are what EnsureFrom was told of the
	// registration's copy, so a test can prove the lookup's own read of it
	// reached provisioning untouched.
	existing    provisioning.Instance
	existingErr error
}

// Instance answers that the registration has no copy yet; what EnsureFrom
// does with that is provisioning's business, not this package's.
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
	// calls counts how often Run was reached, so a test can prove a check
	// upstream of the façade's own call to the runner stopped a request
	// before the journal it wraps was ever written to.
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

// The first line of section 5: the query is the participant's, the address is
// not. A caller cannot name a database, and nothing in the command carries one
// — it is looked up from the registration every time.
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

	// The journal ties a row to the same request in the technical logs, which
	// is what makes "the participant says it failed at 14:02" answerable.
	if run.gotOrigin.RequestID != cmd.RequestID {
		t.Fatalf("request id = %s, want %s", run.gotOrigin.RequestID, cmd.RequestID)
	}
	// And so is where it came from, for the journal row's ip column
	// (design §2.3; CLAUDE.md rule 11).
	if run.gotOrigin.Address != cmd.Address {
		t.Fatalf("address = %v, want %v", run.gotOrigin.Address, cmd.Address)
	}
	if run.got.Registration == uuid.Nil {
		t.Fatal("the query was journalled against no registration")
	}
}

// Who may ask, and when. Each of these is a different sentence to the person
// asking, so each is a different error.
func TestWhoMayAskAndWhen(t *testing.T) {
	for name, given := range map[string]struct {
		people  people
		contest contests.Contest
		want    error
	}{
		"somebody who never registered": {
			people:  people{err: contests.ErrParticipantNotFound},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    queryproxy.ErrNotAParticipant,
		},
		"a contest that has not started": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusPublished},
			want:    queryproxy.ErrContestNotRunning,
		},
		"a contest that has finished": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusFinished},
			want:    queryproxy.ErrContestNotRunning,
		},
		"somebody disqualified": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationDisqualified}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    queryproxy.ErrNotAParticipant,
		},
	} {
		t.Run(name, func(t *testing.T) {
			service := queryproxy.New(given.people, contestStore{contest: given.contest},
				games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
				&databases{database: "x"}, &runner{result: &queryrunner.Result{}})

			if _, err := service.Run(t.Context(), command()); !errors.Is(err, given.want) {
				t.Fatalf("error = %v, want %v", err, given.want)
			}
		})
	}
}

// The guarantee from docs/ARCHITECTURE.md §8: closing does not depend on the
// scheduler that flips contests.Status to finished. A fixed contest past its
// own ends_at must stop taking queries even while a dead or merely slow
// scheduler has left the status at "running".
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
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrContestNotRunning) {
		t.Fatalf("error = %v, want ErrContestNotRunning", err)
	}
}

// The defect this task closes: before queryproxy consulted the shared
// deadline formula, only contests.Status governed whether a query was taken —
// so an individual-timing participant kept querying for as long as the whole
// contest's own window stayed open, regardless of the personal duration_min
// they were actually given. A participant who started an hour ago with a
// ten-minute session must be refused now, even though the contest's own
// ends_at is a day away and its status is still "running".
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
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrContestNotRunning) {
		t.Fatalf("error = %v, want ErrContestNotRunning (the participant's own 10 minutes are long over)", err)
	}
}

// An individual-timing participant still inside their own window keeps
// querying normally: the fix above must not have turned every individual
// contest into a refusal.
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
	)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("a participant well within their own window was refused: %v", err)
	}
}

// The defect finding 1 closes: nothing ever wrote registrations.started_at,
// so an individual-timing participant who had not started got ok=false from
// contests.Deadline forever and was refused on every query, permanently,
// while the contest and its own status both reported "running". Their first
// query is now the deliberate action that starts their own clock (§8), and
// is answered like any other query inside a fresh window rather than
// refused.
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
	)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("the participant's first query: %v", err)
	}
	if starts != 1 {
		t.Fatalf("Start was called %d times, want 1", starts)
	}
}

// A second query must not restart the clock, or cost a write at all: the
// façade only calls Start when the participant it already read back carries
// no StartedAt.
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
	)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("running: %v", err)
	}
	if starts != 0 {
		t.Fatalf("Start was called %d times for a participant who had already started, want 0", starts)
	}
}

// A fixed-timing participant's own clock is never touched: fixed timing
// shares one window, and Deadline never consults StartedAt for it (§8).
func TestAFixedTimingParticipantsClockIsNeverStarted(t *testing.T) {
	starts := 0
	service := queryproxy.New(
		people{participant: contests.Participant{
			ID: uuid.New(), Status: contests.RegistrationActive,
		}, starts: &starts},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
	)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("running: %v", err)
	}
	if starts != 0 {
		t.Fatalf("Start was called %d times for a fixed-timing participant, want 0", starts)
	}
}

// A failure to start the clock is ours, not the participant's query being
// wrong — the same treatment every other lookup failure in Run gets.
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
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

// Finding 1, the critical scenario: an organiser can flip contests.Status to
// "running" hours before starts_at — a manual step in their own workflow that
// says nothing about the wall clock. Before this fix that was the only thing
// queryproxy checked before starting an individual participant's clock, so a
// student's exploratory query the evening before a 09:00 contest would set
// their own started_at to that evening, and their whole duration_min would
// burn before the olympiad even opened — permanently, since nothing can clear
// started_at once Start has written it. The fix compares the wall clock to
// the contest's own window and refuses without writing anything.
func TestAFirstQueryBeforeStartsAtStartsNoClock(t *testing.T) {
	now := time.Now()
	opensTomorrowMorning := now.Add(13 * time.Hour)
	farFuture := now.Add(48 * time.Hour)
	duration := 120
	contest := contests.Contest{
		// An organiser's early flip: Status is already "running", but
		// starts_at is still hours away.
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
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrContestNotRunning) {
		t.Fatalf("a first query before starts_at: error = %v, want ErrContestNotRunning", err)
	}
	if starts != 0 {
		t.Fatalf("Start was called %d times for a query before starts_at, want 0 — nothing may brick this participant", starts)
	}
}

// The same window, checked at its other edge: a dead scheduler that never
// moved a finished individual contest's status out of "running" must not let
// a participant who never queried before now start a clock past ends_at
// either.
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
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrContestNotRunning) {
		t.Fatalf("a first query after ends_at: error = %v, want ErrContestNotRunning", err)
	}
	if starts != 0 {
		t.Fatalf("Start was called %d times for a query after ends_at, want 0", starts)
	}
}

// Finding 2: before this fix, an individual participant's first query started
// their clock before the address restriction was checked, so a query from
// outside the contest's own network burned their first minute and was then
// refused anyway — the same bricking finding 1 closes, milder. The clock must
// start only once the request is otherwise admitted.
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
	)

	fromHome := command()
	fromHome.Address = netip.MustParseAddr("203.0.113.7")
	if _, err := service.Run(t.Context(), fromHome); !errors.Is(err, queryproxy.ErrAddressNotAllowed) {
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
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrNoGameYet) {
		t.Fatalf("error = %v, want ErrNoGameYet", err)
	}
	if starts != 0 {
		t.Fatalf("Start was called %d times for a contest with no game yet, want 0", starts)
	}
}

// The grace period exists for network latency, applies only to acceptance and
// never to what a participant is shown — a query that reaches the server a
// few seconds after the deadline is still honoured, but one that arrives
// after the grace has also elapsed is not. WithClock pins "now" so the test
// does not race the deadline it is asserting against.
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
		).WithClock(func() time.Time { return now }).WithGrace(5 * time.Second)
	}

	withinGrace := build(deadline.Add(3 * time.Second))
	if _, err := withinGrace.Run(t.Context(), command()); err != nil {
		t.Fatalf("a query 3s after the deadline, within a 5s grace, was refused: %v", err)
	}

	pastGrace := build(deadline.Add(6 * time.Second))
	if _, err := pastGrace.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrContestNotRunning) {
		t.Fatalf("error = %v, want ErrContestNotRunning for a query past the grace too", err)
	}
}

// A contest whose template was never built has nothing to give anybody, and
// saying so is not the same as saying the query was wrong.
func TestAContestWithNoGameYet(t *testing.T) {
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{err: provisioning.ErrNoGame},
		&databases{}, &runner{},
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrNoGameYet) {
		t.Fatalf("error = %v, want ErrNoGameYet", err)
	}
}

// A refusal from the validator has to arrive unchanged, because the interface
// turns its code into a sentence in the participant's own language. Wrapping
// it in something of this package's own would leave that with nothing to
// translate.
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

// A restriction applied once is a restriction somebody walks out of the room
// with. The contest names the network it is held on, and every query is
// checked against it — not only the enrolment that happened in the lab.
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
	)

	fromRoom := command()
	fromRoom.Address = netip.MustParseAddr("10.20.3.4")
	if _, err := service.Run(t.Context(), fromRoom); err != nil {
		t.Fatalf("a query from the contest's own network was refused: %v", err)
	}

	fromHome := command()
	fromHome.Address = netip.MustParseAddr("203.0.113.7")
	if _, err := service.Run(t.Context(), fromHome); !errors.Is(err, queryproxy.ErrAddressNotAllowed) {
		t.Fatalf("error = %v, want ErrAddressNotAllowed", err)
	}

	// An address that could not be resolved fails the restriction too: a
	// contest held on one network cannot be honoured without knowing which
	// one this is.
	unknown := command()
	if _, err := service.Run(t.Context(), unknown); !errors.Is(err, queryproxy.ErrAddressNotAllowed) {
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
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrFinished) {
		t.Fatalf("error = %v, want ErrFinished", err)
	}
}

// A contest that closes the catalogues means the schema has to be discovered
// some other way. PostgreSQL's own error names the relation that does not
// exist — which turns guessing into enumeration and hands back the list the
// closed catalogue was hiding. In that contest, and only in that one, the
// database's words are kept back.
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
		)
	}

	// The database's own words, named as such — which is how the client
	// hands them over (rpc.errorFor) and the only shape this may act on: a
	// failure of ours must not be able to wear them.
	probe := &queryrunner.DatabaseError{Message: `ERROR: relation "salaries" does not exist (SQLSTATE 42P01)`}

	_, err := build(closed, probe).Run(t.Context(), command())
	if !errors.Is(err, queryproxy.ErrDatabaseDeclined) {
		t.Fatalf("error = %v, want ErrDatabaseDeclined", err)
	}
	if strings.Contains(err.Error(), "salaries") {
		t.Fatalf("the name leaked anyway: %v", err)
	}

	// With the catalogues open the same message is the most useful sentence
	// there is, and holding it back would only make the contest harder to
	// learn from.
	_, err = build(sqlpolicy.ReadOnly(), probe).Run(t.Context(), command())
	if !strings.Contains(err.Error(), "salaries") {
		t.Fatalf("the database's own words were withheld from an open contest: %v", err)
	}
}

// accessFixture builds a Service with only the two collaborators Access
// touches faked with anything meaningful — games, the database pool and the
// runner never enter Access at all, and passing them zero values here is
// itself part of what this file proves about the method.
func accessFixture(people queryproxy.People, contest contestStore) *queryproxy.Service {
	return queryproxy.New(people, contest, games{}, &databases{}, &runner{})
}

// Access is the admission the participant-facing read endpoints (the story,
// the questions) require, and it is meant to be exactly what Run already
// checks before taking a query — minus the rate limit and the SQL-specific
// work, neither of which a read costs. This is the same table TestWhoMayAskAndWhen
// drives through Run, driven through Access instead, so the two are proven to
// agree rather than merely asserted to.
func TestAccessAgreesWithRunAboutWhoMayAskAndWhen(t *testing.T) {
	for name, given := range map[string]struct {
		people  people
		contest contests.Contest
		want    error
	}{
		"somebody who never registered": {
			people:  people{err: contests.ErrParticipantNotFound},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    queryproxy.ErrNotAParticipant,
		},
		"a contest that has not started": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusPublished},
			want:    queryproxy.ErrContestNotRunning,
		},
		"a contest that has finished": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusFinished},
			want:    queryproxy.ErrContestNotRunning,
		},
		"somebody disqualified": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationDisqualified}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    queryproxy.ErrNotAParticipant,
		},
		"somebody who has finished": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationFinished}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    queryproxy.ErrFinished,
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

// A fixed contest past its own ends_at must refuse a read exactly as it
// refuses a query (§8): the deadline formula is the one this project has, and
// a read that used a second one would be readable past the moment writing
// stops being possible.
func TestAccessRefusesAFixedContestPastItsDeadline(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	contest := contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &past}
	service := accessFixture(people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}}, contestStore{contest: contest})

	if _, _, err := service.Access(t.Context(), uuid.New(), uuid.New(), netip.Addr{}); !errors.Is(err, queryproxy.ErrContestNotRunning) {
		t.Fatalf("error = %v, want ErrContestNotRunning", err)
	}
}

// An individual participant who has not started yet has nothing for the
// deadline formula to compute from — Access must fall back to the contest's
// own window (contest.OpenForStart), exactly as Run does before it will ever
// start a clock, and it must not start one itself: Access also admits the
// answer endpoint and the query log, and a content read starts the clock
// separately, once it has succeeded (StartOnRead).
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

// Under individual timing the story, the questions and the schema are the
// contest itself: reading them is where the participant's time begins, or
// the whole window becomes preparation time the duration never counts. The
// first read starts the clock through the same Start seam Run uses.
func TestStartOnReadStartsAnIndividualParticipantsClock(t *testing.T) {
	contest := individualContest()
	starts := 0
	registered := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered}
	service := accessFixture(people{participant: registered, starts: &starts}, contestStore{contest: contest})

	started, err := service.StartOnRead(t.Context(), contest, registered)
	if err != nil {
		t.Fatalf("StartOnRead() = %v", err)
	}
	if starts != 1 || started.StartedAt == nil {
		t.Fatalf("Start called %d times, StartedAt = %v; want the clock started once", starts, started.StartedAt)
	}
}

// A second read costs no write and moves nothing: the clock already running is
// the one the participant keeps.
func TestStartOnReadDoesNotMoveAClockAlreadyRunning(t *testing.T) {
	contest := individualContest()
	starts := 0
	began := time.Now().Add(-10 * time.Minute)
	running := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive, StartedAt: &began}
	service := accessFixture(people{participant: running, starts: &starts}, contestStore{contest: contest})

	got, err := service.StartOnRead(t.Context(), contest, running)
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

// Fixed timing has one clock for everybody, and nothing a participant reads
// starts anything.
func TestStartOnReadNeverStartsAFixedTimingClock(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	starts := 0
	p := contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}
	service := accessFixture(people{participant: p, starts: &starts}, contestStore{contest: contest})

	if _, err := service.StartOnRead(t.Context(), contest, p); err != nil {
		t.Fatalf("StartOnRead() = %v", err)
	}
	if starts != 0 {
		t.Fatalf("Start called %d times under fixed timing, want 0", starts)
	}
}

// Outside the contest's own window there is no clock to start: the read is
// refused the way Run refuses a first query there, and nothing is written.
func TestStartOnReadOutsideTheWindowStartsNoClock(t *testing.T) {
	for name, shift := range map[string]func(*contests.Contest){
		"before starts_at": func(c *contests.Contest) { later := time.Now().Add(time.Hour); c.StartsAt = &later },
		"after ends_at":    func(c *contests.Contest) { earlier := time.Now().Add(-time.Minute); c.EndsAt = &earlier },
		"not running":      func(c *contests.Contest) { c.Status = contests.StatusFinished },
	} {
		t.Run(name, func(t *testing.T) {
			contest := individualContest()
			shift(&contest)
			starts := 0
			p := contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}
			service := accessFixture(people{participant: p, starts: &starts}, contestStore{contest: contest})

			if _, err := service.StartOnRead(t.Context(), contest, p); !errors.Is(err, queryproxy.ErrContestNotRunning) {
				t.Fatalf("StartOnRead() = %v, want ErrContestNotRunning", err)
			}
			if starts != 0 {
				t.Fatalf("Start called %d times outside the window, want 0", starts)
			}
		})
	}
}

// The write failing is ours, not a refusal of the participant.
func TestStartOnReadMarksAFailureToStartAsOurs(t *testing.T) {
	contest := individualContest()
	p := contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}
	service := accessFixture(people{participant: p, startErr: errors.New("connection reset")}, contestStore{contest: contest})

	if _, err := service.StartOnRead(t.Context(), contest, p); !errors.Is(err, queryproxy.ErrUnavailable) {
		t.Fatalf("StartOnRead() = %v, want ErrUnavailable", err)
	}
}

// Holding the events channel open shows a waiting participant their clock; it
// is not reading the contest, and must never be what starts it.
func TestAccessForEventsNeverStartsTheClock(t *testing.T) {
	contest := individualContest()
	starts := 0
	p := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered}
	service := accessFixture(people{participant: p, starts: &starts}, contestStore{contest: contest})

	participant, _, err := service.AccessForEvents(t.Context(), contest.ID, uuid.New(), netip.Addr{})
	if err != nil {
		t.Fatalf("AccessForEvents() = %v", err)
	}
	if starts != 0 || participant.StartedAt != nil {
		t.Fatalf("Start called %d times by the events channel, want 0", starts)
	}
}

// A query from outside the contest's own network is refused, and Access must
// refuse a read from the same address the same way — the restriction applies
// to the participant, not to which endpoint they asked.
func TestAccessChecksTheAddressRestriction(t *testing.T) {
	inRoom := netip.MustParsePrefix("10.20.0.0/16")
	contest := contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow,
		AllowedCIDRs: []netip.Prefix{inRoom},
	}
	service := accessFixture(people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}}, contestStore{contest: contest})

	if _, _, err := service.Access(t.Context(), uuid.New(), uuid.New(), netip.MustParseAddr("203.0.113.7")); !errors.Is(err, queryproxy.ErrAddressNotAllowed) {
		t.Fatalf("error = %v, want ErrAddressNotAllowed", err)
	}
	if _, _, err := service.Access(t.Context(), uuid.New(), uuid.New(), netip.MustParseAddr("10.20.3.4")); err != nil {
		t.Fatalf("a read from the contest's own network was refused: %v", err)
	}
}

// AccessForEvents is Access with exactly one status added to what it admits
// (finding 4, docs/ARCHITECTURE.md §8): published and not yet started, so the
// events channel can be held open across the published → running transition
// instead of refusing a participant until Access itself would succeed.
//
// The table below is the same shape TestAccessAgreesWithRunAboutWhoMayAskAndWhen
// drives through Access, minus the one row this method exists to change: "a
// contest that has not started" moves from a refusal to an admission, and
// every other row must refuse exactly as it always did.
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
			want:    queryproxy.ErrNotAParticipant,
		},
		"somebody disqualified, even for a published contest": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationDisqualified}},
			contest: contests.Contest{Status: contests.StatusPublished},
			want:    queryproxy.ErrNotAParticipant,
		},
		"a contest that has finished": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusFinished},
			want:    queryproxy.ErrContestNotRunning,
		},
		"a contest still a draft": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}},
			contest: contests.Contest{Status: contests.StatusDraft},
			want:    queryproxy.ErrContestNotRunning,
		},
		"a running contest, exactly as Access itself admits it": {
			people:  people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow},
			want:    nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			service := accessFixture(given.people, contestStore{contest: given.contest})

			_, _, err := service.AccessForEvents(t.Context(), uuid.New(), uuid.New(), netip.Addr{})
			if !errors.Is(err, given.want) {
				t.Fatalf("error = %v, want %v", err, given.want)
			}
		})
	}
}

// The network restriction is not waived just because the contest has not
// started yet — it applies to the participant, not to the contest's status.
func TestAccessForEventsStillChecksTheAddressRestrictionForAPublishedContest(t *testing.T) {
	inRoom := netip.MustParsePrefix("10.20.0.0/16")
	contest := contests.Contest{Status: contests.StatusPublished, AllowedCIDRs: []netip.Prefix{inRoom}}
	service := accessFixture(people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationRegistered}}, contestStore{contest: contest})

	if _, _, err := service.AccessForEvents(t.Context(), uuid.New(), uuid.New(), netip.MustParseAddr("203.0.113.7")); !errors.Is(err, queryproxy.ErrAddressNotAllowed) {
		t.Fatalf("error = %v, want ErrAddressNotAllowed", err)
	}
	if _, _, err := service.AccessForEvents(t.Context(), uuid.New(), uuid.New(), netip.MustParseAddr("10.20.3.4")); err != nil {
		t.Fatalf("a read from the contest's own network was refused for a published contest: %v", err)
	}
}

// What is held back is the database speaking for itself, and nothing else. A
// refusal and the runner's own outcomes carry codes the interface turns into
// sentences, and swallowing one would leave a participant with less than the
// closed catalogue was protecting.
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
		// The query service failing to answer is the one that used to be
		// swallowed here: not a refusal, not a timeout, not on any list of
		// ours this package knew about — so a contest that hides its schema
		// reported its own outage to the participant as "the database refused
		// that query", and nobody looking at the console could tell.
		"the query service failing": fmt.Errorf("%w: connecting to the game database: dial tcp [::1]:5433: connect: connection refused",
			rpc.ErrUnreachable),
	} {
		t.Run(name, func(t *testing.T) {
			service := queryproxy.New(
				people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
				contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
				games{game: provisioning.Contest{Policy: closed}},
				&databases{database: "x"}, &runner{err: failure},
			)

			_, err := service.Run(t.Context(), command())
			if errors.Is(err, queryproxy.ErrDatabaseDeclined) {
				t.Fatalf("%v was swallowed as a database refusal", failure)
			}
		})
	}
}

// Our own failing is not the query being wrong.
//
// A database that cannot be reached, answered as "your request was bad", tells
// the client to stop retrying and the participant to fix a query that was
// fine — with our connection string attached to the explanation.
func TestOurOwnFailuresAreMarkedApartFromTheQuerysOwn(t *testing.T) {
	broken := errors.New("dial tcp 172.28.0.5:5432: connection refused")

	for name, service := range map[string]*queryproxy.Service{
		"the registration cannot be read": queryproxy.New(
			people{err: broken}, contestStore{}, games{}, &databases{}, &runner{}),
		"the contest cannot be read": queryproxy.New(
			people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contestStore{err: broken}, games{}, &databases{}, &runner{}),
		"the database cannot be provided": queryproxy.New(
			people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
			contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
			games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
			&databases{err: broken}, &runner{}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.Run(t.Context(), command())
			if !errors.Is(err, queryproxy.ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
		})
	}
}

// The quota is worked out by asking the game cluster how large the template
// is. A read-only contest cannot grow its database, so that is a round trip to
// another server on every query, for every participant, to produce a number
// nothing will compare against.
func TestAReadOnlyContestDoesNotAskTheClusterAboutSizeAtAll(t *testing.T) {
	db := &databases{database: "x", quota: 1 << 20}
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		db, &runner{result: &queryrunner.Result{}},
	)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("running: %v", err)
	}
	if db.quotaAsked {
		t.Fatal("a read-only contest asked the cluster for a size limit it cannot reach")
	}

	// And a contest that permits writing still gets one, because there the
	// number is the only thing between a participant and the cluster's disk.
	writing := &databases{database: "x", quota: 1 << 20}
	service = queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadWrite("evidence")}},
		writing, &runner{result: &queryrunner.Result{}, quotaSink: &writing.lastQuota},
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

// The classification next door asks "is this one of ours?", and the list it
// asks against lives in the runner. A sentinel added there without being
// listed would be swallowed as the database speaking — in exactly the contest
// that withholds the database's words, where nobody would see it happen.
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

// A failure to open the journal row is ours, not the database refusing the
// participant's SQL — the query never reached it — so even a contest that
// hides its schema must not swallow this one as ErrDatabaseDeclined.
func TestAJournalFailureIsNotSwallowedByAClosedCatalogue(t *testing.T) {
	closed := sqlpolicy.ReadOnly()
	closed.AllowCatalog = false

	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: closed}},
		&databases{database: "x"},
		&runner{err: fmt.Errorf("%w: %w", queryrunner.ErrJournalUnavailable, errors.New("dial tcp: connection refused"))},
	)

	_, err := service.Run(t.Context(), command())
	if errors.Is(err, queryproxy.ErrDatabaseDeclined) {
		t.Fatalf("a journal failure was swallowed as the database refusing the query: %v", err)
	}
	if !errors.Is(err, queryrunner.ErrJournalUnavailable) {
		t.Fatalf("error = %v, want it to still be ErrJournalUnavailable", err)
	}
}

// A megabyte of attacker text must never reach the journal. The true bound
// lives in the checker (sqlpolicy.MaxQueryBytes), well downstream of the
// write the façade's caller makes before ever reaching it — this one is
// enforced first, costs nothing but a comparison, and stops before even the
// participant is looked up.
// An oversized query is still refused for its length, but it now costs the
// same rate-limit accounting an ordinary query does — the participant and the
// contest are still looked up, because that is what the rate check is keyed
// and bounded by, and only then is the length compared. What it must never
// reach is anything downstream of the rate check: a database is not
// provisioned and the Query Runner is not called for a query this cheap a
// comparison already refuses.
//
// This used to refuse before the participant was ever looked up, which meant
// an oversized query never called the rate check at all — refused for free,
// as many times a minute as the network allowed, against no budget
// (CLAUDE.md rule 13 — a refused query still counts against the rate).
func TestAQueryOverTheLengthBoundIsRefusedAfterTheRateCheckAndBeforeAnythingElse(t *testing.T) {
	calls := 0
	db := &databases{database: "x"}
	run := &runner{result: &queryrunner.Result{}}
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}, calls: &calls},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		db, run,
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

// An oversized query still costs its share of the rate budget: it is not a
// free way to make a participant's real queries meet the limit sooner, but it
// is not a way to dodge the limit either.
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

// A query within the bound is unaffected: the check refuses length and
// nothing else.
func TestAQueryWithinTheLengthBoundIsUnaffected(t *testing.T) {
	service, _, _ := fixture(t)
	cmd := command()
	cmd.SQL = "SELECT " + strings.Repeat("a", sqlpolicy.MaxQueryBytes-100)

	if _, err := service.Run(t.Context(), cmd); err != nil {
		t.Fatalf("a query within the bound was refused: %v", err)
	}
}

// A refused query must cost this façade's own rate check, not a row in
// query_log: the Query Runner's own limiter sits behind the journal write the
// façade's caller makes before ever reaching it (CLAUDE.md's security rule
// 13 — a refused query still counts against the rate, and now it is counted
// before the expensive step rather than after).
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

// Finding 4: a query refused for the contest's own timing still costs the
// participant and contest lookups above it, and before this fix the deadline
// refusal returned before the rate check ever ran — so a participant
// hammering this endpoint after their own deadline (or before the contest
// opened) met no limiter at all, indefinitely. The rate check now runs ahead
// of that refusal, so it is what eventually stops the hammering.
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
	).WithClock(func() time.Time { return deadline.Add(time.Minute) })

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrContestNotRunning) {
		t.Fatalf("the first query past the deadline: error = %v, want ErrContestNotRunning", err)
	}

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("a second query in the same state: error = %v, want ErrTooManyQueries — the first should have counted", err)
	}
}

// The same defect, on the other refusal the rate check used to sit behind: a
// contest that has not opened yet.
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
	)

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrContestNotRunning) {
		t.Fatalf("the first query before the contest opened: error = %v, want ErrContestNotRunning", err)
	}

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("a second query in the same state: error = %v, want ErrTooManyQueries — the first should have counted", err)
	}
}

// Finding 3: ErrNotAParticipant, the disqualified refusal and the finished
// refusal all used to return before either rate check ever ran, so any
// authenticated caller could loop this endpoint against a random contest
// identifier forever — each request costing a session read plus this lookup
// — with nothing counting the attempt. The fix is a check keyed by the
// authenticated caller (cmd.UserID), bounded because it is one key per
// account rather than one key per string a caller can invent, and run before
// the participant is even looked up. These three tests reuse one Command (so
// cmd.UserID stays fixed across calls, the way one real caller's requests
// would) and prove the lookup itself is only ever reached once the limiter
// admits the request.
func TestANeverRegisteredCallerEventuallyMeetsTheLimiterBeforeTheLookup(t *testing.T) {
	calls := 0
	service := queryproxy.New(
		people{err: contests.ErrParticipantNotFound, calls: &calls},
		contestStore{}, games{}, &databases{}, &runner{},
	).WithPerMinuteDefault(1)
	cmd := command()

	if _, err := service.Run(t.Context(), cmd); !errors.Is(err, queryproxy.ErrNotAParticipant) {
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
	).WithPerMinuteDefault(1)
	cmd := command()

	if _, err := service.Run(t.Context(), cmd); !errors.Is(err, queryproxy.ErrNotAParticipant) {
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
	).WithPerMinuteDefault(1)
	cmd := command()

	if _, err := service.Run(t.Context(), cmd); !errors.Is(err, queryproxy.ErrFinished) {
		t.Fatalf("the first request: error = %v, want ErrFinished", err)
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

// A legitimate participant is unaffected by the new early check: it is keyed
// by the authenticated caller and set to the installation's own ceiling,
// which effectiveRateLimit already guarantees no contest-specific check below
// it will ever be looser than.
func TestALegitimateParticipantIsUnaffectedByTheEarlyRateCheck(t *testing.T) {
	service, _, _ := fixture(t)
	cmd := command()

	for i := range 3 {
		if _, err := service.Run(t.Context(), cmd); err != nil {
			t.Fatalf("query %d from a legitimate participant: %v", i+1, err)
		}
	}
}

// Finding 7: WithGrace and WithPerMinuteDefault must agree about a negative
// value. config.Load never produces one for either DEADLINE_GRACE or
// QUERY_PER_MINUTE, so a caller passing one here is a bug in the wiring, not
// deployment input to fail closed on quietly.
func TestWithGracePanicsOnANegativeValue(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("WithGrace(-1s) did not panic")
		}
	}()
	queryproxy.New(people{}, contestStore{}, games{}, &databases{}, &runner{}).WithGrace(-time.Second)
}

func TestWithPerMinuteDefaultPanicsOnANegativeValue(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("WithPerMinuteDefault(-1) did not panic")
		}
	}()
	queryproxy.New(people{}, contestStore{}, games{}, &databases{}, &runner{}).WithPerMinuteDefault(-1)
}

// query_rate_limit_per_min used to be stored, validated and served without
// ever reaching anything that checked it — this proves it now does, and that
// a contest which left it at zero still gets the installation's own default
// rather than no limit at all.
func TestTheContestsOwnRateLimitIsEnforced(t *testing.T) {
	strict := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow, Settings: contests.Settings{QueryRateLimitPerMin: 1}}
	lenient := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow} // zero: installation default

	strictService := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: strict},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
	)
	lenientService := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: lenient},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
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

// WithPerMinuteDefault changes what a contest with no rate of its own falls
// back to, so the façade's own pre-check can be kept in step with whatever
// QUERY_PER_MINUTE the Query Runner was actually deployed with.
func TestWithPerMinuteDefaultOverridesTheFallback(t *testing.T) {
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
	).WithPerMinuteDefault(1)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("the first query: %v", err)
	}
	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("error = %v, want ErrTooManyQueries", err)
	}
}

// An organiser could previously raise a contest's own rate above the
// installation's, and the number meant nothing: this façade's pre-check
// admitted every one of them, each paying the journal write, right up until
// the Query Runner's own limiter — enforcing the installation's true figure —
// refused the rest anyway. Measured with the installation at 3 and the
// contest set to 5: only 3 may pass, not 5.
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

// The reverse direction already worked and must keep working: a contest
// tightening its own rate below the installation's is exactly what the
// setting is for.
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

// An installation with no limit of its own (WithPerMinuteDefault(0), the same
// "no limit" convention config.Runner.PerMinute uses) has no ceiling for a
// contest's own setting to be clamped against — the contest's own number is
// what governs, however high it is.
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
	).WithPerMinuteDefault(0)

	for i := range 50 {
		if _, err := service.Run(t.Context(), command()); err != nil {
			t.Fatalf("query %d of 50, within the contest's own allowance: %v", i+1, err)
		}
	}
}

// Finding 3: the participant-facing read endpoints (the story, the question
// list) get a rate check of their own, AdmitRead — and it shares the exact
// instance and key namespace Run's own pre-lookup check uses, rather than a
// second limiter that would let a caller alternate between running queries
// and polling these endpoints to spend two budgets instead of one.
func TestAdmitReadSharesRunsOwnPerAccountBudget(t *testing.T) {
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
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

// The key is bounded because it is the authenticated account, not anything
// the caller supplies in the request: a fresh account has spent nothing,
// however many contests, real or invented, the refused account tried first.
func TestAdmitReadIsKeyedPerAccountNotShared(t *testing.T) {
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
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

// The console closes once no question of the contest is still answerable to
// this participant — every one of them either answered correctly or out of
// attempts. Running SQL cannot lead to an answer any more, so the endpoint
// that exists to help them answer stops taking queries.
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

// The refusal is not the end of the registration: it is narrower than
// ErrFinished, which closes the whole play screen. Nothing here writes
// contests.RegistrationFinished, and the read endpoints that share this
// service's own admission stay open — the story, the questions, the timer.
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

// A participant with a question still open is untouched: this must not have
// turned every console into a refusal.
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

// A build that never wired the reader fails open. A missing wire must not
// lock every participant out of the console mid-olympiad, which is the
// direction the schema panel's own missing wire already fails in
// (WithSchemas).
func TestAServiceWithNoAnswerableWiredStillRunsQueries(t *testing.T) {
	service, _, run := fixture(t)

	if _, err := service.Run(t.Context(), command()); err != nil {
		t.Fatalf("running with no Answerable wired: %v", err)
	}
	if run.calls != 1 {
		t.Fatalf("the runner was reached %d times, want 1", run.calls)
	}
}

// The check is placed ahead of provisioning: a refused query must not create
// a participant's database. It no longer proves the game was never looked
// up — s.lookup answers the participant, the contest and the game together,
// so the game arrives with the same one call the participant and the contest
// already needed, before this check ever runs; what the merge actually saves
// is the two separate round trips TestTheSingleLookupRemovesTwoCoreRoundTripsFromRun
// measures, not this one.
func TestNothingLeftToAnswerIsRefusedBeforeAnyDatabaseIsProvisioned(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	db := &databases{database: "game_c1_u1"}
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}},
		contestStore{contest: contest},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		db, &runner{result: &queryrunner.Result{}},
	).WithAnswerable(&answerable{left: false})

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrNothingLeftToAnswer) {
		t.Fatalf("error = %v, want ErrNothingLeftToAnswer", err)
	}
	if db.asked != nil {
		t.Fatalf("a database was provisioned for a refused query: %+v", db.asked)
	}
}

// The reader failing is ours, not the participant's query being wrong — and
// it must not read as "you have nothing left to do" either.
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

// TestTheSingleLookupRemovesTwoCoreRoundTripsFromRun is the measurement: New's
// own default composes LookupResult from three separate calls (participant,
// contest, game) so every test built against those three fakes keeps
// exercising Run's own code; WithLookup replaces that default with one call,
// and none of the three it replaces runs at all.
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
			service := queryproxy.New(people{}, contestStore{}, games{}, db, &runner{result: &queryrunner.Result{}}).
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

// A lookup that could not be read at all must be marked as ours, exactly as
// a separate People.ByUser or Contests.ByID failure always was.
func TestASingleLookupsFailureIsMarkedAsOurs(t *testing.T) {
	broken := errors.New("dial tcp 172.28.0.5:5432: connection refused")
	service := queryproxy.New(
		people{}, contestStore{}, games{}, &databases{}, &runner{},
	).WithLookup(lookupFake{err: broken})

	if _, err := service.Run(t.Context(), command()); !errors.Is(err, queryproxy.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

// TestASingleLookupsMissingGameIsStillCheckedAtItsUsualPoint is the claim the
// whole merge depends on: reading the game together with the participant and
// the contest must not move when a missing game is actually noticed. A
// lookup that already knows there is no game must still let the address,
// length and answerable checks run first — exactly the order the separate
// calls always had — and only report ErrNoGameYet once nothing else has
// refused first.
func TestASingleLookupsMissingGameIsStillCheckedAtItsUsualPoint(t *testing.T) {
	contest := contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")},
	}
	participant := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}
	lookup := lookupFake{participant: participant, contest: contest, gameErr: provisioning.ErrNoGame}

	// The address restriction is checked well before the game is ever
	// consulted, so a disallowed address must still win.
	fromHome := command()
	fromHome.Address = netip.MustParseAddr("203.0.113.7")
	service := queryproxy.New(people{}, contestStore{}, games{}, &databases{}, &runner{}).WithLookup(lookup)
	if _, err := service.Run(t.Context(), fromHome); !errors.Is(err, queryproxy.ErrAddressNotAllowed) {
		t.Fatalf("error = %v, want ErrAddressNotAllowed — the address check comes before the game is looked at", err)
	}

	// Whether anything is still answerable is checked before the game too:
	// a participant with nothing left to work towards is refused for that,
	// never told their contest has no game, even though both are true.
	fromRoom := command()
	fromRoom.Address = netip.MustParseAddr("10.20.3.4")
	service = queryproxy.New(people{}, contestStore{}, games{}, &databases{}, &runner{}).
		WithLookup(lookup).WithAnswerable(&answerable{left: false})
	if _, err := service.Run(t.Context(), fromRoom); !errors.Is(err, queryproxy.ErrNothingLeftToAnswer) {
		t.Fatalf("error = %v, want ErrNothingLeftToAnswer — answerable comes before the game is looked at", err)
	}

	// Nothing else refuses one from the contest's own network with a
	// question still open: the missing game is what finally answers, exactly
	// as it would have from a separate Games.Game call.
	service = queryproxy.New(people{}, contestStore{}, games{}, &databases{}, &runner{}).WithLookup(lookup)
	if _, err := service.Run(t.Context(), fromRoom); !errors.Is(err, queryproxy.ErrNoGameYet) {
		t.Fatalf("error = %v, want ErrNoGameYet", err)
	}
}

// watcher is the fake behind queryproxy.Watcher: every visit Run reported.
type watcher struct{ visits []monitor.Visit }

func (w *watcher) Observe(_ context.Context, visit monitor.Visit) { w.visits = append(w.visits, visit) }

// An admitted query is a request of the registration, and the tracker of
// address changes and parallel sessions hears of it (design §2.3), with
// where it came from and from which session.
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

// A query refused at admission is not the registration's request: an
// address the contest does not allow, or a contest that is over, observes
// nothing.
func TestARefusedQueryIsNotObserved(t *testing.T) {
	seen := &watcher{}
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusFinished}},
		games{game: provisioning.Contest{Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "x"}, &runner{result: &queryrunner.Result{}},
	).WithWatcher(seen)
	cmd := command()
	cmd.Address = netip.MustParseAddr("192.0.2.44")
	cmd.Session = monitor.SessionTag("token")

	if _, err := service.Run(t.Context(), cmd); !errors.Is(err, queryproxy.ErrContestNotRunning) {
		t.Fatalf("error = %v, want ErrContestNotRunning", err)
	}
	if len(seen.visits) != 0 {
		t.Fatalf("a refused query was observed: %+v", seen.visits)
	}
}

// Errors is how the HTTP layer learns which of this package's errors reach a
// caller, and so which ones need an answer of their own (internal/api's
// errorTable). A sentinel declared here and left out of it would reach a
// client as "internal error" for a refusal that is really theirs, so every
// exported `Err… = errors.New(…)` in the package's source is listed — read
// from the source itself, because a list kept by hand is exactly what drifts.
func TestEveryExportedErrorIsListed(t *testing.T) {
	listed := map[string]bool{}
	for _, err := range queryproxy.Errors() {
		listed[err.Error()] = true
	}

	declared := exportedErrors(t, ".")
	if len(declared) == 0 {
		t.Fatal("found no exported errors in the package source; the scan is broken")
	}
	for name, message := range declared {
		if !listed[message] {
			t.Errorf("%s (%q) is declared but not in Errors()", name, message)
		}
	}
	if len(queryproxy.Errors()) != len(declared) {
		t.Errorf("Errors() lists %d errors, the package declares %d", len(queryproxy.Errors()), len(declared))
	}
}

// exportedErrors reads the package's non-test source and returns every
// exported `Err… = errors.New("…")`, by name, with its message.
func exportedErrors(t *testing.T, dir string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	found := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, ident := range spec.Names {
				if !ident.IsExported() || !strings.HasPrefix(ident.Name, "Err") || i >= len(spec.Values) {
					continue
				}
				call, ok := spec.Values[i].(*ast.CallExpr)
				if !ok || len(call.Args) != 1 {
					continue
				}
				fun, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || fun.Sel.Name != "New" {
					continue
				}
				if pkg, ok := fun.X.(*ast.Ident); !ok || pkg.Name != "errors" {
					continue
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				message, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: %v", ident.Name, err)
				}
				found[ident.Name] = message
			}
			return true
		})
	}
	return found
}
