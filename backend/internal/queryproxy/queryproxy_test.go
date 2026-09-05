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
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
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
}

func (c contestStore) ByID(context.Context, uuid.UUID) (contests.Contest, error) {
	return c.contest, c.err
}

type games struct {
	game provisioning.Contest
	err  error
}

func (g games) Game(context.Context, uuid.UUID) (provisioning.Contest, error) {
	return g.game, g.err
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
}

func (d *databases) Ensure(_ context.Context, c provisioning.Contest, _ uuid.UUID) (string, error) {
	d.asked = &c
	return d.database, d.err
}

func (d *databases) Quota(context.Context, provisioning.Contest) (int64, error) {
	d.quotaAsked = true
	return d.quota, nil
}

type runner struct {
	quotaSink *int64
	got       queryrunner.Request
	gotID     uuid.UUID
	result    *queryrunner.Result
	err       error
	// calls counts how often Run was reached, so a test can prove a check
	// upstream of the façade's own call to the runner stopped a request
	// before the journal it wraps was ever written to.
	calls int
}

func (r *runner) Run(_ context.Context, req queryrunner.Request, id uuid.UUID) (*queryrunner.Result, error) {
	r.calls++
	r.got, r.gotID = req, id
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

	if _, err := service.Run(t.Context(), cmd); err != nil {
		t.Fatalf("running: %v", err)
	}

	// The journal ties a row to the same request in the technical logs, which
	// is what makes "the participant says it failed at 14:02" answerable.
	if run.gotID != cmd.RequestID {
		t.Fatalf("request id = %s, want %s", run.gotID, cmd.RequestID)
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

	probe := errors.New(`ERROR: relation "salaries" does not exist (SQLSTATE 42P01)`)

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
