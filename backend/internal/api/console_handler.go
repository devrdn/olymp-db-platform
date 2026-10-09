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

// Mount registers the route. Authentication only: taking part is a registration
// the façade looks up, not a permission.
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
	// ColumnTypes names each column's type (`text`, `timestamp with time zone`)
	// in the schema panel's vocabulary. It sits beside `columns` instead of
	// turning it into objects, so existing readers of `columns` keep working.
	// Invariant: either empty or exactly as long as `columns`; an entry is
	// empty for a type the runner could not name.
	ColumnTypes []string `json:"column_types"`
	Rows        [][]any  `json:"rows"`
	// Truncated says the answer is longer than what is here, so a cut result is
	// never presented as complete.
	Truncated    bool  `json:"truncated"`
	RowsAffected int64 `json:"rows_affected"`
	// DurationMicros is how long the statement alone took, not the connection,
	// queue or request. Microseconds because the client rounds to milliseconds
	// and a value rounded here would show every quick query as zero. Zero means
	// the runner did not report one.
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
		// From the one place allowed to read a forwarded header, so a
		// network-restricted contest stays on its network.
		Address: clientAddress(r),
		// The technical log's identifier, so a participant's report can be
		// traced.
		RequestID: requestUUID(r.Context()),
		// For the tracker of parallel sessions.
		Session:   sessionTag(r),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// Never nil, so the client draws [] rather than handling null.
	answer := runQueryResponse{
		Columns:        result.Columns,
		ColumnTypes:    result.ColumnTypes,
		Rows:           result.Rows,
		Truncated:      result.Truncated,
		RowsAffected:   result.RowsAffected,
		DurationMicros: result.Duration.Microseconds(),
	}
	answer.Columns = emptyIfNil(answer.Columns)
	answer.ColumnTypes = emptyIfNil(answer.ColumnTypes)
	answer.Rows = emptyIfNil(answer.Rows)
	httpx.JSON(w, r, http.StatusOK, answer)
}

// clientAddress is where the request came from. An unparseable address yields
// the zero value, which fails every network restriction: refusing is the safe
// direction.
func clientAddress(r *http.Request) netip.Addr {
	addr, err := netip.ParseAddr(httpx.ClientIP(r))
	if err != nil {
		return netip.Addr{}
	}
	return addr
}

// sessionTag names the request's session for the monitoring trail by a hash of
// the token (monitor.SessionTag), so the token never leaves the authentication
// layer.
func sessionTag(r *http.Request) string {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return ""
	}
	return monitor.SessionTag(cookie.Value)
}

// requestUUID is the request identifier as the journal's uuid column needs it.
// If the log's identifier is not a UUID, a fresh one loses the correlation but
// never fails the query.
func requestUUID(ctx context.Context) uuid.UUID {
	if id, err := uuid.Parse(logging.RequestIDFrom(ctx)); err == nil {
		return id
	}
	return uuid.New()
}

// fail turns an error into a status, a code and, where there is one, the
// subject that was refused. Every refusal has its own code because the
// interface picks its sentence by code; the subject travels separately so the
// message is not fixed to one language.
func (h *ConsoleHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var refusal *sqlpolicy.Refusal
	if errors.As(err, &refusal) {
		code, known := refusalCodes[refusal.Code]
		if !known {
			// A refusal with no code of ours: still answered as a refusal,
			// since the query was declined, and logged, since the omission is
			// ours.
			h.log.ErrorContext(r.Context(), "a refusal with no code of its own",
				"refusal", refusal.Code, "subject", refusal.Subject)
			code = codeQueryStatementNotSupported
		}
		details := map[string]any{"subject": refusal.Subject}
		// Only a parse error has a position. Omitted rather than zero, because
		// zero is a valid offset.
		if refusal.Position > 0 {
			details["position"] = refusal.Position
		}
		httpx.ErrorWithDetails(w, r, http.StatusBadRequest, code, refusal.Error(), details)
		return
	}

	// Admission and runner outcomes, from the same tables the play screen and
	// events channel use. Checked before rpc.ErrUnreachable; no error currently
	// carries both.
	if queryproxyErrors.answer(w, r, h.log, err) || queryrunnerErrors.answer(w, r, h.log, err) {
		return
	}

	// The query service being unreachable is our failure, not a bad query: 503,
	// so the client may retry.
	if errors.Is(err, rpc.ErrUnreachable) {
		h.log.ErrorContext(r.Context(), "the query service could not be reached", "error", err)
		httpx.Error(w, r, http.StatusServiceUnavailable, codeQueryServiceDown, "The query service is unavailable")
		return
	}

	// The database refusing the query (a missing table, a type error) is the
	// one failure whose own words go out, under its own code with the words in
	// `subject`. A contest that hides its schema never gets here: queryproxy
	// turns this into ErrDatabaseDeclined, since there the message would reveal
	// the schema.
	var database *queryrunner.DatabaseError
	if errors.As(err, &database) {
		httpx.ErrorWithDetails(w, r, http.StatusBadRequest, codeQueryDatabaseError, database.Error(),
			map[string]any{"subject": database.Error()})
		return
	}

	// Everything else is ours and answered as a 500. Only a named database
	// error may speak to the participant; an unanticipated error could carry
	// the game cluster's address and role.
	h.log.ErrorContext(r.Context(), "a query failed for a reason that is not the database's", "error", err)
	httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
}

// refusalCodes maps the validator's vocabulary to the API's. A table so a test
// can check every sqlpolicy code is mapped, including the two that are never
// the participant's fault.
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

// HasRefusalCode reports whether a refusal has a code of its own, for the test
// that walks sqlpolicy.Codes().
func HasRefusalCode(code sqlpolicy.Code) bool {
	_, known := refusalCodes[code]
	return known
}
