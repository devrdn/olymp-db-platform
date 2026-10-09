package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestMiddlewareCountsRequestsByRoutePattern(t *testing.T) {
	m := NewPrometheus()
	router := chi.NewRouter()
	router.Use(Middleware(m))
	router.Get("/contests/{contestID}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/contests/aaa", nil))
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/contests/bbb", nil))

	body := scrape(t, m)
	if !strings.Contains(body, `route="/contests/{contestID}"`) {
		t.Errorf("scrape does not use the route pattern as label:\n%s", body)
	}
	if strings.Contains(body, `route="/contests/aaa"`) {
		t.Errorf("raw path leaked into metric labels, causing unbounded cardinality:\n%s", body)
	}
}

func TestMiddlewareRecordsResponseStatus(t *testing.T) {
	m := NewPrometheus()
	router := chi.NewRouter()
	router.Use(Middleware(m))
	router.Get("/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))

	body := scrape(t, m)
	if !strings.Contains(body, `status="404"`) {
		t.Errorf("scrape does not record the response status:\n%s", body)
	}
}

func TestMiddlewareRecordsRequestDuration(t *testing.T) {
	m := NewPrometheus()
	router := chi.NewRouter()
	router.Use(Middleware(m))
	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {})

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	body := scrape(t, m)
	if !strings.Contains(body, "http_request_duration_seconds") {
		t.Errorf("scrape has no request duration histogram:\n%s", body)
	}
}

func TestHandlerExposesGoRuntimeMetrics(t *testing.T) {
	m := NewPrometheus()

	body := scrape(t, m)

	if !strings.Contains(body, "go_goroutines") {
		t.Errorf("scrape has no Go runtime metrics:\n%s", body)
	}
}

func TestUnmatchedRouteIsLabelledAsUnknown(t *testing.T) {
	m := NewPrometheus()
	router := chi.NewRouter()
	router.Use(Middleware(m))
	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {})

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/no/such/route", nil))

	body := scrape(t, m)
	if strings.Contains(body, `route="/no/such/route"`) {
		t.Errorf("unmatched path leaked into labels, letting clients create series:\n%s", body)
	}
	if !strings.Contains(body, `route="unknown"`) {
		t.Errorf("unmatched request is not grouped under a fixed label:\n%s", body)
	}
}

func TestAnInventedMethodIsLabelledAsOther(t *testing.T) {
	m := NewPrometheus()
	router := chi.NewRouter()
	router.Use(Middleware(m))
	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {})

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("WHATEVER-42", "/healthz", nil))

	body := scrape(t, m)
	if strings.Contains(body, `method="WHATEVER-42"`) {
		t.Errorf("an invented method leaked into labels, letting clients create series:\n%s", body)
	}
	if !strings.Contains(body, `method="other"`) {
		t.Errorf("an invented method is not grouped under a fixed label:\n%s", body)
	}
}

func scrape(t *testing.T, m *Prometheus) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.ScrapeHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics handler status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}
