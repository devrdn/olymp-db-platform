package api_test

import (
	"net/http"
	"testing"

	"github.com/devrdn/olymp-db-platform/backend/internal/monitor"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation"
)

// The route's budget is charged before the contest identifier is even
// parsed (CLAUDE.md rule 13): over budget, a malformed identifier is refused
// for the rate; within it, as malformed, and it is looked up nowhere.
func TestAMalformedContestIdentifierSpendsTheRoutesBudgetFirst(t *testing.T) {
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/contests/not-a-uuid/play/story", ""},
		{http.MethodGet, "/contests/not-a-uuid/play/log.csv", ""},
		{http.MethodPost, "/contests/not-a-uuid/questions/" + "not-a-uuid" + "/answer", `{"value":"x"}`},
	} {
		t.Run(route.path, func(t *testing.T) {
			f := newParticipantFixture(t)

			expectStatus(t, f.send(route.method, route.path, route.body), http.StatusBadRequest, "invalid_contest_id")
			if f.reads.charged() != 1 {
				t.Fatalf("the read budget was charged %d times for a malformed identifier, want 1", f.reads.charged())
			}

			f.reads.refuseWith(participation.ErrTooManyReads)
			expectStatus(t, f.send(route.method, route.path, route.body), http.StatusTooManyRequests, "play_read_too_often")
			if f.roster.Lookups() != 0 {
				t.Fatalf("a malformed identifier was looked up %d times", f.roster.Lookups())
			}
		})
	}
}

// The workspace's and the signals' own budgets are charged before the parse
// too: past their rate a malformed identifier hears the rate.
func TestAMalformedContestIdentifierSpendsAWritesOwnBudgetFirst(t *testing.T) {
	f := newParticipantFixture(t)
	for range monitor.BatchesPerMinute {
		f.send(http.MethodPost, "/contests/not-a-uuid/play/signals", signalsBody(goodAbsence))
	}
	expectStatus(t, f.send(http.MethodPost, "/contests/not-a-uuid/play/signals", signalsBody(goodAbsence)),
		http.StatusTooManyRequests, "signals_too_often")
}
