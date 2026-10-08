package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// answered is what one error turned into: the response a client sees, and
// whether an operator was told.
type answered struct {
	status     int
	code       string
	message    string
	retryAfter string
	logged     bool
}

func answer(t *testing.T, table errorTable, err error) (answered, bool) {
	t.Helper()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	if !table.answer(rec, req, log, err) {
		return answered{}, false
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), decodeErr)
	}
	return answered{
		status: rec.Code, code: body.Error.Code, message: body.Error.Message,
		retryAfter: rec.Header().Get("Retry-After"),
		logged:     strings.Contains(logs.String(), "level=ERROR"),
	}, true
}

// Every error queryproxy hands to a caller has a row, and each row says what
// the three handlers that admit a participant — the console, the play screen
// and the events channel — answered for it before they shared one table. The
// expected values are written out, not derived: they are the contract the
// interface already reads codes from.
func TestEveryQueryproxyErrorHasItsAnswer(t *testing.T) {
	want := map[error]answered{
		queryproxy.ErrNotAParticipant: {status: http.StatusForbidden, code: "not_a_participant",
			message: "The caller is not taking part in this contest"},
		queryproxy.ErrContestNotRunning: {status: http.StatusConflict, code: "contest_not_running",
			message: "The contest is not running"},
		queryproxy.ErrFinished: {status: http.StatusConflict, code: "contest_finished",
			message: "The participant has already finished"},
		queryproxy.ErrNothingLeftToAnswer: {status: http.StatusConflict, code: "nothing_left_to_answer",
			message: "no question of this contest is still answerable"},
		queryproxy.ErrAddressNotAllowed: {status: http.StatusForbidden, code: "address_not_allowed",
			message: "This contest is only available from the university network"},
		queryproxy.ErrNoGameYet: {status: http.StatusConflict, code: "no_game_yet",
			message: "The contest has no game database yet"},
		queryproxy.ErrNoRoomForDatabase: {status: http.StatusServiceUnavailable, code: "game_cluster_full",
			message: "The game cluster has no room for another copy of this contest", logged: true},
		queryproxy.ErrUnavailable: {status: http.StatusInternalServerError, code: "internal_error",
			message: "Internal server error", logged: true},
		queryproxy.ErrDatabaseDeclined: {status: http.StatusBadRequest, code: "query_declined",
			message: "the database refused the query"},
		queryproxy.ErrSchemaHidden: {status: http.StatusForbidden, code: "schema_hidden",
			message: "This contest does not show the game's schema"},
	}

	declared := map[string]bool{}
	for _, info := range httpx.Catalog() {
		declared[info.Code] = true
	}

	for _, err := range queryproxy.Errors() {
		t.Run(err.Error(), func(t *testing.T) {
			expected, known := want[err]
			if !known {
				t.Fatalf("no expected answer written for %q: add one beside its row", err)
			}
			got, ok := answer(t, queryproxyErrors, err)
			if !ok {
				t.Fatal("the table has no row for it, so it would reach the client as internal_error")
			}
			if got != expected {
				t.Errorf("answered %+v, want %+v", got, expected)
			}
			// Wrapped, the way the service hands several of them over, it
			// still finds its row.
			wrapped, ok := answer(t, queryproxyErrors, fmt.Errorf("%w: while admitting", err))
			if !ok || wrapped.status != expected.status || wrapped.code != expected.code {
				t.Errorf("wrapped, answered %+v (matched %v), want status %d and code %q",
					wrapped, ok, expected.status, expected.code)
			}
			if !declared[got.code] {
				t.Errorf("code %q is not in the catalog, so it is not in error-codes.json", got.code)
			}
		})
	}
}

// Every outcome the Query Runner reports, and a journal that could not be
// opened, has a row, with the answer the console gave before the table
// existed — the sentence for the rate refusal and Retry-After are this
// table's own additions.
func TestEveryQueryRunnerOutcomeHasItsAnswer(t *testing.T) {
	want := map[error]answered{
		queryrunner.ErrTimeout: {status: http.StatusGatewayTimeout, code: "query_timed_out",
			message: "the query took too long"},
		queryrunner.ErrCanceled: {status: http.StatusRequestTimeout, code: "query_cancelled",
			message: "the caller stopped waiting"},
		queryrunner.ErrBusy: {status: http.StatusServiceUnavailable, code: "query_busy",
			message: "the system is busy"},
		queryrunner.ErrAlreadyRunning: {status: http.StatusConflict, code: "query_already_running",
			message: "a query is already running"},
		queryrunner.ErrTooManyQueries: {status: http.StatusTooManyRequests, code: "query_too_often",
			message: "This caller is asking faster than this installation allows", retryAfter: "60"},
		queryrunner.ErrDiskFull: {status: http.StatusConflict, code: "query_disk_full",
			message: "the database is at its size limit"},
		queryrunner.ErrResultTooLarge: {status: http.StatusBadRequest, code: "query_result_too_large",
			message: "the result is too large to read"},
		queryrunner.ErrJournalUnavailable: {status: http.StatusInternalServerError, code: "internal_error",
			message: "Internal server error", logged: true},
	}

	for _, err := range append(queryrunner.Outcomes(), queryrunner.ErrJournalUnavailable) {
		t.Run(err.Error(), func(t *testing.T) {
			expected, known := want[err]
			if !known {
				t.Fatalf("no expected answer written for %q: add one beside its row", err)
			}
			got, ok := answer(t, queryrunnerErrors, err)
			if !ok {
				t.Fatal("the table has no row for it, so it would reach the client as internal_error")
			}
			if got != expected {
				t.Errorf("answered %+v, want %+v", got, expected)
			}
		})
	}
}

