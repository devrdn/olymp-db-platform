package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type stubChecker struct {
	name  string
	err   error
	delay time.Duration
}

func (s stubChecker) Name() string { return s.name }

func (s stubChecker) Check(ctx context.Context) error {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.err
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v (raw: %s)", err, rec.Body.String())
	}
	return body
}

func TestLiveHandlerReportsOKWithoutCheckingDependencies(t *testing.T) {
	// Liveness must stay green while a dependency is down: restarting the
	// process would not fix a broken database.
	rec := httptest.NewRecorder()

	Live().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if decodeBody(t, rec)["status"] != "ok" {
		t.Errorf("body = %s, want status ok", rec.Body.String())
	}
}

func TestReadyHandlerReportsOKWhenEveryCheckPasses(t *testing.T) {
	handler := Ready(Options{Logger: quietLogger(), Timeout: time.Second, Checkers: []Checker{stubChecker{name: "core-db"}, stubChecker{name: "redis"}}})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := decodeBody(t, rec)
	checks, ok := body["checks"].(map[string]any)
	if !ok {
		t.Fatalf("body has no checks object: %s", rec.Body.String())
	}
	if checks["core-db"] != "ok" || checks["redis"] != "ok" {
		t.Errorf("checks = %v, want both ok", checks)
	}
}

func TestReadyHandlerReportsUnavailableWhenACheckFails(t *testing.T) {
	handler := Ready(Options{Logger: quietLogger(), Timeout: time.Second, Checkers: []Checker{
		stubChecker{name: "core-db", err: errors.New("dial tcp 10.0.0.5:5432: connection refused")},
		stubChecker{name: "redis"},
	}})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	body := decodeBody(t, rec)
	if body["status"] != "unavailable" {
		t.Errorf("status field = %v, want unavailable", body["status"])
	}
	checks := body["checks"].(map[string]any)
	if checks["core-db"] != "failed" {
		t.Errorf("core-db = %v, want failed", checks["core-db"])
	}
	if checks["redis"] != "ok" {
		t.Errorf("redis = %v, want ok", checks["redis"])
	}
}

func TestReadyHandlerDoesNotLeakInternalErrorDetails(t *testing.T) {
	// The endpoint is reachable from the network: host names, ports and
	// credentials from driver errors must not appear in the response.
	handler := Ready(Options{Logger: quietLogger(), Timeout: time.Second, Checkers: []Checker{
		stubChecker{name: "core-db", err: errors.New("dial tcp 10.0.0.5:5432: password authentication failed for user \"app\"")},
	}})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	for _, leaked := range []string{"10.0.0.5", "5432", "password", "app"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Errorf("response leaks %q: %s", leaked, rec.Body.String())
		}
	}
}

func TestReadyHandlerFailsCheckThatExceedsTimeout(t *testing.T) {
	handler := Ready(Options{Logger: quietLogger(), Timeout: 20 * time.Millisecond, Checkers: []Checker{stubChecker{name: "core-db", delay: time.Second}}})
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("readiness probe hung instead of timing out")
	}

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestReadyHandlerWithoutCheckersReportsOK(t *testing.T) {
	rec := httptest.NewRecorder()

	Ready(Options{Timeout: time.Second}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}
