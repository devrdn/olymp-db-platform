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

// joined is tables one after another, as one table. For rows more than one
// package's table answers — standingErrors — so that they are written once and
// composed into each table that meets them, rather than copied into each.
func joined(tables ...errorTable) errorTable {
	var all errorTable
	for _, t := range tables {
		all = append(all, t...)
	}
	return all
}

// standingErrors answers the participation gate's refusals
// (contests.Gate.StandingOf): what a participant meets on the console, the play
// screen, the events channel and the answer route alike, whichever of them
// asked. Composed into queryproxyErrors and contestsErrors, whose packages both
// hand these over, and walked by both their tests.
var standingErrors = errorTable{
	// The same answer whether the caller never registered, was disqualified,
	// or the contest named in the URL belongs to somebody else entirely:
	// telling those apart would say whether an account is on a roster, or
	// whether a contest exists at all.
	{err: contests.ErrNotAParticipant, status: http.StatusForbidden, code: codeNotAParticipant,
		message: "The caller is not taking part in this contest"},
	{err: contests.ErrContestNotRunning, status: http.StatusConflict, code: codeContestNotRunning,
		message: "The contest is not running"},
	// 409 like contest_not_running, and a code of its own because the play
	// screen acts on the difference: this one stops it for good, the other
	// keeps it waiting.
	{err: contests.ErrContestEnded, status: http.StatusConflict, code: codeContestEnded,
		message: "The contest has ended"},
	{err: contests.ErrParticipantFinished, status: http.StatusConflict, code: codeContestFinished,
		message: "The participant has already finished"},
	{err: contests.ErrDeadlinePassed, status: http.StatusConflict, code: codeDeadlinePassed,
		message: "The deadline for this contest has passed"},
	// Deliberately explicit: "you are on the wrong network" is something the
	// participant can act on, unlike a bare 403. The same answer at
	// enrolment and at the door of the console.
	{err: contests.ErrAddressNotAllowed, status: http.StatusForbidden, code: codeAddressNotAllowed,
		message: "This contest is only available from the university network"},
}

// queryproxyErrors answers every error in queryproxy.Errors(): what the
// console, the play screen and the events channel all meet when they admit a
// participant. TestEveryQueryproxyErrorHasItsAnswer walks that list, so an
// error added there without a row here fails the build's tests rather than a
// participant's request. The gate's refusals, which queryproxy hands over as
// contests declares them, are answered by standingErrors, and that test walks
// them too.
var queryproxyErrors = joined(errorTable{
	// 409 rather than 403, for the same reason codeQuestionClosed is one: a
	// fact about where the contest currently stands for this participant, not
	// a permission they lack, and it stops being true the moment the contest
	// gives them something to answer again.
	{err: queryproxy.ErrNothingLeftToAnswer, status: http.StatusConflict, code: codeNothingLeftToAnswer},
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
}, standingErrors)

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

// contestsErrors answers every error in contests.Errors(): what the
// organiser's contest routes meet, and what a participant meets reading the
// story or submitting an answer. TestEveryContestsErrorHasItsAnswer walks that
// list. A contest's staff routes also meet the account package's refusals and
// answer those first (contestUserErrors). The participation gate's refusals
// are answered by standingErrors.
//
// The mapping is the API's contract: 404 for things that are not there, 400
// for a request that could never be right, 409 for one that is right but not
// now, 422 for a request that is understood and cannot be met.
var contestsErrors = joined(errorTable{
	{err: contests.ErrNotFound, status: http.StatusNotFound, code: codeNotFound,
		message: "Contest not found"},
	// Also the answer when the question named in the URL belongs to another
	// contest: that it exists elsewhere is not this caller's business
	// (contests.Service.Submit's own doc).
	{err: contests.ErrQuestionNotFound, status: http.StatusNotFound, code: codeQuestionNotFound,
		message: "No such question in this contest"},
	{err: contests.ErrStoryNotFound, status: http.StatusNotFound, code: codeStoryNotFound,
		message: "This contest has no story yet"},
	{err: contests.ErrParticipantNotFound, status: http.StatusNotFound, code: codeParticipantNotFound,
		message: "Participant not found"},
	{err: contests.ErrManagerNotFound, status: http.StatusNotFound, code: codeManagerNotFound,
		message: "This user does not staff the contest"},

	// 422 rather than 500: the request was understood and the contest is real,
	// it simply carries more questions than one package holds. Its own code
	// rather than invalid_request, because the caller sent no field to correct
	// — what has to change is the contest.
	{err: contests.ErrPackageTooLarge, status: http.StatusUnprocessableEntity, code: codePackageTooLarge},
	// The publish gate answers with a code and the whole list of what is
	// missing, built by the handler from the typed error before this table is
	// asked (ContestsHandler.fail); this row is the same answer without the
	// list, for the sentinel itself. It goes through the same helper as every
	// other error rather than building its own envelope: writing one by hand is
	// how this code once reached clients undeclared, with no message in any
	// language.
	{err: contests.ErrNotPublishable, status: http.StatusUnprocessableEntity, code: codeNotPublishable,
		message: notPublishableMessage},

	{err: contests.ErrInvalidTransition, status: http.StatusConflict, code: codeInvalidTransition},
	// Its own code, not invalid_transition: the caller asked for something that
	// was legal when they asked, so the interface tells them to look again
	// rather than that they were wrong.
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

	// A request whose shape could never be right, by its own rule: the error's
	// own text says which.
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
	// The caller's own malformed request, like an overlong answer: the
	// interface only ever sends an option id for a choice question.
	{err: contests.ErrNotAChoice, status: http.StatusBadRequest, code: codeAnswerNotAChoice,
		message: "A choice question takes one of its own option identifiers"},
	{err: contests.ErrQuestionClosed, status: http.StatusConflict, code: codeQuestionClosed,
		message: "This question is already answered correctly, or every attempt has been used"},
	// The server is what enforces sequential order (§6.1.1), not the
	// interface: a direct request naming a question that has not opened yet is
	// refused here, the same 409 family as codeQuestionClosed (another fact
	// about this question's current state, not a permission the caller lacks).
	{err: contests.ErrQuestionNotOpen, status: http.StatusConflict, code: codeQuestionNotOpen,
		message: "A question ordered before this one is not closed yet"},
	// Running out of retries is a fact about this exact moment, not an outage —
	// the same 409 family as codeQuestionClosed and codeStatusChanged, and the
	// same honest instruction: try again.
	{err: contests.ErrTooManyAttemptConflicts, status: http.StatusConflict, code: codeAttemptConflict,
		message: "Too many submissions to this question arrived at once; try again"},
}, standingErrors)

// notPublishableMessage is what a contest that is not ready to publish is told,
// with or without the list of problems.
const notPublishableMessage = "The contest is not ready to publish"
