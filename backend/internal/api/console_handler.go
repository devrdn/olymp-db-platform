package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/rpc"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Console is the slice of the query façade this endpoint needs.
type Console interface {
	Run(ctx context.Context, cmd queryproxy.Command) (*queryrunner.Result, error)
}

// ConsoleHandler serves the participant's SQL console.
type ConsoleHandler struct {
	console Console
	mw      *auth.Middleware
	log     *slog.Logger
}

// NewConsoleHandler assembles the console endpoint.
func NewConsoleHandler(console Console, mw *auth.Middleware, log *slog.Logger) *ConsoleHandler {
	return &ConsoleHandler{console: console, mw: mw, log: log}
}

// Mount registers the route.
//
// Authentication and nothing more: taking part in a contest is not a
// permission an administrator grants, it is a registration, and the façade
// looks it up. A permission check here would be a second answer to a question
// already answered in one place.
func (h *ConsoleHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate)
		r.Post("/contests/{contestID}/query", h.run)
	})
}

type runQueryRequest struct {
	SQL string `json:"sql"`
}

type runQueryResponse struct {
	Columns []string `json:"columns"`
	// ColumnTypes names each column's type — `text`, `timestamp with time
	// zone` — in the same vocabulary the schema panel gets from the catalogue,
	// so the console can print it under the column's name.
	//
	// A list beside `columns` rather than a list of objects in place of it,
	// and the reason is what already reads this body. `columns` is a list of
	// strings today: the CSV export takes one (frontend/lib/format/csv.ts) and
	// the results table draws its header from one. Turning it into objects
	// would be a change every one of those has to make in the same commit or
	// the console stops working — for a label under a heading. A second key
	// is additive: a client that has never heard of it keeps working, and one
	// that has reads the nth type under the nth name.
	//
	// The cost of the choice is that the two lists can fall out of step, so
	// the guarantee is written down here and kept at the source: either empty,
	// or exactly as long as `columns`. An entry can be empty on its own, for a
	// type the runner could not name.
	ColumnTypes []string `json:"column_types"`
	Rows        [][]any  `json:"rows"`
	// Truncated says the answer is longer than what is here. A flag rather
	// than a silent cut: nine hundred rows of nine thousand, unannounced, is a
	// wrong answer rather than a short one.
	Truncated    bool  `json:"truncated"`
	RowsAffected int64 `json:"rows_affected"`
	// DurationMicros is how long the statement itself took — the meter under
	// the editor. Microseconds rather than milliseconds because the meter
	// rounds to milliseconds to show it, and a value already rounded here
	// would make every quick query read "0 мс".
	//
	// The statement and nothing around it: not the connection, not the queue,
	// not this request. Zero means the runner did not report one, which is
	// what an older runner and a query that never reached the database both
	// look like.
	DurationMicros int64 `json:"duration_micros"`
}

func (h *ConsoleHandler) run(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())

	contestID, err := uuid.Parse(chi.URLParam(r, "contestID"))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, "The contest identifier is not a UUID")
		return
	}

	var req runQueryRequest
	if !decodeBody(w, r, &req) {
		return
	}

	result, err := h.console.Run(r.Context(), queryproxy.Command{
		ContestID: contestID,
		UserID:    identity.UserID,
		SQL:       req.SQL,
		// Resolved by the one place allowed to read a forwarded header, so
		// that a contest held on one network stays on it.
		Address: clientAddress(r),
		// The same identifier the technical log carries, so a participant
		// saying "it failed at two o'clock" can be answered.
		RequestID: requestUUID(r.Context()),
		// For the tracker of parallel sessions (design §2.3).
		Session:   sessionTag(r),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// Never nil in the body: a client that has to distinguish `null` from `[]`
	// before it can draw a table is a client with a bug waiting.
	answer := runQueryResponse{
		Columns:      result.Columns,
		ColumnTypes:  result.ColumnTypes,
		Rows:         result.Rows,
		Truncated:    result.Truncated,
		RowsAffected: result.RowsAffected,
		// Microseconds, the unit the field's name promises.
		DurationMicros: result.Duration.Microseconds(),
	}
	answer.Columns = emptyIfNil(answer.Columns)
	answer.ColumnTypes = emptyIfNil(answer.ColumnTypes)
	answer.Rows = emptyIfNil(answer.Rows)
	httpx.JSON(w, r, http.StatusOK, answer)
}

// clientAddress is where the request came from.
//
// An unparseable address is not a reason to answer a query: it is a reason to
// refuse one, because a contest restricted to a network cannot be honoured
// without knowing which one this is. The zero value fails every restriction,
// which is the direction to fail in.
func clientAddress(r *http.Request) netip.Addr {
	addr, err := netip.ParseAddr(httpx.ClientIP(r))
	if err != nil {
		return netip.Addr{}
	}
	return addr
}

// sessionTag names the request's session for the monitoring trail: a hash of
// the session token (monitor.SessionTag), so the token itself never leaves
// the authentication layer's own store. Empty without a session cookie, which
// an authenticated route never sees.
func sessionTag(r *http.Request) string {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return ""
	}
	return monitor.SessionTag(cookie.Value)
}

// requestUUID is the request's own identifier, as the journal's column needs
// it.
//
// The technical log's identifier is a string and the column is a uuid, so a
// deployment that ever changes how request identifiers are shaped would
// otherwise fail every query rather than lose the correlation. A fresh one is
// worse than a matching one and far better than a refusal.
func requestUUID(ctx context.Context) uuid.UUID {
	if id, err := uuid.Parse(logging.RequestIDFrom(ctx)); err == nil {
		return id
	}
	return uuid.New()
}

