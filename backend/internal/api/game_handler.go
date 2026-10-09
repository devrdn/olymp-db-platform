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

// Games is the slice of provisioning.Games this handler needs. The upload
// methods answer provisioning.ErrUploadsDisabled when GAME_UPLOAD_DIR is not
// configured; this handler only maps that sentinel.
type Games interface {
	Of(ctx context.Context, contestID uuid.UUID) (provisioning.Template, error)
	// StatusOf is Of without the script or definition: a console polls it every
	// two seconds during a build and needs only the script's length.
	StatusOf(ctx context.Context, contestID uuid.UUID) (provisioning.Template, error)
	SetScript(ctx context.Context, actorID, contestID uuid.UUID, script string) (provisioning.Template, error)
	// RequestBuild rebuilds the game from what is stored, for table data typed
	// in after the last build.
	RequestBuild(ctx context.Context, actorID, contestID uuid.UUID) (provisioning.Template, error)

	BeginUpload(ctx context.Context, contestID uuid.UUID, filename string, declaredBytes int64) (provisioning.Upload, error)
	AppendChunk(ctx context.Context, contestID, uploadID uuid.UUID, offset int64, r io.Reader) (int64, error)
	CurrentUpload(ctx context.Context, contestID uuid.UUID) (provisioning.Upload, error)
	// Upload resolves the upload a file-sourced Template.UploadID names.
	Upload(ctx context.Context, contestID, uploadID uuid.UUID) (provisioning.Upload, error)
	UploadWindow(ctx context.Context, contestID, uploadID uuid.UUID, fromLine, maxLines int, maxBytes int64) (gamefile.Window, error)
	CompleteUpload(ctx context.Context, actorID, contestID, uploadID uuid.UUID) (provisioning.Template, error)
	AbortUpload(ctx context.Context, actorID, contestID, uploadID uuid.UUID) (provisioning.Upload, error)

	// UploadLimits reports the configured chunked-upload ceilings, so this
	// handler keeps no copy of them (CLAUDE.md rule 11).
	UploadLimits() (gamefile.Limits, bool)

	// The table builder: tables described structurally instead of SQL, with
	// each table's data uploaded as CSV.
	SetDefinition(ctx context.Context, actorID, contestID uuid.UUID, definition provisioning.Definition) (provisioning.Template, error)

	BeginTableUpload(ctx context.Context, contestID uuid.UUID, table string, declaredBytes int64) (provisioning.TableData, error)
	// CurrentTableData finds a table's chunked upload still in progress, for a
	// reloaded page.
	CurrentTableData(ctx context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error)
	AppendTableChunk(ctx context.Context, contestID, id uuid.UUID, offset int64, r io.Reader) (int64, error)
	CompleteTableUpload(ctx context.Context, actorID, contestID, id uuid.UUID) (provisioning.TableData, error)
	AbortTableUpload(ctx context.Context, actorID, contestID, id uuid.UUID) (provisioning.TableData, error)
	TableDataWindow(ctx context.Context, contestID uuid.UUID, table string, fromRow int64, maxRows int, maxBytes int64) (provisioning.TableRowWindow, error)
	// AppendTableRow adds one row typed into a form. It is refused while a
	// chunked upload for the same table is still receiving.
	AppendTableRow(ctx context.Context, actorID, contestID uuid.UUID, table string, values []string) (provisioning.TableData, error)
	DeleteTableRow(ctx context.Context, actorID, contestID uuid.UUID, table string, row int64) error

	// TableDataLimits is UploadLimits for the table-data store, which is
	// configured separately; never assume the two are equal.
	TableDataLimits() (gamefile.Limits, bool)
}

// UploadLimiter is the slice of auth.Limiter this handler needs. It has no
// Reset: starting an upload has no success that should forgive earlier
// attempts.
type UploadLimiter interface {
	Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error)
}

// GameDatabases is the slice of provisioning.Service this handler needs: a
// contest's existing databases, and dropping one. Separate from Games because
// another service owns the copies.
type GameDatabases interface {
	Instances(ctx context.Context, contestID uuid.UUID) (provisioning.InstanceList, error)
	DropInstance(ctx context.Context, actorID, contestID uuid.UUID, database string) (provisioning.InstanceRecord, error)
}

// GameHandler serves a contest's game database: the SQL it is built from, and
// how the build went. It is mounted only where a game cluster is configured;
// without one a stored script could never be built.
type GameHandler struct {
	games     Games
	databases GameDatabases
	mw        *auth.Middleware
	log       *slog.Logger
	// limiter paces BeginUpload. It is backed by the shared cache, not an
	// in-process map: an upload spans requests that may reach different
	// replicas, and the disk it reserves is a cluster-wide resource.
	limiter UploadLimiter
	// maxChunkBody bounds one chunk's HTTP body; a field so a test can shrink
	// it.
	maxChunkBody int64
	// maxTableChunkBody is maxChunkBody for table CSV chunks, kept separate
	// because the table-data store is configured independently.
	maxTableChunkBody int64
}

// NewGameHandler assembles the endpoints.
func NewGameHandler(games Games, databases GameDatabases, mw *auth.Middleware, log *slog.Logger, limiter UploadLimiter) *GameHandler {
	return &GameHandler{
		games: games, databases: databases, mw: mw, log: log,
		limiter: limiter, maxChunkBody: defaultMaxGameChunkBodyBytes,
		maxTableChunkBody: defaultMaxGameChunkBodyBytes,
	}
}

// WithMaxChunkBody overrides the chunk-body ceiling NewGameHandler defaults to.
func (h *GameHandler) WithMaxChunkBody(n int64) *GameHandler {
	h.maxChunkBody = n
	return h
}

