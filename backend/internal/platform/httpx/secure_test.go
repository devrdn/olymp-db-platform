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

// Nothing this API serves is cacheable, and one of the things it serves is
// every reference answer of a contest in a single file (GET
// /contests/{id}/export). With no directive a plain 200 GET is heuristically
// cacheable, which puts an answer key in a browser's disk cache on whatever
// machine an organizer downloaded it from.
func TestSecureHeadersForbidsStoringTheResponse(t *testing.T) {
	rec := httptest.NewRecorder()

	SecureHeaders(okHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	// no-store and not no-cache: no-cache permits storing the response and
	// only asks for it to be revalidated, which still leaves it on the disk.
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}
