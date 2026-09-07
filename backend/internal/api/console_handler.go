package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/devrdn/db-contest/backend/internal/auth"
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
	Rows    [][]any  `json:"rows"`
	// Truncated says the answer is longer than what is here. A flag rather
	// than a silent cut: nine hundred rows of nine thousand, unannounced, is a
	// wrong answer rather than a short one.
	Truncated    bool  `json:"truncated"`
	RowsAffected int64 `json:"rows_affected"`
}

func (h *ConsoleHandler) run(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())

	contestID, err := uuid.Parse(chi.URLParam(r, "contestID"))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, "The contest identifier is not a UUID")
		return
	}

	var req runQueryRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
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
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// Never nil in the body: a client that has to distinguish `null` from `[]`
	// before it can draw a table is a client with a bug waiting.
	answer := runQueryResponse{Columns: result.Columns, Rows: result.Rows, Truncated: result.Truncated, RowsAffected: result.RowsAffected}
	if answer.Columns == nil {
		answer.Columns = []string{}
	}
	if answer.Rows == nil {
		answer.Rows = [][]any{}
	}
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

	for _, mapping := range []struct {
		is     error
		status int
		code   httpx.Code
	}{
		{queryproxy.ErrNotAParticipant, http.StatusForbidden, codeNotAParticipant},
		{queryproxy.ErrContestNotRunning, http.StatusConflict, codeContestNotRunning},
		{queryproxy.ErrFinished, http.StatusConflict, codeContestFinished},
		// 409 rather than 403, for the same reason codeQuestionClosed is one:
		// this is a fact about where the contest currently stands for this
		// participant, not a permission they lack, and it stops being true
		// the moment the contest gives them something to answer again.
		{queryproxy.ErrNothingLeftToAnswer, http.StatusConflict, codeNothingLeftToAnswer},
		{queryproxy.ErrAddressNotAllowed, http.StatusForbidden, codeAddressNotAllowed},
		{queryproxy.ErrNoGameYet, http.StatusConflict, codeNoGameYet},
		{queryproxy.ErrDatabaseDeclined, http.StatusBadRequest, codeQueryDeclined},
		{queryrunner.ErrTimeout, http.StatusGatewayTimeout, codeQueryTimedOut},
		{queryrunner.ErrCanceled, http.StatusRequestTimeout, codeQueryCancelled},
		{queryrunner.ErrBusy, http.StatusServiceUnavailable, codeQueryBusy},
		{queryrunner.ErrAlreadyRunning, http.StatusConflict, codeQueryAlreadyRunning},
		{queryrunner.ErrTooManyQueries, http.StatusTooManyRequests, codeQueryTooOften},
		{queryrunner.ErrDiskFull, http.StatusConflict, codeQueryDiskFull},
		{queryrunner.ErrResultTooLarge, http.StatusBadRequest, codeQueryResultTooLarge},
	} {
		if errors.Is(err, mapping.is) {
			httpx.Error(w, r, mapping.status, mapping.code, err.Error())
			return
		}
	}

	// Ours failing is not the query being wrong. A database that cannot be
	// reached answered as `400 invalid_request` tells the client to stop
	// retrying and the participant to fix a query that was fine — with our
	// connection string attached to the explanation.
	switch {
	case errors.Is(err, rpc.ErrUnreachable):
		h.log.ErrorContext(r.Context(), "the query service could not be reached", "error", err)
		httpx.Error(w, r, http.StatusServiceUnavailable, codeQueryServiceDown, "The query service is unavailable")
		return
	case errors.Is(err, queryproxy.ErrUnavailable):
		h.log.ErrorContext(r.Context(), "a query could not be answered", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	case errors.Is(err, queryrunner.ErrJournalUnavailable):
		// The query never reached the database: opening its journal row
		// failed first. That is ours, not the participant's SQL being wrong,
		// so it gets the same treatment as the query service being down
		// rather than the database's own words below — which are shown only
		// for a query that actually reached the database.
		h.log.ErrorContext(r.Context(), "a query could not be journalled", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}

	// What is left is the database refusing the query on its own terms — a
	// missing table, a type error. The participant is shown its words, which
	// are the useful ones.
	httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
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
