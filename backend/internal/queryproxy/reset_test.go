package queryproxy_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// resetFixture is a running contest whose game lets participants write, with
// one participant, reached through the single lookup the deployment uses.
func resetFixture(t *testing.T) (*queryproxy.Service, *databases, *lookupFake) {
	t.Helper()

	contest := contests.Contest{ID: uuid.New(), Status: contests.StatusRunning, Timing: contests.TimingFixed, EndsAt: &openWindow}
	calls := 0
	lookup := &lookupFake{
		participant: contests.Participant{ID: uuid.New(), ContestID: contest.ID, Status: contests.RegistrationActive},
		contest:     contest,
		game: provisioning.Contest{
			ID: contest.ID, Template: "game_tpl_c1", Version: 4, Policy: sqlpolicy.ReadWrite("notes"),
		},
		instance: provisioning.Instance{Database: "game_c1_u1", TemplateVersion: 4, Status: "ready"},
		calls:    &calls,
	}
	db := &databases{}
	service := queryproxy.New(people{}, contestStore{}, games{}, db, &runner{}, fiveSecondGate, quietTrail, quiet{}).
		WithLookup(lookup)
	return service, db, lookup
}

func resetCommand(contestID uuid.UUID) queryproxy.ResetCommand {
	return queryproxy.ResetCommand{
		ContestID: contestID, UserID: uuid.New(),
		Address: netip.MustParseAddr("192.0.2.44"), Session: monitor.SessionTag("token"), UserAgent: "Firefox",
	}
}

func TestAParticipantOfAWritingContestResetsTheirOwnDatabase(t *testing.T) {
	service, db, lookup := resetFixture(t)
	cmd := resetCommand(lookup.contest.ID)

	if err := service.Reset(t.Context(), cmd); err != nil {
		t.Fatalf("reset: %v", err)
	}

	if len(db.resets) != 1 {
		t.Fatalf("%d resets reached provisioning, want 1", len(db.resets))
	}
	got := db.resets[0]
	if got.registration != lookup.participant.ID {
		t.Fatalf("reset registration %v, want the caller's own %v", got.registration, lookup.participant.ID)
	}
	if got.actor != cmd.UserID {
		t.Fatalf("actor = %v, want the participant's account %v", got.actor, cmd.UserID)
	}
	if got.contest.Template != "game_tpl_c1" || got.contest.Version != 4 {
		t.Fatalf("copied from %+v, want the contest's current template", got.contest)
	}
}

func TestAReadOnlyContestHasNothingToReset(t *testing.T) {
	service, db, lookup := resetFixture(t)
	lookup.game.Policy = sqlpolicy.ReadOnly()

	if err := service.Reset(t.Context(), resetCommand(lookup.contest.ID)); !errors.Is(err, queryproxy.ErrResetReadOnly) {
		t.Fatalf("error = %v, want ErrResetReadOnly", err)
	}
	if len(db.resets) != 0 {
		t.Fatal("a read-only contest's database was copied")
	}
}

// Charged before the copy, so the limit is on presses, not on databases made.
func TestResetsBeyondTheLimitAreRefusedBeforeTheCopy(t *testing.T) {
	service, db, lookup := resetFixture(t)
	cmd := resetCommand(lookup.contest.ID)

	for i := range queryproxy.ResetsPerWindow {
		if err := service.Reset(t.Context(), cmd); err != nil {
			t.Fatalf("reset %d: %v", i+1, err)
		}
	}
	if err := service.Reset(t.Context(), cmd); !errors.Is(err, queryproxy.ErrResetTooOften) {
		t.Fatalf("error = %v, want ErrResetTooOften", err)
	}
	if len(db.resets) != queryproxy.ResetsPerWindow {
		t.Fatalf("%d copies were made, want the %d the limit allows", len(db.resets), queryproxy.ResetsPerWindow)
	}
}

