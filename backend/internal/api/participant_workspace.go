package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/workspace"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The participant's own workspace on the play screen: notes and SQL editor
// tabs, stored per registration and autosaved by the interface
// (docs/superpowers/specs/2026-09-17-play-workspace-design.md).
//
// Admitted like the rest of /play (admit, and queryproxy.Service.Access
// behind it): once the contest has ended for the participant, every route
// here answers contest_not_running or contest_finished exactly as /play/story
// does. There is no read-only mode, because the play screen itself is closed
// by then.
//
// Two differences from the other /play routes are the point of this file.
// Nothing here starts an individual participant's clock: keeping notes is not
// reading the contest. And a write does not spend the read budget admit
// charges, which the SQL console shares — autosave during continuous typing
// would otherwise take a participant's queries away from them. A write spends
// the workspace's own budget instead (workspace.Service.AdmitWrite), before
// anything is looked up.

// Workspaces is the slice of workspace.Service these endpoints need.
type Workspaces interface {
	AdmitWrite(ctx context.Context, account uuid.UUID) error
	Get(ctx context.Context, session workspace.Session) (workspace.Workspace, error)
	SaveNotes(ctx context.Context, session workspace.Session, body string) (time.Time, error)
	CreateTab(ctx context.Context, session workspace.Session, title *string) (workspace.Tab, error)
	UpdateTab(ctx context.Context, session workspace.Session, id uuid.UUID, patch workspace.TabPatch) (time.Time, error)
	DeleteTab(ctx context.Context, session workspace.Session, id uuid.UUID) error
	ReorderTabs(ctx context.Context, session workspace.Session, ids []uuid.UUID) error
}

// versionLayout formats a workspace document's updated_at. Unlike the rest of
// the API it keeps the fractional seconds: the interface compares this value
// for equality to tell whether a local draft was written against the copy
// the server still holds, and two saves within one second must not look
// like the same version.
const versionLayout = time.RFC3339Nano

// tabIDParam names the tab in the URL.
const tabIDParam = "tabId"

// WithWorkspace serves the workspace endpoints from workspaces. Without it
// they are not mounted at all.
func (h *ParticipantHandler) WithWorkspace(workspaces Workspaces) *ParticipantHandler {
	h.workspaces = workspaces
	return h
}

// mountWorkspace registers the workspace routes on an authenticated router.
// PUT .../tabs/order is a distinct static path; chi prefers it over the
// {tabId} pattern, which only takes PATCH and DELETE anyway.
func (h *ParticipantHandler) mountWorkspace(r chi.Router) {
	if h.workspaces == nil {
		return
	}
	prefix := "/contests/{" + contestIDParam + "}/play"
	r.Get(prefix+"/workspace", h.getWorkspace)
	r.Put(prefix+"/notes", h.saveNotes)
	r.Post(prefix+"/tabs", h.createTab)
	r.Put(prefix+"/tabs/order", h.reorderTabs)
	r.Patch(prefix+"/tabs/{"+tabIDParam+"}", h.updateTab)
	r.Delete(prefix+"/tabs/{"+tabIDParam+"}", h.deleteTab)
}

// workspaceSession turns an admitted request into the session the workspace
// service works in: whose workspace, and the language a server-named tab
// takes. Whether the participant may use the workspace at all was decided by
// Access, which also closes it with the contest.
func (h *ParticipantHandler) workspaceSession(r *http.Request, participant contests.Participant, contest contests.Contest) workspace.Session {
	return workspace.Session{Registration: participant.ID, Lang: h.languageFor(r, contest)}
}

// admitWorkspaceWrite spends a write of the caller's workspace budget, then
// admits the request like every other /play route except for the read
// budget, which a write does not spend (see the file's doc).
func (h *ParticipantHandler) admitWorkspaceWrite(w http.ResponseWriter, r *http.Request) (workspace.Session, bool) {
	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.workspaces.AdmitWrite(r.Context(), identity.UserID); err != nil {
		h.failWorkspace(w, r, err)
		return workspace.Session{}, false
	}

	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return workspace.Session{}, false
	}
	participant, contest, err := h.access.Access(r.Context(), contestID, identity.UserID, clientAddress(r))
	if err != nil {
		h.fail(w, r, err)
		return workspace.Session{}, false
	}
	h.observe(r, participant, contest)
	return h.workspaceSession(r, participant, contest), true
}

