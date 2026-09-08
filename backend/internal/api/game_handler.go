package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/gamefile"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Games is the slice of provisioning.Games this handler needs.
//
// The upload half (BeginUpload through AbortUpload) is the second way to
// build a contest's game: an organiser's finished dump instead of a script
// typed into the editor. Every method on it answers provisioning.
// ErrUploadsDisabled on a deployment with no GAME_UPLOAD_DIR configured
// (WithUploads never called) — this handler does not check that itself, it
// only maps the sentinel once it comes back (fail, below).
type Games interface {
	Of(ctx context.Context, contestID uuid.UUID) (provisioning.Template, error)
	SetScript(ctx context.Context, actorID, contestID uuid.UUID, script string) (provisioning.Template, error)

	BeginUpload(ctx context.Context, contestID uuid.UUID, filename string, declaredBytes int64) (provisioning.Upload, error)
	AppendChunk(ctx context.Context, contestID, uploadID uuid.UUID, offset int64, r io.Reader) (int64, error)
	CurrentUpload(ctx context.Context, contestID uuid.UUID) (provisioning.Upload, error)
	// Upload resolves the row a file-sourced Template.UploadID names —
	// gameView's own doc says why the status response needs it.
	Upload(ctx context.Context, contestID, uploadID uuid.UUID) (provisioning.Upload, error)
	UploadWindow(ctx context.Context, contestID, uploadID uuid.UUID, fromLine, maxLines int, maxBytes int64) (gamefile.Window, error)
	CompleteUpload(ctx context.Context, actorID, contestID, uploadID uuid.UUID) (provisioning.Template, error)
	AbortUpload(ctx context.Context, actorID, contestID, uploadID uuid.UUID) (provisioning.Upload, error)

	// UploadLimits reports the ceilings a chunked upload must respect —
	// provisioning.Games' own doc explains why this handler reads them here
	// rather than keeping a second copy of GAME_UPLOAD_CHUNK_BYTES /
	// GAME_UPLOAD_MAX_FILE_BYTES. No context and no error: this is an
	// in-process config read, never a call to storage.
	UploadLimits() (gamefile.Limits, bool)
}

// UploadLimiter is the slice of auth.Limiter this handler needs (CLAUDE.md
// rule 3): counting an attempt against a subject and refusing once a window
// has taken too many. Never Reset — starting an upload has no "correct
// password" moment that should forgive the attempts before it, the way a
// successful sign-in does for auth.Service.
type UploadLimiter interface {
	Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error)
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
	// limiter paces BeginUpload (Mount's own routing comment explains why
	// only Begin, never a chunk). Backed by the shared cache rather than an
	// in-process map like events_handler.go's connLimiter: a chunked upload
	// spans many requests that a load balancer may spread across replicas,
	// and what this bounds — a reservation on disk and a row in
	// game_uploads — is a cluster-wide resource, not a socket this one
	// process holds, so the count has to be shared the same way the login
	// throttle's is.
	limiter UploadLimiter
	// maxChunkBody bounds one chunk's HTTP body (appendChunk's own doc). A
	// field defaulted by NewGameHandler rather than only a constant, so a
	// test can shrink it instead of allocating tens of megabytes to prove it
	// trips — the same reason events_handler.go's writeTimeout is a field.
	maxChunkBody int64
}

// NewGameHandler assembles the endpoints.
func NewGameHandler(games Games, databases GameDatabases, mw *auth.Middleware, log *slog.Logger, limiter UploadLimiter) *GameHandler {
	return &GameHandler{
		games: games, databases: databases, mw: mw, log: log,
		limiter: limiter, maxChunkBody: defaultMaxGameChunkBodyBytes,
	}
}

