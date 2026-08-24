package cache

import "testing"

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
