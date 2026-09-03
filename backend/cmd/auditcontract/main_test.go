package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestTheContractListsEveryAction(t *testing.T) {
	document, err := contract()
	if err != nil {
		t.Fatalf("contract() = %v", err)
	}

	var parsed struct {
		Actions []string `json:"actions"`
	}
	if err := json.Unmarshal(document, &parsed); err != nil {
		t.Fatalf("the contract is not JSON: %v", err)
	}

	if len(parsed.Actions) < 30 {
		t.Fatalf("the contract lists %d actions, want the whole vocabulary", len(parsed.Actions))
	}
	seen := make(map[string]bool, len(parsed.Actions))
	for _, action := range parsed.Actions {
		if action == "" {
			t.Error("the contract lists an empty action code")
		}
		if seen[action] {
			t.Errorf("the contract lists %q more than once", action)
		}
		seen[action] = true
	}
}

// TestTheCommittedContractIsCurrent is what makes an action added to
// audit.go without regenerating this file a build failure rather than a
// support ticket: `go test ./...` runs this, so a Go-only change to the
// vocabulary — the constant added, Actions() updated, audit_test.go's own
// guards satisfied — still fails here until `make audit-contract` is run and
// the result committed. The frontend's dictionary test reads the file this
// proves current, never a copy of it, so there is nothing on that side left
// to fall behind either.
func TestTheCommittedContractIsCurrent(t *testing.T) {
	generated, err := contract()
	if err != nil {
		t.Fatalf("contract() = %v", err)
	}

	path, err := contractPath()
	if err != nil {
		t.Fatalf("contractPath() = %v", err)
	}
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — run `make audit-contract`", contractFile, err)
	}

	if string(committed) != string(generated) {
		t.Errorf("%s is out of date — run `make audit-contract` and commit the result", contractFile)
	}
}