// CLAUDE.md rule 13: a refusal after the charge still spent it.
func TestAResetRefusedForAReadOnlyContestStillCountsAgainstTheLimit(t *testing.T) {
	service, _, lookup := resetFixture(t)
	lookup.game.Policy = sqlpolicy.ReadOnly()
	cmd := resetCommand(lookup.contest.ID)

	for range queryproxy.ResetsPerWindow {
		if err := service.Reset(t.Context(), cmd); !errors.Is(err, queryproxy.ErrResetReadOnly) {
			t.Fatalf("error = %v, want ErrResetReadOnly", err)
		}
	}
	if err := service.Reset(t.Context(), cmd); !errors.Is(err, queryproxy.ErrResetTooOften) {
		t.Fatalf("error = %v, want ErrResetTooOften once the refusals used the budget", err)
	}
}

func TestTheResetLimitIsKeptPerRegistration(t *testing.T) {
	service, db, lookup := resetFixture(t)
	cmd := resetCommand(lookup.contest.ID)
	for range queryproxy.ResetsPerWindow {
		if err := service.Reset(t.Context(), cmd); err != nil {
			t.Fatalf("reset: %v", err)
		}
	}

	lookup.participant.ID = uuid.New()
	if err := service.Reset(t.Context(), resetCommand(lookup.contest.ID)); err != nil {
		t.Fatalf("another participant was refused by the first one's limit: %v", err)
	}
	if last := db.resets[len(db.resets)-1]; last.registration != lookup.participant.ID {
		t.Fatalf("reset %v, want the second participant's database", last.registration)
	}
}

// CLAUDE.md rule 5: the account is one key per caller, the contest in the URL
// is not, so the account's budget is spent before anything is looked up.
func TestTheAccountsBudgetIsChargedBeforeAnyLookup(t *testing.T) {
	service, _, lookup := resetFixture(t)
	service.WithPerMinuteDefault(1)
	cmd := resetCommand(lookup.contest.ID)

	if err := service.Reset(t.Context(), cmd); err != nil {
		t.Fatalf("first reset: %v", err)
	}
	before := *lookup.calls
	if err := service.Reset(t.Context(), cmd); !errors.Is(err, queryrunner.ErrTooManyQueries) {
		t.Fatalf("error = %v, want the account's ErrTooManyQueries", err)
	}
	if *lookup.calls != before {
		t.Fatal("a caller over their budget still cost a lookup")
	}
}

func TestTheGateDecidesWhoMayReset(t *testing.T) {
	for name, given := range map[string]struct {
		stage func(*lookupFake)
		want  error
	}{
		"somebody who never registered": {
			stage: func(l *lookupFake) { l.err = contests.ErrParticipantNotFound },
			want:  contests.ErrNotAParticipant,
		},
		"somebody disqualified": {
			stage: func(l *lookupFake) { l.participant.Status = contests.RegistrationDisqualified },
			want:  contests.ErrNotAParticipant,
		},
		"a contest that has finished": {
			stage: func(l *lookupFake) { l.contest.Status = contests.StatusFinished },
			want:  contests.ErrContestEnded,
		},
		"a contest past its end": {
			stage: func(l *lookupFake) { l.contest.EndsAt = &closedWindow },
			want:  contests.ErrDeadlinePassed,
		},
		"a contest that has not started": {
			stage: func(l *lookupFake) { l.contest.Status = contests.StatusPublished },
			want:  contests.ErrContestNotRunning,
		},
		"a caller outside the contest's network": {
			stage: func(l *lookupFake) {
				l.contest.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}
			},
			want: contests.ErrAddressNotAllowed,
		},
	} {
		t.Run(name, func(t *testing.T) {
			service, db, lookup := resetFixture(t)
			given.stage(lookup)

			if err := service.Reset(t.Context(), resetCommand(lookup.contest.ID)); !errors.Is(err, given.want) {
				t.Fatalf("error = %v, want %v", err, given.want)
			}
			if len(db.resets) != 0 {
				t.Fatal("a refused caller's database was copied")
			}
		})
	}
}