// tabID reads the tab named in the URL, answering the refusal itself.
func tabID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, tabIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidTabID, "Tab identifier is not valid")
		return uuid.UUID{}, false
	}
	return id, true
}

type workspaceNotesResponse struct {
	Body string `json:"body"`
	// UpdatedAt is null until the notes are first saved.
	UpdatedAt *string `json:"updated_at"`
}

type workspaceTabResponse struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Position  int    `json:"position"`
	UpdatedAt string `json:"updated_at"`
}

func toWorkspaceTabResponse(tab workspace.Tab) workspaceTabResponse {
	return workspaceTabResponse{
		ID:        tab.ID.String(),
		Title:     tab.Title,
		Body:      tab.Body,
		Position:  tab.Position,
		UpdatedAt: tab.UpdatedAt.UTC().Format(versionLayout),
	}
}

// workspaceResponse is everything the play screen restores.
type workspaceResponse struct {
	Notes workspaceNotesResponse `json:"notes"`
	Tabs  []workspaceTabResponse `json:"tabs"`
}

// updatedResponse answers a write that changed one document.
type updatedResponse struct {
	UpdatedAt string `json:"updated_at"`
}

func updated(at time.Time) updatedResponse {
	return updatedResponse{UpdatedAt: at.UTC().Format(versionLayout)}
}

// getWorkspace serves GET .../play/workspace, creating the first tab when the
// participant has none.
func (h *ParticipantHandler) getWorkspace(w http.ResponseWriter, r *http.Request) {
	participant, contest, ok := h.admit(w, r)
	if !ok {
		return
	}
	found, err := h.workspaces.Get(r.Context(), h.workspaceSession(r, participant, contest))
	if err != nil {
		h.failWorkspace(w, r, err)
		return
	}

	answer := workspaceResponse{
		Notes: workspaceNotesResponse{Body: found.Notes.Body},
		Tabs:  make([]workspaceTabResponse, 0, len(found.Tabs)),
	}
	if found.Notes.UpdatedAt != nil {
		at := found.Notes.UpdatedAt.UTC().Format(versionLayout)
		answer.Notes.UpdatedAt = &at
	}
	for _, tab := range found.Tabs {
		answer.Tabs = append(answer.Tabs, toWorkspaceTabResponse(tab))
	}
	httpx.JSON(w, r, http.StatusOK, answer)
}

// saveNotesRequest is the body of PUT .../play/notes. Body is a pointer so a
// request that forgot it is refused rather than read as "erase my notes".
type saveNotesRequest struct {
	Body *string `json:"body"`
}

func (h *ParticipantHandler) saveNotes(w http.ResponseWriter, r *http.Request) {
	session, ok := h.admitWorkspaceWrite(w, r)
	if !ok {
		return
	}
	var req saveNotesRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Body == nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, "body is required")
		return
	}
	at, err := h.workspaces.SaveNotes(r.Context(), session, *req.Body)
	if err != nil {
		h.failWorkspace(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, updated(at))
}

// createTabRequest is the body of POST .../play/tabs. Without a title the
// server names the tab.
type createTabRequest struct {
	Title *string `json:"title"`
}

