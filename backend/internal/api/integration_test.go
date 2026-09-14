package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/health"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/platform/server"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubChecker reports a fixed outcome for a named dependency.
type stubChecker struct {
	name string
	err  error
}

func (s stubChecker) Name() string                  { return s.name }
func (s stubChecker) Check(_ context.Context) error { return s.err }

// startStack starts the public and internal listeners the way main.go does and
// returns their base URLs.
func startStack(t *testing.T, checkers ...health.Checker) (publicURL, internalURL string) {
	t.Helper()

	log := logging.New("error", io.Discard)
	deps := api.Deps{
		Logger:   log,
		Metrics:  metrics.NewPrometheus(),
		Version:  "integration",
		Checkers: checkers,
	}

	public := server.New("public", "127.0.0.1:0", api.NewRouter(deps), log)
	internal := server.New("internal", "127.0.0.1:0", api.NewInternalRouter(deps), log)

	if err := public.Start(); err != nil {
		t.Fatalf("start public listener: %v", err)
	}
	if err := internal.Start(); err != nil {
		t.Fatalf("start internal listener: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = public.Shutdown(ctx)
		_ = internal.Shutdown(ctx)
	})

	return "http://" + public.Addr(), "http://" + internal.Addr()
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of %s: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

func TestAssembledServiceServesVersionOnThePublicListener(t *testing.T) {
	publicURL, _ := startStack(t)

	status, body := get(t, publicURL+"/api/v1/version")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", status, body)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if payload["version"] != "integration" {
		t.Errorf("version = %q, want integration", payload["version"])
	}
}

func TestAssembledServiceKeepsOperationalEndpointsOffThePublicListener(t *testing.T) {
	// This separation is the reason for two listeners: the published port must
	// not answer for metrics or readiness.
	publicURL, _ := startStack(t)

	for _, path := range []string{"/metrics", "/healthz", "/readyz"} {
		status, _ := get(t, publicURL+path)
		if status != http.StatusNotFound {
			t.Errorf("public %s = %d, want 404", path, status)
		}
	}
}

func TestAssembledServiceServesOperationalEndpointsOnTheInternalListener(t *testing.T) {
	_, internalURL := startStack(t, stubChecker{name: "core-db"}, stubChecker{name: "redis"})

	for _, path := range []string{"/metrics", "/healthz", "/readyz"} {
		status, body := get(t, internalURL+path)
		if status != http.StatusOK {
			t.Errorf("internal %s = %d, want 200 (body: %s)", path, status, body)
		}
	}
}

func TestReadinessTurnsRedWhenADependencyIsDown(t *testing.T) {
	_, internalURL := startStack(t,
		stubChecker{name: "core-db", err: errors.New("connection refused")},
		stubChecker{name: "redis"},
	)

	status, body := get(t, internalURL+"/readyz")

	if status != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d, want 503 (body: %s)", status, body)
	}
}

func TestLivenessStaysGreenWhenADependencyIsDown(t *testing.T) {
	// Restarting the process cannot fix a broken database, so liveness must not
	// follow readiness down.
	_, internalURL := startStack(t, stubChecker{name: "core-db", err: errors.New("connection refused")})

	status, _ := get(t, internalURL+"/healthz")

	if status != http.StatusOK {
		t.Errorf("/healthz = %d, want 200 while a dependency is down", status)
	}
}

func TestSelfHealthcheckProbeSucceedsAgainstTheInternalListener(t *testing.T) {
	// This is what `api -healthcheck` runs inside the container.
	_, internalURL := startStack(t)

	if err := health.Probe(context.Background(), internalURL+"/healthz"); err != nil {
		t.Errorf("Probe() = %v, want nil", err)
	}
}

func TestRequestsAreCountedInTheMetricsExposition(t *testing.T) {
	publicURL, internalURL := startStack(t)

	get(t, publicURL+"/api/v1/version")

	_, body := get(t, internalURL+"/metrics")
	if !strings.Contains(body, `route="/api/v1/version"`) {
		t.Errorf("metrics do not record the served route:\n%s", body)
	}
}

