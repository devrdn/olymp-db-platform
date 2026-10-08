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

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/monitor"
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
//
// Those are queryproxy's own errors and the participation gate's refusals,
// which queryproxy hands over as contests declares them: a participant whose
// time is up meets deadline_passed at the console as at the answer route.
func TestEveryQueryproxyErrorHasItsAnswer(t *testing.T) {
	want := map[error]answered{
		contests.ErrNotAParticipant: {status: http.StatusForbidden, code: "not_a_participant",
			message: "The caller is not taking part in this contest"},
		contests.ErrContestNotRunning: {status: http.StatusConflict, code: "contest_not_running",
			message: "The contest is not running"},
		contests.ErrContestEnded: {status: http.StatusConflict, code: "contest_ended",
			message: "The contest has ended"},
		contests.ErrParticipantFinished: {status: http.StatusConflict, code: "contest_finished",
			message: "The participant has already finished"},
		contests.ErrDeadlinePassed: {status: http.StatusConflict, code: "deadline_passed",
			message: "The deadline for this contest has passed"},
		contests.ErrAddressNotAllowed: {status: http.StatusForbidden, code: "address_not_allowed",
			message: "This contest is only available from the university network"},
		queryproxy.ErrNothingLeftToAnswer: {status: http.StatusConflict, code: "nothing_left_to_answer",
			message: "no question of this contest is still answerable"},
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

	gate := []error{
		contests.ErrNotAParticipant, contests.ErrContestNotRunning, contests.ErrContestEnded,
		contests.ErrParticipantFinished, contests.ErrDeadlinePassed, contests.ErrAddressNotAllowed,
	}
	for _, err := range append(queryproxy.Errors(), gate...) {
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

// Every error monitor hands to a caller has a row, with the answer the
// monitoring handler and the participant's signals route gave before they
// shared one table. The profile answers three of them under codes of its own,
// checked below against its own table.
func TestEveryMonitorErrorHasItsAnswer(t *testing.T) {
	want := map[error]answered{
		monitor.ErrParticipantNotFound: {status: http.StatusNotFound, code: "monitor_participant_not_found",
			message: "No such participant in this contest"},
		monitor.ErrRevisionNotFound: {status: http.StatusNotFound, code: "monitor_revision_not_found",
			message: "No such revision of this participant"},
		monitor.ErrInvalidCursor: {status: http.StatusBadRequest, code: "monitor_invalid_cursor",
			message: "the feed cursor is not valid"},
		monitor.ErrInvalidFeedFilter: {status: http.StatusBadRequest, code: "monitor_invalid_filter",
			message: "the feed filter is not valid"},
		monitor.ErrInvalidQueryFilter: {status: http.StatusBadRequest, code: "monitor_invalid_filter",
			message: "the query filter is not valid"},
		monitor.ErrSignalsTooOften: {status: http.StatusTooManyRequests, code: "signals_too_often",
			message:    "Too many signal batches this minute; keep them and send them later",
			retryAfter: "60"},
		monitor.ErrBatchTooLarge: {status: http.StatusBadRequest, code: "signals_batch_too_large",
			message: "a batch holds at most 50 events"},
		monitor.ErrTooManyEvents: {status: http.StatusConflict, code: "signals_too_many_stored",
			message: "a participant stores at most 20000 events"},
	}

	declared := map[string]bool{}
	for _, info := range httpx.Catalog() {
		declared[info.Code] = true
	}

	for _, err := range monitor.Errors() {
		t.Run(err.Error(), func(t *testing.T) {
			expected, known := want[err]
			if !known {
				t.Fatalf("no expected answer written for %q: add one beside its row", err)
			}
			got, ok := answer(t, monitorErrors, err)
			if !ok {
				t.Fatal("the table has no row for it, so it would reach the client as internal_error")
			}
			if got != expected {
				t.Errorf("answered %+v, want %+v", got, expected)
			}
			wrapped, ok := answer(t, monitorErrors, fmt.Errorf("%w: while reading", err))
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

// The profile reads the same monitoring data under its own codes: a
// registration the monitoring reads do not recognise is the profile's one
// answer for a contest that is not the caller's finished one, and the cursor
// and the filter of the queries tab are profile_ codes.
func TestTheProfileAnswersTheMonitorsErrorsUnderItsOwnCodes(t *testing.T) {
	want := map[error]answered{
		monitor.ErrParticipantNotFound: {status: http.StatusNotFound, code: "profile_contest_not_found",
			message: "No finished contest of yours with that identifier"},
		monitor.ErrInvalidCursor: {status: http.StatusBadRequest, code: "profile_invalid_cursor",
			message: "the feed cursor is not valid"},
		monitor.ErrInvalidQueryFilter: {status: http.StatusBadRequest, code: "profile_invalid_filter",
			message: "the query filter is not valid"},
	}
	for err, expected := range want {
		got, ok := answer(t, profileMonitorErrors, err)
		if !ok || got != expected {
			t.Errorf("%v: answered %+v (matched %v), want %+v", err, got, ok, expected)
		}
	}
}

// Every error contests hands to a caller has a row, with the answer the
// contests handler and the participant's answer route gave before they shared
// one table. A refusal of the request's own shape is invalid_request with the
// error's own text, as it was; the rest have a code each.
func TestEveryContestsErrorHasItsAnswer(t *testing.T) {
	const invalid = "invalid_request"
	want := map[error]answered{
		contests.ErrNotFound: {status: http.StatusNotFound, code: "not_found",
			message: "Contest not found"},
		contests.ErrQuestionNotFound: {status: http.StatusNotFound, code: "question_not_found",
			message: "No such question in this contest"},
		contests.ErrStoryNotFound: {status: http.StatusNotFound, code: "story_not_found",
			message: "This contest has no story yet"},
		contests.ErrParticipantNotFound: {status: http.StatusNotFound, code: "participant_not_found",
			message: "Participant not found"},
		contests.ErrManagerNotFound: {status: http.StatusNotFound, code: "manager_not_found",
			message: "This user does not staff the contest"},
		contests.ErrPackageTooLarge: {status: http.StatusUnprocessableEntity, code: "package_too_large",
			message: "contest carries more questions than one package holds"},
		contests.ErrNotPublishable: {status: http.StatusUnprocessableEntity, code: "not_publishable",
			message: "The contest is not ready to publish"},
		contests.ErrInvalidTransition: {status: http.StatusConflict, code: "invalid_transition",
			message: "contest cannot move to that status"},
		contests.ErrStatusChanged: {status: http.StatusConflict, code: "status_changed",
			message: "contest status changed while the request was being decided"},
		contests.ErrNotEditable: {status: http.StatusConflict, code: "not_editable",
			message: "contest can no longer be edited"},
		contests.ErrFreezeAlreadyReached: {status: http.StatusConflict, code: "freeze_already_reached",
			message: "the leaderboard has already frozen, so the end date can only move later"},
		contests.ErrICPCStartLocked: {status: http.StatusConflict, code: "icpc_start_locked",
			message: "the start date cannot change while ICPC scoring is running"},
		contests.ErrOwnerImmutable: {status: http.StatusConflict, code: "owner_immutable",
			message: "the contest owner cannot be changed here"},
		contests.ErrAlreadyEnrolled: {status: http.StatusConflict, code: "already_enrolled",
			message: "already taking part in this contest"},
		contests.ErrEnrollmentClosed: {status: http.StatusConflict, code: "enrollment_closed",
			message: "this contest is not accepting signups"},
		contests.ErrParticipantStarted: {status: http.StatusConflict, code: "participant_started",
			message: "this participant has a record in this contest; disqualify instead of removing"},
		contests.ErrStaffCannotParticipate: {status: http.StatusConflict, code: "staff_cannot_participate",
			message: "contest staff cannot also register as a participant"},
		contests.ErrParticipantCannotBeStaff: {status: http.StatusConflict, code: "participant_cannot_be_staff",
			message: "a participant cannot be appointed to the contest staff"},
		contests.ErrAddressNotAllowed: {status: http.StatusForbidden, code: "address_not_allowed",
			message: "This contest is only available from the university network"},
		contests.ErrNotAParticipant: {status: http.StatusForbidden, code: "not_a_participant",
			message: "The caller is not taking part in this contest"},
		contests.ErrContestNotRunning: {status: http.StatusConflict, code: "contest_not_running",
			message: "The contest is not running"},
		contests.ErrContestEnded: {status: http.StatusConflict, code: "contest_ended",
			message: "The contest has ended"},
		contests.ErrParticipantFinished: {status: http.StatusConflict, code: "contest_finished",
			message: "The participant has already finished"},
		contests.ErrInvalidContest: {status: http.StatusBadRequest, code: invalid,
			message: "contest is not valid"},
		contests.ErrInvalidQuestion: {status: http.StatusBadRequest, code: invalid,
			message: "question is not valid"},
		contests.ErrInvalidAnswer: {status: http.StatusBadRequest, code: invalid,
			message: "reference answer is not valid"},
		contests.ErrInvalidPolicy: {status: http.StatusBadRequest, code: invalid,
			message: "sql policy is not valid"},
		contests.ErrInvalidRole: {status: http.StatusBadRequest, code: invalid,
			message: "unknown contest role"},
		contests.ErrUnknownLanguage: {status: http.StatusBadRequest, code: invalid,
			message: "language is not available"},
		contests.ErrRosterTooLarge: {status: http.StatusBadRequest, code: invalid,
			message: "too many entries in one roster"},
		contests.ErrQueryTooLong: {status: http.StatusBadRequest, code: invalid,
			message: "search text is too long"},
		contests.ErrAnswerTooLong: {status: http.StatusBadRequest, code: "answer_too_long",
			message: "the answer is too long"},
		contests.ErrNotAChoice: {status: http.StatusBadRequest, code: "answer_not_a_choice",
			message: "A choice question takes one of its own option identifiers"},
		contests.ErrQuestionClosed: {status: http.StatusConflict, code: "question_closed",
			message: "This question is already answered correctly, or every attempt has been used"},
		contests.ErrQuestionNotOpen: {status: http.StatusConflict, code: "question_not_open",
			message: "A question ordered before this one is not closed yet"},
		contests.ErrDeadlinePassed: {status: http.StatusConflict, code: "deadline_passed",
			message: "The deadline for this contest has passed"},
		contests.ErrTooManyAttemptConflicts: {status: http.StatusConflict, code: "attempt_conflict",
			message: "Too many submissions to this question arrived at once; try again"},
	}

	declared := map[string]bool{}
	for _, info := range httpx.Catalog() {
		declared[info.Code] = true
	}

	for _, err := range contests.Errors() {
		t.Run(err.Error(), func(t *testing.T) {
			expected, known := want[err]
			if !known {
				t.Fatalf("no expected answer written for %q: add one beside its row", err)
			}
			got, ok := answer(t, contestsErrors, err)
			if !ok {
				t.Fatal("the table has no row for it, so it would reach the client as internal_error")
			}
			if got != expected {
				t.Errorf("answered %+v, want %+v", got, expected)
			}
			wrapped, ok := answer(t, contestsErrors, fmt.Errorf("%w: while deciding", err))
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