func (h *ParticipantHandler) createTab(w http.ResponseWriter, r *http.Request) {
	session, ok := h.admitWorkspaceWrite(w, r)
	if !ok {
		return
	}
	var req createTabRequest
	if !decodeBody(w, r, &req) {
		return
	}
	tab, err := h.workspaces.CreateTab(r.Context(), session, req.Title)
	if err != nil {
		h.failWorkspace(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, toWorkspaceTabResponse(tab))
}

// updateTabRequest is the body of PATCH .../play/tabs/{tabId}: an absent
// field is left as it is.
type updateTabRequest struct {
	Title *string `json:"title"`
	Body  *string `json:"body"`
}

func (h *ParticipantHandler) updateTab(w http.ResponseWriter, r *http.Request) {
	session, ok := h.admitWorkspaceWrite(w, r)
	if !ok {
		return
	}
	id, ok := tabID(w, r)
	if !ok {
		return
	}
	var req updateTabRequest
	if !decodeBody(w, r, &req) {
		return
	}
	at, err := h.workspaces.UpdateTab(r.Context(), session, id, workspace.TabPatch{Title: req.Title, Body: req.Body})
	if err != nil {
		h.failWorkspace(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, updated(at))
}

func (h *ParticipantHandler) deleteTab(w http.ResponseWriter, r *http.Request) {
	session, ok := h.admitWorkspaceWrite(w, r)
	if !ok {
		return
	}
	id, ok := tabID(w, r)
	if !ok {
		return
	}
	if err := h.workspaces.DeleteTab(r.Context(), session, id); err != nil {
		h.failWorkspace(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reorderTabsRequest is the body of PUT .../play/tabs/order: every tab of the
// workspace, in the new order. Strings rather than UUIDs, so a malformed
// identifier is its own refusal rather than a decoding error.
type reorderTabsRequest struct {
	IDs []string `json:"ids"`
}

func (h *ParticipantHandler) reorderTabs(w http.ResponseWriter, r *http.Request) {
	session, ok := h.admitWorkspaceWrite(w, r)
	if !ok {
		return
	}
	var req reorderTabsRequest
	if !decodeBody(w, r, &req) {
		return
	}
	// Bounded before anything is parsed (CLAUDE.md rule 2): a list longer
	// than a workspace can be is refused as the mismatch it is.
	if len(req.IDs) > workspace.MaxTabs {
		h.failWorkspace(w, r, workspace.ErrOrderMismatch)
		return
	}
	ids := make([]uuid.UUID, 0, len(req.IDs))
	for _, raw := range req.IDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, codeInvalidTabID, "Tab identifier is not valid")
			return
		}
		ids = append(ids, id)
	}
	if err := h.workspaces.ReorderTabs(r.Context(), session, ids); err != nil {
		h.failWorkspace(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// failWorkspace maps a workspace refusal to a response (CLAUDE.md rule 1).
// Anything else is ours, and an internal error.
func (h *ParticipantHandler) failWorkspace(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, workspace.ErrTooOften):
		w.Header().Set("Retry-After", strconv.Itoa(int(workspace.RetryAfter()/time.Second)))
		httpx.Error(w, r, http.StatusTooManyRequests, codeWorkspaceTooOften,
			"Too many workspace saves this minute; wait before saving again")
	case errors.Is(err, workspace.ErrTooManyTabs):
		httpx.Error(w, r, http.StatusConflict, codeWorkspaceTabLimit, err.Error())
	case errors.Is(err, workspace.ErrLastTab):
		httpx.Error(w, r, http.StatusConflict, codeWorkspaceLastTab, err.Error())
	case errors.Is(err, workspace.ErrTabNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeWorkspaceTabNotFound, err.Error())
	case errors.Is(err, workspace.ErrNotesTooLong):
		httpx.Error(w, r, http.StatusBadRequest, codeWorkspaceNotesTooLong, err.Error())
	case errors.Is(err, workspace.ErrTabBodyTooLong):
		httpx.Error(w, r, http.StatusBadRequest, codeWorkspaceTabTooLong, err.Error())
	case errors.Is(err, workspace.ErrTitleInvalid):
		httpx.Error(w, r, http.StatusBadRequest, codeWorkspaceTitleInvalid, err.Error())
	case errors.Is(err, workspace.ErrTextInvalid):
		httpx.Error(w, r, http.StatusBadRequest, codeWorkspaceTextInvalid, err.Error())
	case errors.Is(err, workspace.ErrOrderMismatch):
		httpx.Error(w, r, http.StatusBadRequest, codeWorkspaceOrderMismatch, err.Error())
	case errors.Is(err, workspace.ErrNothingToChange):
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
	default:
		h.log.ErrorContext(r.Context(), "the participant's workspace could not be served", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}