// An error the table does not know is left to the handler, rather than
// answered as something it is not.
func TestAnErrorTheTableDoesNotKnowIsLeftToTheHandler(t *testing.T) {
	if _, ok := answer(t, queryproxyErrors, fmt.Errorf("something else entirely")); ok {
		t.Fatal("the table answered an error it has no row for")
	}
}

// Every error users hands to a caller has a row, with the answer the account
// handlers gave before they shared one table. The one place that answers
// differently, a contest's staff list, is checked below against its own table.
func TestEveryUsersErrorHasItsAnswer(t *testing.T) {
	want := map[error]answered{
		users.ErrNotFound: {status: http.StatusNotFound, code: "not_found",
			message: "User not found"},
		users.ErrInvalidAccount: {status: http.StatusBadRequest, code: "invalid_request",
			message: "account details are not valid"},
		users.ErrLoginTaken: {status: http.StatusConflict, code: "login_taken",
			message: "This login is already in use"},
		users.ErrEmailTaken: {status: http.StatusConflict, code: "email_taken",
			message: "This email is already in use"},
		users.ErrWeakPassword: {status: http.StatusBadRequest, code: "weak_password",
			message: "password does not meet the policy"},
		users.ErrSamePassword: {status: http.StatusBadRequest, code: "same_password",
			message: "Choose a password different from the current one"},
		users.ErrWrongPassword: {status: http.StatusBadRequest, code: "wrong_password",
			message: "Current password is incorrect"},
		users.ErrLastAdministrator: {status: http.StatusConflict, code: "last_administrator",
			message: "this would leave the installation without an administrator"},
		users.ErrReasonRequired: {status: http.StatusBadRequest, code: "reason_required",
			message: "A reason is required"},
		users.ErrAccountDeleted: {status: http.StatusConflict, code: "account_deleted",
			message: "This account is deleted"},
		users.ErrAccountBlocked: {status: http.StatusConflict, code: "account_blocked",
			message: "This account is blocked"},
		users.ErrCannotActOnSelf: {status: http.StatusBadRequest, code: "cannot_act_on_self",
			message: "This operation cannot be performed on your own account"},
		users.ErrRosterTooLarge: {status: http.StatusBadRequest, code: "invalid_request",
			message: "too many rows in one import"},
		users.ErrTooManyAccounts: {status: http.StatusBadRequest, code: "too_many_accounts",
			message: "too many accounts in one operation"},
	}

	declared := map[string]bool{}
	for _, info := range httpx.Catalog() {
		declared[info.Code] = true
	}

	for _, err := range users.Errors() {
		t.Run(err.Error(), func(t *testing.T) {
			expected, known := want[err]
			if !known {
				t.Fatalf("no expected answer written for %q: add one beside its row", err)
			}
			got, ok := answer(t, usersErrors, err)
			if !ok {
				t.Fatal("the table has no row for it, so it would reach the client as internal_error")
			}
			if got != expected {
				t.Errorf("answered %+v, want %+v", got, expected)
			}
			wrapped, ok := answer(t, usersErrors, fmt.Errorf("%w: while saving", err))
			if !ok || wrapped.status != expected.status || wrapped.code != expected.code {
				t.Errorf("wrapped, answered %+v (matched %v), want status %d and code %q",
					wrapped, ok, expected.status, expected.code)
			}
			if !declared[got.code] {
				t.Errorf("code %q is not in the catalog, so it is not in error-codes.json", got.code)
			}
		})
	}
}

// A contest's staff list calls a missing account user_not_found, where the
// accounts screens call it not_found; the same sentinel, so one override.
func TestAContestAnswersAMissingAccountUnderItsOwnCode(t *testing.T) {
	got, ok := answer(t, contestUserErrors, users.ErrNotFound)
	want := answered{status: http.StatusNotFound, code: "user_not_found", message: "User not found"}
	if !ok || got != want {
		t.Errorf("answered %+v (matched %v), want %+v", got, ok, want)
	}
	// Everything else is the package's own answer.
	got, ok = answer(t, contestUserErrors, users.ErrAccountBlocked)
	if !ok || got.code != "account_blocked" {
		t.Errorf("answered %+v (matched %v), want account_blocked", got, ok)
	}
}

// An override answers in place of the row for the same error that the table
// it was derived from holds, the other rows still answer as before, and the
// base table is left unchanged.
func TestAnOverrideTakesPrecedenceOverTheTableItReplacesARowOf(t *testing.T) {
	base := errorTable{
		{err: users.ErrNotFound, status: http.StatusNotFound, code: codeNotFound, message: "base"},
		{err: users.ErrLoginTaken, status: http.StatusConflict, code: codeLoginTaken, message: "taken"},
	}
	derived := base.with(errorRow{
		err: users.ErrNotFound, status: http.StatusGone, code: codeUserNotFound, message: "derived",
	})

	got, _ := answer(t, derived, users.ErrNotFound)
	if got.status != http.StatusGone || got.code != "user_not_found" || got.message != "derived" {
		t.Errorf("derived table answered %+v, want the override", got)
	}
	got, ok := answer(t, derived, users.ErrLoginTaken)
	if !ok || got.code != "login_taken" {
		t.Errorf("derived table answered %+v (matched %v), want the base row for the rest", got, ok)
	}
	got, _ = answer(t, base, users.ErrNotFound)
	if got.message != "base" {
		t.Errorf("the base table answered %+v after deriving from it, want it unchanged", got)
	}
}
