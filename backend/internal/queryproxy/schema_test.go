package queryproxy_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// schemas records which database the reader was pointed at.
type schemas struct {
	schema provisioning.Schema
	err    error
	asked  []string
}

func (s *schemas) Schema(_ context.Context, _ provisioning.Contest, database string) (provisioning.Schema, error) {
	s.asked = append(s.asked, database)
	return s.schema, s.err
}

// admitted is the pair the caller's Access resolved and Schema is handed.
type admitted struct {
	contest     contests.Contest
	participant contests.Participant
}

func schemaFixture(policy sqlpolicy.Policy) (*queryproxy.Service, *databases, *schemas, admitted) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	registration := contests.Participant{ID: uuid.New(), ContestID: contest.ID, UserID: uuid.New(), Status: contests.RegistrationActive}
	db := &databases{database: "game_c1_u1"}
	reader := &schemas{schema: provisioning.Schema{Tables: []provisioning.Table{{Name: "guests"}}}}

	service := queryproxy.New(
		people{participant: registration},
		contestStore{contest: contest},
		games{game: provisioning.Contest{ID: contest.ID, Template: "game_tpl_c1", Version: 3, Policy: policy}},
		db, &runner{},
		fiveSecondGate,
	).WithSchemas(reader)
	return service, db, reader, admitted{contest: contest, participant: registration}
}

func TestSchemaDescribesTheParticipantsOwnDatabase(t *testing.T) {
	service, _, reader, pair := schemaFixture(sqlpolicy.ReadOnly())

	got, err := service.Schema(t.Context(), pair.contest, pair.participant, netip.MustParseAddr("192.0.2.7"))
	if err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	if len(got.Tables) != 1 || got.Tables[0].Name != "guests" {
		t.Fatalf("returned %+v", got.Tables)
	}
	// The database comes from the registration, never from the caller.
	if len(reader.asked) != 1 || reader.asked[0] != "game_c1_u1" {
		t.Fatalf("read the schema of %v, want the participant's own copy", reader.asked)
	}
}

// The panel must not leak what a closed catalogue hides.
func TestSchemaIsRefusedWhereTheContestClosedItsCatalogues(t *testing.T) {
	closed := sqlpolicy.ReadOnly()
	closed.AllowCatalog = false
	service, db, reader, pair := schemaFixture(closed)

	_, err := service.Schema(t.Context(), pair.contest, pair.participant, netip.MustParseAddr("192.0.2.7"))
	if !errors.Is(err, queryproxy.ErrSchemaHidden) {
		t.Fatalf("answered %v, want ErrSchemaHidden", err)
	}
	if len(reader.asked) != 0 {
		t.Fatal("read the schema anyway before refusing")
	}
	if db.asked != nil {
		t.Fatal("provisioned a database for a request that was going to be refused")
	}
}

// internal/app builds this Service, with nils for Run's collaborators, when
// QUERY_RUNNER_ADDR is unset.
func TestSchemaIsRefusedByAConsolelessBuildRatherThanPanicking(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	participant := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}
	service := queryproxy.New(people{participant: participant}, contestStore{contest: contest}, nil, nil, nil, fiveSecondGate)

	if _, err := service.Schema(t.Context(), contest, participant, netip.MustParseAddr("192.0.2.7")); !errors.Is(err, queryproxy.ErrSchemaHidden) {
		t.Fatalf("answered %v, want ErrSchemaHidden", err)
	}
}

func TestSchemaIsRefusedWhenNothingWasWiredToAnswerIt(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	participant := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}
	service := queryproxy.New(
		people{participant: participant},
		contestStore{contest: contest},
		games{game: provisioning.Contest{ID: contest.ID, Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "game_c1_u1"}, &runner{},
		fiveSecondGate,
	)

	if _, err := service.Schema(t.Context(), contest, participant, netip.MustParseAddr("192.0.2.7")); !errors.Is(err, queryproxy.ErrSchemaHidden) {
		t.Fatalf("answered %v, want ErrSchemaHidden", err)
	}
}

