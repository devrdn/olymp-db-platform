package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/gamefile"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// fakeGames stands in for provisioning.Games; the service's decisions are
// tested where it lives.
type fakeGames struct {
	template provisioning.Template
	ofErr    error
	setErr   error
	gotSet   string
	gotActor uuid.UUID

	// Upload half. gotX fields are set even on the error path.
	beginErr        error
	beginResult     provisioning.Upload
	gotBeginContest uuid.UUID
	gotBeginName    string
	gotBeginBytes   int64

	appendErr          error
	appendResult       int64
	gotAppendContest   uuid.UUID
	gotAppendUpload    uuid.UUID
	gotAppendOffset    int64
	gotAppendBodyBytes []byte // read with io.ReadAll; production streams

	// bodyMeter, when set, wraps the request body so AppendChunk can record how
	// much was read on entry.
	bodyMeter *readMeter
	// gotAppendBodyReadOnEntry is zero when the handler streams.
	gotAppendBodyReadOnEntry int64

	// uploads, when set, streams into a real store via realUploadService, since
	// io.ReadAll can hide a refusal (CLAUDE.md rule 10).
	uploads *provisioning.Games

	currentErr    error
	currentResult provisioning.Upload

	// uploadResult and uploadErr back Upload.
	uploadErr        error
	uploadResult     provisioning.Upload
	gotUploadContest uuid.UUID
	gotUploadID      uuid.UUID

	windowErr        error
	windowResult     gamefile.Window
	gotWindowFrom    int
	gotWindowLines   int
	gotWindowBytes   int64
	gotWindowUpload  uuid.UUID
	gotWindowContest uuid.UUID

	completeErr        error
	completeResult     provisioning.Template
	gotCompleteActor   uuid.UUID
	gotCompleteContest uuid.UUID
	gotCompleteUpload  uuid.UUID

	abortErr        error
	abortResult     provisioning.Upload
	gotAbortActor   uuid.UUID
	gotAbortContest uuid.UUID
	gotAbortUpload  uuid.UUID

	// limits and limitsEnabled back UploadLimits; their zero values behave like
	// an installation with no GAME_UPLOAD_DIR.
	limits        gamefile.Limits
	limitsEnabled bool

	// Table builder half; gotX fields as above.
	setDefinitionErr    error
	gotSetDefinition    provisioning.Definition
	gotSetDefinitionFor uuid.UUID

	// requestBuildResult and requestBuildErr back RequestBuild.
	requestBuildResult     provisioning.Template
	requestBuildErr        error
	gotRequestBuildActor   uuid.UUID
	gotRequestBuildContest uuid.UUID

	beginTableErr        error
	beginTableResult     provisioning.TableData
	gotBeginTableContest uuid.UUID
	gotBeginTableTable   string
	gotBeginTableBytes   int64

	appendTableErr          error
	appendTableResult       int64
	gotAppendTableContest   uuid.UUID
	gotAppendTableDataID    uuid.UUID
	gotAppendTableOffset    int64
	gotAppendTableBodyBytes []byte
	// tableBodyMeter and tableStore are bodyMeter and store for
	// AppendTableChunk.
	tableBodyMeter                *readMeter
	gotAppendTableBodyReadOnEntry int64
	// tableData is uploads for AppendTableChunk (realTableDataService).
	tableData *provisioning.Games

	completeTableErr        error
	completeTableResult     provisioning.TableData
	gotCompleteTableActor   uuid.UUID
	gotCompleteTableContest uuid.UUID
	gotCompleteTableDataID  uuid.UUID

	abortTableErr        error
	abortTableResult     provisioning.TableData
	gotAbortTableActor   uuid.UUID
	gotAbortTableContest uuid.UUID
	gotAbortTableDataID  uuid.UUID

	windowTableErr         error
	windowTableResult      provisioning.TableRowWindow
	gotWindowTableContest  uuid.UUID
	gotWindowTableTable    string
	gotWindowTableFrom     int64
	gotWindowTableMaxRows  int
	gotWindowTableMaxBytes int64

	appendRowErr        error
	appendRowResult     provisioning.TableData
	gotAppendRowActor   uuid.UUID
	gotAppendRowContest uuid.UUID
	gotAppendRowTable   string
	gotAppendRowValues  []string

	deleteRowErr        error
	gotDeleteRowActor   uuid.UUID
	gotDeleteRowContest uuid.UUID
	gotDeleteRowTable   string
	gotDeleteRowRow     int64

	// tableLimits and tableLimitsEnabled back TableDataLimits.
	tableLimits        gamefile.Limits
	tableLimitsEnabled bool

	currentTableErr        error
	currentTableResult     provisioning.TableData
	gotCurrentTableContest uuid.UUID
	gotCurrentTableTable   string
}

func (g *fakeGames) CurrentTableData(_ context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	g.gotCurrentTableContest, g.gotCurrentTableTable = contestID, table
	if g.currentTableErr != nil {
		return provisioning.TableData{}, g.currentTableErr
	}
	return g.currentTableResult, nil
}

func (g *fakeGames) SetDefinition(_ context.Context, actorID, _ uuid.UUID, definition provisioning.Definition) (provisioning.Template, error) {
	g.gotSetDefinition, g.gotSetDefinitionFor = definition, actorID
	if g.setDefinitionErr != nil {
		return provisioning.Template{}, g.setDefinitionErr
	}
	g.template = provisioning.Template{
		Database: "game_tpl_cabc", Version: g.template.Version + 1,
		Status: provisioning.TemplatePending, Source: provisioning.SourceBuilder, Definition: definition,
	}
	return g.template, nil
}

func (g *fakeGames) RequestBuild(_ context.Context, actorID, contestID uuid.UUID) (provisioning.Template, error) {
	g.gotRequestBuildActor, g.gotRequestBuildContest = actorID, contestID
	if g.requestBuildErr != nil {
		return provisioning.Template{}, g.requestBuildErr
	}
	return g.requestBuildResult, nil
}

func (g *fakeGames) BeginTableUpload(_ context.Context, contestID uuid.UUID, table string, declaredBytes int64) (provisioning.TableData, error) {
	g.gotBeginTableContest, g.gotBeginTableTable, g.gotBeginTableBytes = contestID, table, declaredBytes
	if g.beginTableErr != nil {
		return provisioning.TableData{}, g.beginTableErr
	}
	return g.beginTableResult, nil
}

