package cache

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/redis/go-redis/v9"
)

func TestNewFallsBackToMemoryWhenNoAddressIsConfigured(t *testing.T) {
	// "No Redis configured" is a supported deployment, not an error.
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
	// A silent downgrade would be the dangerous case: sessions and rate limits
	// stop being shared, and nothing on screen says so.
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
	// An operator who set an address expects that address to be used. Quietly
	// swapping in a different store would hide a broken deployment.
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
	// Readiness reports this, so operators can see a degraded install.
	c, err := New(context.Background(), "", logging.New("error", &bytes.Buffer{}))
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	defer c.Close()

	if got := Mode(c); got != ModeMemory {
		t.Errorf("Mode() = %q, want %q", got, ModeMemory)
	}
}

func TestRedisMissIsReportedAsNotFoundRatherThanError(t *testing.T) {
	// redis.Nil means "no such key"; treating it as a failure would turn every
	// cache miss into a request error.
	value, found, err := classifyGet("", redis.Nil)

	if err != nil {
		t.Errorf("err = %v, want nil for a missing key", err)
	}
	if found {
		t.Error("found = true for a missing key")
	}
	if value != nil {
		t.Errorf("value = %q, want nil", value)
	}
}

func TestRedisFailureIsReportedAsError(t *testing.T) {
	wantErr := errors.New("connection reset")

	_, found, err := classifyGet("", wantErr)

	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want it to wrap %v", err, wantErr)
	}
	if found {
		t.Error("found = true despite a backend failure")
	}
}

func TestRedisHitIsReturned(t *testing.T) {
	value, found, err := classifyGet("stored", nil)

	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !found {
		t.Fatal("found = false for a stored key")
	}
	if string(value) != "stored" {
		t.Errorf("value = %q, want stored", value)
	}
}
