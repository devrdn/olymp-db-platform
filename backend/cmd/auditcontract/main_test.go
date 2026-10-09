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

// TestTheCommittedContractIsCurrent fails until `make audit-contract` is run
// after the vocabulary changes.
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
