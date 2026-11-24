package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestTheContractNamesEveryDeclaredCode(t *testing.T) {
	document, err := errorCodes()
	if err != nil {
		t.Fatalf("errorCodes() = %v", err)
	}

	var parsed struct {
		Codes []struct {
			Code    string `json:"code"`
			Meaning string `json:"meaning"`
		} `json:"codes"`
	}
	if err := json.Unmarshal(document, &parsed); err != nil {
		t.Fatalf("the contract is not JSON: %v", err)
	}

	if len(parsed.Codes) < 30 {
		t.Fatalf("the contract lists %d codes, want the whole vocabulary", len(parsed.Codes))
	}
	for _, entry := range parsed.Codes {
		if strings.TrimSpace(entry.Meaning) == "" {
			t.Errorf("code %q reaches the contract with no meaning", entry.Code)
		}
	}
}

func TestTheCommittedContractIsCurrent(t *testing.T) {
	for _, c := range contracts {
		t.Run(c.file, func(t *testing.T) {
			generated, err := c.build()
			if err != nil {
				t.Fatalf("build %s: %v", c.file, err)
			}

			path, err := contractPath(c.file)
			if err != nil {
				t.Fatalf("contractPath() = %v", err)
			}
			committed, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v — run `make api-contract`", c.file, err)
			}

			if string(committed) != string(generated) {
				t.Errorf("%s is out of date — run `make api-contract` and commit the result", c.file)
			}
		})
	}
}