// WithMaxChunkBody overrides the transport-level chunk-body ceiling
// NewGameHandler defaults to. See maxChunkBody's own doc.
func (h *GameHandler) WithMaxChunkBody(n int64) *GameHandler {
	h.maxChunkBody = n
	return h
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

		// The second way to build a contest's game (Games' own doc, above):
		// an organiser uploads a finished dump instead of writing a script
		// in the editor. The view/edit split is the one this whole handler
		// already uses — looking at an upload's progress needs what reading
		// the contest needs, starting, feeding, finishing or cancelling one
		// needs what editing it needs, because completing an upload replaces
		// the game exactly the way SetScript does (CompleteUpload's own
		// doc: "the same path as SetScript").
		//
		// Chunks are not rate-limited, only BeginUpload is. A chunked
		// upload is meant to send many requests — three gigabytes at eight
		// mebibytes each is close to four hundred of them — and a limiter
		// on every one of those would refuse a caller in the middle of an
		// honest transfer, the exact failure mode connLimiter's own doc
		// warns against for a different endpoint. What actually needs
		// pacing is the one call that reserves a file on disk and a row in
		// game_uploads before a single byte has proven the upload is real
		// (allowUploadBegin's own doc).
		r.Route("/uploads", func(r chi.Router) {
			r.With(h.mw.RequireContestPermission(rbac.PermissionContestView)).
				Get("/current", h.currentUpload)
			r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).
				Post("/", h.beginUpload)

			r.Route("/{"+uploadIDParam+"}", func(r chi.Router) {
				r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).
					Put("/chunk", h.appendChunk)
				r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).
					Post("/complete", h.completeUpload)
				r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).
					Post("/abort", h.abortUpload)
				r.With(h.mw.RequireContestPermission(rbac.PermissionContestView)).
					Get("/window", h.uploadWindow)
			})
		})
	})
}

// uploadIDParam names an upload in a path — provisioning.Upload's own id,
// scoped to the contest it belongs to the same way databaseParam is
// (currentContestUpload's own doc: a mismatch reads as ErrUploadNotFound,
// identically to an id that names no upload at all).
const uploadIDParam = "uploadID"

// databaseParam names the database in a path. Not a UUID: this identifier is
// PostgreSQL's, and it is the string an organizer sees in a cluster listing —
// which is the whole reason the screen shows it.
const databaseParam = "database"

// uploadLimitsResponse is the pair of ceilings a browser needs before it can
// slice a file into chunks and start sending them: the largest single PUT
// .../uploads/{id}/chunk this installation accepts, and the largest total
// file BeginUpload will reserve for. Both come from provisioning.Games.
// UploadLimits — the domain's own gamefile.Limits, never a second copy of
// GAME_UPLOAD_CHUNK_BYTES / GAME_UPLOAD_MAX_FILE_BYTES kept in this package
// (Games.UploadLimits' own doc, CLAUDE.md rule 11).
//
// Enabled travels apart from the two numbers on purpose. An installation
// with no upload volume configured (GAME_UPLOAD_DIR empty) reports both as
// zero, and zero is also a number an operator could genuinely configure —
// without this field the two would be indistinguishable, and a client would
// have no way to tell "uploads are off, do not offer the button" from "the
// operator set a ceiling of zero, which refuses everything anyway". A client
// checks Enabled first; ChunkBytes and MaxFileBytes are only real ceilings
// when it is true.
type uploadLimitsResponse struct {
	Enabled      bool  `json:"enabled"`
	ChunkBytes   int64 `json:"chunk_bytes"`
	MaxFileBytes int64 `json:"max_file_bytes"`
}

// uploadLimitsView reads the configured ceilings once per request. Carried on
// two different responses — gameResponse and uploadResponse, each field's own
// doc says why — because a page needs them at two different moments: opening
// the console, before it has decided to start an upload at all (gameResponse,
// from status/setScript/completeUpload), and reopening after a reload to
// resume one already in progress (uploadResponse, from GET .../uploads/
// current) — CurrentUpload coming back absent still leaves the page needing
// to know how to slice a *new* file correctly.
func (h *GameHandler) uploadLimitsView() uploadLimitsResponse {
	limits, enabled := h.games.UploadLimits()
	if !enabled {
		return uploadLimitsResponse{}
	}
	return uploadLimitsResponse{
		Enabled: true, ChunkBytes: limits.MaxChunkBytes, MaxFileBytes: limits.MaxFileBytes,
	}
}