func (g *fakeGames) AppendTableChunk(_ context.Context, contestID, id uuid.UUID, offset int64, r io.Reader) (int64, error) {
	g.gotAppendTableContest, g.gotAppendTableDataID, g.gotAppendTableOffset = contestID, id, offset
	if g.tableBodyMeter != nil {
		g.gotAppendTableBodyReadOnEntry = g.tableBodyMeter.read
	}
	if g.tableData != nil {
		return g.tableData.AppendTableChunk(context.Background(), contestID, id, offset, r)
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	g.gotAppendTableBodyBytes = body
	if g.appendTableErr != nil {
		return g.appendTableResult, g.appendTableErr
	}
	return g.appendTableResult, nil
}

func (g *fakeGames) CompleteTableUpload(_ context.Context, actorID, contestID, id uuid.UUID) (provisioning.TableData, error) {
	g.gotCompleteTableActor, g.gotCompleteTableContest, g.gotCompleteTableDataID = actorID, contestID, id
	if g.completeTableErr != nil {
		return provisioning.TableData{}, g.completeTableErr
	}
	return g.completeTableResult, nil
}

func (g *fakeGames) AbortTableUpload(_ context.Context, actorID, contestID, id uuid.UUID) (provisioning.TableData, error) {
	g.gotAbortTableActor, g.gotAbortTableContest, g.gotAbortTableDataID = actorID, contestID, id
	if g.abortTableErr != nil {
		return provisioning.TableData{}, g.abortTableErr
	}
	return g.abortTableResult, nil
}

func (g *fakeGames) TableDataWindow(_ context.Context, contestID uuid.UUID, table string, fromRow int64, maxRows int, maxBytes int64) (provisioning.TableRowWindow, error) {
	g.gotWindowTableContest, g.gotWindowTableTable = contestID, table
	g.gotWindowTableFrom, g.gotWindowTableMaxRows, g.gotWindowTableMaxBytes = fromRow, maxRows, maxBytes
	if g.windowTableErr != nil {
		return provisioning.TableRowWindow{}, g.windowTableErr
	}
	return g.windowTableResult, nil
}

func (g *fakeGames) AppendTableRow(_ context.Context, actorID, contestID uuid.UUID, table string, values []string) (provisioning.TableData, error) {
	g.gotAppendRowActor, g.gotAppendRowContest, g.gotAppendRowTable, g.gotAppendRowValues = actorID, contestID, table, values
	if g.appendRowErr != nil {
		return provisioning.TableData{}, g.appendRowErr
	}
	return g.appendRowResult, nil
}

func (g *fakeGames) DeleteTableRow(_ context.Context, actorID, contestID uuid.UUID, table string, row int64) error {
	g.gotDeleteRowActor, g.gotDeleteRowContest, g.gotDeleteRowTable, g.gotDeleteRowRow = actorID, contestID, table, row
	return g.deleteRowErr
}

func (g *fakeGames) TableDataLimits() (gamefile.Limits, bool) {
	return g.tableLimits, g.tableLimitsEnabled
}

// Of answers only what the real repository can: a row with a real status, or
// provisioning.ErrNoGame.
func (g *fakeGames) Of(context.Context, uuid.UUID) (provisioning.Template, error) {
	if g.ofErr != nil {
		return provisioning.Template{}, g.ofErr
	}
	if g.template.Status == "" {
		return provisioning.Template{}, provisioning.ErrNoGame
	}
	return g.template, nil
}

// StatusOf, like the real TemplateStatus, returns the script's length but never
// the script.
func (g *fakeGames) StatusOf(context.Context, uuid.UUID) (provisioning.Template, error) {
	if g.ofErr != nil {
		return provisioning.Template{}, g.ofErr
	}
	if g.template.Status == "" {
		return provisioning.Template{}, provisioning.ErrNoGame
	}
	status := g.template
	status.ScriptBytes = len(status.Script)
	status.Script, status.Definition = "", provisioning.Definition{}
	return status, nil
}

func (g *fakeGames) SetScript(_ context.Context, actorID, _ uuid.UUID, script string) (provisioning.Template, error) {
	g.gotSet, g.gotActor = script, actorID
	if g.setErr != nil {
		return provisioning.Template{}, g.setErr
	}
	g.template = provisioning.Template{
		Database: "game_tpl_cabc", Version: g.template.Version + 1,
		Status: provisioning.TemplatePending, Script: script,
	}
	return g.template, nil
}

func (g *fakeGames) BeginUpload(_ context.Context, contestID uuid.UUID, filename string, declaredBytes int64) (provisioning.Upload, error) {
	g.gotBeginContest, g.gotBeginName, g.gotBeginBytes = contestID, filename, declaredBytes
	if g.beginErr != nil {
		return provisioning.Upload{}, g.beginErr
	}
	return g.beginResult, nil
}

func (g *fakeGames) AppendChunk(_ context.Context, contestID, uploadID uuid.UUID, offset int64, r io.Reader) (int64, error) {
	g.gotAppendContest, g.gotAppendUpload, g.gotAppendOffset = contestID, uploadID, offset
	if g.bodyMeter != nil {
		g.gotAppendBodyReadOnEntry = g.bodyMeter.read
	}
	if g.uploads != nil {
		return g.uploads.AppendChunk(context.Background(), contestID, uploadID, offset, r)
	}
	// Readable here only if the handler never read it.
	body, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	g.gotAppendBodyBytes = body
	if g.appendErr != nil {
		return g.appendResult, g.appendErr
	}
	return g.appendResult, nil
}

// realUploadService is the real provisioning.Games over a real gamefile.Store
// with one upload begun, so refusals use the service's own error mapping.
func realUploadService(t *testing.T, contestID, uploadID uuid.UUID, limits gamefile.Limits) (*provisioning.Games, *gamefile.Store) {
	t.Helper()
	store := beginOn(t, uploadID, limits)
	repo := &chunkRepo{upload: provisioning.Upload{
		ID: uploadID, ContestID: contestID, Status: provisioning.UploadReceiving, DeclaredBytes: 1 << 16,
	}}
	return provisioning.NewGames(repo, nil, nil).WithUploads(store, limits), store
}

// realTableDataService is realUploadService for a table's chunked CSV, over the
// table builder's store.
func realTableDataService(t *testing.T, contestID, dataID uuid.UUID, table string, limits gamefile.Limits) (*provisioning.Games, *gamefile.Store) {
	t.Helper()
	store := beginOn(t, dataID, limits)
	repo := &chunkRepo{data: provisioning.TableData{
		ID: dataID, ContestID: contestID, Table: table, Status: provisioning.TableDataReceiving, DeclaredBytes: 1 << 16,
	}}
	return provisioning.NewGames(repo, nil, nil).WithTableData(store, limits), store
}

// chunkRepo is provisioning.TemplateRepository cut down to what AppendChunk and
// AppendTableChunk use; any other method hits the embedded nil interface and
// panics.
type chunkRepo struct {
	provisioning.TemplateRepository
	upload provisioning.Upload
	data   provisioning.TableData
}

func (c *chunkRepo) Upload(context.Context, uuid.UUID) (provisioning.Upload, error) {
	return c.upload, nil
}

func (c *chunkRepo) UpdateReceived(context.Context, uuid.UUID, int64) error { return nil }

func (c *chunkRepo) TableDataByID(context.Context, uuid.UUID) (provisioning.TableData, error) {
	return c.data, nil
}

func (c *chunkRepo) UpdateTableDataReceived(context.Context, uuid.UUID, int64) error { return nil }

// beginOn is a gamefile.Store on a fresh directory with one file already
// reserved for id.
func beginOn(t *testing.T, id uuid.UUID, limits gamefile.Limits) *gamefile.Store {
	t.Helper()
	store, err := gamefile.NewStore(t.TempDir(), limits)
	if err != nil {
		t.Fatalf("open the upload store: %v", err)
	}
	if err := store.Begin(id.String(), 1<<16); err != nil {
		t.Fatalf("reserve the file: %v", err)
	}
	return store
}

func (g *fakeGames) CurrentUpload(context.Context, uuid.UUID) (provisioning.Upload, error) {
	return g.currentResult, g.currentErr
}

func (g *fakeGames) Upload(_ context.Context, contestID, uploadID uuid.UUID) (provisioning.Upload, error) {
	g.gotUploadContest, g.gotUploadID = contestID, uploadID
	if g.uploadErr != nil {
		return provisioning.Upload{}, g.uploadErr
	}
	return g.uploadResult, nil
}

func (g *fakeGames) UploadWindow(_ context.Context, contestID, uploadID uuid.UUID, fromLine, maxLines int, maxBytes int64) (gamefile.Window, error) {
	g.gotWindowContest, g.gotWindowUpload = contestID, uploadID
	g.gotWindowFrom, g.gotWindowLines, g.gotWindowBytes = fromLine, maxLines, maxBytes
	if g.windowErr != nil {
		return gamefile.Window{}, g.windowErr
	}
	return g.windowResult, nil
}

func (g *fakeGames) CompleteUpload(_ context.Context, actorID, contestID, uploadID uuid.UUID) (provisioning.Template, error) {
	g.gotCompleteActor, g.gotCompleteContest, g.gotCompleteUpload = actorID, contestID, uploadID
	if g.completeErr != nil {
		return provisioning.Template{}, g.completeErr
	}
	return g.completeResult, nil
}

func (g *fakeGames) AbortUpload(_ context.Context, actorID, contestID, uploadID uuid.UUID) (provisioning.Upload, error) {
	g.gotAbortActor, g.gotAbortContest, g.gotAbortUpload = actorID, contestID, uploadID
	if g.abortErr != nil {
		return provisioning.Upload{}, g.abortErr
	}
	return g.abortResult, nil
}

func (g *fakeGames) UploadLimits() (gamefile.Limits, bool) {
	return g.limits, g.limitsEnabled
}

// fakeDatabases stands in for provisioning.Service's instance list and drop.
type fakeDatabases struct {
	list       provisioning.InstanceList
	listErr    error
	dropErr    error
	droppedDB  string
	dropActor  uuid.UUID
	dropTarget uuid.UUID
}

func (d *fakeDatabases) Instances(context.Context, uuid.UUID) (provisioning.InstanceList, error) {
	return d.list, d.listErr
}

func (d *fakeDatabases) DropInstance(_ context.Context, actorID, contestID uuid.UUID, database string) (provisioning.InstanceRecord, error) {
	d.dropActor, d.dropTarget, d.droppedDB = actorID, contestID, database
	if d.dropErr != nil {
		return provisioning.InstanceRecord{}, d.dropErr
	}
	return provisioning.InstanceRecord{Database: database, Status: provisioning.InstanceStatusDropped}, nil
}

type gameFixture struct {
	router    http.Handler
	games     *fakeGames
	databases *fakeDatabases
	handler   *api.GameHandler
	stores    *conteststest.Fixture
	actor     users.User
	cookie    *http.Cookie
}

func newGameFixture(t *testing.T, permissions ...string) *gameFixture {
	t.Helper()

	stores := conteststest.NewFixture()
	stores.Users.GrantRole("staff", permissions...)
	actor := stores.Users.Add(users.User{
		Login: "organizer", FullName: "Organizer", Status: users.StatusActive, Roles: []string{"staff"},
	})

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: actor.ID, Login: actor.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users,
		Authorizer: rbac.New(contestRoles{stores}),
		Cookies:    auth.NewCookieWriter(false), Logger: log,
	})

	games := &fakeGames{}
	databases := &fakeDatabases{}
	limiter := auth.NewLimiter(c)
	router := chi.NewRouter()
	handler := api.NewGameHandler(games, databases, mw, log, limiter)
	handler.Mount(router)

	return &gameFixture{
		router: router, games: games, databases: databases, handler: handler, stores: stores, actor: actor,
		cookie: &http.Cookie{Name: auth.SessionCookieName, Value: token},
	}
}

// Copies of allowUploadBegin's unexported budgets.
const (
	maxUploadBeginsPerAddressInTest = 20
	maxUploadBeginsPerContestInTest = 8
)

// oneOffice is the address several organisers share (httptest's default
// RemoteAddr), spelled out because these tests are about which key refused.
const oneOffice = "192.0.2.1:1234"

