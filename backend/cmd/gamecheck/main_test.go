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
