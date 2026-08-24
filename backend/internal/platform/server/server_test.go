package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
)

func quiet() *slog.Logger { return logging.New("error", io.Discard) }

// startTestServer starts a server on an ephemeral port and stops it when the
// test ends.
func startTestServer(t *testing.T, h http.Handler) *Server {
	t.Helper()
	srv := New("test", "127.0.0.1:0", h, quiet())
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv
}

func TestServerServesRequestsAfterStart(t *testing.T) {
	srv := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("served"))
	}))

	resp, err := http.Get("http://" + srv.Addr() + "/")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "served" {
		t.Errorf("body = %q, want %q", body, "served")
	}
}

func TestAddrReportsBoundPortForEphemeralListener(t *testing.T) {
	srv := startTestServer(t, http.NotFoundHandler())

	if srv.Addr() == "127.0.0.1:0" || srv.Addr() == "" {
		t.Errorf("Addr() = %q, want the port the listener actually bound", srv.Addr())
	}
}

func TestShutdownWaitsForInFlightRequest(t *testing.T) {
	release := make(chan struct{})
	handlerDone := make(chan struct{})
	srv := startTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte("finished"))
		close(handlerDone)
	}))

	// Send a request and wait until the handler is running.
	respCh := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get("http://" + srv.Addr() + "/")
		if err == nil {
			respCh <- resp
		}
	}()
	time.Sleep(50 * time.Millisecond)

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()

	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned while a request was still being handled")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Errorf("Shutdown() = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return after the request completed")
	}

	select {
	case <-handlerDone:
	default:
		t.Error("in-flight handler was cut off by shutdown")
	}

	resp := <-respCh
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "finished" {
		t.Errorf("client got %q, want the completed response", body)
	}
}

func TestStartFailsOnAddressAlreadyInUse(t *testing.T) {
	// Binding eagerly in Start means a port clash is reported at startup
	// instead of disappearing into a background goroutine.
	first := startTestServer(t, http.NotFoundHandler())

	second := New("clash", first.Addr(), http.NotFoundHandler(), quiet())
	err := second.Start()

	if err == nil {
		_ = second.Shutdown(context.Background())
		t.Fatal("Start() succeeded on an occupied address, want error")
	}
}

func TestShutdownIsSafeWhenServerNeverStarted(t *testing.T) {
	srv := New("test", "127.0.0.1:0", http.NotFoundHandler(), quiet())

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown() on an unstarted server = %v, want nil", err)
	}
}

func TestServerLogsStartupWithItsName(t *testing.T) {
	var buf bytes.Buffer
	srv := New("core-api", "127.0.0.1:0", http.NotFoundHandler(), logging.New("info", &buf))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer srv.Shutdown(context.Background())

	if !bytes.Contains(buf.Bytes(), []byte("core-api")) {
		t.Errorf("startup log does not name the server: %s", buf.String())
	}
}
