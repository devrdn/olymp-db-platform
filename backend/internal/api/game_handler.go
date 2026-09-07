package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Games is the slice of provisioning.Games this handler needs.
type Games interface {
	Of(ctx context.Context, contestID uuid.UUID) (provisioning.Template, error)
	SetScript(ctx context.Context, actorID, contestID uuid.UUID, script string) (provisioning.Template, error)
}

// GameHandler serves a contest's game database: the SQL it is built from, and
// how the build went.
//
// Mounted only where a game cluster is configured, the same way the console
// is: without one there is nothing to build a template on, and an endpoint
// that accepted a script it could never build would be a worse answer than
// no endpoint.
type GameHandler struct {
	games Games
	mw    *auth.Middleware
	log   *slog.Logger
}

// NewGameHandler assembles the endpoints.
func NewGameHandler(games Games, mw *auth.Middleware, log *slog.Logger) *GameHandler {
	return &GameHandler{games: games, mw: mw, log: log}
}

// Mount registers the routes.
//
// Reading the game needs the same permission as reading the contest; writing
// its script needs the one that edits content, because that is what the
// script is — the game is as much a part of the contest as the story, and it
// sits behind the same ContentEditable gate.
//
// The status and the script are separate reads on purpose. The status is
// polled while a build runs; the script is up to half a mebibyte and is
// fetched once, when somebody opens it to edit.
func (h *GameHandler) Mount(r chi.Router) {
	r.Route("/contests/{"+contestIDParam+"}/game", func(r chi.Router) {
		r.Use(h.mw.Authenticate)

		r.With(h.mw.RequireContestPermission(rbac.PermissionContestView)).Get("/", h.status)
		r.With(h.mw.RequireContestPermission(rbac.PermissionContestView)).Get("/script", h.script)
		r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).Put("/script", h.setScript)
	})
}

type gameResponse struct {
	Status  string `json:"status"`
	Version int    `json:"version"`
	// Database is the template every participant's copy is made from. Shown
	// to staff so a name in a cluster listing can be traced back to a
	// contest; it is never sent to a participant.
	Database string `json:"database"`
	// BuildError is PostgreSQL's own words when the build failed, empty
	// otherwise. Whoever wrote the script is the person who has to fix it.
	BuildError string `json:"build_error"`
	// ScriptBytes lets the status say whether there is a script at all
	// without carrying it.
	ScriptBytes int       `json:"script_bytes"`
	Building    bool      `json:"building"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type gameScriptResponse struct {
	Script string `json:"script"`
}

type setGameScriptRequest struct {
	Script string `json:"script"`
}

func (h *GameHandler) status(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}

	template, err := h.games.Of(r.Context(), contestID)
	if errors.Is(err, provisioning.ErrNoGame) {
		// Not an error: every contest is in this state until somebody writes
		// its game. Answered as a game with no script rather than a 404, so
		// the interface has one shape to render instead of two.
		httpx.JSON(w, r, http.StatusOK, gameResponse{Status: "absent"})
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, gameResponse{
		Status: string(template.Status), Version: template.Version,
		Database: template.Database, BuildError: template.BuildError,
		ScriptBytes: len(template.Script), Building: template.Building(),
		UpdatedAt: template.UpdatedAt,
	})
}

func (h *GameHandler) script(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}

	template, err := h.games.Of(r.Context(), contestID)
	if errors.Is(err, provisioning.ErrNoGame) {
		httpx.JSON(w, r, http.StatusOK, gameScriptResponse{Script: ""})
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, gameScriptResponse{Script: template.Script})
}

func (h *GameHandler) setScript(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}

	var req setGameScriptRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	template, err := h.games.SetScript(r.Context(), identity.UserID, contestID, req.Script)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// 202, not 204: the script is stored and the build has not happened. The
	// status this returns is `pending`, and the interface watches it.
	httpx.JSON(w, r, http.StatusAccepted, gameResponse{
		Status: string(template.Status), Version: template.Version,
		Database: template.Database, ScriptBytes: len(template.Script),
		Building: template.Building(), UpdatedAt: template.UpdatedAt,
	})
}

func (h *GameHandler) contestID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, "The contest identifier is not a UUID")
		return uuid.Nil, false
	}
	return id, true
}

// fail names every refusal, so the interface can say which one happened
// rather than "internal error" (CLAUDE.md rule 1).
func (h *GameHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, provisioning.ErrScriptEmpty):
		httpx.Error(w, r, http.StatusBadRequest, codeGameScriptEmpty, "The game script is empty")
	case errors.Is(err, provisioning.ErrScriptTooLong):
		httpx.Error(w, r, http.StatusBadRequest, codeGameScriptTooLong, err.Error())
	case errors.Is(err, provisioning.ErrGameNotEditable):
		httpx.Error(w, r, http.StatusConflict, codeGameNotEditable,
			"The game cannot be replaced once the contest is running")
	default:
		h.log.ErrorContext(r.Context(), "a game request failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}