type gameResponse struct {
	Status  string `json:"status"`
	Version int    `json:"version"`
	// Database is the template every participant's copy is made from. Shown
	// to staff so a name in a cluster listing can be traced back to a
	// contest; it is never sent to a participant.
	Database string `json:"database"`
	// Source says which of the two ways this game was built:
	// provisioning.SourceEditor or provisioning.SourceFile — empty only for
	// the synthetic "absent" answer below, a contest with no game at all.
	//
	// Without this the interface has no way to tell the two apart once the
	// page that did the upload is gone: a reload has only this response to
	// go on, and before this field existed it read every game as if it had
	// come from the script editor (the defect this field, and Upload below,
	// exist to close — CLAUDE.md rule 11: the domain already tells the two
	// sources apart, provisioning.Template.Source, and that fact was not
	// crossing the boundary to whoever has to render it).
	Source string `json:"source"`
	// Upload names the file a file-sourced game (Source == "file") was built
	// from — nil for provisioning.SourceEditor, where there is no file to
	// name. See gameUploadSourceResponse's own doc for why this is nested
	// rather than flattened onto this struct.
	Upload *gameUploadSourceResponse `json:"upload,omitempty"`
	// BuildError is PostgreSQL's own words about the *script* when the build
	// failed, empty otherwise. Whoever wrote the script is the person who has
	// to fix it.
	//
	// Never anything else: a build that failed for a reason of ours carries
	// provisioning.BuildFailedInternally instead, decided where the error is
	// produced rather than here. This field is served to anybody holding
	// contest.view, and the same string is kept for good in the
	// contest.game_built audit payload, so a connect string reaching it is a
	// connect string published twice.
	BuildError string `json:"build_error"`
	// ScriptBytes lets the status say whether there is a script at all
	// without carrying it.
	ScriptBytes int       `json:"script_bytes"`
	Building    bool      `json:"building"`
	UpdatedAt   time.Time `json:"updated_at"`
	// UploadLimits are the ceilings a chunked upload must respect —
	// uploadLimitsResponse's own doc says why they travel here and how a
	// client tells "uploads are off" from "the limit is genuinely zero".
	UploadLimits uploadLimitsResponse `json:"upload_limits"`
}

// gameUploadSourceResponse is enough about a file-sourced game's own upload
// for the console to reopen its viewer after a reload — the same window
// uploadWindow already serves, addressed by ID, plus the name and the two
// counts a person needs to recognise which file this was without opening it.
//
// Nested under gameResponse.Upload rather than flattened: these four fields
// are meaningless for an editor-sourced game, and nesting is what lets a
// client tell "this game has no upload" (Upload == nil) from "the upload's
// fields happen to be zero" without a parallel boolean, the same choice
// uploadLimitsResponse's own Enabled field makes for a different pair of
// numbers.
//
// A subset of provisioning.Upload, deliberately: SHA256, DeclaredBytes,
// CreatedAt and the rest belong to uploadResponse, which already serves them
// while an upload is in flight. Once it has become a contest's game, what
// matters here is only what the viewer and "jump to error" need — the row's
// own id, name, final length and line count.
type gameUploadSourceResponse struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	// Bytes is Upload.ReceivedBytes, not DeclaredBytes: by the time a game is
	// file-sourced its upload is sealed, and ReceivedBytes is Store.Complete's
	// own measured length — the number that is actually true of the file on
	// disk, not the browser's claim before a byte of it had arrived.
	Bytes int64 `json:"bytes"`
	Lines int64 `json:"lines"`
}

