package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecureHeadersSetsNoSniff(t *testing.T) {
	rec := httptest.NewRecorder()

	SecureHeaders(okHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestSecureHeadersForbidsFraming(t *testing.T) {
	rec := httptest.NewRecorder()

	SecureHeaders(okHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
		t.Errorf("Content-Security-Policy = %q, want frame-ancestors 'none'", got)
	}
}

func TestSecureHeadersSuppressesReferrer(t *testing.T) {
	rec := httptest.NewRecorder()

	SecureHeaders(okHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

func TestSecureHeadersOmitsHSTSOnPlainHTTP(t *testing.T) {
	rec := httptest.NewRecorder()

	SecureHeaders(okHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost/", nil))

	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("Strict-Transport-Security = %q, want it omitted for plain HTTP", got)
	}
}

func TestSecureHeadersSendsHSTSForForwardedHTTPS(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.28.0.10:52000"
	req.Header.Set("X-Forwarded-Proto", "https")

	resolver(t, "172.28.0.0/16").Middleware(SecureHeaders(okHandler)).ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
		t.Error("Strict-Transport-Security missing for a request forwarded over TLS by a trusted proxy")
	}
}

func TestSecureHeadersIgnoresAForwardedProtoFromAnUntrustedPeer(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.7:41000"
	req.Header.Set("X-Forwarded-Proto", "https")

	resolver(t, "172.28.0.0/16").Middleware(SecureHeaders(okHandler)).ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("Strict-Transport-Security = %q for a direct caller's own claim about the scheme", got)
	}
}

func TestSecureHeadersForbidsStoringTheResponse(t *testing.T) {
	rec := httptest.NewRecorder()

	SecureHeaders(okHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}
