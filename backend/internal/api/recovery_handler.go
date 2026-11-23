package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/httpx"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/password"
	"github.com/devrdn/olymp-db-platform/backend/internal/recovery"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
)

// The recovery endpoints' budgets, refusals included (CLAUDE.md rule 13).
const (
	// RecoveriesPerAddress is per address in recoveryAddressWindow, for each
	// endpoint on its own.
	RecoveriesPerAddress  = 30
	recoveryAddressWindow = 10 * time.Minute
	// RecoveriesPerAccount is per identifier typed, lowercased, in an hour,
	// from any number of addresses. An account's login and its email are two
	// identifiers, so its inbox gets at most twice this many links an hour.
	// An identifier that names no account spends it as well.
	RecoveriesPerAccount  = 5
	recoveryAccountWindow = time.Hour
)

const tooManyRecoveries = "Too many password recovery attempts from this address or for this account; wait before trying again"

var (
	forgotsByAddress = httpx.PerAddress("password_forgot:ip", RecoveriesPerAddress, recoveryAddressWindow,
		codeTooManyAttempts, tooManyRecoveries)
	forgotsByAccount = httpx.PerTypedKey("password_forgot:account", RecoveriesPerAccount, recoveryAccountWindow,
		codeTooManyAttempts, tooManyRecoveries)
	resetsByAddress = httpx.PerAddress("password_reset:ip", RecoveriesPerAddress, recoveryAddressWindow,
		codeTooManyAttempts, tooManyRecoveries)
)

// recoveryBodyBytes bounds a recovery body as it arrives: the largest field
// is a password, at most password.MaxLength bytes even escaped as \u00XX.
const recoveryBodyBytes = 8 << 10

// RecoveryHandler serves the forgotten-password flow: a request for a link
// mailed to the account, and the reset that follows it. Neither needs a
// session, and the reset does not open one.
type RecoveryHandler struct {
	recoveries *recovery.Service
	budgets    budgets
	log        *slog.Logger
}

func NewRecoveryHandler(recoveries *recovery.Service, limiter httpx.Counter, log *slog.Logger) *RecoveryHandler {
	return &RecoveryHandler{recoveries: recoveries, budgets: newBudgets(limiter, log), log: log}
}

// Mount registers the routes under /public/ only while mail is on, whatever
// SELF_SIGNUP_ENABLED says: without mail no link reaches anyone.
func (h *RecoveryHandler) Mount(r chi.Router) {
	if !h.recoveries.Offered() {
		return
	}
	r.Group(func(r chi.Router) {
		r.Use(noStore)
		r.Post("/public/password/forgot", h.forgot)
		r.Post("/public/password/reset", h.reset)
	})
}

type forgotRequest struct {
	// Login is a login or, holding an @, an email.
	Login string `json:"login"`
}

type linkResetRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// forgotResponse is the one answer every admitted request gets, given before
// anything is looked up.
type forgotResponse struct {
	Status string `json:"status"`
}

// The address is spent before the body is read, and the identifier, an
// unbounded key, only once bounded and for a caller the address admitted
// (CLAUDE.md rule 5); then the service spends the recovery mail budgets,
// alike for every identifier. The lookup, the link and the mail happen after
// the answer, so neither its time nor a failure tells whether an account was
// reached.
func (h *RecoveryHandler) forgot(w http.ResponseWriter, r *http.Request) {
	if !h.budgets.spend(w, r, forgotsByAddress.Of(r)) {
		return
	}
	var req forgotRequest
	if !decodeBodyWithin(w, r, &req, recoveryBodyBytes) {
		return
	}
	// Keyed as the service looks the account up.
	typed := strings.ToLower(strings.TrimSpace(req.Login))
	if !withinBounds(w, r, fieldBound{"login", typed, users.MaxEmailLength}) {
		return
	}
	if !h.budgets.spend(w, r, forgotsByAccount.Of(typed)) {
		return
	}

	if _, err := h.recoveries.Submit(r.Context(), recovery.RequestCommand{
		LoginOrEmail: req.Login, Lang: readerMailLanguage(r), Sender: httpx.ClientSubject(r),
	}); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusAccepted, forgotResponse{Status: "link_sent"})
}

// reset spends the address before the body is read and so before the
// password hash, which runs on whatever an unauthenticated caller sends.
func (h *RecoveryHandler) reset(w http.ResponseWriter, r *http.Request) {
	if !h.budgets.spend(w, r, resetsByAddress.Of(r)) {
		return
	}
	var req linkResetRequest
	if !decodeBodyWithin(w, r, &req, recoveryBodyBytes) {
		return
	}
	if !withinBounds(w, r,
		fieldBound{"token", req.Token, recovery.TokenLength},
		fieldBound{"password", req.Password, password.MaxLength},
	) {
		return
	}

	if err := h.recoveries.Complete(r.Context(), req.Token, req.Password); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// recoveryAnswers is what forgot and the reset answer from: the budgets'
// refusals, the link's and the new password's.
var recoveryAnswers = joined(recoveryErrors, usersErrors)

func (h *RecoveryHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if recoveryAnswers.answer(w, r, h.log, err) {
		return
	}
	// The new password found no hashing slot in the sign-in wait.
	if errors.Is(err, password.ErrBusy) {
		busy(w, r)
		return
	}
	h.log.ErrorContext(r.Context(), "the password recovery request failed", "error", err)
	httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
}
