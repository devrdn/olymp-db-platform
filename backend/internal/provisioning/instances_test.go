package provisioning_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

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

	// One call for the whole list, not one per database.
	names, calls := fake.sizeReads()
	if calls != 1 {
		t.Fatalf("the cluster was measured in %d calls, want 1 for the whole list", calls)
	}
	if len(names) != 2 {
		t.Fatalf("the cluster was asked about %d databases, want 2", len(names))
	}
}

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

// pg_database_size errors on a missing name, failing the whole batch.
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

func TestInstancesSaysSoWhenThereAreMoreThanItWillShow(t *testing.T) {
	service, _, contest, _ := serviceFor(t, 0)

	// Inserted directly: the bound is the point, not TopUp.
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

// See Service.DropInstance for why it forces connections closed.
func TestDroppingForcesConnectionsClosedRatherThanWaitingForAnIdleMoment(t *testing.T) {
	service, fake, contest, people := serviceFor(t, 1)

	database, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// DropIdle would refuse a busy database.
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

	// The console calls Ensure on every query.
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

// A drop costs the database contents only; answers, score and clock live in
// the core database.
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
	// A drop does not remove the participant from the contest.
	if _, err := postgres.NewGameInstances(testPool).Of(t.Context(), registration); err != nil {
		t.Fatalf("the participant lost their instance row entirely: %v", err)
	}
}

// db_name is unique installation-wide, so only the contest scope stops an
// organizer from dropping another contest's database.
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

// A 'dropped' row over a live database would never be reclaimed.
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

func TestThePoolIsSizedFromTheRosterAndNotFromAFlatNumber(t *testing.T) {
	service, _, contest, _ := serviceFor(t, 10)

	want, err := service.RosterDepth(provisioning.PoolLimits{Headroom: 3}, nil)(t.Context(), contest)
	if err != nil {
		t.Fatalf("sizing: %v", err)
	}
	// Ten waiting plus a headroom of three.
	if want != 13 {
		t.Fatalf("asked for %d spares for ten waiting participants, want 13", want)
	}
}

func TestTheRosterCannotAskForMoreThanTheDeploymentAllows(t *testing.T) {
	service, _, contest, _ := serviceFor(t, 10)

	want, err := service.RosterDepth(provisioning.PoolLimits{Headroom: 3, MaxCopies: 5}, nil)(t.Context(), contest)
	if err != nil {
		t.Fatalf("sizing: %v", err)
	}
	if want != 5 {
		t.Fatalf("asked for %d spares against a cap of 5", want)
	}
}

func TestAParticipantWhoAlreadyHasACopyIsNotCountedAsWaiting(t *testing.T) {
	service, _, contest, people := serviceFor(t, 1)
	if _, err := service.Ensure(t.Context(), contest, people[0]); err != nil {
		t.Fatalf("provide a copy: %v", err)
	}

	want, err := service.RosterDepth(provisioning.PoolLimits{}, nil)(t.Context(), contest)
	if err != nil {
		t.Fatalf("sizing: %v", err)
	}
	if want != 0 {
		t.Fatalf("counted %d waiting when the only participant already has a copy", want)
	}
}

func TestThePoolIsBoundedByTheRoomOnTheClusterAndNotOnlyByACount(t *testing.T) {
	service, fake, contest, _ := serviceFor(t, 10)
	// 1 GiB per copy, 97 of 100 GiB used: three copies fit, not thirteen.
	fake.templateBytes = 1 << 30
	fake.clusterBytes = 97 << 30

	var told []provisioning.Sizing
	limits := provisioning.PoolLimits{Headroom: 3, MaxCopies: 500, MaxClusterBytes: 100 << 30}
	want, err := service.RosterDepth(limits, func(_ context.Context, _ provisioning.Contest, s provisioning.Sizing) {
		told = append(told, s)
	})(t.Context(), contest)
	if err != nil {
		t.Fatalf("sizing: %v", err)
	}
	if want != 3 {
		t.Fatalf("asked for %d copies with room for three; the count cap of 500 was never the bound", want)
	}

	// The refusal is reported with the numbers behind it.
	if len(told) != 1 {
		t.Fatalf("the refusal was reported %d times, want once", len(told))
	}
	if told[0].Bound != provisioning.BoundDisk {
		t.Fatalf("reported bound %q, want %q", told[0].Bound, provisioning.BoundDisk)
	}
	if told[0].Wanted != 13 || told[0].Depth != 3 {
		t.Fatalf("reported %+v, want thirteen asked for and three granted", told[0])
	}
	if told[0].TemplateBytes != 1<<30 || told[0].ClusterBytes != 97<<30 || told[0].Budget != 100<<30 {
		t.Fatalf("reported %+v without the measurements the refusal was decided on", told[0])
	}
}

// Existing spares are already in the cluster's size, so the budget bounds
// only the copies still to be made.
func TestTheDiskBoundCountsTheCopiesStillToBeMadeAndNotTheOnesAlreadyThere(t *testing.T) {
	service, fake, contest, _ := serviceFor(t, 10)
	if made, err := service.TopUp(t.Context(), contest, 4); err != nil || made != 4 {
		t.Fatalf("staging four spares made %d (%v)", made, err)
	}
	fake.templateBytes = 1 << 30
	fake.clusterBytes = 98 << 30 // two more copies fit

	want, err := service.RosterDepth(
		provisioning.PoolLimits{Headroom: 3, MaxClusterBytes: 100 << 30}, nil)(t.Context(), contest)
	if err != nil {
		t.Fatalf("sizing: %v", err)
	}
	if want != 6 {
		t.Fatalf("granted a depth of %d; four spares exist and two more fit, so six is the answer", want)
	}
}

// Over budget grants nothing new and never goes negative.
func TestAClusterAlreadyOverItsBudgetIsAskedForNothingMore(t *testing.T) {
	service, fake, contest, _ := serviceFor(t, 10)
	fake.templateBytes = 1 << 30
	fake.clusterBytes = 200 << 30

	want, err := service.RosterDepth(
		provisioning.PoolLimits{Headroom: 3, MaxClusterBytes: 100 << 30}, nil)(t.Context(), contest)
	if err != nil {
		t.Fatalf("sizing: %v", err)
	}
	if want != 0 {
		t.Fatalf("granted a depth of %d on a cluster already over budget", want)
	}
}

// The measurement is two catalogue reads per live contest per tick, skipped
// when no byte budget is set.
func TestNoByteBudgetMeansTheClusterIsNeverMeasured(t *testing.T) {
	service, fake, contest, _ := serviceFor(t, 10)

	if _, err := service.RosterDepth(
		provisioning.PoolLimits{Headroom: 3, MaxCopies: 500}, nil)(t.Context(), contest); err != nil {
		t.Fatalf("sizing: %v", err)
	}
	if fake.clusterReads != 0 {
		t.Fatalf("measured the cluster %d times with no byte budget set", fake.clusterReads)
	}
}

// A failed measurement fails closed: the pool is left where it is.
func TestAClusterThatCannotBeMeasuredRefusesToSizeThePool(t *testing.T) {
	service, fake, contest, _ := serviceFor(t, 10)
	fake.clusterBytesFail = errors.New("the game cluster is away")

	if _, err := service.RosterDepth(
		provisioning.PoolLimits{Headroom: 3, MaxClusterBytes: 100 << 30}, nil)(t.Context(), contest); err == nil {
		t.Fatal("a cluster that could not be measured was sized as though it were empty")
	}
}

func TestTheCountCapReportsItselfWhenItIsWhatBound(t *testing.T) {
	service, _, contest, _ := serviceFor(t, 10)

	var told []provisioning.Sizing
	want, err := service.RosterDepth(provisioning.PoolLimits{Headroom: 3, MaxCopies: 5},
		func(_ context.Context, _ provisioning.Contest, s provisioning.Sizing) {
			told = append(told, s)
		})(t.Context(), contest)
	if err != nil {
		t.Fatalf("sizing: %v", err)
	}
	if want != 5 {
		t.Fatalf("asked for %d spares against a cap of 5", want)
	}
	if len(told) != 1 || told[0].Bound != provisioning.BoundCopies || told[0].Wanted != 13 {
		t.Fatalf("reported %+v, want one report naming the count cap", told)
	}
}

func TestAPoolThatGotWhatItAskedForReportsNothing(t *testing.T) {
	service, fake, contest, _ := serviceFor(t, 10)
	fake.templateBytes = 1 << 20
	fake.clusterBytes = 1 << 20

	reported := 0
	want, err := service.RosterDepth(
		provisioning.PoolLimits{Headroom: 3, MaxCopies: 500, MaxClusterBytes: 100 << 30},
		func(context.Context, provisioning.Contest, provisioning.Sizing) { reported++ })(t.Context(), contest)
	if err != nil {
		t.Fatalf("sizing: %v", err)
	}
	if want != 13 || reported != 0 {
		t.Fatalf("granted %d and reported %d times; nothing bound", want, reported)
	}
}
