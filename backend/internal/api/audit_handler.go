package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// AuditHandler serves the trail of who did what.
//
// Behind audit.view, and behind nothing else: the trail names who blocked
// whom, from which address, and when. It is the record that answers "why did
// this participant lose access" months later, which is exactly why reading it
// is a privilege rather than a side effect of being signed in.
//
// Read-only by construction — there is no route here that writes. The trail is
// append-only, and entries arrive in the same transaction as the action they
// describe, never through HTTP.
type AuditHandler struct {
	trail audit.Reader
	mw    *auth.Middleware
	log   *slog.Logger
}

// NewAuditHandler assembles the audit endpoint.
func NewAuditHandler(trail audit.Reader, mw *auth.Middleware, log *slog.Logger) *AuditHandler {
	return &AuditHandler{trail: trail, mw: mw, log: log}
}

// Mount registers the route under /audit, and the action catalogue beside it.
func (h *AuditHandler) Mount(r chi.Router) {
	r.Route("/audit", func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequirePermission(rbac.PermissionAuditView))
		r.Get("/", h.list)
		// A sibling of the trail rather than a value folded into it: it
		// describes what this installation can record, not a page of what it
		// has recorded, so the filter can offer the whole vocabulary before a
		// single matching entry is on screen. Same permission as the trail
		// itself — the roles catalogue beside /users is the same idea.
		r.Get("/actions", h.listActions)
	})
}

// AuditEntryResponse is one line of the trail.
type AuditEntryResponse struct {
	ID      int64  `json:"id"`
	ActorID string `json:"actor_id,omitempty"`
	// ActorLogin is empty for a system event, and for an account deleted since:
	// the trail outlives the people in it.
	ActorLogin string `json:"actor_login,omitempty"`
	Action     string `json:"action"`
	Entity     string `json:"entity,omitempty"`
	EntityID   string `json:"entity_id,omitempty"`
	// EntityLabel names the thing acted upon while it still exists. Absent
	// once it is gone; the identifier stays either way.
	EntityLabel string         `json:"entity_label,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	IP          string         `json:"ip,omitempty"`
	UserAgent   string         `json:"user_agent,omitempty"`
	CreatedAt   string         `json:"created_at"`
}

type auditListResponse struct {
	Items []AuditEntryResponse `json:"items"`
	Total int                  `json:"total"`
}

type auditActionsResponse struct {
	Items []string `json:"items"`
}

// listActions publishes the vocabulary the trail can be filtered by.
//
// Read from audit.Actions() rather than built from the rows on the current
// page: a filter offering only what is already on screen can never find a
// deletion, say, until one happens to be in view — which is the defect this
// endpoint exists to close.
func (h *AuditHandler) listActions(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, r, http.StatusOK, auditActionsResponse{Items: audit.Actions()})
}

func (h *AuditHandler) list(w http.ResponseWriter, r *http.Request) {
	filter, err := auditFilter(r)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	found, total, err := h.trail.List(r.Context(), filter.Normalize())
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not read the audit trail", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}

	items := make([]AuditEntryResponse, 0, len(found))
	for _, record := range found {
		entry := AuditEntryResponse{
			ID:          record.ID,
			ActorLogin:  record.ActorLogin,
			Action:      record.Action,
			Entity:      record.Entity,
			EntityID:    record.EntityID,
			EntityLabel: record.EntityLabel,
			Payload:     record.Payload,
			IP:          record.IP,
			UserAgent:   record.UserAgent,
			CreatedAt:   record.CreatedAt.UTC().Format(timeLayout),
		}
		if record.ActorID != nil {
			entry.ActorID = record.ActorID.String()
		}
		items = append(items, entry)
	}
	httpx.JSON(w, r, http.StatusOK, auditListResponse{Items: items, Total: total})
}

// auditFilter reads the query into a filter.
//
// A malformed value is refused rather than dropped: silently ignoring an
// unparseable actor would answer a question about one person with the whole
// trail, which is both wrong and the opposite of what was asked.
func auditFilter(r *http.Request) (audit.Filter, error) {
	query := r.URL.Query()

	filter := audit.Filter{
		Action:   query.Get("action"),
		Entity:   query.Get("entity"),
		EntityID: query.Get("entity_id"),
		Limit:    intParam(r, "limit"),
		Offset:   intParam(r, "offset"),
	}

	if raw := query.Get("actor"); raw != "" {
		actor, err := uuid.Parse(raw)
		if err != nil {
			return audit.Filter{}, errInvalidActor
		}
		filter.Actor = actor
	}

	// A code that names no action would otherwise return an empty page —
	// indistinguishable from a real search that matched nothing — for what is
	// usually a typo in the address bar. Refused instead, the same way an
	// unparseable actor is.
	if filter.Action != "" && !audit.IsAction(filter.Action) {
		return audit.Filter{}, errInvalidAction
	}

	var err error
	if filter.From, err = auditTime(query.Get("from")); err != nil {
		return audit.Filter{}, err
	}
	if filter.To, err = auditTime(query.Get("to")); err != nil {
		return audit.Filter{}, err
	}
	return filter, nil
}

func auditTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, errInvalidWindow
	}
	utc := parsed.UTC()
	return &utc, nil
}

// The ways a query can be malformed, named so the message the client sees is
// written once.
var (
	errInvalidActor = errors.New("actor must be a UUID")
	// errInvalidAction is a filter naming a code audit.IsAction does not
	// recognise. The same sentinel a service would return, here because the
	// check itself is a query-parameter validation the handler owns rather
	// than a rule any service enforces.
	errInvalidAction = errors.New("action is not one this installation records")
	errInvalidWindow = errors.New("from and to must be RFC 3339 timestamps")
)
