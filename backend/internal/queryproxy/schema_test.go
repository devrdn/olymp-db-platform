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

// schemas is the reader behind the façade, recording which database it was
// pointed at — the fact the panel's whole claim rests on.
type schemas struct {
	schema provisioning.Schema
	err    error
	asked  []string
}

func (s *schemas) Schema(_ context.Context, _ provisioning.Contest, database string) (provisioning.Schema, error) {
	s.asked = append(s.asked, database)
	return s.schema, s.err
}

// admitted is a participant and the contest they were admitted to, as the
// caller's Access resolved them: what Schema is handed.
type admitted struct {
	contest     contests.Contest
	participant contests.Participant
}

// schemaFixture assembles the façade around a policy, so each test can say
// what the contest allows and nothing else.
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
	// A caller names no database, the same guarantee section 5 gives Run: it
	// is looked up from their registration.
	if len(reader.asked) != 1 || reader.asked[0] != "game_c1_u1" {
		t.Fatalf("read the schema of %v, want the participant's own copy", reader.asked)
	}
}

// The whole security point of this endpoint. A contest that closed its
// catalogues must not be handed the same answer through a different door —
// the console's panel would otherwise be a better oracle than the one
// ErrDatabaseDeclined exists to shut.
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

// A build that never wired the reader answers the same way a contest that
// hides its schema does, rather than panicking during an olympiad.
//
// The nils are not hypothetical: internal/app builds exactly this Service for
// the participant read endpoints when QUERY_RUNNER_ADDR is unset, handing it
// nils for the three collaborators only Run uses. Access and AdmitRead never
// touch them — and neither may this.
func TestSchemaIsRefusedByAConsolelessBuildRatherThanPanicking(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	participant := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}
	service := queryproxy.New(people{participant: participant}, contestStore{contest: contest}, nil, nil, nil)

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
	)

	if _, err := service.Schema(t.Context(), contest, participant, netip.MustParseAddr("192.0.2.7")); !errors.Is(err, queryproxy.ErrSchemaHidden) {
		t.Fatalf("answered %v, want ErrSchemaHidden", err)
	}
}

// Schema is handed a participant and a contest the caller's Access already
// admitted (the /play/schema handler, like every other /play route), and
// does not admit them a second time: what the game lookup reads about the
// registration and the contest is not asked again. Here that read would
// refuse — disqualified, finished — and the pair handed in is still described.
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
	).WithSchemas(reader)

	got, err := service.Schema(t.Context(), contest, participant, netip.MustParseAddr("192.0.2.7"))
	if err != nil {
		t.Fatalf("Schema() = %v, want the admitted pair described", err)
	}
	if len(got.Tables) != 1 || got.Tables[0].Name != "guests" {
		t.Fatalf("returned %+v", got.Tables)
	}
}

// individualSchemaFixture is schemaFixture for a participant of an
// individual-timing contest who has not started yet.
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
	).WithSchemas(reader)
	return service, reader, &starts, admitted{contest: contest, participant: registration}
}

// The schema is contest content like the story and the questions: under
// individual timing, reading it is a first read that starts the clock.
func TestReadingTheSchemaStartsAnIndividualParticipantsClock(t *testing.T) {
	service, _, starts, pair := individualSchemaFixture(sqlpolicy.ReadOnly(), nil)

	if _, err := service.Schema(t.Context(), pair.contest, pair.participant, netip.MustParseAddr("192.0.2.7")); err != nil {
		t.Fatalf("Schema() = %v", err)
	}
	if *starts != 1 {
		t.Fatalf("Start called %d times, want 1", *starts)
	}
}

// A schema read that is refused showed nothing, so it starts nothing: not
// where the contest hides its schema, and not from an address the contest
// does not allow — the caller's Access refuses that first, and the start
// asks the gate again rather than trust it did.
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

// A contest whose game was never built has no schema to show, and that is not
// a fault.
func TestSchemaReportsAContestWithNoGame(t *testing.T) {
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	participant := contests.Participant{ID: uuid.New(), Status: contests.RegistrationActive}
	service := queryproxy.New(
		people{participant: participant},
		contestStore{contest: contest},
		games{err: provisioning.ErrNoGame},
		&databases{}, &runner{},
	).WithSchemas(&schemas{})

	if _, err := service.Schema(t.Context(), contest, participant, netip.MustParseAddr("192.0.2.7")); !errors.Is(err, queryproxy.ErrNoGameYet) {
		t.Fatalf("answered %v, want ErrNoGameYet", err)
	}
}

// The deployment wires the single lookup, and the schema panel reads the game
// and the participant's copy through it alone, asked about the admitted
// contest and the admitted participant's own account: none of the default's
// separate reads runs.
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

// A contest that closed its catalogues is refused over the single lookup too,
// before its participant's database is provisioned.
func TestSchemaOverTheSingleLookupStillRefusesAClosedCatalogueFirst(t *testing.T) {
	closed := sqlpolicy.ReadOnly()
	closed.AllowCatalog = false
	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	participant := contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive}
	db := &databases{database: "game_c1_u1"}
	reader := &schemas{}

	service := queryproxy.New(people{}, contestStore{}, games{}, db, &runner{}).
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
