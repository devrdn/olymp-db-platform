package provisioning_test

import (
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// The screen an organizer opens to find one database: the spare pool and the
// participants' own copies, in one list, measured in one round trip.
func TestInstancesListsThePoolAndTheParticipantsCopies(t *testing.T) {
	service, fake, contest, people := serviceFor(t, 1)

	if _, err := service.TopUp(t.Context(), contest, 2); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	held, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	list, err := service.Instances(t.Context(), contest.ID)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if list.Truncated {
		t.Fatal("a contest with two databases came back truncated")
	}
	if len(list.Instances) != 2 {
		t.Fatalf("listed %d databases, want 2", len(list.Instances))
	}

	var spares int
	for _, row := range list.Instances {
		if !row.SizeKnown || row.SizeBytes == 0 {
			t.Fatalf("%s has no size; the screen cannot say how much disk it takes", row.Database)
		}
		if row.Spare() {
			spares++
			continue
		}
		if row.Database != held {
			t.Fatalf("the held copy is %q, want %q", row.Database, held)
		}
		if row.ParticipantLogin == "" {
			t.Fatalf("%s names nobody; the screen cannot say whose it is", row.Database)
		}
	}
	if spares != 1 {
		t.Fatalf("%d spare copies, want the 1 left after the claim", spares)
	}

	// One call for the whole list, not one per database: an olympiad's
	// contest owns a copy per participant, and a round trip each would be
	// hundreds of them for one page.
	names, calls := fake.sizeReads()
	if calls != 1 {
		t.Fatalf("the cluster was measured in %d calls, want 1 for the whole list", calls)
	}
	if len(names) != 2 {
		t.Fatalf("the cluster was asked about %d databases, want 2", len(names))
	}
}

// The rows are the core database's and the sizes are the game cluster's. A
// sick cluster is exactly when an organizer needs to see what exists, so the
// list is served without them rather than refused with them.
func TestInstancesIsStillServedWhenTheClusterCannotBeMeasured(t *testing.T) {
	service, fake, contest, _ := serviceFor(t, 0)
	fake.sizesFail = errors.New("the game cluster is unreachable")

	if _, err := service.TopUp(t.Context(), contest, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}

	list, err := service.Instances(t.Context(), contest.ID)
	if err != nil {
		t.Fatalf("Instances refused the whole list because a size could not be read: %v", err)
	}
	if len(list.Instances) != 1 {
		t.Fatalf("listed %d databases, want 1", len(list.Instances))
	}
	if list.Instances[0].SizeKnown {
		t.Fatal("a size is reported as known although the cluster could not be reached")
	}
}

// pg_database_size raises an error for a name that is not there, so asking
// about a database the sweep already dropped would cost the whole batch its
// sizes — and it would be asking about something that does not exist.
func TestInstancesNeverAsksTheClusterAboutADroppedDatabase(t *testing.T) {
	service, fake, contest, _ := serviceFor(t, 0)

	if _, err := service.TopUp(t.Context(), contest, 2); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	rows := instancesOf(t, t.Context(), contest.ID)
	gone := rows[0].Database
	if err := postgres.NewGameInstances(testPool).MarkDropped(t.Context(), gone); err != nil {
		t.Fatalf("mark dropped: %v", err)
	}

	list, err := service.Instances(t.Context(), contest.ID)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if len(list.Instances) != 2 {
		t.Fatalf("listed %d databases; the dropped row is history and stays visible", len(list.Instances))
	}

	names, _ := fake.sizeReads()
	for _, name := range names {
		if name == gone {
			t.Fatalf("the cluster was asked to measure %s, which is already dropped", gone)
		}
	}
	if len(names) != 1 {
		t.Fatalf("the cluster was asked about %d databases, want only the 1 still there", len(names))
	}
}

// A list that quietly stopped would be read as "that is all there is", which
// on this screen means "your database is gone".
func TestInstancesSaysSoWhenThereAreMoreThanItWillShow(t *testing.T) {
	service, _, contest, _ := serviceFor(t, 0)

	// Written straight to the table rather than through TopUp: the point is
	// the bound, and five hundred and one CREATE DATABASE calls through the
	// fake would say nothing more about it than one INSERT does.
	if _, err := testPool.Exec(t.Context(), `
		INSERT INTO game_instances (contest_id, db_name, template_version, status)
		SELECT $1, 'game_pool_bulk_' || n, 1, 'ready'
		FROM generate_series(1, $2::int) AS n`,
		contest.ID, provisioning.MaxInstancesListed+1); err != nil {
		t.Fatalf("filling the table: %v", err)
	}

	list, err := service.Instances(t.Context(), contest.ID)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if len(list.Instances) != provisioning.MaxInstancesListed {
		t.Fatalf("listed %d databases, want the bound of %d", len(list.Instances), provisioning.MaxInstancesListed)
	}
	if !list.Truncated {
		t.Fatal("the list stopped at the bound without saying there are more")
	}
}

// The choice this whole endpoint turns on. An organizer pressing "drop" has
// decided the copy is broken, usually while its owner is still retrying
// against it — and a connection to a game database is opened per query, so
// DropIdle would refuse for exactly as long as they keep trying. Drop forces
// them closed; see Service.DropInstance's own doc for the full reasoning.
func TestDroppingForcesConnectionsClosedRatherThanWaitingForAnIdleMoment(t *testing.T) {
	service, fake, contest, people := serviceFor(t, 1)

	database, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// Busy is what DropIdle refuses on: if this used DropIdle, the drop would
	// not happen at all.
	fake.markBusy(database)

	if _, err := service.DropInstance(t.Context(), uuid.New(), contest.ID, database); err != nil {
		t.Fatalf("DropInstance: %v", err)
	}

	if _, _, called := fake.outcomeOf(database); called {
		t.Fatal("the organizer's drop went through DropIdle, which refuses while anybody is connected")
	}
	if !droppedByForce(fake, database) {
		t.Fatalf("%s was never dropped from the cluster", database)
	}
	if statusOf(t, t.Context(), database) != "dropped" {
		t.Fatalf("the row is %q after the drop, want dropped", statusOf(t, t.Context(), database))
	}
}

// The reason the drop is safe to offer at all: whatever the participant was
// doing, their next action gives them a working database back, under the
// same name everything already points at.
func TestAParticipantsNextActionRebuildsTheDatabaseThatWasDropped(t *testing.T) {
	service, fake, contest, people := serviceFor(t, 1)

	before, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	made, _ := fake.counts()

	if _, err := service.DropInstance(t.Context(), uuid.New(), contest.ID, before); err != nil {
		t.Fatalf("DropInstance: %v", err)
	}

	// Ensure is what the console calls on every query and the play screen's
	// schema panel on every load.
	after, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure after the drop: %v", err)
	}
	if after != before {
		t.Fatalf("the participant was moved to %q from %q; the name must survive the drop", after, before)
	}
	if remade, _ := fake.counts(); remade != made+1 {
		t.Fatalf("the cluster made %d databases in all, want %d; the dropped row was handed back rather than rebuilt", remade, made+1)
	}
	if status := statusOf(t, t.Context(), after); status == "dropped" {
		t.Fatal("the row is still marked dropped after the participant came back")
	}
}

