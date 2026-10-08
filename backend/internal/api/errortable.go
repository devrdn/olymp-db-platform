package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// errorRow is how one domain error answers a request, wherever it surfaces.
//
// A row rather than a case in each handler's switch: the same sentinel used to
// be answered by three handlers, each with its own copy, and the copies had
// drifted — one logged a full game cluster and two did not, one sent the
// sentinel's own lower-case text where the others sent a sentence. A row is
// the one place that decision is made.
type errorRow struct {
	err    error
	status int
	code   httpx.Code
	// message is the text sent to the client. Empty sends the error's own
	// text, for the rows where that text is the useful part.
	message string
	// logAs, when set, logs the error at Error level under this message: for
	// the answers an operator has to hear about, not only the client.
	logAs string
	// retryAfter, when set, tells the client how long to wait before asking
	// again.
	retryAfter time.Duration
}

// errorTable is a domain package's errors, answered. Rows are matched in
// order with errors.Is, so a wrapped sentinel finds its row.
type errorTable []errorRow

// with returns a table that answers the overrides the way they say and
// everything else as t does. It is for the handler whose wire contract differs
// from the package's for one error — the same sentinel under another code —
// so that the difference is written down once, beside the handler, rather than
// the handler keeping a switch of its own for the whole package. t itself is
// not changed.
func (t errorTable) with(overrides ...errorRow) errorTable {
	derived := make(errorTable, 0, len(overrides)+len(t))
	// Rows are matched in order, so the overrides go first.
	derived = append(derived, overrides...)
	return append(derived, t...)
}

// answer writes the response for err and reports whether a row matched. An
// error no row knows is left to the caller, which answers it itself — usually
// as its own 500.
func (t errorTable) answer(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) bool {
	for _, row := range t {
		if !errors.Is(err, row.err) {
			continue
		}
		if row.logAs != "" {
			log.ErrorContext(r.Context(), row.logAs, "error", err)
		}
		if row.retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(row.retryAfter/time.Second)))
		}
		message := row.message
		if message == "" {
			message = err.Error()
		}
		httpx.Error(w, r, row.status, row.code, message)
		return true
	}
	return false
}

// queryproxyErrors answers every error in queryproxy.Errors(): what the
// console, the play screen and the events channel all meet when they admit a
// participant. TestEveryQueryproxyErrorHasItsAnswer walks that list, so an
// error added there without a row here fails the build's tests rather than a
// participant's request.
var queryproxyErrors = errorTable{
	// The same answer whether the caller never registered, was disqualified,
	// or the contest named in the URL belongs to somebody else entirely:
	// telling those apart would say whether an account is on a roster, or
	// whether a contest exists at all.
	{err: queryproxy.ErrNotAParticipant, status: http.StatusForbidden, code: codeNotAParticipant,
		message: "The caller is not taking part in this contest"},
	{err: queryproxy.ErrContestNotRunning, status: http.StatusConflict, code: codeContestNotRunning,
		message: "The contest is not running"},
	{err: queryproxy.ErrFinished, status: http.StatusConflict, code: codeContestFinished,
		message: "The participant has already finished"},
	// 409 rather than 403, for the same reason codeQuestionClosed is one: a
	// fact about where the contest currently stands for this participant, not
	// a permission they lack, and it stops being true the moment the contest
	// gives them something to answer again.
	{err: queryproxy.ErrNothingLeftToAnswer, status: http.StatusConflict, code: codeNothingLeftToAnswer},
	{err: queryproxy.ErrAddressNotAllowed, status: http.StatusForbidden, code: codeAddressNotAllowed,
		message: "This contest is only available from the university network"},
	{err: queryproxy.ErrNoGameYet, status: http.StatusConflict, code: codeNoGameYet,
		message: "The contest has no game database yet"},
	// 503 and not 500: the game cluster is at the disk budget its operator
	// set, a fact about this installation right now rather than anything
	// broken — a 500 would send them looking for a stack trace that does not
	// exist. Logged as well as answered: the pool's own warning says it
	// stopped growing, and this says participants are now being turned away.
	{err: queryproxy.ErrNoRoomForDatabase, status: http.StatusServiceUnavailable, code: codeGameClusterFull,
		message: "The game cluster has no room for another copy of this contest",
		logAs:   "the game cluster has no room for a participant's database"},
	// Ours, not the participant's: whatever failed underneath is in the log,
	// and none of it goes to the client.
	{err: queryproxy.ErrUnavailable, status: http.StatusInternalServerError, code: httpx.CodeInternalError,
		message: "Internal server error",
		logAs:   "a participant's request could not be answered"},
	// The database's own words, held back because the contest hides its
	// schema (queryproxy.ErrDatabaseDeclined): this text is all that is said.
	{err: queryproxy.ErrDatabaseDeclined, status: http.StatusBadRequest, code: codeQueryDeclined},
	// A rule of the game, not an outage and not a missing resource: the
	// contest exists and the caller is in it. The interface reads this code
	// and simply does not offer the panel.
	{err: queryproxy.ErrSchemaHidden, status: http.StatusForbidden, code: codeSchemaHidden,
		message: "This contest does not show the game's schema"},
}

