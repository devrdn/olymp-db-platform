package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// errorRow is how one domain error answers a request, wherever it surfaces. A
// row, not a case in each handler's switch, so the answer to a sentinel is
// decided in one place.
type errorRow struct {
	err    error
	status int
	code   httpx.Code
	// message is the text sent to the client; empty sends the error's own text.
	message string
	// logAs, when set, logs the error at Error level under this message, for
	// answers an operator must hear about.
	logAs      string
	retryAfter time.Duration
}

// errorTable is a domain package's errors, answered. Rows are matched in order
// with errors.Is, so a wrapped sentinel finds its row.
type errorTable []errorRow

// with returns a table that answers the overrides first and everything else as
// t does, for a handler whose contract names one error differently. t is not
// changed.
func (t errorTable) with(overrides ...errorRow) errorTable {
	derived := make(errorTable, 0, len(overrides)+len(t))
	derived = append(derived, overrides...)
	return append(derived, t...)
}

// answer writes the response for err and reports whether a row matched; an
// unknown error is left to the caller.
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

// joined concatenates tables, so rows shared by several packages
// (standingErrors) are written once.
func joined(tables ...errorTable) errorTable {
	var all errorTable
	for _, t := range tables {
		all = append(all, t...)
	}
	return all
}

// standingErrors answers the participation gate's refusals
// (contests.Gate.StandingOf), which the console, play screen, events channel
// and answer route all meet. Composed into queryproxyErrors and contestsErrors
// and walked by both their tests.
var standingErrors = errorTable{
	// One answer whether the caller never registered, was disqualified, or
	// named someone else's contest: telling them apart would reveal rosters and
	// which contests exist.
	{err: contests.ErrNotAParticipant, status: http.StatusForbidden, code: codeNotAParticipant,
		message: "The caller is not taking part in this contest"},
	{err: contests.ErrContestNotRunning, status: http.StatusConflict, code: codeContestNotRunning,
		message: "The contest is not running"},
	// A code of its own because the play screen stops for good on this one and
	// keeps waiting on contest_not_running.
	{err: contests.ErrContestEnded, status: http.StatusConflict, code: codeContestEnded,
		message: "The contest has ended"},
	{err: contests.ErrParticipantFinished, status: http.StatusConflict, code: codeContestFinished,
		message: "The participant has already finished"},
	{err: contests.ErrDeadlinePassed, status: http.StatusConflict, code: codeDeadlinePassed,
		message: "The deadline for this contest has passed"},
	// Explicit, because the participant can act on "wrong network", unlike a
	// bare 403.
	{err: contests.ErrAddressNotAllowed, status: http.StatusForbidden, code: codeAddressNotAllowed,
		message: "This contest is only available from the university network"},
}

// queryproxyErrors answers every error in queryproxy.Errors(), met when
// admitting a participant. TestEveryQueryproxyErrorHasItsAnswer walks that list
// and standingErrors.
var queryproxyErrors = joined(errorTable{
	// 409, not 403: where the contest stands for this participant, not a
	// permission, and it changes once there is something to answer again.
	{err: queryproxy.ErrNothingLeftToAnswer, status: http.StatusConflict, code: codeNothingLeftToAnswer},
	{err: queryproxy.ErrNoGameYet, status: http.StatusConflict, code: codeNoGameYet,
		message: "The contest has no game database yet"},
	// 503, not 500: the game cluster has reached its operator's disk budget,
	// which is a state, not a fault. Logged too, since participants are now
	// being turned away.
	{err: queryproxy.ErrNoRoomForDatabase, status: http.StatusServiceUnavailable, code: codeGameClusterFull,
		message: "The game cluster has no room for another copy of this contest",
		logAs:   "the game cluster has no room for a participant's database"},
	// Ours, not the participant's: the cause goes to the log only.
	{err: queryproxy.ErrUnavailable, status: http.StatusInternalServerError, code: httpx.CodeInternalError,
		message: "Internal server error",
		logAs:   "a participant's request could not be answered"},
	// The database's message withheld because the contest hides its schema.
	{err: queryproxy.ErrDatabaseDeclined, status: http.StatusBadRequest, code: codeQueryDeclined},
	// A rule of the game, not an outage: the interface simply does not offer
	// the panel.
	{err: queryproxy.ErrSchemaHidden, status: http.StatusForbidden, code: codeSchemaHidden,
		message: "This contest does not show the game's schema"},
}, standingErrors)

// queryRetryAfter is long enough for a slot to be free: the limiter is a
// sliding minute, so a full window always clears.
const queryRetryAfter = time.Minute

