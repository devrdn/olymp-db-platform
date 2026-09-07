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

// GameDatabases is the slice of provisioning.Service this handler needs: the
// databases that already exist for a contest, and removing one of them.
//
// Declared apart from Games rather than folded into it because they are two
// different services — Games owns the script and the template built from it,
// this one owns the pool of copies made from that template. They share a
// screen and nothing else.
type GameDatabases interface {
	Instances(ctx context.Context, contestID uuid.UUID) (provisioning.InstanceList, error)
	DropInstance(ctx context.Context, actorID, contestID uuid.UUID, database string) (provisioning.InstanceRecord, error)
}

// GameHandler serves a contest's game database: the SQL it is built from, and
// how the build went.
//
// Mounted only where a game cluster is configured, the same way the console
// is: without one there is nothing to build a template on, and an endpoint
// that accepted a script it could never build would be a worse answer than
// no endpoint.
type GameHandler struct {
	games     Games
	databases GameDatabases
	mw        *auth.Middleware
	log       *slog.Logger
}

// NewGameHandler assembles the endpoints.
func NewGameHandler(games Games, databases GameDatabases, mw *auth.Middleware, log *slog.Logger) *GameHandler {
	return &GameHandler{games: games, databases: databases, mw: mw, log: log}
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

		// The databases that already exist: the spare pool and the
		// participants' own copies. Reading them needs what reading the
		// contest needs; removing one needs what editing it needs.
		//
		// ContestEdit and not ContestManage, deliberately. ContestManage is
		// owner-only (rbac.ownerOnlyPermissions) and exists for appointing
		// staff — the one power a manager must not be able to give
		// themselves more of. Dropping a broken copy is nothing like that: it
		// is a repair, wanted in the middle of a running olympiad by whoever
		// is on duty, and that is the manager. Requiring the owner to be
		// reachable before a participant's database can be fixed would make
		// the owner's absence a competitor's problem.
		//
		// It is also not a wider door than the one already open beside it.
		// Writing the game script sits behind this same permission and is
		// strictly more destructive: replacing the script raises the
		// template's version, which makes every copy stale, and every stale
		// copy is dropped and rebuilt. Anyone who may take all of them may
		// take one.
		r.With(h.mw.RequireContestPermission(rbac.PermissionContestView)).
			Get("/instances", h.instances)
		r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).
			Delete("/instances/{"+databaseParam+"}", h.dropInstance)
	})
}

// databaseParam names the database in a path. Not a UUID: this identifier is
// PostgreSQL's, and it is the string an organizer sees in a cluster listing —
// which is the whole reason the screen shows it.
const databaseParam = "database"

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

// gameInstanceResponse is one database as staff see it.
//
// A dedicated type rather than the domain record, the same reason every other
// response here has one: direct serialisation publishes whatever field is
// added next.
type gameInstanceResponse struct {
	Database string `json:"database"`
	// Spare says the copy is still in the pool. Carried rather than left to
	// be inferred from an empty registration_id, because "nobody holds this"
	// is the fact the screen groups by.
	Spare bool `json:"spare"`
	// RegistrationID is empty for a spare. Not the user's identifier: the
	// registration is what the instance row actually references, and it is
	// what an audit payload names.
	RegistrationID string `json:"registration_id"`
	// Participant and ParticipantName are empty for a spare, and for a copy
	// whose owner's account has since been deleted — the row outlives the
	// person on it.
	Participant     string `json:"participant"`
	ParticipantName string `json:"participant_name"`
	TemplateVersion int    `json:"template_version"`
	Status          string `json:"status"`
	// SizeBytes and SizeKnown travel together: the sizes come from the game
	// cluster, and a size nobody could read must not reach the screen as a
	// database of zero bytes.
	SizeBytes int64     `json:"size_bytes"`
	SizeKnown bool      `json:"size_known"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type gameInstancesResponse struct {
	Instances []gameInstanceResponse `json:"instances"`
	// Truncated says there are more than this answer carries, so a short list
	// is never read as "your database is gone".
	Truncated bool `json:"truncated"`
}

func (h *GameHandler) instances(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}

	list, err := h.databases.Instances(r.Context(), contestID)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// A non-nil slice, so an empty pool serialises as [] rather than null and
	// the interface has one shape to render.
	rows := make([]gameInstanceResponse, 0, len(list.Instances))
	for _, instance := range list.Instances {
		rows = append(rows, gameInstanceView(instance))
	}
	httpx.JSON(w, r, http.StatusOK, gameInstancesResponse{Instances: rows, Truncated: list.Truncated})
}

func gameInstanceView(instance provisioning.InstanceRecord) gameInstanceResponse {
	view := gameInstanceResponse{
		Database: instance.Database, Spare: instance.Spare(),
		Participant: instance.ParticipantLogin, ParticipantName: instance.ParticipantName,
		TemplateVersion: instance.TemplateVersion, Status: instance.Status,
		SizeBytes: instance.SizeBytes, SizeKnown: instance.SizeKnown,
		CreatedAt: instance.CreatedAt, UpdatedAt: instance.UpdatedAt,
	}
	if instance.Registration != nil {
		view.RegistrationID = instance.Registration.String()
	}
	return view
}

// dropInstance removes one of a contest's databases.
//
// 200 with the row rather than 204: the answer is what the screen replaces
// its own row with, and a participant whose copy this was still has one — in
// status 'dropped' until their next action rebuilds it (provisioning.Service.
// DropInstance's own doc). Saying nothing would leave the screen guessing at
// a state it can be told.
func (h *GameHandler) dropInstance(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	dropped, err := h.databases.DropInstance(
		r.Context(), identity.UserID, contestID, chi.URLParam(r, databaseParam))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, gameInstanceView(dropped))
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
	case errors.Is(err, provisioning.ErrInstanceNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeGameInstanceNotFound,
			"This contest has no database by that name")
	case errors.Is(err, provisioning.ErrInstanceAlreadyDropped):
		httpx.Error(w, r, http.StatusConflict, codeGameInstanceAlreadyDropped,
			"That database has already been removed")
	default:
		h.log.ErrorContext(r.Context(), "a game request failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}