// beginUploadFrom varies the two keys allowUploadBegin bounds independently, so
// a test can tell which one refused.
func (f *gameFixture) beginUploadFrom(remoteAddr, contestID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/contests/"+contestID+"/game/uploads",
		strings.NewReader(`{"filename":"dump.sql","declared_bytes":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// readMeter counts bytes drawn from a request body, so a test can tell when
// they were read.
type readMeter struct {
	r    io.Reader
	read int64
}

func (m *readMeter) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	m.read += int64(n)
	return n, err
}

func (f *gameFixture) doBody(method, path string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *gameFixture) do(method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func TestAContestWithNoGameReportsItAsAbsentRatherThanMissing(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.ofErr = provisioning.ErrNoGame

	rec := f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := decode(t, rec)["status"]; got != "absent" {
		t.Fatalf("status came back %v, want absent", got)
	}
}

func TestStoringAScriptIsAcceptedAndComesBackPending(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	rec := f.do(http.MethodPut, "/contests/"+uuid.NewString()+"/game/script",
		`{"script":"CREATE TABLE guests (id int);"}`)
	// 202, not 204: the script is stored and the build has not happened.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if f.games.gotSet != "CREATE TABLE guests (id int);" {
		t.Fatalf("the service was given %q", f.games.gotSet)
	}
	if f.games.gotActor == uuid.Nil {
		t.Fatal("the actor did not reach the service, so nothing could be recorded against them")
	}
	if got := decode(t, rec)["status"]; got != "pending" {
		t.Fatalf("answered %v, want pending", got)
	}
}

// The status is polled during a build, and the script is up to half a mebibyte.
func TestTheStatusDoesNotCarryTheScriptAndTheScriptEndpointDoes(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.template = provisioning.Template{
		Status: provisioning.TemplateReady, Version: 2,
		Database: "game_tpl_cabc", Script: "CREATE TABLE guests (id int);",
	}
	id := uuid.NewString()

	status := decode(t, f.do(http.MethodGet, "/contests/"+id+"/game", ""))
	if _, carried := status["script"]; carried {
		t.Fatal("the status carries the whole script")
	}
	if status["script_bytes"] == nil || status["script_bytes"].(float64) == 0 {
		t.Fatalf("the status does not say there is a script: %v", status)
	}

	script := decode(t, f.do(http.MethodGet, "/contests/"+id+"/game/script", ""))
	if script["script"] != "CREATE TABLE guests (id int);" {
		t.Fatalf("the script endpoint answered %v", script["script"])
	}
}

// A reloaded page must reopen the viewer on the file the game came from.
func TestAFileSourcedGamesStatusNamesItsSourceAndItsUpload(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contestID, uploadID := uuid.New(), uuid.New()
	f.games.template = provisioning.Template{
		ContestID: contestID, Status: provisioning.TemplateReady, Version: 1, Source: provisioning.SourceFile,
		UploadID: &uploadID, Database: "game_tpl_cabc",
	}
	f.games.uploadResult = provisioning.Upload{
		ID: uploadID, Filename: "dump.sql", ReceivedBytes: 4096, Lines: 7,
		Status: provisioning.UploadComplete,
	}

	rec := f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if body["source"] != "file" {
		t.Fatalf("source = %v, want file", body["source"])
	}
	upload, ok := body["upload"].(map[string]any)
	if !ok {
		t.Fatalf("the response carries no upload object: %v", body)
	}
	if upload["id"] != uploadID.String() || upload["filename"] != "dump.sql" {
		t.Fatalf("upload = %v, want id %s and filename dump.sql", upload, uploadID)
	}
	if upload["bytes"] != float64(4096) || upload["lines"] != float64(7) {
		t.Fatalf("upload = %v, want bytes 4096 and lines 7", upload)
	}
	if f.games.gotUploadContest != contestID || f.games.gotUploadID != uploadID {
		t.Fatalf("the upload lookup was scoped to %v/%v, want %v/%v",
			f.games.gotUploadContest, f.games.gotUploadID, contestID, uploadID)
	}
}

// Clients test whether Upload is present.
func TestAnEditorSourcedGamesStatusNamesItsSourceAndCarriesNoUpload(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.template = provisioning.Template{
		Status: provisioning.TemplateReady, Version: 1, Source: provisioning.SourceEditor,
		Database: "game_tpl_cabc", Script: "CREATE TABLE guests (id int);",
	}

	rec := f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game", "")
	body := decode(t, rec)
	if body["source"] != "editor" {
		t.Fatalf("source = %v, want editor", body["source"])
	}
	if _, carried := body["upload"]; carried {
		t.Fatalf("an editor-sourced game carries an upload object: %v", body)
	}
}

// CLAUDE.md rule 11; the values match no default, so answering a constant
// fails.
func TestGameStatusCarriesTheConfiguredUploadLimitsNotAConstant(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.limitsEnabled = true
	f.games.limits = gamefile.Limits{MaxChunkBytes: 1234567, MaxFileBytes: 9876543210, MaxDirBytes: 1 << 40}

	status := decode(t, f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game", ""))
	limits, ok := status["upload_limits"].(map[string]any)
	if !ok {
		t.Fatalf("the status carries no upload_limits object: %v", status)
	}
	if limits["enabled"] != true {
		t.Fatalf("enabled = %v, want true", limits["enabled"])
	}
	if limits["chunk_bytes"] != float64(1234567) {
		t.Fatalf("chunk_bytes = %v, want 1234567", limits["chunk_bytes"])
	}
	if limits["max_file_bytes"] != float64(9876543210) {
		t.Fatalf("max_file_bytes = %v, want 9876543210", limits["max_file_bytes"])
	}
}

// CLAUDE.md rule 11; the editor reads the "absent" answer before its first
// save.
func TestGameStatusCarriesTheScriptCeilingForAGameAndForNoGame(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	absent := decode(t, f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game", ""))
	if absent["status"] != "absent" {
		t.Fatalf("status = %v, want absent", absent["status"])
	}
	if absent["max_script_bytes"] != float64(provisioning.MaxScriptBytes) {
		t.Fatalf("max_script_bytes on an absent game = %v, want %d",
			absent["max_script_bytes"], provisioning.MaxScriptBytes)
	}

	f.games.template = provisioning.Template{
		Status: provisioning.TemplateReady, Version: 1, Source: provisioning.SourceEditor,
		Database: "game_tpl_cabc", Script: "SELECT 1",
	}
	present := decode(t, f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game", ""))
	if present["max_script_bytes"] != float64(provisioning.MaxScriptBytes) {
		t.Fatalf("max_script_bytes = %v, want %d", present["max_script_bytes"], provisioning.MaxScriptBytes)
	}
}

// The interface must tell "uploads are off" from "a limit of zero".
func TestUploadLimitsAreZeroAndDisabledWhenUploadsAreOff(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	status := decode(t, f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game", ""))
	limits, ok := status["upload_limits"].(map[string]any)
	if !ok {
		t.Fatalf("the status carries no upload_limits object: %v", status)
	}
	if limits["enabled"] != false {
		t.Fatalf("enabled = %v, want false", limits["enabled"])
	}
	if limits["chunk_bytes"] != float64(0) || limits["max_file_bytes"] != float64(0) {
		t.Fatalf("limits = %v, want both zero while disabled", limits)
	}
}

// A reloading page needs the ceilings before it knows whether an upload is in
// progress.
func TestCurrentUploadCarriesTheConfiguredUploadLimits(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.currentErr = provisioning.ErrUploadNotFound
	f.games.limitsEnabled = true
	f.games.limits = gamefile.Limits{MaxChunkBytes: 555555, MaxFileBytes: 777777777}

	body := decode(t, f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game/uploads/current", ""))
	if body["status"] != "absent" {
		t.Fatalf("status = %v, want absent", body["status"])
	}
	limits, ok := body["upload_limits"].(map[string]any)
	if !ok {
		t.Fatalf("the response carries no upload_limits object: %v", body)
	}
	if limits["enabled"] != true || limits["chunk_bytes"] != float64(555555) || limits["max_file_bytes"] != float64(777777777) {
		t.Fatalf("upload_limits = %v, want the configured limits", limits)
	}
}

// CLAUDE.md rule 1: the organiser is told what to fix, not "internal error".
func TestEveryGameRefusalHasItsOwnCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"empty script", provisioning.ErrScriptEmpty, http.StatusBadRequest, "game_script_empty"},
		{"oversized script", provisioning.ErrScriptTooLong, http.StatusBadRequest, "game_script_too_long"},
		{"a running contest", provisioning.ErrGameNotEditable, http.StatusConflict, "game_not_editable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGameFixture(t, rbac.PermissionContestAdminAll)
			f.games.setErr = tc.err

			rec := f.do(http.MethodPut, "/contests/"+uuid.NewString()+"/game/script", `{"script":"x"}`)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v — %s", err, rec.Body)
			}
			if body.Error.Code != tc.code {
				t.Fatalf("code %q, want %q", body.Error.Code, tc.code)
			}
		})
	}
}

// The script runs arbitrary SQL as the provisioning role. No contest role
// separates view from edit, so this proves only that a stranger is refused and
// a manager is not.
func TestWritingTheScriptIsRefusedToAnAccountThatIsNotStaffOnTheContest(t *testing.T) {
	f := newGameFixture(t)

	rec := f.do(http.MethodPut, "/contests/"+uuid.NewString()+"/game/script", `{"script":"DROP DATABASE postgres"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body)
	}
	if f.games.gotSet != "" {
		t.Fatal("the script reached the service despite the refusal")
	}
}