// fail turns what came back into a status, a code and — where there is one —
// the name of the thing that was refused.
//
// Every refusal gets its own code, because the interface chooses its sentence
// by code and by nothing else. The subject travels beside it rather than
// inside the message: "which function" is what the participant needs, and a
// message assembled here would be in one language.
func (h *ConsoleHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var refusal *sqlpolicy.Refusal
	if errors.As(err, &refusal) {
		code, known := refusalCodes[refusal.Code]
		if !known {
			// A refusal this build has no sentence for. Reported as a refusal
			// rather than as a fault, because the query really was declined —
			// and logged, because the omission is ours.
			h.log.ErrorContext(r.Context(), "a refusal with no code of its own",
				"refusal", refusal.Code, "subject", refusal.Subject)
			code = codeQueryStatementNotSupported
		}
		details := map[string]any{"subject": refusal.Subject}
		// Only a parse error names a place in the text — carried so the
		// console can point at it instead of a participant counting
		// characters. Omitted rather than sent as zero: zero is a valid
		// character offset too, and dropping the key is how the client tells
		// "no position" from "the very first character".
		if refusal.Position > 0 {
			details["position"] = refusal.Position
		}
		httpx.ErrorWithDetails(w, r, http.StatusBadRequest, code, refusal.Error(), details)
		return
	}

	// Admission and the runner's own outcomes: the same tables the play
	// screen and the events channel answer from, so a refusal reads the same
	// whichever of them a participant meets it on.
	if queryproxyErrors.answer(w, r, h.log, err) || queryrunnerErrors.answer(w, r, h.log, err) {
		return
	}

	// Ours failing is not the query being wrong. A query service that cannot
	// be reached answered as `400 invalid_request` tells the client to stop
	// retrying and the participant to fix a query that was fine.
	if errors.Is(err, rpc.ErrUnreachable) {
		h.log.ErrorContext(r.Context(), "the query service could not be reached", "error", err)
		httpx.Error(w, r, http.StatusServiceUnavailable, codeQueryServiceDown, "The query service is unavailable")
		return
	}

	// The database refusing the query on its own terms — a missing table, a
	// type error — is the one failure whose own words go out. They are the
	// useful ones: "relation \"guests\" does not exist" is the sentence that
	// says what to change. (A contest that hides its schema never gets here:
	// queryproxy turns this into ErrDatabaseDeclined above, because there the
	// same sentence is a way to enumerate the schema.)
	//
	// Under a code of its own and with the words in `subject`, which is where
	// the console reads a query's specifics. Sent as invalid_request, the
	// console printed its sentence for a malformed form instead — with a
	// support reference under it, as though the refusal were a fault.
	var database *queryrunner.DatabaseError
	if errors.As(err, &database) {
		httpx.ErrorWithDetails(w, r, http.StatusBadRequest, codeQueryDatabaseError, database.Error(),
			map[string]any{"subject": database.Error()})
		return
	}

	// And everything else is ours. Written as "only a named database error
	// speaks" rather than as "what is left must be the database", because the
	// two differ precisely on the error nobody anticipated — and that one used
	// to leave here as a 400 carrying the game cluster's address, its role
	// name and the participant's own database name, telling them to fix a
	// query that was fine. The default is now the answer that is safe to give
	// about a failure whose contents are unknown; making a new failure visible
	// to a participant takes a deliberate line above rather than an omission.
	h.log.ErrorContext(r.Context(), "a query failed for a reason that is not the database's", "error", err)
	httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
}

// refusalCodes maps the validator's vocabulary to the API's.
//
// A table rather than a switch, so that a test can walk sqlpolicy.Codes() and
// insist every one of them is here. Two of them are not a participant's
// business — a policy that does not cohere, and a mode this build does not
// support — but they are mapped anyway, because leaving a hole would mean the
// participant sees whatever the interface says about an answer it cannot read.
var refusalCodes = map[sqlpolicy.Code]httpx.Code{
	sqlpolicy.CodeParseError:            codeQueryParseError,
	sqlpolicy.CodeNotOneStatement:       codeQueryNotOneStatement,
	sqlpolicy.CodeStatementNotSupported: codeQueryStatementNotSupported,
	sqlpolicy.CodeConstructNotSupported: codeQueryConstructNotSupported,
	sqlpolicy.CodeFunctionNotSupported:  codeQueryFunctionNotSupported,
	sqlpolicy.CodeArgumentNotBounded:    codeQueryArgumentNotBounded,
	sqlpolicy.CodeCatalogNotReadable:    codeQueryCatalogNotReadable,
	sqlpolicy.CodeCatalogNotAllowed:     codeQueryCatalogNotAllowed,
	sqlpolicy.CodeTooDeep:               codeQueryTooDeep,
	sqlpolicy.CodeTooLong:               codeQueryTooLong,
	sqlpolicy.CodeTableNotWritable:      codeQueryTableNotWritable,
	sqlpolicy.CodeNotPermitted:          codeQueryNotPermitted,
	sqlpolicy.CodeInvalidPolicy:         codeQueryNotPermitted,
	sqlpolicy.CodeModeNotSupported:      codeQueryNotPermitted,
}

// HasRefusalCode reports whether a refusal has a code of its own.
//
// Exported for the test that walks sqlpolicy.Codes(): the two lists have to
// agree, and the only way to know is to ask.
func HasRefusalCode(code sqlpolicy.Code) bool {
	_, known := refusalCodes[code]
	return known
}
