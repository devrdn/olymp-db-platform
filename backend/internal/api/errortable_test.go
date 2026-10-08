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

// An error the table does not know is left to the handler, rather than
// answered as something it is not.
func TestAnErrorTheTableDoesNotKnowIsLeftToTheHandler(t *testing.T) {
	if _, ok := answer(t, queryproxyErrors, fmt.Errorf("something else entirely")); ok {
		t.Fatal("the table answered an error it has no row for")
	}
}