func TestAManagerOfTheContestMayWriteItsScript(t *testing.T) {
	f := newGameFixture(t)
	contest := uuid.New()
	if err := f.stores.Managers.Grant(context.Background(), contests.Manager{
		ContestID: contest, UserID: f.actor.ID, Role: rbac.RoleManager,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	rec := f.do(http.MethodPut, "/contests/"+contest.String()+"/game/script", `{"script":"SELECT 1"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body)
	}
}

func TestGameEndpointsRefuseAContestIdentifierThatIsNotAUUID(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	if rec := f.do(http.MethodGet, "/contests/not-a-uuid/game", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func registrationID(id uuid.UUID) *uuid.UUID { return &id }

func TestTheInstanceListNamesTheHolderOfEveryCopy(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	held := uuid.New()
	f.databases.list = provisioning.InstanceList{
		Instances: []provisioning.InstanceRecord{
			{
				Database: "game_c1_u1", Registration: registrationID(held),
				ParticipantLogin: "ivan", ParticipantName: "Ivan Petrov",
				TemplateVersion: 2, Status: "ready",
				SizeBytes: 4 << 20, SizeKnown: true,
				CreatedAt: time.Now(), UpdatedAt: time.Now(),
			},
			{Database: "game_pool_c1_a", TemplateVersion: 2, Status: "ready"},
		},
		Truncated: true,
	}

	rec := f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game/instances", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	body := decode(t, rec)
	if body["truncated"] != true {
		t.Fatalf("truncated came back %v; the screen must be able to say there are more", body["truncated"])
	}
	rows, ok := body["instances"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("instances came back %v", body["instances"])
	}

	first := rows[0].(map[string]any)
	if first["database"] != "game_c1_u1" || first["participant"] != "ivan" {
		t.Fatalf("the held copy came back %v", first)
	}
	if first["spare"] != false {
		t.Fatalf("a claimed copy is reported as spare: %v", first)
	}
	if first["registration_id"] != held.String() {
		t.Fatalf("registration_id came back %v, want %v", first["registration_id"], held)
	}
	if first["size_bytes"].(float64) != float64(4<<20) || first["size_known"] != true {
		t.Fatalf("the size came back %v / %v", first["size_bytes"], first["size_known"])
	}

	second := rows[1].(map[string]any)
	if second["spare"] != true {
		t.Fatalf("the unclaimed copy is not reported as spare: %v", second)
	}
	if second["participant"] != "" {
		t.Fatalf("a spare names %v as its holder", second["participant"])
	}
	// A size the cluster could not give must not read as a database of zero
	// bytes, which on this screen would mean "empty" rather than "unknown".
	if second["size_known"] != false {
		t.Fatalf("a size nobody read is reported as known: %v", second)
	}
}

func TestDroppingADatabaseReachesTheServiceWithTheActorAndTheContest(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.New()

	rec := f.do(http.MethodDelete, "/contests/"+contest.String()+"/game/instances/game_c1_u1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	if f.databases.droppedDB != "game_c1_u1" {
		t.Fatalf("the service was asked about %q", f.databases.droppedDB)
	}
	if f.databases.dropActor != f.actor.ID {
		t.Fatal("the actor did not reach the service, so nothing could be recorded against them")
	}
	if f.databases.dropTarget != contest {
		t.Fatalf("the contest reached the service as %v, want %v", f.databases.dropTarget, contest)
	}
	if decode(t, rec)["status"] != "dropped" {
		t.Fatalf("the answer does not say the database is gone: %s", rec.Body)
	}
}

// CLAUDE.md rule 1: a stale page is told which of the two things happened.
func TestEveryInstanceRefusalHasItsOwnCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"another contest's database", provisioning.ErrInstanceNotFound, http.StatusNotFound, "game_instance_not_found"},
		{"already gone", provisioning.ErrInstanceAlreadyDropped, http.StatusConflict, "game_instance_already_dropped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGameFixture(t, rbac.PermissionContestAdminAll)
			f.databases.dropErr = tc.err

			rec := f.do(http.MethodDelete, "/contests/"+uuid.NewString()+"/game/instances/game_c1_u1", "")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if code := errorCode(t, rec); code != tc.code {
				t.Fatalf("code %q, want %q", code, tc.code)
			}
		})
	}
}

// As for the script, no role separates view from edit.
func TestDroppingADatabaseIsRefusedToAnAccountThatIsNotStaffOnTheContest(t *testing.T) {
	f := newGameFixture(t)

	rec := f.do(http.MethodDelete, "/contests/"+uuid.NewString()+"/game/instances/game_c1_u1", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body)
	}
	if f.databases.droppedDB != "" {
		t.Fatalf("%q reached the service despite the refusal", f.databases.droppedDB)
	}
}

func TestAManagerOfTheContestMayDropOneOfItsDatabases(t *testing.T) {
	f := newGameFixture(t)
	contest := uuid.New()
	if err := f.stores.Managers.Grant(context.Background(), contests.Manager{
		ContestID: contest, UserID: f.actor.ID, Role: rbac.RoleManager,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	rec := f.do(http.MethodDelete, "/contests/"+contest.String()+"/game/instances/game_c1_u1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestTheInstanceListIsRefusedToAnAccountThatIsNotStaffOnTheContest(t *testing.T) {
	f := newGameFixture(t)

	rec := f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game/instances", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body)
	}
}

// --- Uploading a finished dump ----------------------------------------------

func TestBeginningAnUploadReservesOneAndReturnsIt(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.NewString()
	f.games.beginResult = provisioning.Upload{
		ID: uuid.New(), Filename: "dump.sql", DeclaredBytes: 12345,
		Status: provisioning.UploadReceiving,
	}

	rec := f.do(http.MethodPost, "/contests/"+contest+"/game/uploads",
		`{"filename":"dump.sql","declared_bytes":12345}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body)
	}
	if f.games.gotBeginName != "dump.sql" || f.games.gotBeginBytes != 12345 {
		t.Fatalf("the service was asked to begin %q / %d", f.games.gotBeginName, f.games.gotBeginBytes)
	}
	if f.games.gotBeginContest.String() != contest {
		t.Fatalf("the contest reached the service as %v, want %v", f.games.gotBeginContest, contest)
	}
	body := decode(t, rec)
	if body["status"] != "receiving" || body["filename"] != "dump.sql" {
		t.Fatalf("answered %v", body)
	}
}

// Each request targets a different contest, so only the address budget can
// refuse the twenty-first.
func TestBeginningAnUploadIsRateLimitedPerAddressAcrossContests(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	for i := range maxUploadBeginsPerAddressInTest {
		rec := f.beginUploadFrom(oneOffice, uuid.NewString())
		if rec.Code != http.StatusCreated {
			t.Fatalf("request %d of the address's own budget answered %d: %s", i+1, rec.Code, rec.Body)
		}
	}

	last := f.beginUploadFrom(oneOffice, uuid.NewString())
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429: %s", last.Code, last.Body)
	}
	if code := errorCode(t, last); code != "game_upload_too_often" {
		t.Fatalf("code %q, want game_upload_too_often", code)
	}
	if !strings.Contains(last.Body.String(), "from this address") {
		t.Fatalf("the refusal does not name the address budget: %s", last.Body)
	}
}

func TestBeginningAnUploadLimitsAnIPv6NetworkAsOneAddress(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	for i := range maxUploadBeginsPerAddressInTest {
		rec := f.beginUploadFrom(fmt.Sprintf("[2001:db8:1:2::%x]:1234", i+1), uuid.NewString())
		if rec.Code != http.StatusCreated {
			t.Fatalf("request %d answered %d: %s", i+1, rec.Code, rec.Body)
		}
	}
	last := f.beginUploadFrom("[2001:db8:1:2::ffff]:1234", uuid.NewString())
	if last.Code != http.StatusTooManyRequests || !strings.Contains(last.Body.String(), "from this address") {
		t.Fatalf("another host of the same /64: status %d, want the address budget's 429: %s", last.Code, last.Body)
	}
}

// Every request comes from a different address, so the ninth refusal can only
// be the contest's.
func TestBeginningAnUploadIsRateLimitedPerContestAcrossAddresses(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.NewString()

	for i := range maxUploadBeginsPerContestInTest {
		rec := f.beginUploadFrom(fmt.Sprintf("198.51.100.%d:5000", i+1), contest)
		if rec.Code != http.StatusCreated {
			t.Fatalf("request %d of the contest's own budget answered %d: %s", i+1, rec.Code, rec.Body)
		}
	}

	last := f.beginUploadFrom("198.51.100.200:5000", contest)
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429: %s", last.Code, last.Body)
	}
	if !strings.Contains(last.Body.String(), "for this contest") {
		t.Fatalf("the refusal does not name the contest budget: %s", last.Body)
	}
}

// CLAUDE.md rule 5. Both keys answer alike, so the order shows in the contest
// budget afterwards.
func TestTheAddressBudgetIsSpentBeforeTheContestsOwn(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	for range maxUploadBeginsPerAddressInTest {
		if rec := f.beginUploadFrom(oneOffice, uuid.NewString()); rec.Code != http.StatusCreated {
			t.Fatalf("filling the address budget answered %d: %s", rec.Code, rec.Body)
		}
	}

	// Refused by the address. Checked contest-first, this would have spent one
	// of the eight below.
	victim := uuid.NewString()
	if rec := f.beginUploadFrom(oneOffice, victim); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429: %s", rec.Code, rec.Body)
	}

	for i := range maxUploadBeginsPerContestInTest {
		rec := f.beginUploadFrom(fmt.Sprintf("203.0.113.%d:5000", i+1), victim)
		if rec.Code != http.StatusCreated {
			t.Fatalf("begin %d of 8 for the contest answered %d — the refused request spent one: %s",
				i+1, rec.Code, rec.Body)
		}
	}
}

