package provisioning_test

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// records is a stand-in for the core database's own account of which
// databases it believes exist. A fake rather than the real repository here,
// unlike everywhere else in this package: what these tests are about is the
// decision taken from those rows, and the rows themselves are checked against
// real SQL by internal/postgres's own RecordedDatabases test.
type records []provisioning.DatabaseRecord

func (r records) RecordedDatabases(context.Context) ([]provisioning.DatabaseRecord, error) {
	return r, nil
}

// onCluster is the game cluster as the orphan sweep sees it: a set of
// databases that exist, and a record of what it was asked to remove.
type onCluster struct {
	present map[string]int64
	busy    map[string]bool
	fail    map[string]error

	askedSizesOf []string
	askedToDrop  []string
}

func cluster0(present ...string) *onCluster {
	c := &onCluster{present: map[string]int64{}}
	for _, name := range present {
		c.present[name] = 8 << 20
	}
	return c
}

func (c *onCluster) DatabaseSizes(_ context.Context, names []string) (map[string]int64, error) {
	c.askedSizesOf = append(c.askedSizesOf, names...)
	sizes := map[string]int64{}
	for _, name := range names {
		if size, there := c.present[name]; there {
			sizes[name] = size
		}
	}
	return sizes, nil
}

func (c *onCluster) DropIdle(_ context.Context, name string) (bool, error) {
	c.askedToDrop = append(c.askedToDrop, name)
	if err, refused := c.fail[name]; refused {
		return false, err
	}
	if c.busy[name] {
		return false, nil
	}
	delete(c.present, name)
	return true, nil
}

func (c *onCluster) wasAskedToDrop(name string) bool {
	for _, asked := range c.askedToDrop {
		if asked == name {
			return true
		}
	}
	return false
}

// instanceRow and templateRow are the two shapes a recorded database takes.
func instanceRow(contest uuid.UUID, database, status string) provisioning.DatabaseRecord {
	return provisioning.DatabaseRecord{Database: database, Status: status, ContestID: contest}
}

func templateRow(contest uuid.UUID, database, status string) provisioning.DatabaseRecord {
	return provisioning.DatabaseRecord{Database: database, Status: status, ContestID: contest, Template: true}
}

func orphanNamed(found []provisioning.Orphan, database string) (provisioning.Orphan, bool) {
	for _, o := range found {
		if o.Database == database {
			return o, true
		}
	}
	return provisioning.Orphan{}, false
}

// The class this job exists for, and the only one: the row says the database
// is gone, and the database is still on the cluster. Eleven of these are on
// the development cluster because a test swept rows a fake cluster only
// pretended to drop.
func TestOrphansAreDatabasesTheCoreDatabaseBelievesAreAlreadyGone(t *testing.T) {
	contest := uuid.New()
	cluster := cluster0("game_pool_stranded")
	sweeper := provisioning.NewOrphanSweeper(
		records{instanceRow(contest, "game_pool_stranded", "dropped")}, cluster)

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	orphan, ok := orphanNamed(found, "game_pool_stranded")
	if !ok {
		t.Fatalf("found = %+v, want game_pool_stranded in it", found)
	}
	if orphan.ContestID != contest {
		t.Fatalf("orphan = %+v, want contest %s — an operator has to know whose it was", orphan, contest)
	}
	if orphan.SizeBytes == 0 {
		t.Fatal("the orphan reports no size; the disk it holds is the whole reason to remove it")
	}
	if orphan.Template {
		t.Fatal("an instance was reported as a template")
	}
}

// A template is the largest single database a contest owns, and it is
// stranded by the same accident. It has to be found, and reported as what it
// is: dropping it is not the same event as dropping one participant's copy.
func TestOrphansIncludeAStrandedTemplate(t *testing.T) {
	contest := uuid.New()
	cluster := cluster0("game_tpl_stranded")
	sweeper := provisioning.NewOrphanSweeper(
		records{templateRow(contest, "game_tpl_stranded", "dropped")}, cluster)

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	orphan, ok := orphanNamed(found, "game_tpl_stranded")
	if !ok {
		t.Fatalf("found = %+v, want the template in it", found)
	}
	if !orphan.Template {
		t.Fatal("a template was reported as an instance")
	}
}

