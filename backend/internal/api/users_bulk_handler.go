package api

// Bulk account operations. Each answers 200 with what happened, even when some
// selected accounts were skipped, because the rest did apply. Only a refusal of
// the whole request is a 4xx, decided in the domain (users/bulk.go).

import (
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

type bulkStatusRequest struct {
	IDs    []uuid.UUID `json:"ids"`
	Status string      `json:"status"`
	Reason string      `json:"reason"`
}

type bulkResponse struct {
	Changed []uuid.UUID       `json:"changed"`
	Skipped []skippedResponse `json:"skipped"`
}

// skippedResponse is one account the operation declined to touch. Reason is a
// code from a closed set (users.SkipNotFound and siblings) the client
// translates.
type skippedResponse struct {
	ID     uuid.UUID `json:"id"`
	Login  string    `json:"login"`
	Reason string    `json:"reason"`
}

func asBulkResponse(res users.BulkResult) bulkResponse {
	changed := emptyIfNil(res.Changed)
	skipped := make([]skippedResponse, 0, len(res.Skipped))
	for _, s := range res.Skipped {
		skipped = append(skipped, skippedResponse{ID: s.ID, Login: s.Login, Reason: s.Reason})
	}
	return bulkResponse{Changed: changed, Skipped: skipped}
}

func (h *UsersHandler) bulkStatus(w http.ResponseWriter, r *http.Request) {
	var req bulkStatusRequest
	if !decodeBody(w, r, &req) {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())

	res, err := h.service.BulkSetStatus(r.Context(), identity.UserID, req.IDs, req.Status, req.Reason)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, asBulkResponse(res))
}

// bulkRolesRequest gives every selected account exactly this role set,
// replacing what it held, as PUT /roles does for one.
type bulkRolesRequest struct {
	IDs   []uuid.UUID `json:"ids"`
	Roles []string    `json:"roles"`
}

func (h *UsersHandler) bulkRoles(w http.ResponseWriter, r *http.Request) {
	var req bulkRolesRequest
	if !decodeBody(w, r, &req) {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())

	res, err := h.service.BulkReplaceRoles(r.Context(), identity.UserID, req.IDs, req.Roles)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, asBulkResponse(res))
}

type bulkPasswordResetRequest struct {
	IDs []uuid.UUID `json:"ids"`
}

// issuedResponse is one account's new one-time password, shown only here: never
// stored in clear, never logged.
type issuedResponse struct {
	ID              uuid.UUID `json:"id"`
	Login           string    `json:"login"`
	OneTimePassword string    `json:"one_time_password"`
}

type bulkPasswordResetResponse struct {
	Issued  []issuedResponse  `json:"issued"`
	Skipped []skippedResponse `json:"skipped"`
}

func (h *UsersHandler) bulkResetPassword(w http.ResponseWriter, r *http.Request) {
	var req bulkPasswordResetRequest
	if !decodeBody(w, r, &req) {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())

	res, err := h.service.BulkResetPassword(r.Context(), identity.UserID, req.IDs)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	issued := make([]issuedResponse, 0, len(res.Issued))
	for _, one := range res.Issued {
		issued = append(issued, issuedResponse{ID: one.ID, Login: one.Login, OneTimePassword: one.OneTimePassword})
	}
	skipped := make([]skippedResponse, 0, len(res.Skipped))
	for _, s := range res.Skipped {
		skipped = append(skipped, skippedResponse{ID: s.ID, Login: s.Login, Reason: s.Reason})
	}
	httpx.JSON(w, r, http.StatusOK, bulkPasswordResetResponse{Issued: issued, Skipped: skipped})
}