// RequireContestPermission refuses before the handler, so neither the limiter
// nor the service is reached.
func TestBeginningAnUploadIsRefusedToAnAccountThatIsNotStaffOnTheContest(t *testing.T) {
	f := newGameFixture(t)

	rec := f.do(http.MethodPost, "/contests/"+uuid.NewString()+"/game/uploads",
		`{"filename":"dump.sql","declared_bytes":1}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body)
	}
	if f.games.gotBeginName != "" {
		t.Fatal("the request reached the service despite the refusal")
	}
}

// CLAUDE.md rule 12: the fake records how much of the body was read on entry,
// zero only when streamed.
func TestAppendingAChunkStreamsTheBodyToTheService(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest, upload := uuid.NewString(), uuid.NewString()
	f.games.appendResult = 19

	const payload = "CREATE TABLE t (id int);"
	meter := &readMeter{r: strings.NewReader(payload)}
	f.games.bodyMeter = meter

	rec := f.doBody(http.MethodPut,
		"/contests/"+contest+"/game/uploads/"+upload+"/chunk?offset=7", meter)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	if f.games.gotAppendBodyReadOnEntry != 0 {
		t.Fatalf("%d of the chunk's %d bytes were already in memory before AppendChunk was called",
			f.games.gotAppendBodyReadOnEntry, len(payload))
	}
	if meter.read != int64(len(payload)) {
		t.Fatalf("the service drew %d bytes from the body, want %d", meter.read, len(payload))
	}
	if f.games.gotAppendOffset != 7 {
		t.Fatalf("offset reached the service as %d, want 7", f.games.gotAppendOffset)
	}
	if string(f.games.gotAppendBodyBytes) != "CREATE TABLE t (id int);" {
		t.Fatalf("the service received %q", f.games.gotAppendBodyBytes)
	}
	if f.games.gotAppendContest.String() != contest || f.games.gotAppendUpload.String() != upload {
		t.Fatalf("scoped to %v/%v, want %v/%v", f.games.gotAppendContest, f.games.gotAppendUpload, contest, upload)
	}
	if decode(t, rec)["received_bytes"] != float64(19) {
		t.Fatalf("received_bytes came back %v", decode(t, rec)["received_bytes"])
	}
}

func TestAppendingAChunkWithoutANumericOffsetIsRefused(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	rec := f.do(http.MethodPut,
		"/contests/"+uuid.NewString()+"/game/uploads/"+uuid.NewString()+"/chunk?offset=not-a-number",
		"x")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
	}
	if f.games.gotAppendBodyBytes != nil {
		t.Fatal("the body reached the service despite the malformed offset")
	}
}

// Refused as ErrUploadChunkTooLarge through a real store (CLAUDE.md rule 10),
// with both numbers equal as app.go sets them: only the probe read sees the
// excess, which an io.ReadAll double never meets.
func TestAppendingAChunkOverTheTransportCeilingIsRefused(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest, upload := uuid.New(), uuid.New()
	limits := gamefile.Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 1 << 20, MaxChunkBytes: 8}
	var store *gamefile.Store
	f.games.uploads, store = realUploadService(t, contest, upload, limits)
	f.handler.WithMaxChunkBody(8)

	rec := f.do(http.MethodPut,
		"/contests/"+contest.String()+"/game/uploads/"+upload.String()+"/chunk?offset=0",
		"123456789") // 9 bytes, one past the 8-byte ceiling
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "game_upload_chunk_too_large" {
		t.Fatalf("code %q, want game_upload_chunk_too_large", code)
	}
	received, err := store.Received(upload.String())
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if received != 0 {
		t.Fatalf("the store kept %d bytes of a chunk it refused, want 0", received)
	}
}

// The listener's ReadTimeout is sized for JSON, so this route sets its own read
// deadline for multi-megabyte chunks; proven across a real listener (CLAUDE.md
// rule 10).
func TestASlowChunkBodyIsNotCutOffByTheListenersReadTimeout(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.appendResult = 4

	srv := httptest.NewUnstartedServer(f.router)
	srv.Config.ReadTimeout = 100 * time.Millisecond // stands in for the listener's 30 s
	srv.Start()
	defer srv.Close()

	body, writes := io.Pipe()
	go func() {
		_, _ = writes.Write([]byte("ab"))
		time.Sleep(400 * time.Millisecond) // a stall the listener's own timeout would cut
		_, _ = writes.Write([]byte("cd"))
		_ = writes.Close()
	}()

	url := srv.URL + "/contests/" + uuid.NewString() + "/game/uploads/" + uuid.NewString() + "/chunk?offset=0"
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, url, body)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.AddCookie(f.cookie)

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("the chunk request failed outright: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 — the listener's own read timeout cut the body off", resp.StatusCode)
	}
	if string(f.games.gotAppendBodyBytes) != "abcd" {
		t.Fatalf("the service received %q, want the whole slow body", f.games.gotAppendBodyBytes)
	}
}

// CLAUDE.md rule 1: its own sentinel and code, never a 500.
func TestAppendingAChunkOutOfOrderNamesItsOwnCode(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.appendErr = provisioning.ErrUploadChunkOutOfOrder

	rec := f.do(http.MethodPut,
		"/contests/"+uuid.NewString()+"/game/uploads/"+uuid.NewString()+"/chunk?offset=5",
		"x")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "game_upload_chunk_out_of_order" {
		t.Fatalf("code %q, want game_upload_chunk_out_of_order", code)
	}
}

// The same "absent" shape status() uses for a contest with no game.
func TestCurrentUploadReportsAbsentWhenThereIsNone(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.currentErr = provisioning.ErrUploadNotFound

	rec := f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game/uploads/current", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	if decode(t, rec)["status"] != "absent" {
		t.Fatalf("answered %v, want absent", decode(t, rec))
	}
}

func TestCurrentUploadReturnsTheInProgressOne(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.currentResult = provisioning.Upload{
		ID: uuid.New(), Filename: "dump.sql", ReceivedBytes: 40, Status: provisioning.UploadReceiving,
	}

	rec := f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game/uploads/current", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if body["status"] != "receiving" || body["received_bytes"] != float64(40) {
		t.Fatalf("answered %v", body)
	}
}

// The same path as SetScript, so the same answer shape and status.
func TestCompletingAnUploadReplacesTheGame(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest, upload := uuid.NewString(), uuid.NewString()
	f.games.completeResult = provisioning.Template{
		Database: "game_tpl_cabc", Version: 3, Status: provisioning.TemplatePending,
	}

	rec := f.do(http.MethodPost, "/contests/"+contest+"/game/uploads/"+upload+"/complete", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body)
	}
	if f.games.gotCompleteActor != f.actor.ID {
		t.Fatal("the actor did not reach the service, so nothing could be recorded against them")
	}
	if f.games.gotCompleteContest.String() != contest || f.games.gotCompleteUpload.String() != upload {
		t.Fatalf("scoped to %v/%v, want %v/%v", f.games.gotCompleteContest, f.games.gotCompleteUpload, contest, upload)
	}
	if decode(t, rec)["status"] != "pending" {
		t.Fatalf("answered %v, want pending", decode(t, rec))
	}
}

func TestAbortingAnUploadCancelsIt(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest, upload := uuid.NewString(), uuid.NewString()
	f.games.abortResult = provisioning.Upload{ID: uuid.MustParse(upload), Status: provisioning.UploadAborted}

	rec := f.do(http.MethodPost, "/contests/"+contest+"/game/uploads/"+upload+"/abort", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	if f.games.gotAbortActor != f.actor.ID {
		t.Fatal("the actor did not reach the service, so nothing could be recorded against them")
	}
	if f.games.gotAbortContest.String() != contest || f.games.gotAbortUpload.String() != upload {
		t.Fatalf("scoped to %v/%v, want %v/%v", f.games.gotAbortContest, f.games.gotAbortUpload, contest, upload)
	}
	if decode(t, rec)["status"] != "aborted" {
		t.Fatalf("answered %v, want aborted", decode(t, rec))
	}
}

func TestUploadWindowPastTheEndIsAnEmptyWindowNotAnError(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.windowResult = gamefile.Window{FromLine: 10_000, TotalLines: 40}

	rec := f.do(http.MethodGet,
		"/contests/"+uuid.NewString()+"/game/uploads/"+uuid.NewString()+"/window?from=10000", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if lines, ok := body["lines"].([]any); !ok || len(lines) != 0 {
		t.Fatalf("lines came back %v, want an empty list", body["lines"])
	}
	if body["total_lines"] != float64(40) {
		t.Fatalf("total_lines came back %v, want 40", body["total_lines"])
	}
}

// CLAUDE.md rule 2: a query string must not decide how much memory a request
// holds.
func TestUploadWindowClampsCallerSuppliedBudgets(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	rec := f.do(http.MethodGet,
		"/contests/"+uuid.NewString()+"/game/uploads/"+uuid.NewString()+"/window?max_lines=999999999&max_bytes=999999999999", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if f.games.gotWindowLines != 1000 {
		t.Fatalf("max_lines reached the service as %d, want the 1000-line ceiling", f.games.gotWindowLines)
	}
	if f.games.gotWindowBytes != 1<<20 {
		t.Fatalf("max_bytes reached the service as %d, want the 1 MiB ceiling", f.games.gotWindowBytes)
	}
}

// fail is one switch for every route, so one endpoint covers
// provisioning/upload.go's sentinels.
func TestEveryUploadRefusalHasItsOwnCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"uploads not configured on this installation", provisioning.ErrUploadsDisabled, http.StatusNotFound, "game_uploads_disabled"},
		{"an invalid filename", provisioning.ErrUploadFilenameInvalid, http.StatusBadRequest, "game_upload_filename_invalid"},
		{"a declared size over the limit", provisioning.ErrUploadTooLarge, http.StatusBadRequest, "game_upload_too_large"},
		{"the upload directory is full", provisioning.ErrUploadStoreFull, http.StatusConflict, "game_upload_store_full"},
		{"a chunk sent out of order", provisioning.ErrUploadChunkOutOfOrder, http.StatusConflict, "game_upload_chunk_out_of_order"},
		{"a chunk over the domain's own limit", provisioning.ErrUploadChunkTooLarge, http.StatusBadRequest, "game_upload_chunk_too_large"},
		{"a chunk body that stopped arriving", provisioning.ErrUploadChunkIncomplete, http.StatusRequestTimeout, "game_upload_chunk_incomplete"},
		{"received bytes short of the declared length", provisioning.ErrUploadLengthMismatch, http.StatusConflict, "game_upload_length_mismatch"},
		{"another contest's upload", provisioning.ErrUploadNotFound, http.StatusNotFound, "game_upload_not_found"},
		{"a second upload while one is already receiving", provisioning.ErrUploadInProgress, http.StatusConflict, "game_upload_in_progress"},
		{"an upload already sealed or cancelled", provisioning.ErrUploadAlreadyComplete, http.StatusConflict, "game_upload_already_complete"},
		{"a window read before the upload was completed", provisioning.ErrUploadIncomplete, http.StatusConflict, "game_upload_incomplete"},
		{"a line index that no longer matches its data", provisioning.ErrUploadIndexCorrupt, http.StatusConflict, "game_upload_index_corrupt"},
		{"a line too far past its index mark to walk to", provisioning.ErrUploadWindowUnreachable, http.StatusUnprocessableEntity, "game_upload_window_unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGameFixture(t, rbac.PermissionContestAdminAll)
			f.games.appendErr = tc.err

			rec := f.do(http.MethodPut,
				"/contests/"+uuid.NewString()+"/game/uploads/"+uuid.NewString()+"/chunk?offset=0", "x")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if code := errorCode(t, rec); code != tc.code {
				t.Fatalf("code %q, want %q", code, tc.code)
			}
		})
	}
}

// beginUpload is tested above; this covers the other writes and the two reads.
func TestUploadEndpointsAreRefusedToAnAccountThatIsNotStaffOnTheContest(t *testing.T) {
	upload := uuid.NewString()
	for _, tc := range []struct {
		name       string
		method     string
		pathSuffix string
	}{
		{"feeding a chunk", http.MethodPut, "/game/uploads/" + upload + "/chunk?offset=0"},
		{"completing", http.MethodPost, "/game/uploads/" + upload + "/complete"},
		{"cancelling", http.MethodPost, "/game/uploads/" + upload + "/abort"},
		{"reading the window", http.MethodGet, "/game/uploads/" + upload + "/window"},
		{"reading the current upload", http.MethodGet, "/game/uploads/current"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGameFixture(t)
			contest := uuid.NewString()

			rec := f.do(tc.method, "/contests/"+contest+tc.pathSuffix, "x")
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body)
			}
		})
	}
}

// Only a permitted session reaching each handler's own success shape, not chi's
// 404, proves Mount holds all six upload routes.
func TestGameUploadRoutesAreMounted(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest, upload := uuid.NewString(), uuid.New()
	f.games.abortResult = provisioning.Upload{ID: upload}

	for _, tc := range []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"current", http.MethodGet, "/game/uploads/current", http.StatusOK},
		{"begin", http.MethodPost, "/game/uploads", http.StatusCreated},
		{"chunk", http.MethodPut, "/game/uploads/" + upload.String() + "/chunk?offset=0", http.StatusOK},
		{"complete", http.MethodPost, "/game/uploads/" + upload.String() + "/complete", http.StatusAccepted},
		{"abort", http.MethodPost, "/game/uploads/" + upload.String() + "/abort", http.StatusOK},
		{"window", http.MethodGet, "/game/uploads/" + upload.String() + "/window", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := ""
			if tc.method == http.MethodPost && tc.name == "begin" {
				body = `{"filename":"dump.sql","declared_bytes":1}`
			}
			if tc.method == http.MethodPut {
				body = "x"
			}
			rec := f.do(tc.method, "/contests/"+contest+tc.path, body)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

// --- The table builder: a structural description instead of SQL -----------

func TestDefinitionRoundTripsThroughGetAndPut(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.NewString()

	body := `{"tables":[{"name":"suspects","columns":[
		{"name":"id","type":"integer"},
		{"name":"nickname","type":"text","nullable":true}
	],"primary_key":["id"]}]}`

	rec := f.do(http.MethodPut, "/contests/"+contest+"/game/definition", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body)
	}
	if f.games.gotSetDefinitionFor != f.actor.ID {
		t.Fatal("the actor did not reach the service, so nothing could be recorded against them")
	}
	if len(f.games.gotSetDefinition.Tables) != 1 || f.games.gotSetDefinition.Tables[0].Name != "suspects" {
		t.Fatalf("the service was given %+v", f.games.gotSetDefinition)
	}
	if got := f.games.gotSetDefinition.Tables[0].Columns[1]; got.Name != "nickname" || !got.Nullable {
		t.Fatalf("the nullable column came back as %+v", got)
	}

	read := decode(t, f.do(http.MethodGet, "/contests/"+contest+"/game/definition", ""))
	tables, ok := read["tables"].([]any)
	if !ok || len(tables) != 1 {
		t.Fatalf("tables came back %v", read["tables"])
	}
	table := tables[0].(map[string]any)
	if table["name"] != "suspects" {
		t.Fatalf("table name came back %v", table["name"])
	}
	columns := table["columns"].([]any)
	if len(columns) != 2 || columns[0].(map[string]any)["type"] != "integer" {
		t.Fatalf("columns came back %v", columns)
	}
}

// The same "absent" shape status() gives.
func TestDefinitionOfANonBuilderGameComesBackEmptyNotAnError(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.template = provisioning.Template{
		Status: provisioning.TemplateReady, Source: provisioning.SourceEditor, Script: "SELECT 1",
	}

	read := decode(t, f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game/definition", ""))
	tables, ok := read["tables"].([]any)
	if !ok || len(tables) != 0 {
		t.Fatalf("tables came back %v, want an empty list", read["tables"])
	}
}

// ErrDefinitionTableLocked is a 409, "delete the rows first", not a 400 "fix
// the form".
func TestEveryDefinitionRefusalHasItsOwnCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"no tables at all", provisioning.ErrDefinitionEmpty, http.StatusBadRequest, "game_definition_empty"},
		{"past the size limits", provisioning.ErrDefinitionTooLarge, http.StatusBadRequest, "game_definition_too_large"},
		{"not a plain identifier", provisioning.ErrDefinitionInvalidName, http.StatusBadRequest, "game_definition_invalid_name"},
		{"a table or column named twice", provisioning.ErrDefinitionDuplicateName, http.StatusBadRequest, "game_definition_duplicate_name"},
		{"a table with no columns", provisioning.ErrDefinitionTableEmpty, http.StatusBadRequest, "game_definition_table_empty"},
		{"a type outside the closed set", provisioning.ErrDefinitionInvalidType, http.StatusBadRequest, "game_definition_invalid_type"},
		{"a primary key naming a missing column", provisioning.ErrDefinitionInvalidPrimaryKey, http.StatusBadRequest, "game_definition_invalid_primary_key"},
		// The one refusal here that is not "the form is wrong".
		{"a table with data whose structure would change", provisioning.ErrDefinitionTableLocked, http.StatusConflict, "game_definition_table_locked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGameFixture(t, rbac.PermissionContestAdminAll)
			f.games.setDefinitionErr = tc.err

			rec := f.do(http.MethodPut, "/contests/"+uuid.NewString()+"/game/definition",
				`{"tables":[{"name":"t","columns":[{"name":"c","type":"text"}]}]}`)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if code := errorCode(t, rec); code != tc.code {
				t.Fatalf("code %q, want %q", code, tc.code)
			}
		})
	}
}

func TestWritingTheDefinitionIsRefusedToAnAccountThatIsNotStaffOnTheContest(t *testing.T) {
	f := newGameFixture(t)

	rec := f.do(http.MethodPut, "/contests/"+uuid.NewString()+"/game/definition", `{"tables":[]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body)
	}
	if f.games.gotSetDefinition.Tables != nil {
		t.Fatal("the definition reached the service despite the refusal")
	}
}