// gameView assembles gameResponse from a domain Template — the one place
// that shape is built, so status, setScript and completeUpload cannot drift
// from one another the way three separate literals eventually would (they
// did, before this existed: setScript and completeUpload never carried
// Source or Upload at all, which was half of this defect by itself).
//
// Resolving Upload costs a second read when the game is file-sourced. That
// read is never allowed to fail the whole response: an organiser asking
// "what is my game's status" must still get an answer when only the file
// detail could not be read, the same reasoning gameInstanceView gives for a
// size the cluster could not report. The failure is logged, not swallowed
// silently — an Upload that is unexpectedly missing for a file-sourced
// Template is a data inconsistency worth an operator seeing.
func (h *GameHandler) gameView(ctx context.Context, template provisioning.Template) gameResponse {
	resp := gameResponse{
		Status: string(template.Status), Version: template.Version,
		Source:   string(template.Source),
		Database: template.Database, BuildError: template.BuildError,
		ScriptBytes: len(template.Script), Building: template.Building(),
		UpdatedAt: template.UpdatedAt, UploadLimits: h.uploadLimitsView(),
	}
	if template.Source == provisioning.SourceFile && template.UploadID != nil {
		upload, err := h.games.Upload(ctx, template.ContestID, *template.UploadID)
		if err != nil {
			h.log.ErrorContext(ctx, "could not read a file-sourced game's own upload",
				"contest_id", template.ContestID, "upload_id", *template.UploadID, "error", err)
		} else {
			resp.Upload = &gameUploadSourceResponse{
				ID: upload.ID.String(), Filename: upload.Filename,
				Bytes: upload.ReceivedBytes, Lines: upload.Lines,
			}
		}
	}
	return resp
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
		httpx.JSON(w, r, http.StatusOK, gameResponse{Status: "absent", UploadLimits: h.uploadLimitsView()})
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, h.gameView(r.Context(), template))
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
	httpx.JSON(w, r, http.StatusAccepted, h.gameView(r.Context(), template))
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

func (h *GameHandler) uploadID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, uploadIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, "The upload identifier is not a UUID")
		return uuid.Nil, false
	}
	return id, true
}

// --- Uploading a finished dump ---------------------------------------------

