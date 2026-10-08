package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
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