// The reverse direction, and the one that must never go wrong: a database the
// core database still considers live is not this job's business, whatever
// else is true of it. Not merely "not dropped" — never even asked about.
func TestALiveDatabaseIsNeverAnOrphan(t *testing.T) {
	contest := uuid.New()
	cluster := cluster0("game_pool_live", "game_tpl_live")
	sweeper := provisioning.NewOrphanSweeper(records{
		instanceRow(contest, "game_pool_live", "ready"),
		templateRow(contest, "game_tpl_live", "ready"),
	}, cluster)

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("found = %+v, want nothing — both rows say the databases are live", found)
	}

	// Not asked about at all, rather than asked about and spared: the plan an
	// operator reads is built only from names the core database has already
	// given up on.
	for _, name := range cluster.askedSizesOf {
		if name == "game_pool_live" || name == "game_tpl_live" {
			t.Fatalf("the cluster was asked about %s, a database the core database still considers live", name)
		}
	}
	if _, err := sweeper.Remove(t.Context(), found); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(cluster.askedToDrop) != 0 {
		t.Fatalf("the sweep asked to drop %v with nothing to do", cluster.askedToDrop)
	}
}

// One name, two rows disagreeing about it. db_name is unique in
// game_instances and template_db belongs to another table entirely, so this
// should not happen — which is exactly why the answer must not depend on
// which row the query happened to return first. Any row saying the database
// is live settles it.
func TestADatabaseIsSparedWhenAnyRowStillCallsItLive(t *testing.T) {
	contest := uuid.New()
	cluster := cluster0("game_disputed")
	sweeper := provisioning.NewOrphanSweeper(records{
		instanceRow(contest, "game_disputed", "dropped"),
		templateRow(contest, "game_disputed", "ready"),
	}, cluster)

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if _, ok := orphanNamed(found, "game_disputed"); ok {
		t.Fatalf("found = %+v; a database one row still calls 'ready' was offered for removal", found)
	}
}

// The ordinary state of a healthy installation: the row says dropped and the
// database really is gone. Nothing to do, and nothing to report — otherwise
// every past reclaim would show up as a job for an operator forever.
func TestARowWhoseDatabaseIsAlreadyGoneIsNotAnOrphan(t *testing.T) {
	sweeper := provisioning.NewOrphanSweeper(
		records{instanceRow(uuid.New(), "game_pool_long_gone", "dropped")}, cluster0())

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("found = %+v, want nothing: the database is not on the cluster", found)
	}
}

// Removing them is the job's other half, and it has to be safe to run twice:
// the second pass finds the database gone and reports nothing left to do.
func TestRemoveDropsEveryOrphanAndIsSafeToRunTwice(t *testing.T) {
	contest := uuid.New()
	cluster := cluster0("game_pool_stranded", "game_tpl_stranded")
	sweeper := provisioning.NewOrphanSweeper(records{
		instanceRow(contest, "game_pool_stranded", "dropped"),
		templateRow(contest, "game_tpl_stranded", "dropped"),
	}, cluster)

	first, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	result, err := sweeper.Remove(t.Context(), first)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if result.Removed != 2 || result.Busy != 0 || result.Failed != 0 {
		t.Fatalf("result = %+v, want 2 removed and nothing else", result)
	}

	second, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("the second Find: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("a second pass still offers %+v; the job is not idempotent", second)
	}
	again, err := sweeper.Remove(t.Context(), second)
	if err != nil {
		t.Fatalf("the second Remove: %v", err)
	}
	if again.Removed != 0 {
		t.Fatalf("the second pass removed %d databases, want 0", again.Removed)
	}
}