// CLAUDE.md rule 11, for the table builder's ceilings.
func TestGameStatusCarriesBuilderLimits(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	status := decode(t, f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game", ""))
	limits, ok := status["builder_limits"].(map[string]any)
	if !ok {
		t.Fatalf("the status carries no builder_limits object: %v", status)
	}
	if limits["max_tables"] != float64(provisioning.MaxDefinitionTables) {
		t.Fatalf("max_tables = %v, want %d", limits["max_tables"], provisioning.MaxDefinitionTables)
	}
	types, ok := limits["column_types"].([]any)
	if !ok || len(types) != len(provisioning.ColumnTypes) {
		t.Fatalf("column_types = %v", limits["column_types"])
	}
}

// --- The table builder: one table's own CSV data ---------------------------

func TestBeginningATableUploadReachesTheServiceAndReturnsIt(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.NewString()
	f.games.beginTableResult = provisioning.TableData{
		ID: uuid.New(), Table: "suspects", DeclaredBytes: 512, Status: provisioning.TableDataReceiving,
	}

	rec := f.do(http.MethodPost, "/contests/"+contest+"/game/tables/suspects/data", `{"declared_bytes":512}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body)
	}
	if f.games.gotBeginTableTable != "suspects" || f.games.gotBeginTableBytes != 512 {
		t.Fatalf("the service was asked to begin %q / %d", f.games.gotBeginTableTable, f.games.gotBeginTableBytes)
	}
	if f.games.gotBeginTableContest.String() != contest {
		t.Fatalf("the contest reached the service as %v, want %v", f.games.gotBeginTableContest, contest)
	}
	body := decode(t, rec)
	if body["status"] != "receiving" || body["table"] != "suspects" {
		t.Fatalf("answered %v", body)
	}
	if _, ok := body["builder_limits"]; !ok {
		t.Fatalf("the response carries no builder_limits object: %v", body)
	}
}

func TestAppendingATableChunkStreamsTheBodyToTheService(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest, dataID := uuid.NewString(), uuid.NewString()
	f.games.appendTableResult = 24

	const payload = "id,nickname\n1,Ann\n"
	meter := &readMeter{r: strings.NewReader(payload)}
	f.games.tableBodyMeter = meter

	rec := f.doBody(http.MethodPut,
		"/contests/"+contest+"/game/tables/suspects/data/"+dataID+"/chunk?offset=0", meter)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	if f.games.gotAppendTableBodyReadOnEntry != 0 {
		t.Fatalf("%d of the chunk's %d bytes were already in memory before AppendTableChunk was called",
			f.games.gotAppendTableBodyReadOnEntry, len(payload))
	}
	if string(f.games.gotAppendTableBodyBytes) != payload {
		t.Fatalf("the service received %q", f.games.gotAppendTableBodyBytes)
	}
	if f.games.gotAppendTableContest.String() != contest || f.games.gotAppendTableDataID.String() != dataID {
		t.Fatalf("scoped to %v/%v, want %v/%v", f.games.gotAppendTableContest, f.games.gotAppendTableDataID, contest, dataID)
	}
	if decode(t, rec)["received_bytes"] != float64(24) {
		t.Fatalf("received_bytes came back %v", decode(t, rec)["received_bytes"])
	}
}

// Through a real store, as for the dump (CLAUDE.md rule 10).
func TestAppendingATableChunkOverTheTransportCeilingIsRefused(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest, dataID := uuid.New(), uuid.New()
	limits := gamefile.Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 1 << 20, MaxChunkBytes: 8}
	var store *gamefile.Store
	f.games.tableData, store = realTableDataService(t, contest, dataID, "suspects", limits)
	f.handler.WithMaxTableChunkBody(8)

	rec := f.do(http.MethodPut,
		"/contests/"+contest.String()+"/game/tables/suspects/data/"+dataID.String()+"/chunk?offset=0",
		"123456789") // 9 bytes, one past the 8-byte ceiling
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "game_table_data_chunk_too_large" {
		t.Fatalf("code %q, want game_table_data_chunk_too_large", code)
	}
	received, err := store.Received(dataID.String())
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if received != 0 {
		t.Fatalf("the store kept %d bytes of a chunk it refused, want 0", received)
	}
}

func TestCurrentTableDataReturnsTheInProgressOne(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.currentTableResult = provisioning.TableData{
		ID: uuid.New(), Table: "suspects", ReceivedBytes: 40, Status: provisioning.TableDataReceiving,
	}
	contest := uuid.NewString()

	rec := f.do(http.MethodGet, "/contests/"+contest+"/game/tables/suspects/data/current", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if f.games.gotCurrentTableContest.String() != contest || f.games.gotCurrentTableTable != "suspects" {
		t.Fatalf("scoped to %v/%q, want %v/suspects", f.games.gotCurrentTableContest, f.games.gotCurrentTableTable, contest)
	}
	body := decode(t, rec)
	if body["status"] != "receiving" || body["received_bytes"] != float64(40) {
		t.Fatalf("answered %v", body)
	}
}

func TestCurrentTableDataReportsAbsentWhenThereIsNone(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.currentTableErr = provisioning.ErrTableDataNotFound

	rec := f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game/tables/suspects/data/current", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if body["status"] != "absent" {
		t.Fatalf("answered %v, want absent", body)
	}
	if body["table"] != "suspects" {
		t.Fatalf("answered %v, want table %q", body, "suspects")
	}
	if _, ok := body["builder_limits"]; !ok {
		t.Fatalf("the response carries no builder_limits object: %v", body)
	}
}

// A table no longer in the definition is a refusal, not the "absent" answer.
func TestCurrentTableDataForAnUnknownTableIsRefused(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.currentTableErr = provisioning.ErrTableUnknown

	rec := f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game/tables/ghosts/data/current", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "game_table_unknown" {
		t.Fatalf("code %q, want game_table_unknown", code)
	}
}

// A body well inside the dump's 64 MiB default is still refused by a smaller
// table ceiling.
func TestATableChunkCeilingIsIndependentOfTheDumpsOwn(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.handler.WithMaxTableChunkBody(4)
	// The dump's own ceiling is left at its 64 MiB default throughout.

	rec := f.do(http.MethodPut,
		"/contests/"+uuid.NewString()+"/game/tables/suspects/data/"+uuid.NewString()+"/chunk?offset=0",
		"12345") // 5 bytes, one past the table's own 4-byte ceiling
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "game_table_data_chunk_too_large" {
		t.Fatalf("code %q, want game_table_data_chunk_too_large", code)
	}
}

func TestCompletingATableUploadReturnsTheRow(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest, dataID := uuid.NewString(), uuid.NewString()
	f.games.completeTableResult = provisioning.TableData{
		ID: uuid.MustParse(dataID), Table: "suspects", Lines: 3, Status: provisioning.TableDataComplete,
	}

	rec := f.do(http.MethodPost, "/contests/"+contest+"/game/tables/suspects/data/"+dataID+"/complete", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	if f.games.gotCompleteTableActor != f.actor.ID {
		t.Fatal("the actor did not reach the service")
	}
	if f.games.gotCompleteTableContest.String() != contest || f.games.gotCompleteTableDataID.String() != dataID {
		t.Fatalf("scoped to %v/%v, want %v/%v", f.games.gotCompleteTableContest, f.games.gotCompleteTableDataID, contest, dataID)
	}
	body := decode(t, rec)
	if body["status"] != "complete" || body["lines"] != float64(3) {
		t.Fatalf("answered %v", body)
	}
}

func TestAbortingATableUploadCancelsIt(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest, dataID := uuid.NewString(), uuid.NewString()
	f.games.abortTableResult = provisioning.TableData{ID: uuid.MustParse(dataID), Status: provisioning.TableDataAborted}

	rec := f.do(http.MethodPost, "/contests/"+contest+"/game/tables/suspects/data/"+dataID+"/abort", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	if f.games.gotAbortTableActor != f.actor.ID {
		t.Fatal("the actor did not reach the service")
	}
	if decode(t, rec)["status"] != "aborted" {
		t.Fatalf("answered %v, want aborted", decode(t, rec))
	}
}

func TestTableDataWindowPastTheEndIsAnEmptyWindowNotAnError(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.windowTableResult = provisioning.TableRowWindow{FromRow: 10_000, TotalRows: 3}

	rec := f.do(http.MethodGet,
		"/contests/"+uuid.NewString()+"/game/tables/suspects/data/window?from=10000", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if rows, ok := body["rows"].([]any); !ok || len(rows) != 0 {
		t.Fatalf("rows came back %v, want an empty list", body["rows"])
	}
	if body["total_rows"] != float64(3) {
		t.Fatalf("total_rows came back %v, want 3", body["total_rows"])
	}
}

// CLAUDE.md rule 2, as for the dump's window.
func TestTableDataWindowClampsCallerSuppliedBudgets(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	rec := f.do(http.MethodGet,
		"/contests/"+uuid.NewString()+"/game/tables/suspects/data/window?max_rows=999999999&max_bytes=999999999999", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if f.games.gotWindowTableMaxRows != maxTableWindowRowsInTest {
		t.Fatalf("max_rows reached the service as %d, want the %d-row ceiling", f.games.gotWindowTableMaxRows, maxTableWindowRowsInTest)
	}
	if f.games.gotWindowTableMaxBytes != maxTableWindowBytesInTest {
		t.Fatalf("max_bytes reached the service as %d, want the %d-byte ceiling", f.games.gotWindowTableMaxBytes, maxTableWindowBytesInTest)
	}
}

// Copies of tableDataWindow's unexported ceilings.
const (
	maxTableWindowRowsInTest  = 1000
	maxTableWindowBytesInTest = 1 << 20
)

func TestAppendingATableRowReachesTheServiceAndReturnsIt(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.NewString()
	f.games.appendRowResult = provisioning.TableData{Table: "suspects", Lines: 1, Status: provisioning.TableDataComplete}

	rec := f.do(http.MethodPost, "/contests/"+contest+"/game/tables/suspects/rows", `{"values":["1","Ann"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body)
	}
	if f.games.gotAppendRowActor != f.actor.ID {
		t.Fatal("the actor did not reach the service")
	}
	if f.games.gotAppendRowTable != "suspects" || len(f.games.gotAppendRowValues) != 2 {
		t.Fatalf("the service received table %q, values %v", f.games.gotAppendRowTable, f.games.gotAppendRowValues)
	}
	if f.games.gotAppendRowContest.String() != contest {
		t.Fatalf("the contest reached the service as %v, want %v", f.games.gotAppendRowContest, contest)
	}
}

func TestDeletingATableRowSucceedsWithNoContent(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.NewString()

	rec := f.do(http.MethodDelete, "/contests/"+contest+"/game/tables/suspects/rows/3", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204: %s", rec.Code, rec.Body)
	}
	if f.games.gotDeleteRowActor != f.actor.ID {
		t.Fatal("the actor did not reach the service")
	}
	if f.games.gotDeleteRowTable != "suspects" || f.games.gotDeleteRowRow != 3 {
		t.Fatalf("the service was asked to delete table %q row %d", f.games.gotDeleteRowTable, f.games.gotDeleteRowRow)
	}
}

func TestDeletingATableRowWithAMalformedRowNumberIsRefused(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	rec := f.do(http.MethodDelete, "/contests/"+uuid.NewString()+"/game/tables/suspects/rows/not-a-number", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
	}
	if f.games.gotDeleteRowRow != 0 {
		t.Fatal("a malformed row number reached the service")
	}
}

// The service refuses with ErrTableDataDisabled; the handler must not panic on
// a store never opened.
func TestTableDataDisabledAnswersANamedRefusalNotAPanic(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"beginning an upload", http.MethodPost, "/game/tables/suspects/data"},
		{"adding a row", http.MethodPost, "/game/tables/suspects/rows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGameFixture(t, rbac.PermissionContestAdminAll)
			f.games.beginTableErr = provisioning.ErrTableDataDisabled
			f.games.appendRowErr = provisioning.ErrTableDataDisabled

			body := "{}"
			if tc.name == "adding a row" {
				body = `{"values":["1"]}`
			}
			rec := f.do(tc.method, "/contests/"+uuid.NewString()+tc.path, body)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status %d, want 404: %s", rec.Code, rec.Body)
			}
			if code := errorCode(t, rec); code != "game_table_data_disabled" {
				t.Fatalf("code %q, want game_table_data_disabled", code)
			}
		})
	}
}

