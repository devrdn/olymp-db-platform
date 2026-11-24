package main

import (
	"encoding/json"
	"testing"

	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
)

func parsedLimits(t *testing.T) limitsDocument {
	t.Helper()
	out, err := limits()
	if err != nil {
		t.Fatalf("limits() = %v", err)
	}
	var parsed limitsDocument
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("the limits contract is not JSON: %v", err)
	}
	return parsed
}

func TestEveryLimitIsAPositiveNumber(t *testing.T) {
	document := parsedLimits(t)

	if len(document.Limits) == 0 || len(document.DurationsMs) == 0 {
		t.Fatalf("the contract has %d limits and %d durations, want both", len(document.Limits), len(document.DurationsMs))
	}
	for name, value := range document.Limits {
		if value <= 0 {
			t.Errorf("limit %s = %d, want a positive bound", name, value)
		}
	}
	for name, value := range document.DurationsMs {
		if value <= 0 {
			t.Errorf("duration %s = %d ms, want a positive span", name, value)
		}
	}
}

// The interface builds its status buttons from this table, so a status it
// leaves out has no buttons at all.
func TestTheTransitionsCoverEveryStatus(t *testing.T) {
	document := parsedLimits(t)

	for _, status := range []string{
		contests.StatusDraft, contests.StatusPublished, contests.StatusRunning,
		contests.StatusFinished, contests.StatusArchived,
	} {
		if _, ok := document.ContestTransitions[status]; !ok {
			t.Errorf("the transitions leave out %q", status)
		}
	}
	for from, targets := range document.ContestTransitions {
		for _, to := range targets {
			if _, ok := document.ContestTransitions[to]; !ok {
				t.Errorf("%s → %s leads to a status the table does not list", from, to)
			}
			if err := (contests.Contest{Status: from}).CanTransitionTo(to); err != nil {
				t.Errorf("the contract allows %s → %s, which CanTransitionTo refuses: %v", from, to, err)
			}
		}
	}
}