// uploadResponse is one upload as staff see it — never the path it lives at
// on disk, which internal/gamefile alone knows and which is exactly the kind
// of filesystem detail participantSafeError (participant_handler.go) already
// argues must not reach a client verbatim.
type uploadResponse struct {
	ID            string `json:"id"`
	Filename      string `json:"filename"`
	DeclaredBytes int64  `json:"declared_bytes"`
	ReceivedBytes int64  `json:"received_bytes"`
	// SHA256 is empty until the upload is complete — Store.Complete's own
	// one sequential pass is what computes it.
	SHA256    string    `json:"sha256"`
	Lines     int64     `json:"lines"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// UploadLimits repeats gameResponse's own field of the same name — see its
	// doc. Carried here too because GET .../uploads/current is the other
	// moment a page needs these numbers: reopening after a reload, before it
	// even knows whether an upload is in progress, and (when CurrentUpload
	// comes back absent) still needing to know how to slice a *new* file.
	UploadLimits uploadLimitsResponse `json:"upload_limits"`
}

// uploadView is a method, not a plain function, only so it can reach
// h.uploadLimitsView() — everything else about one upload's shape comes from
// u alone.
func (h *GameHandler) uploadView(u provisioning.Upload) uploadResponse {
	return uploadResponse{
		ID: u.ID.String(), Filename: u.Filename,
		DeclaredBytes: u.DeclaredBytes, ReceivedBytes: u.ReceivedBytes,
		SHA256: u.SHA256, Lines: u.Lines, Status: string(u.Status),
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
		UploadLimits: h.uploadLimitsView(),
	}
}

// currentUpload lets a reloaded page find an upload already in progress and
// offer to resume it, rather than a second BeginUpload refusing with no way
// to explain why (CurrentUpload's own doc).
//
// 200 with Status "absent" for "there is none" rather than 404 — the same
// call status() makes above for a contest with no game at all, and for the
// same reason: one shape for the interface to render instead of two.
func (h *GameHandler) currentUpload(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}

	upload, err := h.games.CurrentUpload(r.Context(), contestID)
	if errors.Is(err, provisioning.ErrUploadNotFound) {
		httpx.JSON(w, r, http.StatusOK, uploadResponse{Status: "absent", UploadLimits: h.uploadLimitsView()})
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, h.uploadView(upload))
}

type beginUploadRequest struct {
	// Filename is the name the browser's file picker reported — kept only
	// for this screen and never used as a path (provisioning.
	// MaxUploadFilenameBytes's own doc).
	Filename string `json:"filename"`
	// DeclaredBytes is what the browser's File object reports before a byte
	// is sent. Checked again by Store.Complete against what actually landed
	// (ErrUploadLengthMismatch), so a lie here is caught, never trusted.
	DeclaredBytes int64 `json:"declared_bytes"`
}

// uploadBeginWindow, maxUploadBeginsPerAddress and maxUploadBeginsPerContest
// bound allowUploadBegin. Deliberately tight: an honest browser calls
// BeginUpload once per file it means to send — CurrentUpload, not a second
// Begin, is how a reloaded page resumes one already in progress — so a
// handful of attempts in ten minutes already covers picking the wrong file
// and starting over a few times.
const (
	uploadBeginWindow         = 10 * time.Minute
	maxUploadBeginsPerAddress = 20
	maxUploadBeginsPerContest = 8
)

// allowUploadBegin applies CLAUDE.md rule 5's ordering to the one call an
// upload spends that is worth pacing: BeginUpload itself, which reserves a
// file on disk and a row in game_uploads before a single byte has proven the
// upload is real (Games.BeginUpload's own doc). A chunk is never checked
// here — see Mount's own routing comment for why.
//
// The address key is checked first, and second here means something
// different than it does in auth.Service.checkThrottle. This route sits
// behind authentication and RequireContestPermission already, so — unlike a
// login string — neither key below is a space an unauthorised caller can
// mint counters in for free; both name something that already had to be real
// (a session, a contest this actor may edit) before this method runs. The
// address is still checked first anyway, because it is the tighter, shared
// budget: several organisers on one office network share it, and spending it
// before the contest-scoped key means one contest's misbehaving client
// cannot burn through a budget that address's other, unrelated contests also
// depend on.
func (h *GameHandler) allowUploadBegin(w http.ResponseWriter, r *http.Request, contestID uuid.UUID) bool {
	ctx := r.Context()

	if addr := httpx.ClientIP(r); addr != "" {
		allowed, err := h.limiter.Allow(ctx, "game_upload_begin:ip:"+addr, maxUploadBeginsPerAddress, uploadBeginWindow)
		if err != nil {
			h.log.ErrorContext(ctx, "could not check the upload rate limit", "error", err)
			httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
			return false
		}
		if !allowed {
			httpx.Error(w, r, http.StatusTooManyRequests, codeGameUploadTooOften,
				"Too many uploads have been started from this address; wait before trying again")
			return false
		}
	}

	allowed, err := h.limiter.Allow(ctx, "game_upload_begin:contest:"+contestID.String(), maxUploadBeginsPerContest, uploadBeginWindow)
	if err != nil {
		h.log.ErrorContext(ctx, "could not check the upload rate limit", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return false
	}
	if !allowed {
		httpx.Error(w, r, http.StatusTooManyRequests, codeGameUploadTooOften,
			"Too many uploads have been started for this contest; wait before trying again")
		return false
	}
	return true
}

// beginUpload reserves a new upload for this contest's game.
//
// 201, not 202: nothing has been built yet, not even accepted for building —
// a row now exists that chunks may be appended to, which is what 201 says.
func (h *GameHandler) beginUpload(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	if !h.allowUploadBegin(w, r, contestID) {
		return
	}

	var req beginUploadRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	upload, err := h.games.BeginUpload(r.Context(), contestID, req.Filename, req.DeclaredBytes)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, h.uploadView(upload))
}

// defaultMaxGameChunkBodyBytes is NewGameHandler's default for maxChunkBody,
// which appendChunk uses in place of httpx.DecodeJSON's own maxBodyBytes.
// This route never calls DecodeJSON: its body is one chunk's raw bytes, not
// JSON, and a one-mebibyte cap sized for a request document would refuse an
// ordinary chunk of a multi-gigabyte dump before its first byte reached
// AppendChunk.
//
// This is not a gap in CLAUDE.md rule 12, it is rule 12 — the socket still
// gets an explicit ceiling, http.MaxBytesReader, exactly where the bytes
// arrive, just sized to a chunk instead of a JSON document. So the next
// reader does not conclude the limit was simply forgotten: it is here, it is
// large on purpose.
//
// It is only a default. A deployment that configures GAME_UPLOAD_CHUNK_BYTES
// gets that number here too (app.go's WithMaxChunkBody call), because two
// independent ceilings on the same thing means the smaller one decides and
// the configured one is a lie: set the chunk size above this constant and
// every chunk of that size would be refused by a limit the operator never
// chose. One number, named once, enforced twice — here on the socket, and
// again in provisioning.Games.AppendChunk, which is what turns it into
// ErrUploadChunkTooLarge for the client either way.
const defaultMaxGameChunkBodyBytes = 64 << 20 // 64 MiB

// minChunkUploadBytesPerSecond, minChunkReadTimeout and maxChunkReadTimeout
// size the read deadline appendChunk gives itself.
//
// internal/platform/server's ReadTimeout bounds a whole request, body
// included, and its own doc names the premise it was chosen under: "far
// beyond any legitimate JSON body". A chunk of an uploaded dump is not a
// JSON body. At GAME_UPLOAD_CHUNK_BYTES's default of 8 MiB, that thirty
// seconds is a demand for ~2.2 Mbit/s sustained on every one of the four
// hundred-odd chunks a three-gigabyte dump takes — from an organiser at home
// or on a loaded university network, with no buffering proxy in front (Caddy
// streams request bodies), one stall is a deadline that fires in the middle
// of a body.
//
// So the rate is stated rather than implied: the route waits for a transfer
// as slow as 32 KiB/s (about 256 kbit/s), derived from the ceiling this
// installation actually configured rather than fixed at one number, because
// an operator who raises the chunk size is not thereby demanding a faster
// uplink from the same people. The floor keeps a tiny ceiling (a test's, or
// a deliberately small deployment's) from producing a deadline measured in
// milliseconds; the cap keeps a very large one from turning this route into
// a place to park a connection all afternoon — a slow-loris client still
// meets a bound, just one sized to the body instead of to a JSON document.
const (
	minChunkUploadBytesPerSecond = 32 << 10
	minChunkReadTimeout          = 30 * time.Second
	maxChunkReadTimeout          = 10 * time.Minute
)

// chunkReadTimeout is how long this route will wait for one chunk's body,
// from the moment the handler starts reading it.
func (h *GameHandler) chunkReadTimeout() time.Duration {
	d := time.Duration(h.maxChunkBody/minChunkUploadBytesPerSecond) * time.Second
	return min(max(d, minChunkReadTimeout), maxChunkReadTimeout)
}

// chunkResponse answers one appendChunk call: how much of the upload the
// server now holds, which is what a resuming browser needs to pick its next
// offset — nothing else about the upload is worth a round trip on every one
// of what may be several hundred chunks.
type chunkResponse struct {
	ReceivedBytes int64 `json:"received_bytes"`
}

// appendChunk writes one chunk of an upload already begun.
//
// The chunk is streamed straight through: r.Body, wrapped only in
// http.MaxBytesReader, reaches Games.AppendChunk as an io.Reader and is never
// read into a []byte or passed through io.ReadAll here. A three-gigabyte
// upload sent one eight-mebibyte chunk at a time must not cost this process
// a memory spike proportional to the chunk, let alone the whole file
// (CLAUDE.md rule 12; gamefile.Store.Append's own doc explains the copy on
// the other end of this same reader).
func (h *GameHandler) appendChunk(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	uploadID, ok := h.uploadID(w, r)
	if !ok {
		return
	}
	offset, ok := h.chunkOffset(w, r)
	if !ok {
		return
	}

	// This route's own read deadline, in place of the listener's — see
	// minChunkUploadBytesPerSecond for the arithmetic that makes the shared
	// one wrong here. It replaces the deadline for this request alone and
	// leaves the strict listener-wide ReadTimeout exactly as it is for every
	// other route; net/http sets the next request's deadline itself when
	// this one is done.
	//
	// A server that cannot do it (a test's recorder) says so with
	// http.ErrNotSupported, and that is not a reason to refuse a chunk: a
	// deadline is a bound on this handler, not a permission it needs.
	// Anything else is worth an operator's attention, because it means
	// bodies on this route are running under a timeout meant for JSON.
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(h.chunkReadTimeout())); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		h.log.WarnContext(r.Context(), "could not extend the read deadline for an upload chunk", "error", err)
	}

	body := http.MaxBytesReader(w, r.Body, h.maxChunkBody)
	received, err := h.games.AppendChunk(r.Context(), contestID, uploadID, offset, body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// The transport's own ceiling tripped, not the domain's — see
			// defaultMaxGameChunkBodyBytes's own doc for why this answers
			// exactly as ErrUploadChunkTooLarge rather than a second code.
			h.fail(w, r, provisioning.ErrUploadChunkTooLarge)
			return
		}
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, chunkResponse{ReceivedBytes: received})
}

// chunkOffset reads the byte offset a chunk continues from. Not decoded by
// httpx.DecodeJSON alongside the chunk's bytes — the two are different kinds
// of thing on the wire (one small integer, one up to h.maxChunkBody of raw
// data) — so it travels as a query parameter instead, the same way
// contest_people_handler.go's search terms and users_handler.go's paging do.
func (h *GameHandler) chunkOffset(w http.ResponseWriter, r *http.Request) (int64, bool) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, "The chunk offset must be a non-negative integer")
		return 0, false
	}
	return offset, true
}

// completeUpload seals an upload and replaces the contest's game with it.
//
// 202, like setScript: CompleteUpload's own doc calls this "the same path as
// SetScript", one GameEditable check and one audit write rather than a
// second parallel one, and the answer follows suit — the status comes back
// pending and the interface watches it the same way.
func (h *GameHandler) completeUpload(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	uploadID, ok := h.uploadID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	template, err := h.games.CompleteUpload(r.Context(), identity.UserID, contestID, uploadID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusAccepted, h.gameView(r.Context(), template))
}

// abortUpload cancels an organiser's own upload before it became anybody's
// game. 200 with the row, not 204, the same reasoning dropInstance's own doc
// gives: the answer is what the screen replaces its own row with.
func (h *GameHandler) abortUpload(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	uploadID, ok := h.uploadID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	upload, err := h.games.AbortUpload(r.Context(), identity.UserID, contestID, uploadID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, h.uploadView(upload))
}

// defaultUploadWindowLines, maxUploadWindowLines, defaultUploadWindowBytes
// and maxUploadWindowBytes bound uploadWindow's two caller-supplied budgets.
//
// gamefile.Window's own doc calls maxBytes "the caller's own budget for this
// call" and readWindowLines' own doc says what it costs: memory bounded by
// maxBytes, held as a []string. Here the caller is whoever can reach this
// HTTP endpoint, so CLAUDE.md rule 2 puts a ceiling on what an organiser's
// own query parameters may ask for — max_bytes=999999999 must not become a
// gigabyte read into this process's memory just because it fits in an int64.
const (
	defaultUploadWindowLines = 200
	maxUploadWindowLines     = 1000
	defaultUploadWindowBytes = 256 << 10 // 256 KiB
	maxUploadWindowBytes     = 1 << 20   // 1 MiB
)

type uploadWindowResponse struct {
	FromLine   int      `json:"from_line"`
	Lines      []string `json:"lines"`
	TotalLines int64    `json:"total_lines"`
	// Truncated says the byte budget stopped the window before maxLines was
	// reached, possibly mid-line (gamefile.Window's own doc).
	Truncated bool `json:"truncated"`
}

// uploadWindow serves a slice of a completed upload's lines — the console's
// own preview of a script it will not run yet, the same role script() plays
// for one written directly in the editor.
//
// A window past the end of the file is an empty one, not an error: paging
// past the last page is a normal outcome, and UploadWindow (gamefile.Window's
// own doc) already treats it as such — this handler adds nothing on top, it
// only clamps the two budgets before either reaches the service.
func (h *GameHandler) uploadWindow(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	uploadID, ok := h.uploadID(w, r)
	if !ok {
		return
	}

	fromLine := intQueryParam(r, "from", 1)
	maxLines := clampedIntQueryParam(r, "max_lines", defaultUploadWindowLines, maxUploadWindowLines)
	maxBytes := clampedInt64QueryParam(r, "max_bytes", defaultUploadWindowBytes, maxUploadWindowBytes)

	window, err := h.games.UploadWindow(r.Context(), contestID, uploadID, fromLine, maxLines, maxBytes)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// A non-nil slice, so a window past the end of the file (or one asked for
	// zero lines) serialises as [] rather than null and the interface has one
	// shape to render — the same reasoning instances gives above for an empty
	// pool.
	lines := window.Lines
	if lines == nil {
		lines = []string{}
	}
	httpx.JSON(w, r, http.StatusOK, uploadWindowResponse{
		FromLine: window.FromLine, Lines: lines,
		TotalLines: window.TotalLines, Truncated: window.Truncated,
	})
}

// intQueryParam reads name as a non-negative int, or def when it is absent or
// cannot be parsed. Malformed input reading as the default rather than a 400
// is deliberate: this backs a preview window, not a write, so the honest
// answer to "I could not make sense of that" is the same page an omitted
// parameter gets, not a refusal.
func intQueryParam(r *http.Request, name string, def int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// clampedIntQueryParam is intQueryParam with an upper bound applied after —
// see defaultUploadWindowLines's own doc for why one is needed at all.
func clampedIntQueryParam(r *http.Request, name string, def, max int) int {
	n := intQueryParam(r, name, def)
	if n > max {
		return max
	}
	return n
}

// clampedInt64QueryParam is clampedIntQueryParam for the one budget large
// enough to need 64 bits: max_bytes.
func clampedInt64QueryParam(r *http.Request, name string, def, max int64) int64 {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
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

	// --- The uploaded dump (provisioning/upload.go's own twelve sentinels) --

	case errors.Is(err, provisioning.ErrUploadsDisabled):
		httpx.Error(w, r, http.StatusNotFound, codeGameUploadsDisabled,
			"File uploads are not enabled on this installation")
	case errors.Is(err, provisioning.ErrUploadFilenameInvalid):
		httpx.Error(w, r, http.StatusBadRequest, codeGameUploadFilenameInvalid,
			"The upload's filename is missing or too long")
	case errors.Is(err, provisioning.ErrUploadTooLarge):
		httpx.Error(w, r, http.StatusBadRequest, codeGameUploadTooLarge,
			"The upload exceeds the maximum file size this installation accepts")
	case errors.Is(err, provisioning.ErrUploadStoreFull):
		httpx.Error(w, r, http.StatusConflict, codeGameUploadStoreFull,
			"The upload directory is full; try again once other uploads have finished or been removed")
	case errors.Is(err, provisioning.ErrUploadChunkOutOfOrder):
		httpx.Error(w, r, http.StatusConflict, codeGameUploadChunkOutOfOrder,
			"This chunk does not continue where the upload left off")
	case errors.Is(err, provisioning.ErrUploadChunkTooLarge):
		httpx.Error(w, r, http.StatusBadRequest, codeGameUploadChunkTooLarge,
			"The chunk exceeds the maximum chunk size this installation accepts")
	case errors.Is(err, provisioning.ErrUploadChunkIncomplete):
		// 408, not 500 and not 400: nothing about the request was wrong, it
		// simply did not finish arriving, and the client's own retry is the
		// right next move (CLAUDE.md rule 1 — a named refusal rather than
		// "internal error" for something the caller can act on).
		httpx.Error(w, r, http.StatusRequestTimeout, codeGameUploadChunkIncomplete,
			"The chunk did not arrive in full; nothing was kept, so send it again from the offset the server reports")
	case errors.Is(err, provisioning.ErrUploadLengthMismatch):
		httpx.Error(w, r, http.StatusConflict, codeGameUploadLengthMismatch,
			"The bytes received do not match the length declared when the upload began")
	case errors.Is(err, provisioning.ErrUploadNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeGameUploadNotFound,
			"This contest has no upload by that identifier")
	case errors.Is(err, provisioning.ErrUploadInProgress):
		httpx.Error(w, r, http.StatusConflict, codeGameUploadInProgress,
			"This contest already has an upload in progress")
	case errors.Is(err, provisioning.ErrUploadAlreadyComplete):
		httpx.Error(w, r, http.StatusConflict, codeGameUploadAlreadyComplete,
			"This upload has already been completed or cancelled")
	case errors.Is(err, provisioning.ErrUploadIncomplete):
		httpx.Error(w, r, http.StatusConflict, codeGameUploadIncomplete,
			"This upload has not been completed yet")

	default:
		h.log.ErrorContext(r.Context(), "a game request failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}