// queryrunnerErrors answers every error in queryrunner.Outcomes() (walked by
// TestEveryQueryRunnerOutcomeHasItsAnswer), plus a journal that could not be
// opened.
var queryrunnerErrors = errorTable{
	{err: queryrunner.ErrTimeout, status: http.StatusGatewayTimeout, code: codeQueryTimedOut},
	{err: queryrunner.ErrCanceled, status: http.StatusRequestTimeout, code: codeQueryCancelled},
	{err: queryrunner.ErrBusy, status: http.StatusServiceUnavailable, code: codeQueryBusy},
	{err: queryrunner.ErrAlreadyRunning, status: http.StatusConflict, code: codeQueryAlreadyRunning},
	// One code whether the runner or a façade's pre-check caught the caller:
	// one limit, checked in two places.
	{err: queryrunner.ErrTooManyQueries, status: http.StatusTooManyRequests, code: codeQueryTooOften,
		message:    "This caller is asking faster than this installation allows",
		retryAfter: queryRetryAfter},
	{err: queryrunner.ErrDiskFull, status: http.StatusConflict, code: codeQueryDiskFull},
	{err: queryrunner.ErrResultTooLarge, status: http.StatusBadRequest, code: codeQueryResultTooLarge},
	// The journal row could not be opened, so the query never ran. Ours; none
	// of the database's words go out.
	{err: queryrunner.ErrJournalUnavailable, status: http.StatusInternalServerError, code: httpx.CodeInternalError,
		message: "Internal server error",
		logAs:   "a query could not be journalled"},
}

// usersErrors answers every error in users.Errors() (walked by
// TestEveryUsersErrorHasItsAnswer).
var usersErrors = errorTable{
	{err: users.ErrNotFound, status: http.StatusNotFound, code: codeNotFound,
		message: "User not found"},
	{err: users.ErrLoginTaken, status: http.StatusConflict, code: codeLoginTaken,
		message: "This login is already in use"},
	{err: users.ErrEmailTaken, status: http.StatusConflict, code: codeEmailTaken,
		message: "This email is already in use"},
	// 409, not 403: the caller has the right, and the installation's state
	// refuses.
	{err: users.ErrLastAdministrator, status: http.StatusConflict, code: codeLastAdministrator},
	{err: users.ErrCannotActOnSelf, status: http.StatusBadRequest, code: codeCannotActOnSelf,
		message: "This operation cannot be performed on your own account"},
	{err: users.ErrReasonRequired, status: http.StatusBadRequest, code: codeReasonRequired,
		message: "A reason is required"},
	// 409, not 403: the caller has the right, and the deleted account's state
	// refuses.
	{err: users.ErrAccountDeleted, status: http.StatusConflict, code: codeAccountDeleted,
		message: "This account is deleted"},
	// Reuses the code auth's sign-in refusal answers with; the client already
	// has a message for it.
	{err: users.ErrAccountBlocked, status: http.StatusConflict, code: codeAccountBlocked,
		message: "This account is blocked"},
	{err: users.ErrTooManyAccounts, status: http.StatusBadRequest, code: codeTooManyAccounts},
	{err: users.ErrRosterTooLarge, status: http.StatusBadRequest, code: codeInvalidRequest},
	{err: users.ErrInvalidAccount, status: http.StatusBadRequest, code: codeInvalidRequest},
	// Only changing one's own password can be refused for strength; resets and
	// imports generate theirs. The unchanged-password case gets a sentence,
	// since the sentinel's text is written for a log.
	{err: users.ErrWeakPassword, status: http.StatusBadRequest, code: codeWeakPassword},
	{err: users.ErrSamePassword, status: http.StatusBadRequest, code: codeSamePassword,
		message: "Choose a password different from the current one"},
	{err: users.ErrWrongPassword, status: http.StatusBadRequest, code: codeWrongPassword,
		message: "Current password is incorrect"},
}

// monitorErrors answers every error in monitor.Errors() (walked by
// TestEveryMonitorErrorHasItsAnswer). The profile derives its own codes with
// with (profileMonitorErrors).
var monitorErrors = errorTable{
	{err: monitor.ErrParticipantNotFound, status: http.StatusNotFound, code: codeMonitorParticipantNotFound,
		message: "No such participant in this contest"},
	{err: monitor.ErrRevisionNotFound, status: http.StatusNotFound, code: codeMonitorRevisionNotFound,
		message: "No such revision of this participant"},
	{err: monitor.ErrInvalidCursor, status: http.StatusBadRequest, code: codeMonitorInvalidCursor},
	// Both filters share one code; the error's own text says which was refused.
	{err: monitor.ErrInvalidFeedFilter, status: http.StatusBadRequest, code: codeMonitorInvalidFilter},
	{err: monitor.ErrInvalidQueryFilter, status: http.StatusBadRequest, code: codeMonitorInvalidFilter},
	{err: monitor.ErrSignalsTooOften, status: http.StatusTooManyRequests, code: codeSignalsTooOften,
		message:    "Too many signal batches this minute; keep them and send them later",
		retryAfter: monitor.SignalRetryAfter()},
	{err: monitor.ErrBatchTooLarge, status: http.StatusBadRequest, code: codeSignalsBatchTooLarge},
	// 409, not 429: refused by what this registration has already stored, not
	// by speed, so the collector should drop the batch rather than retry
	// (docs/ARCHITECTURE.md §9.4).
	{err: monitor.ErrTooManyEvents, status: http.StatusConflict, code: codeSignalsTooManyStored},
}

