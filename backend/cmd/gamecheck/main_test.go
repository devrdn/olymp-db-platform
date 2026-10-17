package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

type stubCheck struct {
	missing []provisioning.ReadyTemplate
	fail    error
}

func (s stubCheck) Missing(context.Context) ([]provisioning.ReadyTemplate, error) {
	return s.missing, s.fail
}

func twoMissing() []provisioning.ReadyTemplate {
	return []provisioning.ReadyTemplate{
		{ContestID: uuid.New(), ContestStatus: "published", Database: "game_tpl_c1"},
		{ContestID: uuid.New(), ContestStatus: "running", Database: "game_tpl_c2"},
	}
}

func TestEveryMissingTemplateIsNamedWithItsContest(t *testing.T) {
	missing := twoMissing()
	var out bytes.Buffer

	err := check(t.Context(), stubCheck{missing: missing}, &out)
	if !errors.Is(err, errTemplatesMissing) {
		t.Fatalf("check returned %v, want errTemplatesMissing so a move procedure stops on it", err)
	}

	printed := out.String()
	for _, template := range missing {
		for _, detail := range []string{template.Database, template.ContestID.String(), template.ContestStatus} {
			if !strings.Contains(printed, detail) {
				t.Fatalf("the report does not show %q; an operator cannot rebuild what it does not name:\n%s",
					detail, printed)
			}
		}
	}
}

func TestAClusterWithEveryTemplateIsReportedClean(t *testing.T) {
	var out bytes.Buffer

	if err := check(t.Context(), stubCheck{}, &out); err != nil {
		t.Fatalf("check: %v", err)
	}
	if printed := out.String(); !strings.Contains(printed, "every template") {
		t.Fatalf("the report does not say the cluster is complete:\n%s", printed)
	}
}

// An empty answer from a failure must not read like a complete cluster.
func TestACheckThatCannotLookIsNotReportedClean(t *testing.T) {
	outage := errors.New("connection refused")
	var out bytes.Buffer

	if err := check(t.Context(), stubCheck{fail: outage}, &out); !errors.Is(err, outage) {
		t.Fatalf("check returned %v, want the failure", err)
	}
	if printed := out.String(); strings.Contains(printed, "every template") {
		t.Fatalf("a failed check was reported clean:\n%s", printed)
	}
}

func TestAFinishedContestsMissingTemplateIsListedButDoesNotFailTheCheck(t *testing.T) {
	missing := []provisioning.ReadyTemplate{
		{ContestID: uuid.New(), ContestStatus: "finished", Database: "game_tpl_done"},
		{ContestID: uuid.New(), ContestStatus: "archived", Database: "game_tpl_old"},
	}
	var out bytes.Buffer

	if err := check(t.Context(), stubCheck{missing: missing}, &out); err != nil {
		t.Fatalf("check returned %v; a contest that is over needs no game", err)
	}
	printed := out.String()
	for _, template := range missing {
		if !strings.Contains(printed, template.Database) {
			t.Fatalf("the report does not list %s:\n%s", template.Database, printed)
		}
	}
	if !strings.Contains(printed, "no game needed") {
		t.Fatalf("the report does not say these contests need no game:\n%s", printed)
	}
}

func TestOneContestStillToBePlayedFailsTheCheck(t *testing.T) {
	missing := []provisioning.ReadyTemplate{
		{ContestID: uuid.New(), ContestStatus: "finished", Database: "game_tpl_done"},
		{ContestID: uuid.New(), ContestStatus: "draft", Database: "game_tpl_draft"},
	}
	var out bytes.Buffer

	if err := check(t.Context(), stubCheck{missing: missing}, &out); !errors.Is(err, errTemplatesMissing) {
		t.Fatalf("check returned %v, want errTemplatesMissing for the draft", err)
	}
}

func TestTheGameClusterIsReadUnderTheAPIsName(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://core.invalid/core")
	t.Setenv("GAME_DB_ADMIN_DSN", "postgres://game.invalid/game")
	t.Setenv("GAME_PROVISIONER_DSN", "")

	err := run(nil)
	if err == nil || !strings.Contains(err.Error(), "GAME_PROVISIONER_DSN") {
		t.Fatalf("run returned %v, want it to ask for GAME_PROVISIONER_DSN", err)
	}
}

// Every row marked "to be rebuilt" must be one the footer says how to rebuild,
// a running contest's included.
func TestTheReportSaysHowToRebuildARunningContestsGame(t *testing.T) {
	var out bytes.Buffer

	_ = check(t.Context(), stubCheck{missing: twoMissing()}, &out)

	printed := out.String()
	if strings.Contains(printed, "cannot be rebuilt") {
		t.Fatalf("the report marks a running contest to be rebuilt and says it cannot be:\n%s", printed)
	}
	for _, line := range strings.Split(printed, "\n") {
		if strings.Contains(line, "/game/build") && strings.Contains(line, "running") {
			return
		}
	}
	t.Fatalf("the report does not name the build route for a running contest:\n%s", printed)
}