func TestAnAdmittedResetIsObservedAndARefusedOneIsNot(t *testing.T) {
	service, _, lookup := resetFixture(t)
	seen := &watcher{}
	service.WithWatcher(seen)
	cmd := resetCommand(lookup.contest.ID)

	if err := service.Reset(t.Context(), cmd); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if len(seen.visits) != 1 {
		t.Fatalf("the reset was observed %d times, want once", len(seen.visits))
	}
	got := seen.visits[0]
	if got.Registration != lookup.participant.ID || got.Address != cmd.Address ||
		got.Session != cmd.Session || got.UserAgent != cmd.UserAgent {
		t.Fatalf("visit = %+v, want the registration and the command's address, session and browser", got)
	}

	lookup.contest.Status = contests.StatusFinished
	_ = service.Reset(t.Context(), cmd)
	if len(seen.visits) != 1 {
		t.Fatal("a reset refused by the gate was observed")
	}
}

// A participant who never ran anything has no copy yet, and their first query
// makes a fresh one anyway.
func TestResettingADatabaseNeverMadeIsNotAnError(t *testing.T) {
	service, db, lookup := resetFixture(t)
	db.resetErr = provisioning.ErrNoInstance

	if err := service.Reset(t.Context(), resetCommand(lookup.contest.ID)); err != nil {
		t.Fatalf("error = %v, want none", err)
	}
}

func TestAResetWithoutAGameOrACopyIsNamed(t *testing.T) {
	for name, given := range map[string]struct {
		gameErr  error
		resetErr error
		want     error
	}{
		"a contest whose game was never built": {gameErr: provisioning.ErrNoGame, want: queryproxy.ErrNoGameYet},
		"a game that cannot be read":           {gameErr: errors.New("connection refused"), want: queryproxy.ErrUnavailable},
		"a full game cluster":                  {resetErr: provisioning.ErrClusterFull, want: queryproxy.ErrNoRoomForDatabase},
		"a copy that failed":                   {resetErr: errors.New("copy the template: disk I/O"), want: queryproxy.ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			service, db, lookup := resetFixture(t)
			lookup.gameErr = given.gameErr
			db.resetErr = given.resetErr

			if err := service.Reset(t.Context(), resetCommand(lookup.contest.ID)); !errors.Is(err, given.want) {
				t.Fatalf("error = %v, want %v", err, given.want)
			}
		})
	}
}

// A caller who left while waiting for their turn is not the service failing.
func TestAResetWhoseCallerLeftIsACancellationNotAFailure(t *testing.T) {
	service, db, lookup := resetFixture(t)
	db.resetErr = context.Canceled
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := service.Reset(ctx, resetCommand(lookup.contest.ID))
	if !errors.Is(err, queryrunner.ErrCanceled) {
		t.Fatalf("error = %v, want ErrCanceled", err)
	}
	if errors.Is(err, queryproxy.ErrUnavailable) {
		t.Fatal("a caller who left was reported as the service failing")
	}
}

// As in Run: once the console has closed there is nothing to work towards, so
// a copy would be spent for nothing.
func TestAResetIsRefusedOnceNothingIsLeftToAnswer(t *testing.T) {
	service, db, lookup := resetFixture(t)
	answers := &answerable{left: false}
	service.WithAnswerable(answers)

	if err := service.Reset(t.Context(), resetCommand(lookup.contest.ID)); !errors.Is(err, queryproxy.ErrNothingLeftToAnswer) {
		t.Fatalf("error = %v, want ErrNothingLeftToAnswer", err)
	}
	if len(db.resets) != 0 {
		t.Fatal("a database was copied for a participant with nothing left to answer")
	}

	answers.err = errors.New("connection refused")
	if err := service.Reset(t.Context(), resetCommand(lookup.contest.ID)); !errors.Is(err, queryproxy.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable when it cannot be worked out", err)
	}
}

// The read-only refusal is decided from values in hand, before the round trip.
func TestAReadOnlyContestIsRefusedWithoutAskingWhatIsLeft(t *testing.T) {
	service, _, lookup := resetFixture(t)
	lookup.game.Policy = sqlpolicy.ReadOnly()
	answers := &answerable{left: true}
	service.WithAnswerable(answers)

	if err := service.Reset(t.Context(), resetCommand(lookup.contest.ID)); !errors.Is(err, queryproxy.ErrResetReadOnly) {
		t.Fatalf("error = %v, want ErrResetReadOnly", err)
	}
	if answers.calls != 0 {
		t.Fatal("a read-only contest still cost the answerable round trip")
	}
}
