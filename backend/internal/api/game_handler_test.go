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
	if err := store.Begin(uploadID.String()); err != nil {
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

// CLAUDE.md rule 5: the address-scoped budget is spent before the
// contest-scoped one, and here that means the address limit trips first when
// every request in the loop shares one contest and one httptest RemoteAddr.
func TestBeginningAnUploadIsRateLimitedPerAddress(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.NewString()

	var last *httptest.ResponseRecorder
	for i := 0; i < 21; i++ { // maxUploadBeginsPerAddress is 20
		last = f.do(http.MethodPost, "/contests/"+contest+"/game/uploads",
			`{"filename":"dump.sql","declared_bytes":1}`)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429: %s", last.Code, last.Body)
	}
	if code := errorCode(t, last); code != "game_upload_too_often" {
		t.Fatalf("code %q, want game_upload_too_often", code)
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

// The chunk is streamed straight into AppendChunk: fakeGames.AppendChunk
// reads the io.Reader it is given with io.ReadAll on its own side, which only
// returns the real bytes if the handler handed over something still readable
// — not a buffer it had already drained into memory itself.
func TestAppendingAChunkStreamsTheBodyToTheService(t *testing.T) {
	f := newGameFixture(t, rbac.PermissionContestAdminAll)
	contest, upload := uuid.NewString(), uuid.NewString()
	f.games.appendResult = 19

	rec := f.do(http.MethodPut,
		"/contests/"+contest+"/game/uploads/"+upload+"/chunk?offset=7",
		"CREATE TABLE t (id int);")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body)
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
