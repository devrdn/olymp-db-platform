package monitor

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
)

func TestStaffSeeTheParticipantsSQLFeedbackAndNotOurInfrastructure(t *testing.T) {
	shown := []struct{ status, text string }{
		{"error", `ERROR: column "alibi" does not exist (SQLSTATE 42703)`},
		{"error", `ERROR: division by zero (SQLSTATE 22012)`},
		{"rejected", "only SELECT statements are allowed here"},
		{"timeout", "the query ran longer than 5s"},
	}
	for _, c := range shown {
		if got := StaffErrorText(c.status, c.text); got != c.text {
			t.Errorf("%s %q: shown as %q, want it whole", c.status, c.text, got)
		}
	}
	withheld := []struct{ status, text string }{
		{"error", `failed to connect to host=10.0.0.5 user=game_p1 database=game_c1: server error (FATAL: password authentication failed for user "game_p1" (SQLSTATE 28P01))`},
		{"error", `the query service could not answer: rpc error: code = Unavailable desc = connection refused`},
		{"error", `FATAL: terminating connection due to administrator command (SQLSTATE 57P01)`},
		{"error", `FATAL: database "game_c1_p9" does not exist (SQLSTATE 3D000)`},
		{"error", `dial tcp 10.0.0.5:5432: i/o timeout`},
		{"error", "context canceled"},
		{"running", "anything"},
	}
	for _, c := range withheld {
		if got := StaffErrorText(c.status, c.text); got != "" {
			t.Errorf("%s %q: shown as %q, want it withheld", c.status, c.text, got)
		}
	}
}

func TestStaffSeeTheRunnersOwnVerdicts(t *testing.T) {
	for _, err := range []error{queryrunner.ErrResultTooLarge, queryrunner.ErrCanceled, queryrunner.ErrTimeout} {
		for _, text := range []string{err.Error(), "run the query: " + err.Error()} {
			if got := StaffErrorText("error", text); got != text {
				t.Errorf("%q: shown as %q, want it whole", text, got)
			}
		}
	}
	if got := StaffErrorText("error", "the result is too large to read, host=10.0.0.5"); got != "" {
		t.Errorf("a verdict followed by our infrastructure: %q, want it withheld", got)
	}
}
