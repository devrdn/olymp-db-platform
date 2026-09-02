package queryproxy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// The collaborators are faked because each is tested where it lives: the
// repositories against a real database, the provisioner against a real
// cluster, the runner against both. What is under test here is the order of
// the decisions and what each refusal is called — which is the whole of this
// package.

type people struct {
	participant contests.Participant
	err         error
}

func (p people) ByUser(context.Context, uuid.UUID, uuid.UUID) (contests.Participant, error) {
	return p.participant, p.err
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
	// asked records what Ensure was called with.
	asked *provisioning.Contest
}

func (d *databases) Ensure(_ context.Context, c provisioning.Contest, _ uuid.UUID) (string, error) {
	d.asked = &c
	return d.database, d.err
}

func (d *databases) Quota(context.Context, provisioning.Contest) (int64, error) {
	return d.quota, nil
}

type runner struct {
	got    queryrunner.Request
	gotID  uuid.UUID
	result *queryrunner.Result
	err    error
}

func (r *runner) Run(_ context.Context, req queryrunner.Request, id uuid.UUID) (*queryrunner.Result, error) {
	r.got, r.gotID = req, id
	return r.result, r.err
}

func fixture(t *testing.T) (*queryproxy.Service, *databases, *runner) {
	t.Helper()

	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning}
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

func TestTheQuotaAndTheRequestIdentifierAreCarriedThrough(t *testing.T) {
	service, db, run := fixture(t)
	cmd := command()

	if _, err := service.Run(t.Context(), cmd); err != nil {
		t.Fatalf("running: %v", err)
	}

	if run.got.DiskQuotaBytes != db.quota {
		t.Fatalf("quota = %d, want %d", run.got.DiskQuotaBytes, db.quota)
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
			contest: contests.Contest{Status: contests.StatusRunning},
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
			contest: contests.Contest{Status: contests.StatusRunning},
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

// A contest whose template was never built has nothing to give anybody, and
// saying so is not the same as saying the query was wrong.
func TestAContestWithNoGameYet(t *testing.T) {
	service := queryproxy.New(
		people{participant: contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}},
		contestStore{contest: contests.Contest{Status: contests.StatusRunning}},
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