// The lookup's view of the registration (disqualified, finished here) is not
// asked again: Access already admitted the pair.
func TestSchemaDescribesThePairItWasHandedWithoutAdmittingItAgain(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	participant := contests.Participant{ID: uuid.New(), ContestID: contest.ID, UserID: uuid.New(), Status: contests.RegistrationActive}
	since := participant
	since.Status = contests.RegistrationDisqualified
	ended := contest
	ended.Status = contests.StatusFinished
	reader := &schemas{schema: provisioning.Schema{Tables: []provisioning.Table{{Name: "guests"}}}}
	service := queryproxy.New(
		people{participant: since}, contestStore{contest: ended},
		games{game: provisioning.Contest{ID: contest.ID, Policy: sqlpolicy.ReadOnly()}},
		&databases{database: "game_c1_u1"}, &runner{},
		fiveSecondGate,
	).WithSchemas(reader)

	got, err := service.Schema(t.Context(), contest, participant, netip.MustParseAddr("192.0.2.7"))
	if err != nil {
		t.Fatalf("Schema() = %v, want the admitted pair described", err)
	}
	if len(got.Tables) != 1 || got.Tables[0].Name != "guests" {
		t.Fatalf("returned %+v", got.Tables)
	}
}

// A registration removed and re-added since Access owns a different database,
// so nothing is provisioned or read for it.
func TestSchemaRefusesARegistrationReplacedSinceItWasAdmitted(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	participant := contests.Participant{ID: uuid.New(), ContestID: contest.ID, UserID: uuid.New(), Status: contests.RegistrationActive}
	readded := participant
	readded.ID = uuid.New()
	db := &databases{database: "game_c1_u1"}
	reader := &schemas{schema: provisioning.Schema{Tables: []provisioning.Table{{Name: "guests"}}}}
	service := queryproxy.New(
		people{participant: readded}, contestStore{contest: contest},
		games{game: provisioning.Contest{ID: contest.ID, Policy: sqlpolicy.ReadOnly()}},
		db, &runner{},
		fiveSecondGate,
	).WithSchemas(reader)

	if _, err := service.Schema(t.Context(), contest, participant, netip.MustParseAddr("192.0.2.7")); !errors.Is(err, contests.ErrNotAParticipant) {
		t.Fatalf("Schema() = %v, want ErrNotAParticipant", err)
	}
	if db.asked != nil || len(reader.asked) != 0 {
		t.Fatalf("provisioned %v and read %v for a registration that was replaced, want neither", db.asked, reader.asked)
	}
}

func individualSchemaFixture(policy sqlpolicy.Policy, allowed []netip.Prefix) (*queryproxy.Service, *schemas, *int, admitted) {
	contest := individualContest()
	contest.AllowedCIDRs = allowed
	starts := 0
	registration := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationRegistered}
	reader := &schemas{schema: provisioning.Schema{Tables: []provisioning.Table{{Name: "guests"}}}}
	service := queryproxy.New(
		people{participant: registration, starts: &starts},
		contestStore{contest: contest},
		games{game: provisioning.Contest{ID: contest.ID, Template: "game_tpl_c1", Version: 3, Policy: policy}},
		&databases{database: "game_c1_u1"}, &runner{},
		fiveSecondGate,
	).WithSchemas(reader)
	return service, reader, &starts, admitted{contest: contest, participant: registration}
}

func TestReadingTheSchemaStartsAnIndividualParticipantsClock(t *testing.T) {
	service, _, starts, pair := individualSchemaFixture(sqlpolicy.ReadOnly(), nil)

	if _, err := service.Schema(t.Context(), pair.contest, pair.participant, netip.MustParseAddr("192.0.2.7")); err != nil {
		t.Fatalf("Schema() = %v", err)
	}
	if *starts != 1 {
		t.Fatalf("Start called %d times, want 1", *starts)
	}
}

// A refused read starts nothing. For a disallowed address the start asks the
// gate itself rather than trust Access did.
func TestARefusedSchemaReadStartsNoClock(t *testing.T) {
	closed := sqlpolicy.ReadOnly()
	closed.AllowCatalog = false
	for name, given := range map[string]struct {
		policy  sqlpolicy.Policy
		allowed []netip.Prefix
		want    error
	}{
		"an address the contest does not allow": {sqlpolicy.ReadOnly(), []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}, contests.ErrAddressNotAllowed},
		"a contest that hides its schema":       {closed, nil, queryproxy.ErrSchemaHidden},
	} {
		t.Run(name, func(t *testing.T) {
			service, _, starts, pair := individualSchemaFixture(given.policy, given.allowed)

			if _, err := service.Schema(t.Context(), pair.contest, pair.participant, netip.MustParseAddr("192.0.2.7")); !errors.Is(err, given.want) {
				t.Fatalf("Schema() = %v, want %v", err, given.want)
			}
			if *starts != 0 {
				t.Fatalf("Start called %d times by a refused read, want 0", *starts)
			}
		})
	}
}

