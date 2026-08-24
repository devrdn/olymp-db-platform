package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSecureHeadersSetsNoSniff(t *testing.T) {
	// The API returns JSON only; content sniffing turns a reflected value into
	// an execution vector.
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
	// Sending HSTS over plain HTTP is ignored by browsers and would pin
	// developers running the stack locally without TLS.
	rec := httptest.NewRecorder()

	SecureHeaders(okHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost/", nil))

	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("Strict-Transport-Security = %q, want it omitted for plain HTTP", got)
	}
}

func TestSecureHeadersSendsHSTSForForwardedHTTPS(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")

	SecureHeaders(okHandler).ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
		t.Error("Strict-Transport-Security missing for a request forwarded over TLS")
	}
}