// startDegradedStack assembles the service the way main.go would with no Redis
// and no Prometheus, and returns the two base URLs plus the startup log.
func startDegradedStack(t *testing.T) (publicURL, internalURL string, startupLog *bytes.Buffer) {
	t.Helper()

	buf := &bytes.Buffer{}
	log := logging.New("info", buf)

	cacheBackend, err := cache.New(context.Background(), "", log)
	if err != nil {
		t.Fatalf("cache.New with no address returned error: %v", err)
	}
	t.Cleanup(func() { _ = cacheBackend.Close() })

	recorder, err := metrics.New(metrics.BackendLog, log)
	if err != nil {
		t.Fatalf("metrics.New(log) returned error: %v", err)
	}

	deps := api.Deps{
		Logger:    log,
		Metrics:   recorder,
		Version:   "degraded",
		CacheMode: cache.Mode(cacheBackend),
		Checkers:  []health.Checker{cacheChecker{cacheBackend}},
	}

	public := server.New("public", "127.0.0.1:0", api.NewRouter(deps), log)
	internal := server.New("internal", "127.0.0.1:0", api.NewInternalRouter(deps), log)
	if err := public.Start(); err != nil {
		t.Fatalf("start public listener: %v", err)
	}
	if err := internal.Start(); err != nil {
		t.Fatalf("start internal listener: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = public.Shutdown(ctx)
		_ = internal.Shutdown(ctx)
	})

	return "http://" + public.Addr(), "http://" + internal.Addr(), buf
}

// cacheChecker adapts the cache to the readiness probe.
type cacheChecker struct{ c cache.Cache }

func (c cacheChecker) Name() string                    { return "cache" }
func (c cacheChecker) Check(ctx context.Context) error { return c.c.Ping(ctx) }

func TestServiceServesWithNeitherRedisNorPrometheus(t *testing.T) {
	publicURL, _, _ := startDegradedStack(t)

	status, body := get(t, publicURL+"/api/v1/version")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", status, body)
	}
}