func TestSchemaReportsAContestWithNoGame(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	participant := contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}
	service := queryproxy.New(
		people{participant: participant},
		contestStore{contest: contest},
		games{err: provisioning.ErrNoGame},
		&databases{}, &runner{},
		fiveSecondGate,
	).WithSchemas(&schemas{})

	if _, err := service.Schema(t.Context(), contest, participant, netip.MustParseAddr("192.0.2.7")); !errors.Is(err, queryproxy.ErrNoGameYet) {
		t.Fatalf("answered %v, want ErrNoGameYet", err)
	}
}

// None of the default lookup's separate reads runs once the single lookup is
// wired.
func TestSchemaUsesTheSingleLookupOnceWired(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	participant := contests.Participant{ID: uuid.New(), ContestID: contest.ID, UserID: uuid.New(), Status: contests.RegistrationActive}
	game := provisioning.Contest{ID: contest.ID, Template: "game_tpl_c1", Version: 3, Policy: sqlpolicy.ReadOnly()}
	instance := provisioning.Instance{Database: "game_c1_u1", TemplateVersion: 3, Status: "ready"}
	peopleCalls, contestCalls, gameCalls, lookupCalls := 0, 0, 0, 0
	var asked [2]uuid.UUID
	db := &databases{database: "game_c1_u1"}
	reader := &schemas{schema: provisioning.Schema{Tables: []provisioning.Table{{Name: "guests"}}}}

	service := queryproxy.New(
		people{participant: participant, calls: &peopleCalls},
		contestStore{contest: contest, calls: &contestCalls},
		games{game: game, calls: &gameCalls},
		db, &runner{},
		fiveSecondGate,
	).WithSchemas(reader).
		WithLookup(lookupFake{participant: participant, contest: contest, game: game, instance: instance, calls: &lookupCalls, asked: &asked})

	if _, err := service.Schema(t.Context(), contest, participant, netip.MustParseAddr("192.0.2.7")); err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	if lookupCalls != 1 || peopleCalls != 0 || contestCalls != 0 || gameCalls != 0 {
		t.Fatalf("calls = lookup %d, participant %d, contest %d, game %d; want 1, 0, 0 and 0",
			lookupCalls, peopleCalls, contestCalls, gameCalls)
	}
	if asked != [2]uuid.UUID{contest.ID, participant.UserID} {
		t.Fatalf("the lookup was asked about %v, want the admitted contest %v and account %v", asked, contest.ID, participant.UserID)
	}
	if db.existing != instance || db.existingErr != nil {
		t.Fatalf("EnsureFrom was told %+v, %v; want the copy the lookup read", db.existing, db.existingErr)
	}
}

func TestSchemaOverTheSingleLookupStillRefusesAClosedCatalogueFirst(t *testing.T) {
	closed := sqlpolicy.ReadOnly()
	closed.AllowCatalog = false
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	participant := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}
	db := &databases{database: "game_c1_u1"}
	reader := &schemas{}

	service := queryproxy.New(people{}, contestStore{}, games{}, db, &runner{}, fiveSecondGate).
		WithSchemas(reader).
		WithLookup(lookupFake{participant: participant, contest: contest,
			game: provisioning.Contest{ID: contest.ID, Template: "game_tpl_c1", Policy: closed}})

	if _, err := service.Schema(t.Context(), contest, participant, netip.MustParseAddr("192.0.2.7")); !errors.Is(err, queryproxy.ErrSchemaHidden) {
		t.Fatalf("answered %v, want ErrSchemaHidden", err)
	}
	if db.asked != nil || len(reader.asked) != 0 {
		t.Fatal("provisioned or read a database for a request that was going to be refused")
	}
}
