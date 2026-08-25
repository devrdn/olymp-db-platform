package cache

import (
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
)

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

func TestOptionsRejectsMalformedAddress(t *testing.T) {
	_, err := Options("redis://[::1]:namedport/0")

	if err == nil {
		t.Fatal("Options() succeeded, want error for malformed address")
	}
}

func TestOptionsAcceptsHostPortForm(t *testing.T) {
	// docker-compose passes a bare host:port; the URL form is optional.
	opts, err := Options("redis:6379")
	if err != nil {
		t.Fatalf("Options() returned error: %v", err)
	}

	if opts.Addr != "redis:6379" {
		t.Errorf("Addr = %q, want redis:6379", opts.Addr)
	}
}

func TestOptionsAcceptsURLForm(t *testing.T) {
	opts, err := Options("redis://cache.internal:6380/3")
	if err != nil {
		t.Fatalf("Options() returned error: %v", err)
	}

	if opts.Addr != "cache.internal:6380" {
		t.Errorf("Addr = %q, want cache.internal:6380", opts.Addr)
	}
	if opts.DB != 3 {
		t.Errorf("DB = %d, want 3", opts.DB)
	}
}
