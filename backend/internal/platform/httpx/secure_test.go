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
	req.RemoteAddr = "172.28.0.10:52000"
	req.Header.Set("X-Forwarded-Proto", "https")

	resolver(t, "172.28.0.0/16").Middleware(SecureHeaders(okHandler)).ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
		t.Error("Strict-Transport-Security missing for a request forwarded over TLS by a trusted proxy")
	}
}

// The same header from a peer nobody vouched for says nothing. HSTS is the
// harmless half of what isTLS decides — announcing it over plain HTTP is
// ignored by browsers — but the header goes through one gate, not two, so
// that the answer here and the answer requestScheme (csrf.go) builds a
// same-site comparison out of cannot drift apart (CLAUDE.md rule 9).
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
