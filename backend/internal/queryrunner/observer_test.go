package queryrunner_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5/pgconn"
)

// The outcome is the journal's status, so a dashboard and the query log never
// disagree about how a query ended, and every pair is one Endings declares.
func TestEveryEndingIsTheJournalsStatusAndADeclaredReason(t *testing.T) {
	missing := &pgconn.PgError{Code: "42P01", Message: `relation "x" does not exist`}

	for _, tc := range []struct {
		name     string
		err      error
		admitted bool
		outcome  queryrunner.Status
		reason   string
	}{
		{"an answer", nil, true, queryrunner.StatusOK, ""},
		{"a refusal", &sqlpolicy.Refusal{Code: sqlpolicy.CodeTooDeep}, false, queryrunner.StatusRejected, "too_deep"},
		{"the rate", queryrunner.ErrTooManyQueries, false, queryrunner.StatusRejected, "rate_limited"},
		{"a full instance", queryrunner.ErrBusy, false, queryrunner.StatusRejected, "busy"},
		{"a second tab", queryrunner.ErrAlreadyRunning, false, queryrunner.StatusRejected, "already_running"},
		{"the disk quota", fmt.Errorf("%w: 10 bytes of 5", queryrunner.ErrDiskFull), true, queryrunner.StatusRejected, "disk_full"},
		{"the queue wait", fmt.Errorf("%w: waiting for a free slot", queryrunner.ErrTimeout), false, queryrunner.StatusTimeout, "queue"},
		{"the deadline", fmt.Errorf("%w: %w", queryrunner.ErrTimeout, missing), true, queryrunner.StatusTimeout, "deadline"},
		{"the caller leaving", fmt.Errorf("%w: %w", queryrunner.ErrCanceled, context.Canceled), true, queryrunner.StatusError, "cancelled"},
		{"an unreadable answer", queryrunner.ErrResultTooLarge, true, queryrunner.StatusError, "result_too_large"},
		{"the database's words", missing, true, queryrunner.StatusError, "database"},
		{"anything else", errors.New("the network fell over"), true, queryrunner.StatusError, "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome, reason := queryrunner.Ending(tc.err, tc.admitted)
			if outcome != string(tc.outcome) || reason != tc.reason {
				t.Fatalf("ending = %s/%s, want %s/%s", outcome, reason, tc.outcome, tc.reason)
			}
			if !slices.Contains(queryrunner.Endings()[outcome], reason) {
				t.Fatalf("%s/%s is not declared in Endings, so metrics would count it as other", outcome, reason)
			}
		})
	}
}

// A refusal's reason is its code, and a new code must not land in "other".
func TestEveryRefusalCodeIsADeclaredReason(t *testing.T) {
	rejected := queryrunner.Endings()[string(queryrunner.StatusRejected)]
	for _, code := range sqlpolicy.Codes() {
		if !slices.Contains(rejected, string(code)) {
			t.Errorf("refusal code %q is not a declared reason", code)
		}
	}
}