func TestDeletingANonexistentTableRowNamesItsOwnCode(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.deleteRowErr = provisioning.ErrTableRowNotFound

	rec := f.do(http.MethodDelete, "/contests/"+uuid.NewString()+"/game/tables/suspects/rows/99", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "game_table_row_not_found" {
		t.Fatalf("code %q, want game_table_row_not_found", code)
	}
}

// The handler must not tell another contest's id from a missing one.
func TestAnotherContestsTableDataIsReportedAsNotFound(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.completeTableErr = provisioning.ErrTableDataNotFound

	rec := f.do(http.MethodPost,
		"/contests/"+uuid.NewString()+"/game/tables/suspects/data/"+uuid.NewString()+"/complete", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "game_table_data_not_found" {
		t.Fatalf("code %q, want game_table_data_not_found", code)
	}
}

// completeTableUpload can raise every tabledata.go and tablecsv.go sentinel
// except ErrTableUnknown, and fail is one switch for every route.
func TestEveryTableDataRefusalHasItsOwnCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"table data not configured", provisioning.ErrTableDataDisabled, http.StatusNotFound, "game_table_data_disabled"},
		{"an upload already in progress", provisioning.ErrTableDataInProgress, http.StatusConflict, "game_table_data_in_progress"},
		{"another contest's or a missing upload", provisioning.ErrTableDataNotFound, http.StatusNotFound, "game_table_data_not_found"},
		{"an upload already sealed or cancelled", provisioning.ErrTableDataAlreadyComplete, http.StatusConflict, "game_table_data_already_complete"},
		{"a chunk sent out of order", provisioning.ErrTableDataChunkOutOfOrder, http.StatusConflict, "game_table_data_chunk_out_of_order"},
		{"a chunk over the domain's own limit", provisioning.ErrTableDataChunkTooLarge, http.StatusBadRequest, "game_table_data_chunk_too_large"},
		{"a chunk body that stopped arriving", provisioning.ErrTableDataChunkIncomplete, http.StatusRequestTimeout, "game_table_data_chunk_incomplete"},
		{"a declared size over the limit", provisioning.ErrTableDataTooLarge, http.StatusBadRequest, "game_table_data_too_large"},
		{"the table data volume is full", provisioning.ErrTableDataStoreFull, http.StatusConflict, "game_table_data_store_full"},
		{"received bytes short of the declared length", provisioning.ErrTableDataLengthMismatch, http.StatusConflict, "game_table_data_length_mismatch"},
		{"another form's row landed first", provisioning.ErrTableDataChanged, http.StatusConflict, "game_table_data_changed"},
		{"a header that does not match the table", provisioning.ErrTableHeaderMismatch, http.StatusBadRequest, "game_table_header_mismatch"},
		{"a row whose field count is wrong", provisioning.ErrTableRowFieldCount, http.StatusBadRequest, "game_table_row_field_count"},
		{"a value that does not match its column's type", provisioning.ErrTableValueInvalid, http.StatusBadRequest, "game_table_value_invalid"},
		{"a field longer than the platform allows", provisioning.ErrTableFieldTooLong, http.StatusBadRequest, "game_table_field_too_long"},
		{"a line longer than the platform allows", provisioning.ErrTableLineTooLong, http.StatusBadRequest, "game_table_line_too_long"},
		{"more rows than the platform allows", provisioning.ErrTableTooManyRows, http.StatusBadRequest, "game_table_too_many_rows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGameFixture(t, rbac.PermissionContestAdminAll)
			f.games.completeTableErr = tc.err

			rec := f.do(http.MethodPost,
				"/contests/"+uuid.NewString()+"/game/tables/suspects/data/"+uuid.NewString()+"/complete", "")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if code := errorCode(t, rec); code != tc.code {
				t.Fatalf("code %q, want %q", code, tc.code)
			}
		})
	}
}

