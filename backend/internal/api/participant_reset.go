package api

import (
	"context"
	"net/http"

	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/queryproxy"
	"github.com/go-chi/chi/v5"
)

// DatabaseReset is the slice of queryproxy.Service the reset route needs.
type DatabaseReset interface {
	Reset(ctx context.Context, cmd queryproxy.ResetCommand) error
}

// WithDatabaseReset serves the participant's reset from resets; without it
// the route is not mounted, as when no game cluster or Query Runner is
// deployed and there is no copy to remake.
func (h *ParticipantHandler) WithDatabaseReset(resets DatabaseReset) *ParticipantHandler {
	h.resets = resets
	return h
}

func (h *ParticipantHandler) mountReset(r chi.Router) {
	if h.resets == nil {
		return
	}
	r.Post("/contests/{"+contestIDParam+"}/play/reset", h.resetDatabase)
}

// resetDatabase gives the participant their database back as the contest
// started it, and answers 204.
//
// Reset admits the request itself, through the same module, on the console's
// account rate and the registration's resets; the contest identifier goes to
// it unparsed, so a malformed one is charged like any other press.
func (h *ParticipantHandler) resetDatabase(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	err := h.resets.Reset(r.Context(), queryproxy.ResetCommand{
		ContestID: chi.URLParam(r, contestIDParam),
		UserID:    identity.UserID,
		Address:   clientAddress(r),
		Session:   sessionTag(h.mw, r),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