// queryRetryAfter is how long a caller over its query rate waits before a
// place is certain to be free: the limiter is a sliding minute
// (queryrunner.RateLimiter), so a full window always clears.
const queryRetryAfter = time.Minute

// queryrunnerErrors answers the Query Runner's outcomes — every error in
// queryrunner.Outcomes(), which TestEveryQueryRunnerOutcomeHasItsAnswer walks
// — and a journal that could not be opened. The console meets all of them;
// the play screen and the events channel meet the rate refusal.
var queryrunnerErrors = errorTable{
	{err: queryrunner.ErrTimeout, status: http.StatusGatewayTimeout, code: codeQueryTimedOut},
	{err: queryrunner.ErrCanceled, status: http.StatusRequestTimeout, code: codeQueryCancelled},
	{err: queryrunner.ErrBusy, status: http.StatusServiceUnavailable, code: codeQueryBusy},
	{err: queryrunner.ErrAlreadyRunning, status: http.StatusConflict, code: codeQueryAlreadyRunning},
	// The same code whether the runner or a façade's own pre-check caught the
	// caller: one limit, checked in two places.
	{err: queryrunner.ErrTooManyQueries, status: http.StatusTooManyRequests, code: codeQueryTooOften,
		message:    "This caller is asking faster than this installation allows",
		retryAfter: queryRetryAfter},
	{err: queryrunner.ErrDiskFull, status: http.StatusConflict, code: codeQueryDiskFull},
	{err: queryrunner.ErrResultTooLarge, status: http.StatusBadRequest, code: codeQueryResultTooLarge},
	// The query never reached the database: opening its journal row failed
	// first. Ours, not the participant's SQL, so none of the database's own
	// words go out.
	{err: queryrunner.ErrJournalUnavailable, status: http.StatusInternalServerError, code: httpx.CodeInternalError,
		message: "Internal server error",
		logAs:   "a query could not be journalled"},
}