// The guarantee that bounds what a drop costs: the database contents, and
// nothing else. Answers, score and clock are the olympiad's result and live
// in the core database — an olympiad is not replayed because a database was
// remade.
func TestDroppingLeavesTheAnswersScoreAndClockAlone(t *testing.T) {
	service, _, contest, people := serviceFor(t, 1)
	registration := people[0]

	database, err := service.Ensure(t.Context(), contest, registration)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// A participant mid-contest: started, scored, with an answer recorded.
	var question uuid.UUID
	if err := testPool.QueryRow(t.Context(), `
		INSERT INTO questions (contest_id, ord, points)
		VALUES ($1, 1, 7) RETURNING id`, contest.ID).Scan(&question); err != nil {
		t.Fatalf("create question: %v", err)
	}
	if _, err := testPool.Exec(t.Context(), `
		UPDATE registrations SET started_at = now() - interval '20 minutes', total_score = 7
		WHERE id = $1`, registration); err != nil {
		t.Fatalf("start the participant: %v", err)
	}
	if _, err := testPool.Exec(t.Context(), `
		INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct, points_awarded)
		VALUES ($1, $2, 1, 'the butler', true, 7)`, registration, question); err != nil {
		t.Fatalf("record an answer: %v", err)
	}

	type record struct {
		score   int
		started time.Time
	}
	read := func() record {
		t.Helper()
		var r record
		if err := testPool.QueryRow(t.Context(),
			`SELECT total_score, started_at FROM registrations WHERE id = $1`, registration).
			Scan(&r.score, &r.started); err != nil {
			t.Fatalf("read the registration: %v", err)
		}
		return r
	}
	before := read()

	if _, err := service.DropInstance(t.Context(), uuid.New(), contest.ID, database); err != nil {
		t.Fatalf("DropInstance: %v", err)
	}

	if after := read(); after != before {
		t.Fatalf("the participant's score and clock changed: %+v then %+v", before, after)
	}

	var answers int
	if err := testPool.QueryRow(t.Context(),
		`SELECT count(*) FROM submissions WHERE registration_id = $1 AND is_correct AND points_awarded = 7`,
		registration).Scan(&answers); err != nil {
		t.Fatalf("read the answers: %v", err)
	}
	if answers != 1 {
		t.Fatalf("%d answers survived the drop, want 1", answers)
	}
	// The registration itself is still there — a drop is not a way to remove
	// somebody from a contest.
	if _, err := postgres.NewGameInstances(testPool).Of(t.Context(), registration); err != nil {
		t.Fatalf("the participant lost their instance row entirely: %v", err)
	}
}

