package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/registrations"
)

// The public form's budget, refusals included (CLAUDE.md rule 13).
const (
	// RegistrationsPerAddress is per address in registrationAddressWindow;
	// guests register at the venue behind one Wi-Fi address, so it is generous.
	RegistrationsPerAddress   = 120
	registrationAddressWindow = 10 * time.Minute
	// RegistrationsPerEmail is per lowercased email in an hour, so the form
	// cannot be used to flood one inbox from many addresses.
	RegistrationsPerEmail   = 5
	registrationEmailWindow = time.Hour
)

const eventRegistrationIDParam = "registrationID"

// RegistrationsHandler serves the event's public registration form and the
// organizers' list with its arrival mark.
type RegistrationsHandler struct {
	service  *registrations.Service
	limiter  *auth.Limiter
	mw       *auth.Middleware
	log      *slog.Logger
	formOpen bool
}

func NewRegistrationsHandler(service *registrations.Service, limiter *auth.Limiter, mw *auth.Middleware, log *slog.Logger) *RegistrationsHandler {
	return &RegistrationsHandler{service: service, limiter: limiter, mw: mw, log: log}
}

// WithPublicForm mounts the public form; without it only the organizers'
// routes exist.
func (h *RegistrationsHandler) WithPublicForm() *RegistrationsHandler {
	h.formOpen = true
	return h
}

// Mount registers the form, when open, under /public/, which needs no
// session, and the list under /registrations for whoever manages accounts.
func (h *RegistrationsHandler) Mount(r chi.Router) {
	if h.formOpen {
		r.Post("/public/registrations", h.register)
	}
	r.Route("/registrations", func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequirePermission(rbac.PermissionUsersManage))
		r.Get("/", h.list)
		r.Put("/{"+eventRegistrationIDParam+"}/check-in", h.checkIn)
	})
}

type registerRequest struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Email     string `json:"email"`
}

type registerCreatedResponse struct {
	Status    string `json:"status"`
	Code      string `json:"code"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Email     string `json:"email"`
	Account   string `json:"account"`
	// Login and OneTimePassword are absent when the account already existed.
	Login           string `json:"login,omitempty"`
	OneTimePassword string `json:"one_time_password,omitempty"`
	EmailSent       bool   `json:"email_sent"`
}

// registerExistingResponse carries no code and no name: whoever knows an
// address must not read its code off the page.
type registerExistingResponse struct {
	Status    string `json:"status"`
	Email     string `json:"email"`
	EmailSent bool   `json:"email_sent"`
}

func (h *RegistrationsHandler) register(w http.ResponseWriter, r *http.Request) {
	// The address before the body is even read, and the email, an unbounded
	// key, only for a caller the address admitted (CLAUDE.md rule 5).
	if !h.admit(w, r, "registration:ip:"+addressKey(r), RegistrationsPerAddress, registrationAddressWindow) {
		return
	}
	var req registerRequest
	if !decodeBody(w, r, &req) {
		return
	}
	applicant, err := registrations.Parse(req.FirstName, req.LastName, req.Email)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.admit(w, r, "registration:email:"+applicant.EmailKey, RegistrationsPerEmail, registrationEmailWindow) {
		return
	}

	out, err := h.service.Register(r.Context(), applicant)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	noIndex(w)
	if out.Status == registrations.StatusExisting {
		httpx.JSON(w, r, http.StatusOK, registerExistingResponse{
			Status: string(out.Status), Email: applicant.Email, EmailSent: out.EmailSent,
		})
		return
	}
	resp := registerCreatedResponse{
		Status: string(out.Status), Code: out.Registration.Code,
		FirstName: out.Registration.FirstName, LastName: out.Registration.LastName,
		Email: out.Registration.Email, Account: "existing", EmailSent: out.EmailSent,
	}
	if !out.Account.Existing {
		resp.Account = "created"
		resp.Login = out.Account.Login
		resp.OneTimePassword = out.Account.OneTimePassword
	}
	httpx.JSON(w, r, http.StatusCreated, resp)
}

type registrationResponse struct {
	ID          string  `json:"id"`
	FirstName   string  `json:"first_name"`
	LastName    string  `json:"last_name"`
	Email       string  `json:"email"`
	Code        string  `json:"code"`
	UserID      *string `json:"user_id"`
	CreatedAt   string  `json:"created_at"`
	CheckedInAt *string `json:"checked_in_at"`
}

func toRegistrationResponse(reg registrations.Registration) registrationResponse {
	out := registrationResponse{
		ID: reg.ID.String(), FirstName: reg.FirstName, LastName: reg.LastName,
		Email: reg.Email, Code: reg.Code, CreatedAt: reg.CreatedAt.UTC().Format(time.RFC3339),
	}
	if reg.UserID != nil {
		id := reg.UserID.String()
		out.UserID = &id
	}
	if reg.CheckedInAt != nil {
		at := reg.CheckedInAt.UTC().Format(time.RFC3339)
		out.CheckedInAt = &at
	}
	return out
}

type registrationListResponse struct {
	Registrations []registrationResponse `json:"registrations"`
	Total         int                    `json:"total"`
	CheckedIn     int                    `json:"checked_in"`
}

func (h *RegistrationsHandler) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.service.List(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := registrationListResponse{Registrations: make([]registrationResponse, 0, len(list)), Total: len(list)}
	for _, reg := range list {
		if reg.CheckedInAt != nil {
			out.CheckedIn++
		}
		out.Registrations = append(out.Registrations, toRegistrationResponse(reg))
	}
	httpx.JSON(w, r, http.StatusOK, out)
}

type checkInRequest struct {
	CheckedIn *bool `json:"checked_in"`
}

func (h *RegistrationsHandler) checkIn(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, eventRegistrationIDParam))
	if err != nil {
		h.fail(w, r, registrations.ErrNotFound)
		return
	}
	var req checkInRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.CheckedIn == nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, "checked_in is required")
		return
	}
	updated, err := h.service.SetCheckedIn(r.Context(), id, *req.CheckedIn)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toRegistrationResponse(updated))
}

func (h *RegistrationsHandler) admit(w http.ResponseWriter, r *http.Request, subject string, limit int, window time.Duration) bool {
	allowed, err := h.limiter.Allow(r.Context(), subject, limit, window)
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not check the registration rate limit", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return false
	}
	if !allowed {
		httpx.Error(w, r, http.StatusTooManyRequests, codeTooManyAttempts,
			"Too many registrations from this address or for this email; wait before trying again")
		return false
	}
	return true
}

func (h *RegistrationsHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if registrationsErrors.answer(w, r, h.log, err) {
		return
	}
	// The account's password found no hashing slot in the sign-in wait.
	if errors.Is(err, password.ErrBusy) {
		busy(w, r)
		return
	}
	h.log.ErrorContext(r.Context(), "the event registration request failed", "error", err)
	httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
}
