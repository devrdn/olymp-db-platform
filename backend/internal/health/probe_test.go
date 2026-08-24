package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProbeSucceedsOnHealthyEndpoint(t *testing.T) {
	srv := httptest.NewServer(Live())
	defer srv.Close()

	if err := Probe(context.Background(), srv.URL); err != nil {
		t.Errorf("Probe() = %v, want nil", err)
	}
}

func TestProbeFailsOnUnhealthyStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := Probe(context.Background(), srv.URL)

	if err == nil {
		t.Fatal("Probe() succeeded on a 503 response, want error")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error %q does not report the status code", err)
	}
}

func TestProbeFailsWhenServerIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(Live())
	url := srv.URL
	srv.Close() // nothing is listening any more

	if err := Probe(context.Background(), url); err == nil {
		t.Error("Probe() succeeded against a closed server, want error")
	}
}

func TestProbeRespectsContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := Probe(ctx, srv.URL)

	if err == nil {
		t.Fatal("Probe() succeeded despite the deadline, want error")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("Probe took %v; it ignored the context deadline", elapsed)
	}
}