func TestDegradedServiceReportsItselfReady(t *testing.T) {
	// The in-process cache is a working backend, not a failure: readiness must
	// be green so the instance receives traffic.
	_, internalURL, _ := startDegradedStack(t)

	status, body := get(t, internalURL+"/readyz")

	if status != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200 (body: %s)", status, body)
	}
	var payload struct {
		Cache string `json:"cache"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if payload.Cache != cache.ModeMemory {
		t.Errorf("cache = %q, want %q so the degradation is visible", payload.Cache, cache.ModeMemory)
	}
}

func TestDegradedServiceHasNoScrapeEndpoint(t *testing.T) {
	_, internalURL, _ := startDegradedStack(t)

	status, _ := get(t, internalURL+"/metrics")

	if status != http.StatusNotFound {
		t.Errorf("/metrics = %d, want 404 when no Prometheus backend is active", status)
	}
}

func TestDegradedStartupWarnsAboutTheCacheFallback(t *testing.T) {
	_, _, startupLog := startDegradedStack(t)

	out := startupLog.String()
	if !strings.Contains(out, "WARN") {
		t.Errorf("startup did not warn about the in-process cache: %s", out)
	}
}

func TestDegradedServiceStillMeasuresTraffic(t *testing.T) {
	// Metrics move to the log stream; they do not disappear.
	publicURL, _, startupLog := startDegradedStack(t)
	get(t, publicURL+"/api/v1/version")
	startupLog.Reset()

	// Force the digest instead of waiting for the interval.
	rec, err := metrics.New(metrics.BackendLog, logging.New("info", startupLog))
	if err != nil {
		t.Fatalf("metrics.New returned error: %v", err)
	}
	rec.ObserveRequest(http.MethodGet, "/api/v1/version", 200, 3*time.Millisecond, false)
	rec.(*metrics.Log).Flush()

	if !strings.Contains(startupLog.String(), "http metrics") {
		t.Errorf("log backend produced no metric record: %s", startupLog.String())
	}
}

// deletionFixture mounts the account-management endpoints and the
// authentication endpoints on one router, sharing one account store and one
// session store — the two halves of the deletion round trip, wired the way
// main.go wires them, rather than each tested against its own handler alone.
type deletionFixture struct {
	router http.Handler
	repo   *userstest.Repository
	admin  users.User
	cookie *http.Cookie
}

func newDeletionFixture(t *testing.T) *deletionFixture {
	t.Helper()

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	repo := userstest.New()
	repo.GrantRole("admin", rbac.PermissionUsersManage)
	admin := repo.Add(users.User{Login: "root", FullName: "Root", Status: users.StatusActive, Roles: []string{"admin"}})

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	adminToken, err := sessions.Create(context.Background(), auth.Principal{UserID: admin.ID, Login: admin.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}

	authService := auth.NewService(auth.ServiceConfig{
		Users: repo, Sessions: sessions, Audit: audit.New(&apiSink{}), Limiter: auth.NewLimiter(c), Logger: log,
		Passwords: passwordtest.NewHasher(),
	})
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: repo,
		Authorizer: rbac.New(noRoles{}), Cookies: auth.NewCookieWriter(false), Logger: log,
	})
	usersService := users.NewService(repo, audit.New(&apiSink{}), &userstest.SpyUnitOfWork{}, passwordtest.NewHasher())

	router := chi.NewRouter()
	api.NewUsersHandler(usersService, repo, authService, mw, log).Mount(router)
	api.NewAuthHandler(authService, usersService, repo, mw, auth.NewCookieWriter(false), log).Mount(router)

	return &deletionFixture{
		router: router,
		repo:   repo,
		admin:  admin,
		cookie: &http.Cookie{Name: auth.SessionCookieName, Value: adminToken},
	}
}

// asAdmin sends a request carrying the administrator's session cookie.
func (f *deletionFixture) asAdmin(method, path, body string) *httptest.ResponseRecorder {
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

// signIn attempts a login with no session cookie, the way an anonymous client
// would.
func (f *deletionFixture) signIn(login, plaintext string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/auth/login",
		strings.NewReader(`{"login":"`+login+`","password":"`+plaintext+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// TestDeletionAndBulkRoundTrip is the whole path the design promises,
// end to end over HTTP: two accounts, deleted together by one bulk call,
// vanish from the register an administrator reads by default; restoring one
// brings it back to the register and to being able to sign in, while the
// other stays exactly as locked out as the day it was deleted.
func TestDeletionAndBulkRoundTrip(t *testing.T) {
	f := newDeletionFixture(t)
	hash := passwordtest.Hash(t, testPassword)
	first := f.repo.Add(users.User{
		Login: "orlov", FullName: "Orlov", PasswordHash: hash, Status: users.StatusActive,
	})
	second := f.repo.Add(users.User{
		Login: "popa", FullName: "Popa", PasswordHash: hash, Status: users.StatusActive,
	})

	// Both signed in successfully before anything happened to them.
	if rec := f.signIn("orlov", testPassword); rec.Code != http.StatusOK {
		t.Fatalf("orlov could not sign in before deletion: status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	if rec := f.signIn("popa", testPassword); rec.Code != http.StatusOK {
		t.Fatalf("popa could not sign in before deletion: status = %d (body: %s)", rec.Code, rec.Body.String())
	}

	// Select both and delete them in one bulk call.
	rec := f.asAdmin(http.MethodPost, "/users/bulk/status", `{"ids":["`+
		first.ID.String()+`","`+second.ID.String()+`"],"status":"deleted","reason":"graduated"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk delete status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var bulkBody struct {
		Changed []string `json:"changed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bulkBody); err != nil {
		t.Fatalf("bulk delete response is not JSON: %v", err)
	}
	if len(bulkBody.Changed) != 2 {
		t.Fatalf("bulk delete changed %v, want both accounts", bulkBody.Changed)
	}

	// The default listing is the register an administrator reads; a deleted
	// account is not in it.
	rec = f.asAdmin(http.MethodGet, "/users?limit=200", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"orlov"`) || strings.Contains(rec.Body.String(), `"popa"`) {
		t.Errorf("the default listing still names a deleted account: %s", rec.Body.String())
	}

	// Restore one of the two.
	rec = f.asAdmin(http.MethodPost, "/users/"+first.ID.String()+"/restore", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("restore status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}

	// The restored account signs in again with its unchanged password...
	rec = f.signIn("orlov", testPassword)
	if rec.Code != http.StatusOK {
		t.Errorf("restored account could not sign in: status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	// ...while the one nobody restored is still refused.
	rec = f.signIn("popa", testPassword)
	if rec.Code == http.StatusOK {
		t.Error("the account that was never restored signed in successfully")
	}
	if code := errorCode(t, rec); code != "account_blocked" {
		t.Errorf("popa's sign-in code = %q, want account_blocked", code)
	}
}

// TestStaffUnlockReopensSignInOverHTTP is the unlock as the deployment runs
// it: a guessing limit spent through the login endpoint, cleared through the
// account endpoint by an administrator, with one account store and one cache
// between them.
func TestStaffUnlockReopensSignInOverHTTP(t *testing.T) {
	f := newDeletionFixture(t)
	owner := f.repo.Add(users.User{
		Login: "orlov", FullName: "Orlov", PasswordHash: passwordtest.Hash(t, testPassword), Status: users.StatusActive,
	})

	var rec *httptest.ResponseRecorder
	for range maxLoginAttempts + 1 {
		rec = f.signIn("orlov", "a guess")
	}
	if rec = f.signIn("orlov", testPassword); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("before the unlock: status = %d, want 429 (body: %s)", rec.Code, rec.Body.String())
	}

	if rec = f.asAdmin(http.MethodPost, "/users/"+owner.ID.String()+"/sign-in/unlock", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("unlock status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}

	if rec = f.signIn("orlov", testPassword); rec.Code != http.StatusOK {
		t.Errorf("after the unlock: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}