// A database somebody is connected to is left where it is. The core database
// has given up on it, but a connection has not, and this job cannot tell a
// forgotten psql from something that matters — the same reasoning DropIdle
// itself is built on, which is why the sweep is given no forcing drop at all.
func TestRemoveLeavesABusyDatabaseAlone(t *testing.T) {
	contest := uuid.New()
	cluster := cluster0("game_pool_busy")
	cluster.busy = map[string]bool{"game_pool_busy": true}
	sweeper := provisioning.NewOrphanSweeper(
		records{instanceRow(contest, "game_pool_busy", "dropped")}, cluster)

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	result, err := sweeper.Remove(t.Context(), found)
	if err != nil {
		t.Fatalf("a busy database was reported as a failure: %v", err)
	}
	if result.Busy != 1 || result.Removed != 0 {
		t.Fatalf("result = %+v, want one busy and none removed", result)
	}
	if !cluster.wasAskedToDrop("game_pool_busy") {
		t.Fatal("the busy database was never even offered to the cluster")
	}
}

// One cluster refusing must not cost the operator the rest of the run: they
// came to reclaim disk, and a job that stops at the first awkward database
// leaves most of it behind.
func TestRemoveOneFailureDoesNotStopTheRest(t *testing.T) {
	contest := uuid.New()
	cluster := cluster0("game_pool_broken", "game_pool_fine")
	cluster.fail = map[string]error{"game_pool_broken": errors.New("the cluster refused")}
	sweeper := provisioning.NewOrphanSweeper(records{
		instanceRow(contest, "game_pool_broken", "dropped"),
		instanceRow(contest, "game_pool_fine", "dropped"),
	}, cluster)

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	result, err := sweeper.Remove(t.Context(), found)
	if err == nil {
		t.Fatal("a refused drop produced no error")
	}
	if result.Failed != 1 || result.Removed != 1 {
		t.Fatalf("result = %+v, want one failure and one removal", result)
	}
	if !cluster.wasAskedToDrop("game_pool_fine") {
		t.Fatal("the second database was never attempted after the first failed")
	}
}

// The trail is how anybody afterwards finds out where the disk went. The
// absence of one is what made the eleven stranded databases a piece of
// detective work rather than a lookup, so a repair that removes them for good
// says so, with the action code an organizer already searches by.
func TestRemoveRecordsWhatItDropped(t *testing.T) {
	contest := uuid.New()
	cluster := cluster0("game_pool_stranded", "game_tpl_stranded")
	trail := &sink{}
	sweeper := provisioning.NewOrphanSweeper(records{
		instanceRow(contest, "game_pool_stranded", "dropped"),
		templateRow(contest, "game_tpl_stranded", "dropped"),
	}, cluster).WithAudit(audit.New(trail))

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if _, err := sweeper.Remove(t.Context(), found); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	entries := entriesFor(trail, contest)
	if len(entries) != 2 {
		t.Fatalf("%d entries recorded, want one per removed database: %+v", len(entries), entries)
	}
	actions := map[string]string{}
	for _, e := range entries {
		if e.Entity != "contest" || e.EntityID != contest.String() {
			t.Fatalf("entry = %+v, want it against the contest", e)
		}
		database, named := e.Payload["database"].(string)
		if !named {
			t.Fatalf("entry = %+v names no database; that string is what an operator has in hand", e)
		}
		actions[database] = e.Action
	}
	if actions["game_pool_stranded"] != audit.ActionGameInstanceReclaim {
		t.Fatalf("the instance was recorded as %q, want %q", actions["game_pool_stranded"], audit.ActionGameInstanceReclaim)
	}
	if actions["game_tpl_stranded"] != audit.ActionGameTemplateReclaim {
		t.Fatalf("the template was recorded as %q, want %q", actions["game_tpl_stranded"], audit.ActionGameTemplateReclaim)
	}
}

// A database that was never dropped is not audited as though it had been —
// the trail has to be worth reading.
func TestRemoveRecordsNothingForADatabaseItLeftAlone(t *testing.T) {
	contest := uuid.New()
	cluster := cluster0("game_pool_busy")
	cluster.busy = map[string]bool{"game_pool_busy": true}
	trail := &sink{}
	sweeper := provisioning.NewOrphanSweeper(
		records{instanceRow(contest, "game_pool_busy", "dropped")}, cluster).
		WithAudit(audit.New(trail))

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if _, err := sweeper.Remove(t.Context(), found); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if entries := entriesFor(trail, contest); len(entries) != 0 {
		t.Fatalf("a database that was left alone was audited as removed: %+v", entries)
	}
}
