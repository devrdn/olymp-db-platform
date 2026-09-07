package api_test

import (
	"context"
	"encoding/json"
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
	router := chi.NewRouter()
	api.NewGameHandler(games, databases, mw, log).Mount(router)

	return &gameFixture{
		router: router, games: games, databases: databases, stores: stores, actor: actor,
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