// WithMaxTableChunkBody is WithMaxChunkBody for maxTableChunkBody.
func (h *GameHandler) WithMaxTableChunkBody(n int64) *GameHandler {
	h.maxTableChunkBody = n
	return h
}

// Mount registers the routes. Reading the game needs contest view; writing it
// needs contest edit, since the game is contest content.
//
// The status and the script are separate reads: the status is polled during a
// build, while the script (up to half a mebibyte) is fetched once for editing.
func (h *GameHandler) Mount(r chi.Router) {
	r.Route("/contests/{"+contestIDParam+"}/game", func(r chi.Router) {
		r.Use(h.mw.Authenticate)

		r.With(h.mw.RequireContestPermission(rbac.PermissionContestView)).Get("/", h.status)
		r.With(h.mw.RequireContestPermission(rbac.PermissionContestView)).Get("/script", h.script)
		r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).Put("/script", h.setScript)

		// Rebuilding needs ContestEdit, like replacing the game.
		r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).Post("/build", h.requestBuild)

		// Listing databases needs ContestView; dropping one needs ContestEdit,
		// not the owner-only ContestManage. Dropping a broken copy is a repair
		// the manager on duty must be able to make mid-contest without the
		// owner. It grants nothing new: replacing the script under the same
		// permission already makes every copy stale and rebuilds them all.
		r.With(h.mw.RequireContestPermission(rbac.PermissionContestView)).
			Get("/instances", h.instances)
		r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).
			Delete("/instances/{"+databaseParam+"}", h.dropInstance)

		// Uploading a finished dump. Completing one replaces the game as
		// SetScript does, so every write needs ContestEdit.
		//
		// Only BeginUpload is rate-limited, not chunks: an honest 3 GB upload
		// is hundreds of chunks, and limiting each would break a legitimate
		// transfer. Begin is the call that reserves disk and a game_uploads row
		// before any byte arrives.
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

		// The table builder: tables described structurally, data loaded as CSV.
		// Same view/edit split as above.
		r.Route("/definition", func(r chi.Router) {
			r.With(h.mw.RequireContestPermission(rbac.PermissionContestView)).Get("/", h.definition)
			r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).Put("/", h.setDefinition)
		})

		r.Route("/tables/{"+tableParam+"}", func(r chi.Router) {
			r.Route("/data", func(r chi.Router) {
				r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).
					Post("/", h.beginTableUpload)
				r.With(h.mw.RequireContestPermission(rbac.PermissionContestView)).
					Get("/current", h.currentTableData)
				r.With(h.mw.RequireContestPermission(rbac.PermissionContestView)).
					Get("/window", h.tableDataWindow)
				r.Route("/{"+tableDataIDParam+"}", func(r chi.Router) {
					r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).
						Put("/chunk", h.appendTableChunk)
					r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).
						Post("/complete", h.completeTableUpload)
					r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).
						Post("/abort", h.abortTableUpload)
				})
			})
			r.Route("/rows", func(r chi.Router) {
				r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).
					Post("/", h.appendTableRow)
				r.With(h.mw.RequireContestPermission(rbac.PermissionContestEdit)).
					Delete("/{"+rowParam+"}", h.deleteTableRow)
			})
		})
	})
}

// uploadIDParam names an upload in a path. An upload of another contest reads
// as ErrUploadNotFound.
const uploadIDParam = "uploadID"

// databaseParam names the database in a path. Not a UUID: it is the PostgreSQL
// name an organiser sees in a cluster listing.
const databaseParam = "database"

// uploadLimitsResponse is what a browser needs to slice a file into chunks: the
// largest chunk and the largest file. Both come from
// provisioning.Games.UploadLimits, never a copy kept here (CLAUDE.md rule 11).
//
// Enabled is separate because a deployment without GAME_UPLOAD_DIR reports both
// numbers as zero, and zero is also a value an operator could configure.
// ChunkBytes and MaxFileBytes mean something only when Enabled is true.
type uploadLimitsResponse struct {
	Enabled      bool  `json:"enabled"`
	ChunkBytes   int64 `json:"chunk_bytes"`
	MaxFileBytes int64 `json:"max_file_bytes"`
}

// uploadLimitsView reads the configured upload ceilings. Both gameResponse and
// uploadResponse carry them: a page needs them before starting an upload, and
// after a reload even when no upload is in progress.
func (h *GameHandler) uploadLimitsView() uploadLimitsResponse {
	limits, enabled := h.games.UploadLimits()
	if !enabled {
		return uploadLimitsResponse{}
	}
	return uploadLimitsResponse{
		Enabled: true, ChunkBytes: limits.MaxChunkBytes, MaxFileBytes: limits.MaxFileBytes,
	}
}

// builderLimitsResponse is every ceiling the table builder must respect,
// published so the browser keeps no copy of its own (CLAUDE.md rule 11).
// ChunkBytes and MaxFileBytes are per-deployment configuration
// (provisioning.Games.TableDataLimits); the rest are the platform constants
// that also bound the server side.
type builderLimitsResponse struct {
	// Enabled works as in uploadLimitsResponse: the byte ceilings mean
	// something only when it is true.
	Enabled            bool  `json:"enabled"`
	ChunkBytes         int64 `json:"chunk_bytes"`
	MaxFileBytes       int64 `json:"max_file_bytes"`
	MaxTables          int   `json:"max_tables"`
	MaxTableColumns    int   `json:"max_table_columns"`
	MaxDefinitionBytes int64 `json:"max_definition_bytes"`
	// MaxFieldBytes bounds one CSV field; MaxLineBytes one CSV line (the header
	// or a data row).
	MaxFieldBytes int64 `json:"max_field_bytes"`
	MaxLineBytes  int64 `json:"max_line_bytes"`
	MaxRows       int64 `json:"max_rows"`
	// MaxDeletedRows is how many rows may be tombstoned before DeleteTableRow
	// refuses another.
	MaxDeletedRows int `json:"max_deleted_rows"`
	// ColumnTypes is the closed set of column types, in the order the domain
	// declares them.
	ColumnTypes []string `json:"column_types"`
}