// The sentinels completeTableUpload cannot raise, through the routes that can:
// ErrTableUnknown, ErrTableRowNotFound, ErrTableRowAlreadyDeleted and
// ErrTooManyDeletedRows (migration 27's CHECK).
func TestTheRemainingTableRefusalsHaveTheirOwnCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"a table not in the current definition", provisioning.ErrTableUnknown, http.StatusNotFound, "game_table_unknown"},
		{"a row the file does not have", provisioning.ErrTableRowNotFound, http.StatusNotFound, "game_table_row_not_found"},
		{"a row already deleted", provisioning.ErrTableRowAlreadyDeleted, http.StatusConflict, "game_table_row_already_deleted"},
		{"past the deleted-row limit", provisioning.ErrTooManyDeletedRows, http.StatusBadRequest, "game_table_too_many_deleted_rows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGameFixture(t, rbac.PermissionContestAdminAll)
			f.games.deleteRowErr = tc.err

			rec := f.do(http.MethodDelete, "/contests/"+uuid.NewString()+"/game/tables/suspects/rows/1", "")
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if code := errorCode(t, rec); code != tc.code {
				t.Fatalf("code %q, want %q", code, tc.code)
			}
		})
	}
}

// validateRow puts the row and column in the message; fail() must pass that
// text through.
func TestATableValueRefusalCarriesTheRowAndColumnToTheInterface(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.completeTableErr = fmt.Errorf("%w: row 42, column \"age\": %q is not a whole number that fits a 32-bit integer",
		provisioning.ErrTableValueInvalid, "not-a-number")

	rec := f.do(http.MethodPost,
		"/contests/"+uuid.NewString()+"/game/tables/suspects/data/"+uuid.NewString()+"/complete", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
	}
	// The column arrives JSON-escaped (\"age\"), so the word is checked, not
	// Go's %q spelling.
	if !strings.Contains(rec.Body.String(), "row 42") || !strings.Contains(rec.Body.String(), "age") {
		t.Fatalf("the refusal does not name the row and column: %s", rec.Body)
	}
}

// CLAUDE.md rule 11; the values match no default.
func TestBuilderLimitsCarryTheConfiguredTableDataLimitsNotAConstant(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.tableLimitsEnabled = true
	f.games.tableLimits = gamefile.Limits{MaxChunkBytes: 2222222, MaxFileBytes: 333333333}

	status := decode(t, f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game", ""))
	limits, ok := status["builder_limits"].(map[string]any)
	if !ok {
		t.Fatalf("the status carries no builder_limits object: %v", status)
	}
	if limits["enabled"] != true {
		t.Fatalf("enabled = %v, want true", limits["enabled"])
	}
	if limits["chunk_bytes"] != float64(2222222) || limits["max_file_bytes"] != float64(333333333) {
		t.Fatalf("builder_limits = %v, want the configured table-data limits", limits)
	}
}

// A definition needs no CSV, so the structural ceilings are still published.
func TestBuilderLimitsAreDisabledButStructuralLimitsStillShowWhenTableDataIsOff(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	status := decode(t, f.do(http.MethodGet, "/contests/"+uuid.NewString()+"/game", ""))
	limits := status["builder_limits"].(map[string]any)
	if limits["enabled"] != false {
		t.Fatalf("enabled = %v, want false", limits["enabled"])
	}
	if limits["chunk_bytes"] != float64(0) || limits["max_file_bytes"] != float64(0) {
		t.Fatalf("limits = %v, want both zero while disabled", limits)
	}
	if limits["max_tables"] != float64(provisioning.MaxDefinitionTables) {
		t.Fatalf("max_tables = %v, want %d even while table data is disabled", limits["max_tables"], provisioning.MaxDefinitionTables)
	}
}

func TestTableEndpointsAreRefusedToAnAccountThatIsNotStaffOnTheContest(t *testing.T) {
	dataID := uuid.NewString()
	for _, tc := range []struct {
		name       string
		method     string
		pathSuffix string
	}{
		{"writing the definition", http.MethodPut, "/game/definition"},
		{"beginning a table upload", http.MethodPost, "/game/tables/suspects/data"},
		{"feeding a table chunk", http.MethodPut, "/game/tables/suspects/data/" + dataID + "/chunk?offset=0"},
		{"completing a table upload", http.MethodPost, "/game/tables/suspects/data/" + dataID + "/complete"},
		{"cancelling a table upload", http.MethodPost, "/game/tables/suspects/data/" + dataID + "/abort"},
		{"adding a row", http.MethodPost, "/game/tables/suspects/rows"},
		{"deleting a row", http.MethodDelete, "/game/tables/suspects/rows/1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGameFixture(t)
			contest := uuid.NewString()

			rec := f.do(tc.method, "/contests/"+contest+tc.pathSuffix, "x")
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body)
			}
		})
	}
}

// Reading the definition and a table's window need only contest.view.
func TestTableReadEndpointsAreRefusedToAnAccountThatIsNotStaffOnTheContest(t *testing.T) {
	for _, tc := range []struct {
		name       string
		method     string
		pathSuffix string
	}{
		{"reading the definition", http.MethodGet, "/game/definition"},
		{"reading a table's window", http.MethodGet, "/game/tables/suspects/data/window"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGameFixture(t)

			rec := f.do(tc.method, "/contests/"+uuid.NewString()+tc.pathSuffix, "")
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body)
			}
		})
	}
}

// Only a permitted session reaching each handler's success shape proves Mount
// holds every table-builder route.
func TestGameTableBuilderRoutesAreMounted(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest, dataID := uuid.NewString(), uuid.New()
	f.games.abortTableResult = provisioning.TableData{ID: dataID}

	for _, tc := range []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"read definition", http.MethodGet, "/game/definition", http.StatusOK},
		{"write definition", http.MethodPut, "/game/definition", http.StatusAccepted},
		{"begin table upload", http.MethodPost, "/game/tables/suspects/data", http.StatusCreated},
		{"table chunk", http.MethodPut, "/game/tables/suspects/data/" + dataID.String() + "/chunk?offset=0", http.StatusOK},
		{"complete table upload", http.MethodPost, "/game/tables/suspects/data/" + dataID.String() + "/complete", http.StatusOK},
		{"abort table upload", http.MethodPost, "/game/tables/suspects/data/" + dataID.String() + "/abort", http.StatusOK},
		{"table window", http.MethodGet, "/game/tables/suspects/data/window", http.StatusOK},
		{"add row", http.MethodPost, "/game/tables/suspects/rows", http.StatusCreated},
		{"delete row", http.MethodDelete, "/game/tables/suspects/rows/1", http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := ""
			switch {
			case tc.method == http.MethodPut && tc.name == "write definition":
				body = `{"tables":[]}`
			case tc.method == http.MethodPost && tc.name == "add row":
				body = `{"values":["1"]}`
			case tc.method == http.MethodPost && tc.name == "begin table upload":
				body = `{"declared_bytes":1}`
			case tc.method == http.MethodPut:
				body = "x"
			}
			rec := f.do(tc.method, "/contests/"+contest+tc.path, body)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

// 202: nothing is built yet.
func TestAskingForABuildAnswersTheGameItWillBuild(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.NewString()
	f.games.requestBuildResult = provisioning.Template{
		Database: "game_x", Version: 4,
		Status: provisioning.TemplatePending, Source: provisioning.SourceBuilder,
	}

	rec := f.do(http.MethodPost, "/contests/"+contest+"/game/build", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body)
	}
	if f.games.gotRequestBuildActor != f.actor.ID {
		t.Fatal("the actor did not reach the service, so nothing could be recorded against them")
	}

	body := decode(t, rec)
	if body["status"] != "pending" || body["version"] != float64(4) {
		t.Fatalf("body = %+v, want the pending game at version 4", body)
	}
	if body["needs_build"] != false {
		t.Fatal("the game still reads as needing a build after one was asked for")
	}
}

// Raising the version takes every participant's database at once.
func TestAskingForABuildWhileTheContestRunsIs409GameNotEditable(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.requestBuildErr = provisioning.ErrGameNotEditable

	rec := f.do(http.MethodPost, "/contests/"+uuid.NewString()+"/game/build", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "game_not_editable" {
		t.Fatalf("code %q, want %q", code, "game_not_editable")
	}
}

// A running or waiting build already loads everything stored before it started,
// so a second would build the same thing twice.
func TestAskingForABuildWhileOneRunsIs409BuildInProgress(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.requestBuildErr = provisioning.ErrBuildInProgress

	rec := f.do(http.MethodPost, "/contests/"+uuid.NewString()+"/game/build", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "build_in_progress" {
		t.Fatalf("code %q, want %q", code, "build_in_progress")
	}
}

// Its own code rather than the participant's no_game_yet, which means "try
// again shortly": on staff routes waiting never produces a game, only saving a
// script, dump or table definition does.
func TestAskingForABuildWithNoGameIs404NoGameToBuild(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.requestBuildErr = provisioning.ErrNoGame

	rec := f.do(http.MethodPost, "/contests/"+uuid.NewString()+"/game/build", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "no_game_to_build" {
		t.Fatalf("code %q, want %q", code, "no_game_to_build")
	}
}

func TestAskingForABuildIsRefusedToAnAccountThatIsNotStaffOnTheContest(t *testing.T) {
	f := newGameFixture(t)

	rec := f.do(http.MethodPost, "/contests/"+uuid.NewString()+"/game/build", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body)
	}
}
