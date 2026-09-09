package api_test

import (
	"context"
	"encoding/json"
	"errors"
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

// fakeGames stands in for provisioning.Games: the handler's job is the URL,
// the permission, the shape of the answer and the name of every refusal, and
// what the service decides is tested where the service lives.
type fakeGames struct {
	template provisioning.Template
	ofErr    error
	setErr   error
	gotSet   string
	gotActor uuid.UUID

	// Upload half. Every gotX field is set the moment the corresponding
	// method is called — even on the error path — the same way gotSet and
	// gotActor above prove a request actually reached the service.
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
	gotAppendBodyBytes []byte // read via io.ReadAll here — a test double, not the production streaming path

	// bodyMeter, when set, is the request body the caller sent, wrapped so
	// that it counts what has been taken from it. AppendChunk records its
	// reading at the moment it is entered, which is the one moment that
	// tells a streamed body from a buffered one — see
	// gotAppendBodyReadOnEntry.
	bodyMeter *readMeter
	// gotAppendBodyReadOnEntry is how many bytes of the request body had
	// already been consumed by the time the handler called AppendChunk. Zero
	// on the streaming path this route promises; the whole body on a handler
	// that read it into memory first.
	gotAppendBodyReadOnEntry int64

	// store, when set, replaces that io.ReadAll with the real thing: the
	// body is streamed into a real gamefile.Store on a real directory, and
	// its errors are translated exactly as provisioning.Games.AppendChunk
	// translates them (appendToRealStore below). It is what the tests about
	// the *byte path* use — a ceiling the body runs into, a body that stops
	// arriving — because io.ReadAll is the one thing the production path
	// never does, and reading the body whole is what hid a refusal that
	// never happened (CLAUDE.md rule 10).
	store *gamefile.Store

	currentErr    error
	currentResult provisioning.Upload

	// uploadResult and uploadErr back Upload — the lookup gameView makes for
	// a file-sourced Template, to resolve the row its UploadID names.
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

	// limits and limitsEnabled back UploadLimits. Left at their zero values —
	// an empty gamefile.Limits and enabled=false — a fixture behaves like an
	// installation with no GAME_UPLOAD_DIR configured, exactly the state
	// TestUploadLimitsAreZeroAndDisabledWhenUploadsAreOff exercises.
	limits        gamefile.Limits
	limitsEnabled bool

	// The table builder's own third way: a structural description
	// (SetDefinition) and one table's own chunked CSV data. Every gotX field
	// below follows beginResult's own convention: set the moment the call is
	// made, even on the error path, so a test can prove a request actually
	// reached the service.
	setDefinitionErr    error
	gotSetDefinition    provisioning.Definition
	gotSetDefinitionFor uuid.UUID

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
	// tableBodyMeter and tableStore are bodyMeter and store above, for
	// AppendTableChunk instead of AppendChunk — the same reason each exists:
	// proving the byte path streams rather than buffers, and running a chunk
	// into a real gamefile.Store so a transport-ceiling test meets the same
	// probe read the production path does (CLAUDE.md rule 10).
	tableBodyMeter                *readMeter
	gotAppendTableBodyReadOnEntry int64
	tableStore                    *gamefile.Store

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

	// tableLimits and tableLimitsEnabled back TableDataLimits — limits' own
	// doc, for the table builder's independently configured store.
	tableLimits        gamefile.Limits
	tableLimitsEnabled bool

	// currentTableErr and currentTableResult back CurrentTableData —
	// currentErr and currentResult's own convention, for a table's own
	// chunked CSV upload instead of a dump.
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

func (g *fakeGames) BeginTableUpload(_ context.Context, contestID uuid.UUID, table string, declaredBytes int64) (provisioning.TableData, error) {
	g.gotBeginTableContest, g.gotBeginTableTable, g.gotBeginTableBytes = contestID, table, declaredBytes
	if g.beginTableErr != nil {
		return provisioning.TableData{}, g.beginTableErr
	}
	return g.beginTableResult, nil
}

// appendToRealTableStore is appendToRealStore's own translation, for the
// table builder's independent gamefile.Store and its own sentinels.
func appendToRealTableStore(store *gamefile.Store, id uuid.UUID, offset int64, r io.Reader) (int64, error) {
	received, err := store.Append(id.String(), offset, r)
	switch {
	case err == nil:
		return received, nil
	case errors.Is(err, gamefile.ErrChunkIncomplete):
		return received, fmt.Errorf("%w: %w", provisioning.ErrTableDataChunkIncomplete, err)
	case errors.Is(err, gamefile.ErrChunkTooLarge):
		return received, provisioning.ErrTableDataChunkTooLarge
	case errors.Is(err, gamefile.ErrChunkOutOfOrder):
		return received, provisioning.ErrTableDataChunkOutOfOrder
	default:
		return received, fmt.Errorf("gamefile: %w", err)
	}
}

// realTableStore is realUploadStore's own shape for a table's chunked CSV.
func realTableStore(t *testing.T, dataID uuid.UUID, limits gamefile.Limits) *gamefile.Store {
	t.Helper()
	store, err := gamefile.NewStore(t.TempDir(), limits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	if err := store.Begin(dataID.String(), 1<<16); err != nil {
		t.Fatalf("begin the table upload: %v", err)
	}
	return store
}

func (g *fakeGames) AppendTableChunk(_ context.Context, contestID, id uuid.UUID, offset int64, r io.Reader) (int64, error) {
	g.gotAppendTableContest, g.gotAppendTableDataID, g.gotAppendTableOffset = contestID, id, offset
	if g.tableBodyMeter != nil {
		g.gotAppendTableBodyReadOnEntry = g.tableBodyMeter.read
	}
	if g.tableStore != nil {
		return appendToRealTableStore(g.tableStore, id, offset, r)
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

func (g *fakeGames) Of(context.Context, uuid.UUID) (provisioning.Template, error) {
	return g.template, g.ofErr
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
	if g.store != nil {
		return appendToRealStore(g.store, uploadID, offset, r)
	}
	// io.ReadAll here is what proves the handler handed AppendChunk a real
	// io.Reader rather than something it had already drained: reading it a
	// second time, from the fake, is only possible if the handler never read
	// it at all.
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

// appendToRealStore is what provisioning.Games.AppendChunk does with the
// reader the handler hands it: one Store.Append, and its sentinels
// translated into the domain's (provisioning's own wrapGamefileErr). The
// translation is repeated here rather than skipped because a fake that
// answered with gamefile's sentinels would be testing a service that does
// not exist — and because *how* it translates is part of what these tests
// check: the cause travels on inside the domain error, so the HTTP layer can
// still recognise the http.MaxBytesError it created itself.
func appendToRealStore(store *gamefile.Store, uploadID uuid.UUID, offset int64, r io.Reader) (int64, error) {
	received, err := store.Append(uploadID.String(), offset, r)
	switch {
	case err == nil:
		return received, nil
	case errors.Is(err, gamefile.ErrChunkIncomplete):
		return received, fmt.Errorf("%w: %w", provisioning.ErrUploadChunkIncomplete, err)
	case errors.Is(err, gamefile.ErrChunkTooLarge):
		return received, provisioning.ErrUploadChunkTooLarge
	case errors.Is(err, gamefile.ErrChunkOutOfOrder):
		return received, provisioning.ErrUploadChunkOutOfOrder
	default:
		return received, fmt.Errorf("gamefile: %w", err)
	}
}

// realUploadStore is a gamefile.Store on a fresh directory with one upload
// already begun — the state a chunk arrives into.
func realUploadStore(t *testing.T, uploadID uuid.UUID, limits gamefile.Limits) *gamefile.Store {
	t.Helper()
	store, err := gamefile.NewStore(t.TempDir(), limits)
	if err != nil {
		t.Fatalf("open the upload store: %v", err)
	}
	if err := store.Begin(uploadID.String(), 1<<16); err != nil {
		t.Fatalf("begin the upload: %v", err)
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

// fakeDatabases stands in for provisioning.Service's own half: the rows an
// organizer reads and the drop they ask for. What the drop actually does to a
// cluster is tested where the service lives; here the questions are the URL,
// the permission, the shape of the answer and the name of every refusal.
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

// The two budgets allowUploadBegin enforces, restated here because this is an
// external test package and they are unexported. A change to either constant
// without a change here shows up as a test that stops asserting the boundary
// it names — which is why the loops below run right up to the limit and then
// one past it, rather than "enough" times.
const (
	maxUploadBeginsPerAddressInTest = 20
	maxUploadBeginsPerContestInTest = 8
)

// oneOffice is the address several organisers share — httptest's own default
// RemoteAddr, spelled out because these tests are about which key a refusal
// came from.
const oneOffice = "192.0.2.1:1234"

// beginUploadFrom starts an upload for one contest as seen from one address:
// the two keys allowUploadBegin bounds, varied independently, which is the
// only way a test can tell which of them refused a request.
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

// readMeter counts what has been drawn from a request body, so a test can ask
// *when* the bytes were read rather than only what they were. The handler
// under test is on one side of it and the service double on the other, and
// the whole difference between streaming and buffering is which of the two
// had read them by the time the service was called.
type readMeter struct {
	r    io.Reader
	read int64
}

func (m *readMeter) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	m.read += int64(n)
	return n, err
}

// doBody is do with a body the caller owns — an io.Reader rather than a
// string, so the test can watch it being consumed.
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

// Every contest is in this state until somebody writes its game, so it is one
// shape for the interface to render rather than a 404 to special-case.
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

// The status is polled while a build runs; the script is up to half a
// mebibyte. Carrying one inside the other would make every poll pay for it.
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

// This is the fix for the defect this file's own doc names: before it, the
// status carried neither the source a game was built from nor which upload a
// file-sourced one came from, so a reloaded page could not tell a file-
// sourced game from an editor-sourced one, let alone reopen the viewer on the
// file it was built from. Source and Upload are what closes that — read from
// provisioning.Template.Source and .UploadID, resolved to the upload's own
// row through fakeGames.Upload, never invented here.
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

// An editor-sourced game — every game that predates the upload feature, and
// every one written directly since — carries no upload object at all, not
// one whose fields are merely empty: a client tells the two apart by
// whether Upload is present (uploadLimitsResponse's own Enabled field makes
// the identical choice for a different pair of numbers).
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

// The status carries the chunk and file ceilings a browser needs before it
// can slice a file and start a chunked upload — CLAUDE.md rule 11: the value
// that decides whether AppendChunk accepts a chunk must reach the client that
// has to obey it. The values chosen here are neither the package's own
// defaults (defaultMaxGameChunkBodyBytes, 64 MiB) nor the configuration
// defaults (8 MiB / 4 GiB, config.go's own int64Env calls) — if the handler
// answered a constant instead of what provisioning.Games.UploadLimits
// actually reports, this test would still pass with the wrong numbers, which
// is exactly the failure mode CLAUDE.md rule 11 names.
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

// A deployment with no GAME_UPLOAD_DIR configured never calls WithUploads, so
// fakeGames.limitsEnabled stays at its zero value here — the same state
// provisioning.Games.UploadLimits reports for that installation. The
// interface must be able to tell "uploads are off" from "the operator
// configured a limit of zero" (uploadLimitsResponse's own doc), so this
// checks both halves: enabled is false, and the numbers are not silently
// reported as some other ceiling.
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

// GET .../uploads/current is the other moment a reloading page needs the
// ceilings — before it even knows whether an upload is in progress (Games'
// own doc on the two situations this covers). Both branches of currentUpload
// (an upload found, and "absent") must carry them; this checks the one
// actually returned when there is nothing to resume, since a fresh page load
// with no prior upload is the common case.
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

// CLAUDE.md rule 1: every refusal is a sentinel the handler can name, so the
// organiser is told what to fix rather than "internal error".
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

// The script runs with the provisioning role's privileges: whoever may write
// it can run arbitrary SQL on the game cluster. So the route is gated, and an
// authenticated account that is not staff on this contest is refused.
//
// Note what this cannot assert. The contest roles are owner and manager, and
// both carry contest.view *and* contest.edit — there is no role that reads
// without writing — so no test here can tell the two permissions apart on
// this route. What it can prove is that the gate exists and that it
// discriminates: a stranger is refused and a manager is not.
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

// registrationID is a stable pointer for a fixture row's holder.
func registrationID(id uuid.UUID) *uuid.UUID { return &id }

// What the screen is for: which databases exist, whose each one is, and how
// much disk it takes. A spare and a participant's copy are one row apart, and
// the answer has to tell them apart.
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

// The destructive half. The organiser's identity has to reach the service —
// nothing can be recorded against them otherwise — and so does the contest,
// which is what scopes the drop to their own olympiad.
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

// CLAUDE.md rule 1: every refusal is a sentinel the handler names, so a stale
// page is told which of the two things happened rather than "internal error".
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

// Dropping somebody's database is destructive and contest-scoped, so it sits
// behind the same gate as writing the script.
//
// The same limit applies here that TestWritingTheScriptIsRefusedToAnAccount
// ThatIsNotStaffOnTheContest documents: no contest role grants view without
// edit, so nothing here can tell contest.view and contest.edit apart on a
// route. What it can prove is that the gate exists and discriminates — a
// stranger is refused, and a manager, who is the person on duty when a
// database goes wrong mid-olympiad, is not.
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

// Reading the list is behind the contest's own view permission, so a stranger
// cannot learn which databases an olympiad owns.
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

// The address budget, proven where only it can answer: every request goes to
// a *different* contest, so the contest-scoped counter never reaches its own
// eight and the twenty-first refusal can only have come from the address.
//
// Twenty-one requests into one contest — what this asserted before — proved
// neither. maxUploadBeginsPerContest is 8 against maxUploadBeginsPerAddress's
// 20, so the ninth request was already refused by the contest key, and both
// keys answer with the same status and the same code: deleting the address
// block from allowUploadBegin outright left the test green.
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

// And the contest budget, proven the same way round: every request comes from
// a different address, so the address counter never reaches twenty and the
// ninth refusal can only be the contest's own.
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

// CLAUDE.md rule 5, as a fact somebody can observe rather than a comment: the
// address key is spent *before* the contest key, so a caller the address
// budget refuses never spends a counter in the contest's own budget.
//
// The two keys answer with the same status and code, so order cannot be read
// off the refusal itself. What can be read off it is the contest budget
// afterwards: a fresh contest whose eight begins are all still there was
// never charged for the request the address refused.
func TestTheAddressBudgetIsSpentBeforeTheContestsOwn(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)

	for range maxUploadBeginsPerAddressInTest {
		if rec := f.beginUploadFrom(oneOffice, uuid.NewString()); rec.Code != http.StatusCreated {
			t.Fatalf("filling the address budget answered %d: %s", rec.Code, rec.Body)
		}
	}

	// Refused by the address. If the contest key were checked first, this
	// request would have spent one of the eight below on its way to the same
	// 429.
	victim := uuid.NewString()
	if rec := f.beginUploadFrom(oneOffice, victim); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429: %s", rec.Code, rec.Body)
	}

	// From an address with a budget of its own, the victim contest must
	// still have all eight of its begins.
	for i := range maxUploadBeginsPerContestInTest {
		rec := f.beginUploadFrom(fmt.Sprintf("203.0.113.%d:5000", i+1), victim)
		if rec.Code != http.StatusCreated {
			t.Fatalf("begin %d of 8 for the contest answered %d — the refused request spent one: %s",
				i+1, rec.Code, rec.Body)
		}
	}
}

// A refused attempt must still count against the budget (CLAUDE.md rule 13):
// a caller who cannot pass RequireContestPermission must not be able to probe
// the limiter for free either. Asserted by the same threshold as the address
// test above holding even though the middleware never lets these requests
// reach beginUpload — the limiter check sits inside the handler, not before
// authentication, so RequireContestPermission's own 403 is what is actually
// being proven never to reach the limiter at all in this case; the
// permission test below covers that half on its own.
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

// The chunk is streamed straight into AppendChunk, never read into memory by
// the handler first (CLAUDE.md rule 12; appendChunk's own doc).
//
// What proves that is *when* the body was read, not what came back from it.
// Asserting only that the fake's own io.ReadAll returned the right bytes —
// what this test did before — is satisfied just as well by a handler that
// drains r.Body itself and hands over a bytes.Reader of what it kept: the
// fake reads the same bytes either way, and rule 12's whole point (an
// eight-mebibyte chunk of a three-gigabyte upload must not become an
// eight-mebibyte allocation per request in the API process) went unchecked.
// So the body counts what is taken from it, and the fake records that count
// at the instant it is entered: zero on the streaming path, the whole chunk
// on a buffering one.
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

// The transport-level ceiling (appendChunk's own doc on maxChunkBody): a body
// larger than it is refused, and reported as the same refusal
// ErrUploadChunkTooLarge would produce — not a second code to explain.
//
// Proven on the path the deployment uses, not against a double that reads
// the body with io.ReadAll (CLAUDE.md rule 10). The two numbers below are
// deliberately equal, because app.go makes them equal — the socket's ceiling
// *is* GAME_UPLOAD_CHUNK_BYTES — and that is the arrangement in which the
// refusal used to disappear: io.LimitReader took exactly the cap without an
// error, the probe read came back (0, "request body too large"), and a
// refused 9-byte chunk was answered 200 OK with 8 bytes quietly kept. A
// double that drains the body with io.ReadAll never meets the probe at all,
// which is why this one streams into a real gamefile.Store.
func TestAppendingAChunkOverTheTransportCeilingIsRefused(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	upload := uuid.New()
	f.games.store = realUploadStore(t, upload, gamefile.Limits{
		MaxFileBytes: 1 << 20, MaxDirBytes: 1 << 20, MaxChunkBytes: 8,
	})
	f.handler.WithMaxChunkBody(8)

	rec := f.do(http.MethodPut,
		"/contests/"+uuid.NewString()+"/game/uploads/"+upload.String()+"/chunk?offset=0",
		"123456789") // 9 bytes, one past the 8-byte ceiling
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "game_upload_chunk_too_large" {
		t.Fatalf("code %q, want game_upload_chunk_too_large", code)
	}
	received, err := f.games.store.Received(upload.String())
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if received != 0 {
		t.Fatalf("the store kept %d bytes of a chunk it refused, want 0", received)
	}
}

// The listener's ReadTimeout bounds the whole request, body included, and it
// is sized for a JSON document (internal/platform/server's own doc). A chunk
// is not one: it is megabytes, sent over whatever uplink an organiser has,
// and a Caddy in front does not buffer request bodies. So this route takes
// its own read deadline, sized to the body it accepts.
//
// Proven across a real listener with a real ReadTimeout, because that is
// where the guarantee lives — a recorder never has a deadline to miss
// (CLAUDE.md rule 10). The numbers are scaled down by three orders of
// magnitude; the shape is the deployment's: a body that pauses for longer
// than the listener's own timeout while it is still being sent.
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

// A chunk out of order is CLAUDE.md rule 1's own example in this task's
// brief: its own sentinel, its own code, never a 500.
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

// A page reload finds nothing to resume as an empty, successful answer, not a
// 404 — the same "absent" shape status() uses for a contest with no game.
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

// CompleteUpload's own doc calls this "the same path as SetScript" — the
// answer follows the exact shape and status setScript's own test asserts.
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

// Paging past the last line is a normal outcome, not a failure: gamefile.
// Window's own doc treats it as an empty window, and this handler adds
// nothing on top of that.
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

// CLAUDE.md rule 2: max_lines and max_bytes are the caller's own budget for
// one gamefile.Window call, and an organiser's query string is not a trusted
// source for how much of this process's memory one request may hold
// (readWindowLines' own doc). A value past the ceiling is clamped, not
// honoured verbatim.
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

// CLAUDE.md rule 1: every one of provisioning/upload.go's thirteen sentinels
// gets its own code and its own status, proven through one endpoint the same
// way TestEveryGameRefusalHasItsOwnCode and TestEveryInstanceRefusalHasItsOwn
// Code already prove it for the script and instance sentinels — fail is one
// switch shared by every route on this handler, so which endpoint raises the
// sentinel does not change what it maps to.
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

// Starting, feeding, finishing and cancelling an upload all sit behind
// contest.edit, exactly where writing the script does — replacing the game
// through an upload is no less destructive than SetScript (Mount's own
// comment). Only beginUpload is exercised directly above
// (TestBeginningAnUploadIsRefusedToAnAccountThatIsNotStaffOnTheContest); this
// covers the other three write endpoints and the two read ones with the same
// gate.
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

// The 401 every unauthenticated request on this handler gets is not proof a
// route exists — chi answers a genuinely unmounted path with the router's own
// 404 before this handler is ever reached, but this whole fixture's router
// has nothing else mounted for a 401 to come from either. Only a real,
// permitted session reaching each handler's own success shape (not chi's
// codeNotFound) proves the routing table in Mount actually holds all six
// upload routes.
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

// A PUT is decoded into the exact provisioning.Definition the service is
// asked to save, and a GET answers back the shape a saved builder-sourced
// game carries — the round trip this task's own brief asks for ("читается и
// пишется целиком одним запросом").
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

// A contest with no game, or one built the other two ways, must not refuse
// this read: a console that has not yet learned which of the three ways
// built a game can still ask for a definition and get an empty one back,
// exactly the "absent" shape status() and script() already give.
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

// CLAUDE.md rule 1: every provisioning.Definition.Validate sentinel gets its
// own code, so an organiser is told which mistake they made rather than
// "internal error".
func TestEveryDefinitionRefusalHasItsOwnCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"no tables at all", provisioning.ErrDefinitionEmpty, "game_definition_empty"},
		{"past the size limits", provisioning.ErrDefinitionTooLarge, "game_definition_too_large"},
		{"not a plain identifier", provisioning.ErrDefinitionInvalidName, "game_definition_invalid_name"},
		{"a table or column named twice", provisioning.ErrDefinitionDuplicateName, "game_definition_duplicate_name"},
		{"a table with no columns", provisioning.ErrDefinitionTableEmpty, "game_definition_table_empty"},
		{"a type outside the closed set", provisioning.ErrDefinitionInvalidType, "game_definition_invalid_type"},
		{"a primary key naming a missing column", provisioning.ErrDefinitionInvalidPrimaryKey, "game_definition_invalid_primary_key"},
		{"a table with data whose structure would change", provisioning.ErrDefinitionTableLocked, "game_definition_table_locked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGameFixture(t, rbac.PermissionContestAdminAll)
			f.games.setDefinitionErr = tc.err

			rec := f.do(http.MethodPut, "/contests/"+uuid.NewString()+"/game/definition",
				`{"tables":[{"name":"t","columns":[{"name":"c","type":"text"}]}]}`)
			if rec.Code < 400 || rec.Code >= 500 {
				t.Fatalf("status %d, want a 4xx: %s", rec.Code, rec.Body)
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

// The status published alongside every other game screen must carry the
// table builder's own ceilings too — builderLimitsResponse's own doc, the
// same rule 11 concern uploadLimitsResponse's own tests already cover for
// the dump.
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

// The chunk is streamed straight into AppendTableChunk, never buffered by
// the handler first — appendChunk's own test of the identical concern, for
// the table builder's independent transport ceiling.
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

// The transport-level ceiling (appendTableChunk's own doc), proven the same
// way TestAppendingAChunkOverTheTransportCeilingIsRefused proves it for the
// dump: a real gamefile.Store, so the refusal comes from the same probe read
// the production path meets rather than from a double that drains the body
// with io.ReadAll first (CLAUDE.md rule 10). This is the mandatory "превышение
// размера чанка" case for the table builder's own independent ceiling.
func TestAppendingATableChunkOverTheTransportCeilingIsRefused(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	dataID := uuid.New()
	f.games.tableStore = realTableStore(t, dataID, gamefile.Limits{
		MaxFileBytes: 1 << 20, MaxDirBytes: 1 << 20, MaxChunkBytes: 8,
	})
	f.handler.WithMaxTableChunkBody(8)

	rec := f.do(http.MethodPut,
		"/contests/"+uuid.NewString()+"/game/tables/suspects/data/"+dataID.String()+"/chunk?offset=0",
		"123456789") // 9 bytes, one past the 8-byte ceiling
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "game_table_data_chunk_too_large" {
		t.Fatalf("code %q, want game_table_data_chunk_too_large", code)
	}
	received, err := f.games.tableStore.Received(dataID.String())
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if received != 0 {
		t.Fatalf("the store kept %d bytes of a chunk it refused, want 0", received)
	}
}

// A reloaded page finds a table's own chunked upload still in progress and
// can offer to resume it — CurrentUpload's own test
// (TestCurrentUploadReturnsTheInProgressOne), mirrored here for a table's
// CSV instead of a dump.
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

// A reloaded page finding nothing to resume gets an empty, successful
// answer, not a 404 — TestCurrentUploadReportsAbsentWhenThereIsNone's own
// doc, for a table's own CSV.
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

// A refusal that is not "nothing in progress" — the table itself is not
// (or no longer) part of the contest's current definition — is still a
// refusal, never folded into the "absent" answer above.
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

// Proof that appendTableChunk's own transport ceiling (maxTableChunkBody) is
// independent of appendChunk's (maxChunkBody): a body that would be refused
// by the dump's default 64 MiB ceiling but fits comfortably inside it is
// still refused here once the table builder's own, separately configured
// ceiling is set below the body's size — the two fields must never collide
// onto one number.
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

// The mandatory "окно на несуществующей строке" case: a window whose
// fromRow is past the file's own end comes back as an empty page, not an
// error — provisioning.Games.TableDataWindow's own doc, and this handler
// adds nothing on top of it (uploadWindow's identical test for the dump).
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

// The same rule 2 concern uploadWindow's own test already proves for the
// dump: an organiser's query string is not a trusted source for how much of
// this process's memory one request may hold.
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

// The two ceilings tableDataWindow clamps to, restated here because this is
// an external test package and they are unexported — chunkOffset's own
// TestAppendingAChunkWithoutANumericOffsetIsRefused gives the identical
// reason for restating a package-private constant in this file.
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

// A row number that is not a positive integer is refused before the service
// is ever asked — the same shape TestAppendingAChunkWithoutANumericOffset
// IsRefused proves for the dump's own offset.
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

// The mandatory "работа при выключенной возможности" case: an installation
// with no table-data volume configured answers every one of these routes
// with a named refusal — provisioning.ErrTableDataDisabled, mapped below —
// never a panic from dereferencing a store that was never opened. The
// service itself is what refuses (Games' own nil check); this proves the
// handler passes that refusal through as a 4xx rather than assuming success.
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

// A row a delete asked for that the file does not have is refused by name —
// mirroring TestEveryInstanceRefusalHasItsOwnCode's own reasoning: an
// organiser is told which of several similar things happened.
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

// The mandatory "чужой конкурс" case: a table-data id that belongs to
// another contest reads identically to one that does not exist at all
// (tableDataByIDForContest's own doc) — the handler must not distinguish
// them, so this asserts on the code the service's own answer produces
// rather than on anything the handler could leak about the other contest.
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

// CLAUDE.md rule 1, for every sentinel in provisioning/tabledata.go and the
// CSV parsing it drives (provisioning/tablecsv.go) — this task's own brief
// names both files by name. Proven through completeTableUpload, the one
// route that can raise every one of them: the header check, the row check
// and every tabledata.go sentinel besides ErrTableUnknown (its own test,
// below) all reach CompleteTableUpload's own return in the real service, and
// fail is one switch shared by every route on this handler.
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

// The other sentinels not reachable through completeTableUpload above:
// ErrTableUnknown (a table not in the contest's current definition),
// ErrTableRowNotFound and ErrTableRowAlreadyDeleted (DeleteTableRow's own),
// and ErrTooManyDeletedRows (migration 27's own CHECK, surfaced as a
// sentinel) — proven through the routes that can actually raise them.
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

// The mandatory requirement this task's own brief states by name: a CSV
// refusal must carry the row number and the column to the interface, or a
// refusal on a file of a million rows is useless. provisioning's own
// validateRow already writes both into the sentinel's message
// (fmt.Errorf("%w: row %d, column %q: ...")); this proves fail() passes
// that text through as the response's own message rather than replacing it
// with a fixed sentence that drops the specifics.
func TestATableValueRefusalCarriesTheRowAndColumnToTheInterface(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	f.games.completeTableErr = fmt.Errorf("%w: row 42, column \"age\": %q is not a whole number that fits a 32-bit integer",
		provisioning.ErrTableValueInvalid, "not-a-number")

	rec := f.do(http.MethodPost,
		"/contests/"+uuid.NewString()+"/game/tables/suspects/data/"+uuid.NewString()+"/complete", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
	}
	// The column name reaches the wire JSON-escaped (\"age\"), not literally
	// quoted — checked for the word itself rather than for Go's own %q
	// spelling of it.
	if !strings.Contains(rec.Body.String(), "row 42") || !strings.Contains(rec.Body.String(), "age") {
		t.Fatalf("the refusal does not name the row and column: %s", rec.Body)
	}
}

// CLAUDE.md rule 11, for the table builder's own independently configured
// ceilings: the response must carry provisioning.Games.TableDataLimits' own
// answer, never defaultMaxGameChunkBodyBytes or any other constant this
// package keeps. The numbers chosen here are neither that default nor the
// dump's own configured pair a sibling test uses, so a handler that
// answered with the wrong source would still be caught.
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

// A deployment with no table-data volume configured never calls
// WithTableData, so fakeGames.tableLimitsEnabled stays false — the state
// TableDataLimits reports for that installation — while the structural
// ceilings (max_tables and friends) are still published: a definition may
// be described without ever uploading a byte of CSV.
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

// Every write endpoint of the table builder's own group sits behind
// contest.edit, the mandatory "отсутствие права" case, mirroring
// TestUploadEndpointsAreRefusedToAnAccountThatIsNotStaffOnTheContest.
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

// Only a real, permitted session reaching each handler's own success shape
// proves the routing table in Mount actually holds every table-builder
// route — TestGameUploadRoutesAreMounted's own reasoning: a 401 from an
// unmounted path proves nothing here, since authentication runs first.
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
