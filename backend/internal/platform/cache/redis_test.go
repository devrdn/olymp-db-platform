package cache

import (
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
	"time"
)

func TestRedisMissIsReportedAsNotFoundRatherThanError(t *testing.T) {
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

func TestOptionsBoundHowLongACacheCallCanTake(t *testing.T) {
	opts, err := Options("redis://localhost:6379/0")
	if err != nil {
		t.Fatalf("Options() = %v", err)
	}

	if opts.ReadTimeout <= 0 || opts.ReadTimeout > time.Second {
		t.Errorf("ReadTimeout = %v, want a bound of at most a second", opts.ReadTimeout)
	}
	if opts.WriteTimeout <= 0 || opts.WriteTimeout > time.Second {
		t.Errorf("WriteTimeout = %v, want a bound of at most a second", opts.WriteTimeout)
	}
	if opts.DialTimeout <= 0 || opts.DialTimeout > 2*time.Second {
		t.Errorf("DialTimeout = %v, want a bound of at most two seconds", opts.DialTimeout)
	}
	if opts.MaxRetries > 1 {
		t.Errorf("MaxRetries = %d, want at most one retry", opts.MaxRetries)
	}
}

func TestOptionsKeepTimeoutsTheAddressNames(t *testing.T) {
	opts, err := Options("redis://localhost:6379/0?read_timeout=4s")
	if err != nil {
		t.Fatalf("Options() = %v", err)
	}

	if opts.ReadTimeout != 4*time.Second {
		t.Errorf("ReadTimeout = %v, want the 4s the address asked for", opts.ReadTimeout)
	}
}