// usersErrors answers every error in users.Errors(): what the account screens
// meet. TestEveryUsersErrorHasItsAnswer walks that list. A handler whose
// contract names an error differently derives its own table with with.
var usersErrors = errorTable{
	{err: users.ErrNotFound, status: http.StatusNotFound, code: codeNotFound,
		message: "User not found"},
	{err: users.ErrLoginTaken, status: http.StatusConflict, code: codeLoginTaken,
		message: "This login is already in use"},
	{err: users.ErrEmailTaken, status: http.StatusConflict, code: codeEmailTaken,
		message: "This email is already in use"},
	// 409, not 403: whoever asked is entitled to do this, and it is the
	// state of the installation that refuses. Telling them they lack
	// permission would send them looking for a right they already hold.
	{err: users.ErrLastAdministrator, status: http.StatusConflict, code: codeLastAdministrator},
	{err: users.ErrCannotActOnSelf, status: http.StatusBadRequest, code: codeCannotActOnSelf,
		message: "This operation cannot be performed on your own account"},
	{err: users.ErrReasonRequired, status: http.StatusBadRequest, code: codeReasonRequired,
		message: "A reason is required"},
	// 409, not 403, the same choice as ErrLastAdministrator above and for the
	// same reason: the caller holds the right to do this, and it is the
	// account's own state — deleted — that refuses it, not a permission they
	// lack.
	{err: users.ErrAccountDeleted, status: http.StatusConflict, code: codeAccountDeleted,
		message: "This account is deleted"},
	// The same wire code auth's own sign-in refusal answers with — the
	// client's dictionary already carries a message for it — reused rather
	// than declared a second time under a name of its own.
	{err: users.ErrAccountBlocked, status: http.StatusConflict, code: codeAccountBlocked,
		message: "This account is blocked"},
	{err: users.ErrTooManyAccounts, status: http.StatusBadRequest, code: codeTooManyAccounts},
	{err: users.ErrRosterTooLarge, status: http.StatusBadRequest, code: codeInvalidRequest},
	{err: users.ErrInvalidAccount, status: http.StatusBadRequest, code: codeInvalidRequest},
	// Only changing your own password refuses a new one: a reset and an import
	// generate theirs. The sentinel's own text says which rule the policy
	// refused; the unchanged password gets a sentence rather than the
	// sentinel's text, which is written for a log.
	{err: users.ErrWeakPassword, status: http.StatusBadRequest, code: codeWeakPassword},
	{err: users.ErrSamePassword, status: http.StatusBadRequest, code: codeSamePassword,
		message: "Choose a password different from the current one"},
	{err: users.ErrWrongPassword, status: http.StatusBadRequest, code: codeWrongPassword,
		message: "Current password is incorrect"},
}

// monitorErrors answers every error in monitor.Errors(): what the
// organiser's monitoring screens meet, and what a participant's signals route
// meets. TestEveryMonitorErrorHasItsAnswer walks that list. The profile reads
// the same data under codes of its own and derives its table with with
// (profileMonitorErrors).
var monitorErrors = errorTable{
	{err: monitor.ErrParticipantNotFound, status: http.StatusNotFound, code: codeMonitorParticipantNotFound,
		message: "No such participant in this contest"},
	{err: monitor.ErrRevisionNotFound, status: http.StatusNotFound, code: codeMonitorRevisionNotFound,
		message: "No such revision of this participant"},
	{err: monitor.ErrInvalidCursor, status: http.StatusBadRequest, code: codeMonitorInvalidCursor},
	// A feed's filter and a participant's queries' filter are one code; the
	// text, the error's own, says which was refused.
	{err: monitor.ErrInvalidFeedFilter, status: http.StatusBadRequest, code: codeMonitorInvalidFilter},
	{err: monitor.ErrInvalidQueryFilter, status: http.StatusBadRequest, code: codeMonitorInvalidFilter},
	{err: monitor.ErrSignalsTooOften, status: http.StatusTooManyRequests, code: codeSignalsTooOften,
		message:    "Too many signal batches this minute; keep them and send them later",
		retryAfter: monitor.SignalRetryAfter()},
	{err: monitor.ErrBatchTooLarge, status: http.StatusBadRequest, code: codeSignalsBatchTooLarge},
	// Conflict and not 429: the batch is refused by what this registration has
	// already stored, not by how fast it is arriving, so waiting changes
	// nothing and the collector should drop it rather than keep it (design
	// §9.4 — a 4xx that is not 429 is discarded).
	{err: monitor.ErrTooManyEvents, status: http.StatusConflict, code: codeSignalsTooManyStored},
}
