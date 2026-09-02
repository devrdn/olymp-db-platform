package api_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// The validator's vocabulary and the API's are two lists, and the interface
// chooses its sentence from the second. A refusal with no code of its own
// would reach a participant as whatever the interface says about an answer it
// cannot read — during a contest, about a query that may have been perfectly
// reasonable.
//
// This walks the first list rather than a copy of it, so adding a refusal
// without a code fails here instead of there.
func TestEveryRefusalHasACodeOfItsOwn(t *testing.T) {
	for _, code := range sqlpolicy.Codes() {
		t.Run(string(code), func(t *testing.T) {
			if !api.HasRefusalCode(code) {
				t.Fatalf("%q has no API code, so a participant meeting it is told nothing", code)
			}
		})
	}
}