// contestsErrors answers every error in contests.Errors() (walked by
// TestEveryContestsErrorHasItsAnswer). Staff routes answer account refusals
// first (contestUserErrors); gate refusals come from standingErrors.
//
// The contract: 404 for what is not there, 400 for a request that could never
// be right, 409 for one that is right but not now, 422 for one that is
// understood and cannot be met.
var contestsErrors = joined(errorTable{
	{err: contests.ErrNotFound, status: http.StatusNotFound, code: codeNotFound,
		message: "Contest not found"},
	// Also the answer for a question of another contest: that it exists
	// elsewhere is not the caller's business.
	{err: contests.ErrQuestionNotFound, status: http.StatusNotFound, code: codeQuestionNotFound,
		message: "No such question in this contest"},
	{err: contests.ErrStoryNotFound, status: http.StatusNotFound, code: codeStoryNotFound,
		message: "This contest has no story yet"},
	{err: contests.ErrParticipantNotFound, status: http.StatusNotFound, code: codeParticipantNotFound,
		message: "Participant not found"},
	{err: contests.ErrManagerNotFound, status: http.StatusNotFound, code: codeManagerNotFound,
		message: "This user does not staff the contest"},

	// 422: the contest has more questions than one package holds. Its own code,
	// since there is no field for the caller to correct.
	{err: contests.ErrPackageTooLarge, status: http.StatusUnprocessableEntity, code: codePackageTooLarge},
	// ContestsHandler.fail answers this with the list of problems; this row is
	// the same answer without it, for the bare sentinel.
	{err: contests.ErrNotPublishable, status: http.StatusUnprocessableEntity, code: codeNotPublishable,
		message: notPublishableMessage},

	{err: contests.ErrInvalidTransition, status: http.StatusConflict, code: codeInvalidTransition},
	// Its own code: the request was legal when sent, so the interface says to
	// look again rather than that it was wrong.
	{err: contests.ErrStatusChanged, status: http.StatusConflict, code: codeStatusChanged},
	{err: contests.ErrNotEditable, status: http.StatusConflict, code: codeNotEditable},
	{err: contests.ErrFreezeAlreadyReached, status: http.StatusConflict, code: codeFreezeAlreadyReached},
	{err: contests.ErrICPCStartLocked, status: http.StatusConflict, code: codeICPCStartLocked},
	{err: contests.ErrOwnerImmutable, status: http.StatusConflict, code: codeOwnerImmutable},
	{err: contests.ErrAlreadyEnrolled, status: http.StatusConflict, code: codeAlreadyEnrolled},
	{err: contests.ErrEnrollmentClosed, status: http.StatusConflict, code: codeEnrollmentClosed},
	{err: contests.ErrParticipantStarted, status: http.StatusConflict, code: codeParticipantStarted},
	{err: contests.ErrStaffCannotParticipate, status: http.StatusConflict, code: codeStaffCannotParticipate},
	{err: contests.ErrParticipantCannotBeStaff, status: http.StatusConflict, code: codeParticipantCannotBeStaff},

	// A request that could never be right; the error's text says why.
	{err: contests.ErrInvalidContest, status: http.StatusBadRequest, code: codeInvalidRequest},
	{err: contests.ErrInvalidQuestion, status: http.StatusBadRequest, code: codeInvalidRequest},
	{err: contests.ErrInvalidAnswer, status: http.StatusBadRequest, code: codeInvalidRequest},
	{err: contests.ErrInvalidPolicy, status: http.StatusBadRequest, code: codeInvalidRequest},
	{err: contests.ErrInvalidRole, status: http.StatusBadRequest, code: codeInvalidRequest},
	{err: contests.ErrUnknownLanguage, status: http.StatusBadRequest, code: codeInvalidRequest},
	{err: contests.ErrRosterTooLarge, status: http.StatusBadRequest, code: codeInvalidRequest},
	{err: contests.ErrQueryTooLong, status: http.StatusBadRequest, code: codeInvalidRequest},

	// What a participant meets submitting an answer.
	{err: contests.ErrAnswerTooLong, status: http.StatusBadRequest, code: codeAnswerTooLong},
	// The caller's own malformed request: the interface only sends an option id
	// for a choice question.
	{err: contests.ErrNotAChoice, status: http.StatusBadRequest, code: codeAnswerNotAChoice,
		message: "A choice question takes one of its own option identifiers"},
	{err: contests.ErrQuestionClosed, status: http.StatusConflict, code: codeQuestionClosed,
		message: "This question is already answered correctly, or every attempt has been used"},
	// The server enforces sequential order (§6.1.1): naming a question not yet
	// open is a 409, a fact about its state.
	{err: contests.ErrQuestionNotOpen, status: http.StatusConflict, code: codeQuestionNotOpen,
		message: "A question ordered before this one is not closed yet"},
	// Running out of retries is momentary, not an outage: 409, try again.
	{err: contests.ErrTooManyAttemptConflicts, status: http.StatusConflict, code: codeAttemptConflict,
		message: "Too many submissions to this question arrived at once; try again"},
}, standingErrors)

// notPublishableMessage is what an unready contest is told, with or without the
// list of problems.
const notPublishableMessage = "The contest is not ready to publish"