// db_name is unique installation-wide, so scoping is the only thing standing
// between a contest-scoped permission and somebody else's olympiad.
func TestDroppingADatabaseOfAnotherContestIsRefused(t *testing.T) {
	mine, fake, contestA, _ := serviceFor(t, 0)
	_, _, contestB, _ := serviceFor(t, 0)

	if _, err := mine.TopUp(t.Context(), contestB, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	theirs := instancesOf(t, t.Context(), contestB.ID)[0].Database

	_, err := mine.DropInstance(t.Context(), uuid.New(), contestA.ID, theirs)
	if !errors.Is(err, provisioning.ErrInstanceNotFound) {
		t.Fatalf("error = %v, want ErrInstanceNotFound", err)
	}
	if droppedByForce(fake, theirs) {
		t.Fatalf("%s was dropped by an organizer of another contest", theirs)
	}
	if statusOf(t, t.Context(), theirs) == "dropped" {
		t.Fatalf("%s was marked dropped by an organizer of another contest", theirs)
	}
}

// Two organizers on the same page, or one page left open while the reclaim
// sweep ran. "Already gone, reload" is a different sentence from "that is not
// a database of this contest", so it is a different refusal.
func TestDroppingSomethingAlreadyDroppedSaysSoRatherThanPretendingToWork(t *testing.T) {
	service, _, contest, _ := serviceFor(t, 0)

	if _, err := service.TopUp(t.Context(), contest, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	database := instancesOf(t, t.Context(), contest.ID)[0].Database

	if _, err := service.DropInstance(t.Context(), uuid.New(), contest.ID, database); err != nil {
		t.Fatalf("the first drop: %v", err)
	}
	_, err := service.DropInstance(t.Context(), uuid.New(), contest.ID, database)
	if !errors.Is(err, provisioning.ErrInstanceAlreadyDropped) {
		t.Fatalf("error = %v, want ErrInstanceAlreadyDropped", err)
	}
}

// Removing somebody's database is exactly what the trail is for, and the
// entry has to name the person who did it — the sweep's own entry has no
// actor, so a shared action code could not answer "who took this away".
func TestDroppingRecordsWhoDidItAgainstTheContest(t *testing.T) {
	service, _, s, contest, people := serviceWithAudit(t, t.Context(), 1)
	actor := uuid.New()

	database, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := service.DropInstance(t.Context(), actor, contest.ID, database); err != nil {
		t.Fatalf("DropInstance: %v", err)
	}

	entries := entriesFor(s, contest.ID)
	if len(entries) != 1 {
		t.Fatalf("%d entries recorded against the contest, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Action != audit.ActionGameInstanceDrop {
		t.Fatalf("action = %q, want %q", entry.Action, audit.ActionGameInstanceDrop)
	}
	if entry.ActorID == nil || *entry.ActorID != actor {
		t.Fatalf("actor = %v, want %v; a person did this, not the sweep", entry.ActorID, actor)
	}
	if entry.Payload["database"] != database {
		t.Fatalf("payload names %v, want %q — that string is what anybody looking for it has in hand",
			entry.Payload["database"], database)
	}
}

// The order the drop and the mark happen in. A cluster that refuses must not
// leave a row saying 'dropped' over a database that is still there: nothing
// repairs that one, because Reclaimable skips dropped rows and the disk is
// leaked for good.
func TestARowIsNotMarkedDroppedWhenTheClusterRefused(t *testing.T) {
	service, fake, contest, _ := serviceFor(t, 0)

	if _, err := service.TopUp(t.Context(), contest, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	database := instancesOf(t, t.Context(), contest.ID)[0].Database
	fake.dropFail = errors.New("the game cluster is unreachable")

	if _, err := service.DropInstance(t.Context(), uuid.New(), contest.ID, database); err == nil {
		t.Fatal("a cluster that refused the drop was reported as a success")
	}
	if status := statusOf(t, t.Context(), database); status == "dropped" {
		t.Fatal("the row was marked dropped although the database is still there")
	}
}

// The pool's depth is the roster's, not a number somebody guessed.
//
// A flat depth is a bet that no more than that many participants turn up, and
// losing it does not degrade gracefully: everybody past it waits for CREATE
// DATABASE inside their own page load, through a maintenance pool of ten
// connections, at the one moment three hundred of them arrive at once.
func TestThePoolIsSizedFromTheRosterAndNotFromAFlatNumber(t *testing.T) {
	service, _, contest, _ := serviceFor(t, 10)

	want, err := service.RosterDepth(3, 0)(t.Context(), contest)
	if err != nil {
		t.Fatalf("sizing: %v", err)
	}
	// Everybody waiting, plus the headroom that makes a late enrolment free
	// rather than a wait.
	if want != 13 {
		t.Fatalf("asked for %d spares for ten waiting participants, want 13", want)
	}
}

// A mistyped roster must not be able to ask the cluster for more than the
// deployment is willing to hold.
func TestTheRosterCannotAskForMoreThanTheDeploymentAllows(t *testing.T) {
	service, _, contest, _ := serviceFor(t, 10)

	want, err := service.RosterDepth(3, 5)(t.Context(), contest)
	if err != nil {
		t.Fatalf("sizing: %v", err)
	}
	if want != 5 {
		t.Fatalf("asked for %d spares against a cap of 5", want)
	}
}

// Somebody who already holds a copy is not waiting for one, and counting them
// would have the tender make a spare nobody will ever claim.
func TestAParticipantWhoAlreadyHasACopyIsNotCountedAsWaiting(t *testing.T) {
	service, _, contest, people := serviceFor(t, 1)
	if _, err := service.Ensure(t.Context(), contest, people[0]); err != nil {
		t.Fatalf("provide a copy: %v", err)
	}

	want, err := service.RosterDepth(0, 0)(t.Context(), contest)
	if err != nil {
		t.Fatalf("sizing: %v", err)
	}
	if want != 0 {
		t.Fatalf("counted %d waiting when the only participant already has a copy", want)
	}
}