// builderLimitsView fills in the constant ceilings even when no table-data
// store is configured: a definition can be described and validated without any
// CSV.
func (h *GameHandler) builderLimitsView() builderLimitsResponse {
	limits, enabled := h.games.TableDataLimits()
	types := make([]string, len(provisioning.ColumnTypes))
	for i, t := range provisioning.ColumnTypes {
		types[i] = string(t)
	}
	resp := builderLimitsResponse{
		MaxTables:          provisioning.MaxDefinitionTables,
		MaxTableColumns:    provisioning.MaxDefinitionTableColumns,
		MaxDefinitionBytes: provisioning.MaxDefinitionBytes,
		MaxFieldBytes:      provisioning.MaxTableFieldBytes,
		MaxLineBytes:       provisioning.MaxTableLineBytes,
		MaxRows:            provisioning.MaxTableDataRows,
		MaxDeletedRows:     provisioning.MaxTableDeletedRows,
		ColumnTypes:        types,
	}
	if enabled {
		resp.Enabled, resp.ChunkBytes, resp.MaxFileBytes = true, limits.MaxChunkBytes, limits.MaxFileBytes
	}
	return resp
}

type gameResponse struct {
	Status  string `json:"status"`
	Version int    `json:"version"`
	// Database is the template every participant's copy is made from. Shown to
	// staff only, never to a participant.
	Database string `json:"database"`
	// Source says which way the game was built (a provisioning.Source value),
	// empty only for the "absent" answer. After a reload this response is all
	// the page has to tell (CLAUDE.md rule 11).
	Source string `json:"source"`
	// Upload names the file a file-sourced game was built from; nil otherwise.
	Upload *gameUploadSourceResponse `json:"upload,omitempty"`
	// BuildError is PostgreSQL's message about the script when the build
	// failed, empty otherwise.
	//
	// A failure of ours carries provisioning.BuildFailedInternally instead,
	// decided where the error is produced: anyone with contest.view reads this
	// field and the contest.game_built audit payload keeps it, so a connect
	// string here would be published twice.
	BuildError string `json:"build_error"`
	// ScriptBytes says whether there is a script without carrying it.
	ScriptBytes int `json:"script_bytes"`
	// MaxScriptBytes is provisioning.MaxScriptBytes, published so the editor
	// can refuse an over-long script without keeping its own copy of the limit
	// (CLAUDE.md rule 11).
	MaxScriptBytes int       `json:"max_script_bytes"`
	Building       bool      `json:"building"`
	UpdatedAt      time.Time `json:"updated_at"`
	// UploadLimits and BuilderLimits travel on the status because it is read
	// before any way of building the game is offered.
	UploadLimits  uploadLimitsResponse  `json:"upload_limits"`
	BuilderLimits builderLimitsResponse `json:"builder_limits"`
	// NeedsBuild means the built database lacks data added since
	// (provisioning.Template.NeedsBuild). Sent rather than derived on the
	// client, because staleness is a domain rule (CLAUDE.md rule 11).
	NeedsBuild bool `json:"needs_build"`
}

// gameUploadSourceResponse identifies the upload a file-sourced game was built
// from, enough for the console to reopen its viewer after a reload. Nested so
// that nil means "no upload" without a separate flag.
type gameUploadSourceResponse struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	// Bytes is the measured ReceivedBytes, not the browser's declared size.
	Bytes int64 `json:"bytes"`
	Lines int64 `json:"lines"`
}

