package api

// Bulk account operations.
//
// Each answers 200 with an account of what happened, never a failure because
// one identifier in the selection was unusable: the rest of the selection
// applied, and an error would misreport that. Only a refusal of the request as
// a whole — an unbounded selection, a missing reason, a status nothing has —
// is a 4xx, and that refusal is already decided in the domain (users.bulk.go);
// this file only carries it to the wire.

import (
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// bulkStatusRequest is a selection and the status to move it to.
type bulkStatusRequest struct {
	IDs    []uuid.UUID `json:"ids"`
	Status string      `json:"status"`
	Reason string      `json:"reason"`
}

// bulkResponse is what a status or role operation did.
type bulkResponse struct {
	Changed []uuid.UUID       `json:"changed"`
	Skipped []skippedResponse `json:"skipped"`
}

// skippedResponse is one account the operation declined to touch. The reason
// is a code from a closed vocabulary (users.SkipNotFound and its siblings),
// which the client renders in its own language rather than showing verbatim.
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

// bulkRolesRequest is a selection and the role set to give every account in
// it. Every account in the selection gets exactly this set, replacing
// whatever it held — the same replace semantics as the single-account
// PUT /roles.
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

// bulkPasswordResetRequest is only ever the selection: there is nothing else
// to say about a password reset.
type bulkPasswordResetRequest struct {
	IDs []uuid.UUID `json:"ids"`
}

// issuedResponse is one account's new one-time password. Shown once, here,
// exactly like the single-account reset and the account import: it is never
// stored in clear and cannot be fetched later, and it is never logged.
type issuedResponse struct {
	ID              uuid.UUID `json:"id"`
	Login           string    `json:"login"`
	OneTimePassword string    `json:"one_time_password"`
}

// bulkPasswordResetResponse reports the passwords issued and the accounts
// skipped.
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
