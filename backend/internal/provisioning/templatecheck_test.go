package provisioning_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// readyTemplates fakes the core database's ready templates; internal/postgres
// tests the real ReadyTemplates query.
type readyTemplates struct {
	rows []provisioning.ReadyTemplate
	fail error
}

func (r readyTemplates) ReadyTemplates(context.Context) ([]provisioning.ReadyTemplate, error) {
	return r.rows, r.fail
}

// lookingCluster is a fake TemplatePresence that records what it was asked.
type lookingCluster struct {
	present map[string]bool
	fail    error
	asked   [][]string
}

func clusterHolding(names ...string) *lookingCluster {
	c := &lookingCluster{present: map[string]bool{}}
	for _, name := range names {
		c.present[name] = true
	}
	return c
}

func (c *lookingCluster) MissingDatabases(_ context.Context, names []string) ([]string, error) {
	c.asked = append(c.asked, names)
	if c.fail != nil {
		return nil, c.fail
	}
	var missing []string
	for _, name := range names {
		if !c.present[name] {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

func readyTemplate(database, contestStatus string) provisioning.ReadyTemplate {
	return provisioning.ReadyTemplate{ContestID: uuid.New(), ContestStatus: contestStatus, Database: database}
}

func TestTheCheckReportsEveryReadyTemplateTheClusterLacks(t *testing.T) {
	gone := readyTemplate("game_tpl_gone", "published")
	there := readyTemplate("game_tpl_there", "draft")
	alsoGone := readyTemplate("game_tpl_also_gone", "running")
	store := readyTemplates{rows: []provisioning.ReadyTemplate{gone, there, alsoGone}}

	missing, err := provisioning.NewTemplateCheck(store, clusterHolding(there.Database)).Missing(t.Context())
	if err != nil {
		t.Fatalf("Missing: %v", err)
	}
	if want := []provisioning.ReadyTemplate{gone, alsoGone}; !slices.Equal(missing, want) {
		t.Fatalf("missing = %+v, want %+v", missing, want)
	}
}

func TestTheCheckAsksTheClusterOnceForEveryReadyTemplate(t *testing.T) {
	store := readyTemplates{rows: []provisioning.ReadyTemplate{
		readyTemplate("game_tpl_a", "draft"), readyTemplate("game_tpl_b", "published"),
	}}
	cluster := clusterHolding("game_tpl_a", "game_tpl_b")

	missing, err := provisioning.NewTemplateCheck(store, cluster).Missing(t.Context())
	if err != nil {
		t.Fatalf("Missing: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("a cluster holding every template was reported missing %+v", missing)
	}
	if len(cluster.asked) != 1 || !slices.Equal(cluster.asked[0], []string{"game_tpl_a", "game_tpl_b"}) {
		t.Fatalf("the cluster was asked %v, want one question naming both templates", cluster.asked)
	}
}

func TestTheCheckWithNoReadyTemplateDoesNotAskTheCluster(t *testing.T) {
	cluster := clusterHolding()

	missing, err := provisioning.NewTemplateCheck(readyTemplates{}, cluster).Missing(t.Context())
	if err != nil {
		t.Fatalf("Missing: %v", err)
	}
	if len(missing) != 0 || len(cluster.asked) != 0 {
		t.Fatalf("no ready template, yet missing %+v after asking %v", missing, cluster.asked)
	}
}

// An empty answer from a failure must not read like a cluster that has
// everything.
func TestTheCheckSurfacesAFailureOnEitherSide(t *testing.T) {
	outage := errors.New("connection refused")
	rows := []provisioning.ReadyTemplate{readyTemplate("game_tpl_a", "draft")}

	for name, check := range map[string]*provisioning.TemplateCheck{
		"core database": provisioning.NewTemplateCheck(readyTemplates{fail: outage}, clusterHolding()),
		"game cluster":  provisioning.NewTemplateCheck(readyTemplates{rows: rows}, &lookingCluster{fail: outage}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := check.Missing(t.Context()); !errors.Is(err, outage) {
				t.Fatalf("Missing returned %v, want the %s's failure", err, name)
			}
		})
	}
}

// A draft counts: its built game is what it will be published and played
// with. An unknown status counts too, so the check fails safe.
func TestAGameIsNeededUntilItsContestIsFinished(t *testing.T) {
	for status, needed := range map[string]bool{
		"draft": true, "published": true, "running": true, "finished": false, "archived": false, "": true,
	} {
		if got := readyTemplate("game_tpl_a", status).GameNeeded(); got != needed {
			t.Errorf("GameNeeded() for a %q contest = %v, want %v", status, got, needed)
		}
	}
}