// gameView builds gameResponse from a Template; status, setScript and
// completeUpload share it so their answers cannot drift.
//
// Failing to read a file-sourced game's upload is logged, not returned: the
// status must still answer when only the file detail is unreadable.
func (h *GameHandler) gameView(ctx context.Context, template provisioning.Template) gameResponse {
	resp := gameResponse{
		Status: string(template.Status), Version: template.Version,
		Source:   string(template.Source),
		Database: template.Database, BuildError: template.BuildError,
		ScriptBytes: template.ScriptBytes, MaxScriptBytes: provisioning.MaxScriptBytes,
		Building:  template.Building(),
		UpdatedAt: template.UpdatedAt, UploadLimits: h.uploadLimitsView(),
		BuilderLimits: h.builderLimitsView(),
		NeedsBuild:    template.NeedsBuild(),
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

	template, err := h.games.StatusOf(r.Context(), contestID)
	if errors.Is(err, provisioning.ErrNoGame) {
		// Not an error: every contest starts without a game. Answered as an
		// "absent" game, with the ceilings, so the interface has one shape and
		// the editor knows its limits before the first save.
		httpx.JSON(w, r, http.StatusOK, gameResponse{
			Status: "absent", MaxScriptBytes: provisioning.MaxScriptBytes,
			UploadLimits: h.uploadLimitsView(), BuilderLimits: h.builderLimitsView(),
		})
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
	if !decodeBody(w, r, &req) {
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

// gameInstanceResponse is one database as staff see it. A dedicated type, so a
// field added to the domain record is not published automatically.
type gameInstanceResponse struct {
	Database string `json:"database"`
	// Spare says the copy is still in the pool; the screen groups by it.
	Spare bool `json:"spare"`
	// RegistrationID is empty for a spare. It names the registration, not the
	// user, because that is what the instance row and the audit payload
	// reference.
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

	// Non-nil, so an empty pool serialises as [] rather than null.
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

// dropInstance removes one of a contest's databases. It answers 200 with the
// row, which the screen replaces its own with: a participant's copy stays in
// status 'dropped' until their next action rebuilds it.
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

// uploadResponse is one upload as staff see it, never its path on disk.
type uploadResponse struct {
	ID            string `json:"id"`
	Filename      string `json:"filename"`
	DeclaredBytes int64  `json:"declared_bytes"`
	ReceivedBytes int64  `json:"received_bytes"`
	// SHA256 is empty until the upload is complete.
	SHA256    string    `json:"sha256"`
	Lines     int64     `json:"lines"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// UploadLimits is carried here too: a page reopening after a reload reads
	// this before it knows whether an upload is in progress.
	UploadLimits uploadLimitsResponse `json:"upload_limits"`
}

func (h *GameHandler) uploadView(u provisioning.Upload) uploadResponse {
	return uploadResponse{
		ID: u.ID.String(), Filename: u.Filename,
		DeclaredBytes: u.DeclaredBytes, ReceivedBytes: u.ReceivedBytes,
		SHA256: u.SHA256, Lines: u.Lines, Status: string(u.Status),
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
		UploadLimits: h.uploadLimitsView(),
	}
}

// currentUpload lets a reloaded page find an upload in progress and resume it.
// "None" is 200 with Status "absent", not 404, so the interface has one shape
// to render.
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
	// Filename is what the browser's file picker reported; it is never used as
	// a path.
	Filename string `json:"filename"`
	// DeclaredBytes is the browser's claim; Store.Complete checks it against
	// what arrived (ErrUploadLengthMismatch).
	DeclaredBytes int64 `json:"declared_bytes"`
}

// These bound allowUploadBegin. An honest browser begins once per file (a
// reloaded page resumes through CurrentUpload), so a few attempts in ten
// minutes covers picking the wrong file and starting over.
const (
	uploadBeginWindow         = 10 * time.Minute
	maxUploadBeginsPerAddress = 20
	maxUploadBeginsPerContest = 8
)

// allowUploadBegin paces BeginUpload, which reserves disk and a game_uploads
// row before any byte arrives. Chunks are never checked here.
//
// The address key is checked first (CLAUDE.md rule 5). Both keys here name
// something already authenticated and authorised, so neither lets a caller mint
// counters freely.
func (h *GameHandler) allowUploadBegin(w http.ResponseWriter, r *http.Request, contestID uuid.UUID) bool {
	ctx := r.Context()

	// The address as a rate-limit subject: an IPv6 /64 is one caller.
	if addr := httpx.ClientSubject(r); addr != "" {
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

// beginUpload reserves a new upload for this contest's game. 201: a row now
// exists that chunks may be appended to; nothing is accepted for building yet.
func (h *GameHandler) beginUpload(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	if !h.allowUploadBegin(w, r, contestID) {
		return
	}

	var req beginUploadRequest
	if !decodeBody(w, r, &req) {
		return
	}

	upload, err := h.games.BeginUpload(r.Context(), contestID, req.Filename, req.DeclaredBytes)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, h.uploadView(upload))
}

// defaultMaxGameChunkBodyBytes is the default for maxChunkBody. A chunk is raw
// bytes, not JSON, so DecodeJSON's 1 MiB cap does not apply;
// http.MaxBytesReader still bounds it on the socket (CLAUDE.md rule 12).
//
// A deployment that sets GAME_UPLOAD_CHUNK_BYTES gets that number here too
// (app.go calls WithMaxChunkBody): a second, independent ceiling would silently
// override the configured one. provisioning.Games.AppendChunk enforces the same
// number as ErrUploadChunkTooLarge.
const defaultMaxGameChunkBodyBytes = 64 << 20 // 64 MiB

// These size appendChunk's read deadline.
//
// The server's ReadTimeout is sized for JSON bodies. At the default 8 MiB chunk
// its thirty seconds demands about 2.2 Mbit/s on every chunk, and Caddy streams
// bodies, so one stall on a slow network would fail a chunk. This route allows
// 32 KiB/s instead, derived from the configured chunk ceiling so a larger chunk
// does not demand a faster uplink. The floor keeps a tiny ceiling from
// producing a millisecond deadline; the cap still bounds a slow-loris client.
const (
	minChunkUploadBytesPerSecond = 32 << 10
	minChunkReadTimeout          = 30 * time.Second
	maxChunkReadTimeout          = 10 * time.Minute
)

func (h *GameHandler) chunkReadTimeout() time.Duration {
	return chunkTimeoutFor(h.maxChunkBody)
}

func (h *GameHandler) tableChunkReadTimeout() time.Duration {
	return chunkTimeoutFor(h.maxTableChunkBody)
}

func chunkTimeoutFor(maxBody int64) time.Duration {
	d := time.Duration(maxBody/minChunkUploadBytesPerSecond) * time.Second
	return min(max(d, minChunkReadTimeout), maxChunkReadTimeout)
}

// chunkResponse carries only the received length: a resuming browser needs it
// to pick its next offset, and it is sent for each of hundreds of chunks.
type chunkResponse struct {
	ReceivedBytes int64 `json:"received_bytes"`
}

// appendChunk writes one chunk of an upload already begun. The body streams
// straight to Games.AppendChunk and is never read into memory here (CLAUDE.md
// rule 12).
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

	// Extend the read deadline for this request only; the listener-wide
	// ReadTimeout stays for every other route. A test recorder answers
	// http.ErrNotSupported, which is no reason to refuse a chunk; any other
	// failure means this body runs under the JSON timeout, so it is logged.
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(h.chunkReadTimeout())); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		h.log.WarnContext(r.Context(), "could not extend the read deadline for an upload chunk", "error", err)
	}

	body := http.MaxBytesReader(w, r.Body, h.maxChunkBody)
	received, err := h.games.AppendChunk(r.Context(), contestID, uploadID, offset, body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// The transport ceiling tripped. It enforces the same number as the
			// domain, so it answers ErrUploadChunkTooLarge too.
			h.fail(w, r, provisioning.ErrUploadChunkTooLarge)
			return
		}
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, chunkResponse{ReceivedBytes: received})
}

// chunkOffset reads the byte offset a chunk continues from. It travels as a
// query parameter because the body is the chunk's raw bytes.
func (h *GameHandler) chunkOffset(w http.ResponseWriter, r *http.Request) (int64, bool) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, "The chunk offset must be a non-negative integer")
		return 0, false
	}
	return offset, true
}

// completeUpload seals an upload and replaces the contest's game with it. 202,
// like setScript: the game is now pending a build.
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

// abortUpload cancels an upload before it became the game. 200 with the row,
// which the screen replaces its own with.
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

// These bound uploadWindow's caller-supplied budgets (CLAUDE.md rule 2): the
// window is held in memory, so max_bytes must not turn into a gigabyte read.
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
	// Truncated says the byte budget stopped the window before maxLines,
	// possibly mid-line.
	Truncated bool `json:"truncated"`
}

// uploadWindow serves a slice of a completed upload's lines, the console's
// preview of a script it will not run yet. A window past the end of the file is
// empty, not an error.
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
	// Non-nil, so an empty window serialises as [] rather than null.
	httpx.JSON(w, r, http.StatusOK, uploadWindowResponse{
		FromLine: window.FromLine, Lines: emptyIfNil(window.Lines),
		TotalLines: window.TotalLines, Truncated: window.Truncated,
	})
}

// intQueryParam reads name as a non-negative int, or def when it is absent or
// malformed. Malformed input falls back instead of failing because it only
// shapes a preview, never a write.
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

func clampedIntQueryParam(r *http.Request, name string, def, max int) int {
	return min(intQueryParam(r, name, def), max)
}

func clampedInt64QueryParam(r *http.Request, name string, def, max int64) int64 {
	return min(int64QueryParam(r, name, def), max)
}

// int64QueryParam is intQueryParam for 64 bits. It is never clamped: any row
// number is a valid start, and rows past the end come back as an empty window.
func int64QueryParam(r *http.Request, name string, def int64) int64 {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// --- The table builder: a structural description instead of SQL -----------

// tableParam names a table in a path, compared verbatim with the current
// definition. A valid table name is a plain identifier, so it never holds a
// slash.
const tableParam = "table"

// tableDataIDParam names a table's chunked CSV upload in a path. An id
// belonging to another contest or table reads as ErrTableDataNotFound.
const tableDataIDParam = "dataID"

// rowParam names a data row by its stable 1-based number: deleting a row
// tombstones it, so numbers never shift.
const rowParam = "row"

// columnDefinitionView is one column on the wire, in both directions. One
// symmetric type is enough because provisioning.ColumnDefinition carries
// nothing a client should not see.
type columnDefinitionView struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Nullable is omitted when false, so a request that leaves it out gets the
	// stricter default.
	Nullable bool `json:"nullable,omitempty"`
}

type tableDefinitionView struct {
	Name       string                 `json:"name"`
	Columns    []columnDefinitionView `json:"columns"`
	PrimaryKey []string               `json:"primary_key,omitempty"`
}

// definitionRequest is the whole structural description. Bounded by
// provisioning.MaxDefinitionBytes, it is read as one ordinary JSON document.
type definitionRequest struct {
	Tables []tableDefinitionView `json:"tables"`
}

// definitionResponse is the saved description plus the ceilings a client needs
// to edit it.
type definitionResponse struct {
	Tables        []tableDefinitionView `json:"tables"`
	BuilderLimits builderLimitsResponse `json:"builder_limits"`
}

// toDefinition only reshapes the request; provisioning.Games.SetDefinition
// validates it.
func toDefinition(req definitionRequest) provisioning.Definition {
	tables := make([]provisioning.TableDefinition, len(req.Tables))
	for i, t := range req.Tables {
		columns := make([]provisioning.ColumnDefinition, len(t.Columns))
		for j, c := range t.Columns {
			columns[j] = provisioning.ColumnDefinition{
				Name: c.Name, Type: provisioning.ColumnType(c.Type), Nullable: c.Nullable,
			}
		}
		tables[i] = provisioning.TableDefinition{Name: t.Name, Columns: columns, PrimaryKey: t.PrimaryKey}
	}
	return provisioning.Definition{Tables: tables}
}

func (h *GameHandler) definitionView(d provisioning.Definition) definitionResponse {
	tables := make([]tableDefinitionView, len(d.Tables))
	for i, t := range d.Tables {
		columns := make([]columnDefinitionView, len(t.Columns))
		for j, c := range t.Columns {
			columns[j] = columnDefinitionView{Name: c.Name, Type: string(c.Type), Nullable: c.Nullable}
		}
		tables[i] = tableDefinitionView{Name: t.Name, Columns: columns, PrimaryKey: t.PrimaryKey}
	}
	return definitionResponse{Tables: tables, BuilderLimits: h.builderLimitsView()}
}

// definition reads the contest's structural description. A contest with no
// game, or one built another way, answers an empty definition, so the console
// can ask without first checking how the game was built.
func (h *GameHandler) definition(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}

	template, err := h.games.Of(r.Context(), contestID)
	switch {
	case errors.Is(err, provisioning.ErrNoGame):
		httpx.JSON(w, r, http.StatusOK, h.definitionView(provisioning.Definition{}))
		return
	case err != nil:
		h.fail(w, r, err)
		return
	case template.Source != provisioning.SourceBuilder:
		httpx.JSON(w, r, http.StatusOK, h.definitionView(provisioning.Definition{}))
		return
	}
	httpx.JSON(w, r, http.StatusOK, h.definitionView(template.Definition))
}

// setDefinition stores the structural description the game is built from. 202,
// like setScript: storing makes the game pending and a worker builds it.
func (h *GameHandler) setDefinition(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}

	var req definitionRequest
	if !decodeBody(w, r, &req) {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	template, err := h.games.SetDefinition(r.Context(), identity.UserID, contestID, toDefinition(req))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusAccepted, h.gameView(r.Context(), template))
}

// requestBuild asks for the game to be rebuilt from what is stored, so table
// rows added after the last build reach a database. 202: the build runs in the
// background, and the body is the pending game the status screen continues
// from.
func (h *GameHandler) requestBuild(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	asked, err := h.games.RequestBuild(r.Context(), identity.UserID, contestID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusAccepted, h.gameView(r.Context(), asked))
}

// tableDataResponse is one table's CSV data as staff see it, never its path on
// disk. BuilderLimits travels here for a page reopening the builder after a
// reload.
type tableDataResponse struct {
	ID            string `json:"id"`
	Table         string `json:"table"`
	DeclaredBytes int64  `json:"declared_bytes"`
	ReceivedBytes int64  `json:"received_bytes"`
	Lines         int64  `json:"lines"`
	// ActiveRows is Lines minus the tombstoned rows.
	ActiveRows    int64                 `json:"active_rows"`
	DeletedRows   []int64               `json:"deleted_rows"`
	Status        string                `json:"status"`
	CreatedAt     time.Time             `json:"created_at"`
	UpdatedAt     time.Time             `json:"updated_at"`
	BuilderLimits builderLimitsResponse `json:"builder_limits"`
}

func (h *GameHandler) tableDataView(d provisioning.TableData) tableDataResponse {
	return tableDataResponse{
		ID: d.ID.String(), Table: d.Table,
		DeclaredBytes: d.DeclaredBytes, ReceivedBytes: d.ReceivedBytes,
		Lines: d.Lines, ActiveRows: d.ActiveRows(), DeletedRows: emptyIfNil(d.DeletedRows),
		Status: string(d.Status), CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		BuilderLimits: h.builderLimitsView(),
	}
}

type beginTableUploadRequest struct {
	DeclaredBytes int64 `json:"declared_bytes"`
}

// beginTableUpload reserves a new chunked CSV upload for one table. It shares
// beginUpload's rate-limit budget: both reserve disk and a row before any byte
// arrives, so the risk is the same whichever kind is started.
func (h *GameHandler) beginTableUpload(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	if !h.allowUploadBegin(w, r, contestID) {
		return
	}

	var req beginTableUploadRequest
	if !decodeBody(w, r, &req) {
		return
	}

	table := chi.URLParam(r, tableParam)
	data, err := h.games.BeginTableUpload(r.Context(), contestID, table, req.DeclaredBytes)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, h.tableDataView(data))
}

// currentTableData lets a reloaded page find a table's upload in progress.
// "None" is 200 with Status "absent", as in currentUpload.
func (h *GameHandler) currentTableData(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	table := chi.URLParam(r, tableParam)

	data, err := h.games.CurrentTableData(r.Context(), contestID, table)
	if errors.Is(err, provisioning.ErrTableDataNotFound) {
		httpx.JSON(w, r, http.StatusOK, tableDataResponse{
			Table: table, Status: "absent", DeletedRows: []int64{}, BuilderLimits: h.builderLimitsView(),
		})
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, h.tableDataView(data))
}

// appendTableChunk streams one chunk of a table's CSV upload like appendChunk,
// under the table store's own body ceiling and read deadline.
func (h *GameHandler) appendTableChunk(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	dataID, ok := h.tableDataID(w, r)
	if !ok {
		return
	}
	offset, ok := h.chunkOffset(w, r)
	if !ok {
		return
	}

	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(h.tableChunkReadTimeout())); err != nil &&
		!errors.Is(err, http.ErrNotSupported) {
		h.log.WarnContext(r.Context(), "could not extend the read deadline for a table-data chunk", "error", err)
	}

	body := http.MaxBytesReader(w, r.Body, h.maxTableChunkBody)
	received, err := h.games.AppendTableChunk(r.Context(), contestID, dataID, offset, body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// The transport ceiling tripped; answered as the domain's refusal,
			// as in appendChunk.
			h.fail(w, r, provisioning.ErrTableDataChunkTooLarge)
			return
		}
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, chunkResponse{ReceivedBytes: received})
}

// completeTableUpload finalises a table's CSV upload. 200, not 202: unlike
// completeUpload it never replaces the game or starts a build.
func (h *GameHandler) completeTableUpload(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	dataID, ok := h.tableDataID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	data, err := h.games.CompleteTableUpload(r.Context(), identity.UserID, contestID, dataID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, h.tableDataView(data))
}

// abortTableUpload cancels a table's CSV upload before it completed.
func (h *GameHandler) abortTableUpload(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	dataID, ok := h.tableDataID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	data, err := h.games.AbortTableUpload(r.Context(), identity.UserID, contestID, dataID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, h.tableDataView(data))
}

// These bound tableDataWindow's budgets, as the upload window constants do for
// uploadWindow. Separate constants because the two windows read different
// stores and are tuned independently.
const (
	defaultTableWindowRows  = 200
	maxTableWindowRows      = 1000
	defaultTableWindowBytes = 256 << 10 // 256 KiB
	maxTableWindowBytes     = 1 << 20   // 1 MiB
)

type tableRowResponse struct {
	Row    int64    `json:"row"`
	Fields []string `json:"fields"`
}

type tableRowWindowResponse struct {
	FromRow   int64              `json:"from_row"`
	Rows      []tableRowResponse `json:"rows"`
	TotalRows int64              `json:"total_rows"`
	Truncated bool               `json:"truncated"`
}

// tableDataWindow serves a page of one table's data rows. No file yet, or a
// start past the last row, is an empty page, not an error.
func (h *GameHandler) tableDataWindow(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	table := chi.URLParam(r, tableParam)

	fromRow := int64QueryParam(r, "from", 1)
	maxRows := clampedIntQueryParam(r, "max_rows", defaultTableWindowRows, maxTableWindowRows)
	maxBytes := clampedInt64QueryParam(r, "max_bytes", defaultTableWindowBytes, maxTableWindowBytes)

	window, err := h.games.TableDataWindow(r.Context(), contestID, table, fromRow, maxRows, maxBytes)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	rows := make([]tableRowResponse, 0, len(window.Rows))
	for _, row := range window.Rows {
		rows = append(rows, tableRowResponse{Row: row.Row, Fields: emptyIfNil(row.Fields)})
	}
	httpx.JSON(w, r, http.StatusOK, tableRowWindowResponse{
		FromRow: window.FromRow, Rows: rows, TotalRows: window.TotalRows, Truncated: window.Truncated,
	})
}

type appendTableRowRequest struct {
	// Values are in the table's column order; an empty string is NULL, as an
	// unquoted empty CSV field is.
	Values []string `json:"values"`
}

// appendTableRow adds one row typed into a form. 201: a row now exists, and the
// first one also creates the table's file.
func (h *GameHandler) appendTableRow(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	table := chi.URLParam(r, tableParam)

	var req appendTableRowRequest
	if !decodeBody(w, r, &req) {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	data, err := h.games.AppendTableRow(r.Context(), identity.UserID, contestID, table, req.Values)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, h.tableDataView(data))
}

// deleteTableRow tombstones one row of a table's data. 204: the domain returns
// nothing to carry.
func (h *GameHandler) deleteTableRow(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	table := chi.URLParam(r, tableParam)
	row, err := strconv.ParseInt(chi.URLParam(r, rowParam), 10, 64)
	if err != nil || row < 1 {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, "The row number must be a positive integer")
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.games.DeleteTableRow(r.Context(), identity.UserID, contestID, table, row); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *GameHandler) tableDataID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, tableDataIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, "The table data identifier is not a UUID")
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
	case errors.Is(err, provisioning.ErrBuildInProgress):
		httpx.Error(w, r, http.StatusConflict, codeBuildInProgress,
			"The game is already being built, or is waiting to be")
	case errors.Is(err, provisioning.ErrNoGame):
		// Not codeNoGameYet, which a participant's console renders as "your
		// database is not ready — try again shortly". Every route on this
		// handler is staff, and that advice is wrong for them: waiting
		// produces no game, saving one does.
		httpx.Error(w, r, http.StatusNotFound, codeNoGameToBuild,
			"This contest has no game stored to build")
	case errors.Is(err, provisioning.ErrInstanceNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeGameInstanceNotFound,
			"This contest has no database by that name")
	case errors.Is(err, provisioning.ErrInstanceAlreadyDropped):
		httpx.Error(w, r, http.StatusConflict, codeGameInstanceAlreadyDropped,
			"That database has already been removed")

	// --- The uploaded dump (provisioning/upload.go) ---

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
		// 408: nothing was wrong with the request, it did not finish arriving,
		// and the client should resend it.
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
	case errors.Is(err, provisioning.ErrUploadIndexCorrupt):
		// 409 and not 500: the request was right, the state on the volume is
		// not, and the organiser has a move — upload the file again.
		httpx.Error(w, r, http.StatusConflict, codeGameUploadIndexCorrupt,
			"The upload's line index is damaged and it can no longer be read; upload the file again")
	case errors.Is(err, provisioning.ErrUploadWindowUnreachable):
		// 422 and not 500: the request is well-formed and the file is sound,
		// but this particular line cannot be shown at a price a preview may
		// pay. The organiser's move is a line nearer a mark.
		httpx.Error(w, r, http.StatusUnprocessableEntity, codeGameUploadWindowUnreachable,
			"That line is too far past the file's nearest index mark to preview; start the window nearer the beginning of its thousand-line block")

	// --- The table builder's structural description (provisioning/definition.go) --

	case errors.Is(err, provisioning.ErrDefinitionEmpty):
		httpx.Error(w, r, http.StatusBadRequest, codeGameDefinitionEmpty, err.Error())
	case errors.Is(err, provisioning.ErrDefinitionTooLarge):
		httpx.Error(w, r, http.StatusBadRequest, codeGameDefinitionTooLarge, err.Error())
	case errors.Is(err, provisioning.ErrDefinitionInvalidName):
		httpx.Error(w, r, http.StatusBadRequest, codeGameDefinitionInvalidName, err.Error())
	case errors.Is(err, provisioning.ErrDefinitionDuplicateName):
		httpx.Error(w, r, http.StatusBadRequest, codeGameDefinitionDuplicateName, err.Error())
	case errors.Is(err, provisioning.ErrDefinitionTableEmpty):
		httpx.Error(w, r, http.StatusBadRequest, codeGameDefinitionTableEmpty, err.Error())
	case errors.Is(err, provisioning.ErrDefinitionInvalidType):
		httpx.Error(w, r, http.StatusBadRequest, codeGameDefinitionInvalidType, err.Error())
	case errors.Is(err, provisioning.ErrDefinitionInvalidPrimaryKey):
		httpx.Error(w, r, http.StatusBadRequest, codeGameDefinitionInvalidPrimaryKey, err.Error())
	case errors.Is(err, provisioning.ErrDefinitionTableLocked):
		// 409, not 400: the definition is well-formed but conflicts with the
		// table's existing data.
		httpx.Error(w, r, http.StatusConflict, codeGameDefinitionTableLocked, err.Error())

	// --- The table builder's CSV data (provisioning/tabledata.go, tablecsv.go) ---

	case errors.Is(err, provisioning.ErrTableDataDisabled):
		httpx.Error(w, r, http.StatusNotFound, codeGameTableDataDisabled, err.Error())
	case errors.Is(err, provisioning.ErrTableUnknown):
		httpx.Error(w, r, http.StatusNotFound, codeGameTableUnknown, err.Error())
	case errors.Is(err, provisioning.ErrTableDataInProgress):
		httpx.Error(w, r, http.StatusConflict, codeGameTableDataInProgress, err.Error())
	case errors.Is(err, provisioning.ErrTableDataNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeGameTableDataNotFound, err.Error())
	case errors.Is(err, provisioning.ErrTableDataAlreadyComplete):
		httpx.Error(w, r, http.StatusConflict, codeGameTableDataAlreadyComplete, err.Error())
	case errors.Is(err, provisioning.ErrTableDataChunkOutOfOrder):
		httpx.Error(w, r, http.StatusConflict, codeGameTableDataChunkOutOfOrder, err.Error())
	case errors.Is(err, provisioning.ErrTableDataChunkTooLarge):
		httpx.Error(w, r, http.StatusBadRequest, codeGameTableDataChunkTooLarge, err.Error())
	case errors.Is(err, provisioning.ErrTableDataChunkIncomplete):
		// 408, as for ErrUploadChunkIncomplete.
		httpx.Error(w, r, http.StatusRequestTimeout, codeGameTableDataChunkIncomplete,
			"The chunk did not arrive in full; nothing was kept, so send it again from the offset the server reports")
	case errors.Is(err, provisioning.ErrTableDataTooLarge):
		httpx.Error(w, r, http.StatusBadRequest, codeGameTableDataTooLarge, err.Error())
	case errors.Is(err, provisioning.ErrTableDataStoreFull):
		httpx.Error(w, r, http.StatusConflict, codeGameTableDataStoreFull, err.Error())
	case errors.Is(err, provisioning.ErrTableDataLengthMismatch):
		httpx.Error(w, r, http.StatusConflict, codeGameTableDataLengthMismatch, err.Error())
	case errors.Is(err, provisioning.ErrTableDataChanged):
		// 409: another row reached the end of the file first; this one has to
		// be added again over the current state.
		httpx.Error(w, r, http.StatusConflict, codeGameTableDataChanged, err.Error())
	case errors.Is(err, provisioning.ErrTableRowNotFound):
		httpx.Error(w, r, http.StatusNotFound, codeGameTableRowNotFound, err.Error())
	case errors.Is(err, provisioning.ErrTableRowAlreadyDeleted):
		httpx.Error(w, r, http.StatusConflict, codeGameTableRowAlreadyDeleted, err.Error())
	case errors.Is(err, provisioning.ErrTooManyDeletedRows):
		httpx.Error(w, r, http.StatusBadRequest, codeGameTableTooManyDeletedRows, err.Error())

	// The CSV content refusals (tablecsv.go) name the row and column at fault
	// in their own text, so err.Error() is the message.
	case errors.Is(err, provisioning.ErrTableHeaderMismatch):
		httpx.Error(w, r, http.StatusBadRequest, codeGameTableHeaderMismatch, err.Error())
	case errors.Is(err, provisioning.ErrTableRowFieldCount):
		httpx.Error(w, r, http.StatusBadRequest, codeGameTableRowFieldCount, err.Error())
	case errors.Is(err, provisioning.ErrTableValueInvalid):
		httpx.Error(w, r, http.StatusBadRequest, codeGameTableValueInvalid, err.Error())
	case errors.Is(err, provisioning.ErrTableFieldTooLong):
		httpx.Error(w, r, http.StatusBadRequest, codeGameTableFieldTooLong, err.Error())
	case errors.Is(err, provisioning.ErrTableLineTooLong):
		httpx.Error(w, r, http.StatusBadRequest, codeGameTableLineTooLong, err.Error())
	case errors.Is(err, provisioning.ErrTableTooManyRows):
		httpx.Error(w, r, http.StatusBadRequest, codeGameTableTooManyRows, err.Error())

	default:
		h.log.ErrorContext(r.Context(), "a game request failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}
