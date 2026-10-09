package cache

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
)

func TestNewFallsBackToMemoryWhenNoAddressIsConfigured(t *testing.T) {
	var buf bytes.Buffer
	c, err := New(context.Background(), "", logging.New("info", &buf))
	if err != nil {
		t.Fatalf("New() with no address returned error: %v", err)
	}
	defer c.Close()

	if _, ok := c.(*Memory); !ok {
		t.Errorf("backend is %T, want the in-process store", c)
	}
}

func TestMemoryFallbackIsAnnouncedLoudly(t *testing.T) {
	var buf bytes.Buffer
	c, err := New(context.Background(), "", logging.New("info", &buf))
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	defer c.Close()

	out := buf.String()
	if !strings.Contains(out, "WARN") {
		t.Errorf("fallback was not logged as a warning: %s", out)
	}
	if !strings.Contains(out, "replica") && !strings.Contains(out, "single") {
		t.Errorf("warning does not state the single-instance limitation: %s", out)
	}
}

func TestNewFailsWhenConfiguredRedisIsUnreachable(t *testing.T) {
	_, err := New(context.Background(), "127.0.0.1:1", logging.New("error", &bytes.Buffer{}))

	if err == nil {
		t.Fatal("New() succeeded against an unreachable Redis, want error")
	}
}

func TestNewRejectsMalformedAddress(t *testing.T) {
	_, err := New(context.Background(), "redis://[::1]:namedport/0", logging.New("error", &bytes.Buffer{}))

	if err == nil {
		t.Fatal("New() accepted a malformed address, want error")
	}
}

func TestModeNamesTheActiveBackend(t *testing.T) {
	c, err := New(context.Background(), "", logging.New("error", &bytes.Buffer{}))
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	defer c.Close()

	if got := Mode(c); got != ModeMemory {
		t.Errorf("Mode() = %q, want %q", got, ModeMemory)
	}
}
