package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func csrfRequest(method, origin string) *http.Request {
	req := httptest.NewRequest(method, "https://contest.university.edu/api/v1/users", nil)
	req.Host = "contest.university.edu"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

func TestSafeMethodsPassWithoutAnOrigin(t *testing.T) {
	// A GET carries no side effect, and requiring the header would break
	// ordinary navigation and monitoring.
	rec := httptest.NewRecorder()

	CheckOrigin(nil)(okHandler).ServeHTTP(rec, csrfRequest(http.MethodGet, ""))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for a safe method", rec.Code)
	}
}

func TestMutatingRequestFromTheSameOriginIsAllowed(t *testing.T) {
	rec := httptest.NewRecorder()

	CheckOrigin(nil)(okHandler).ServeHTTP(rec, csrfRequest(http.MethodPost, "https://contest.university.edu"))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestMutatingRequestFromAnotherOriginIsRejected(t *testing.T) {
	// This is the request a malicious page makes with the victim's cookie
	// attached; SameSite is the first line and this is the second.
	rec := httptest.NewRecorder()

	CheckOrigin(nil)(okHandler).ServeHTTP(rec, csrfRequest(http.MethodPost, "https://evil.example"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a cross-origin write", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "cross_origin") {
		t.Errorf("body = %s, want a structured cross_origin error", rec.Body.String())
	}
}

func TestMutatingRequestWithNoOriginIsAllowedForNonBrowserClients(t *testing.T) {
	// curl, a health probe and a server-side integration send no Origin.
	// Browsers always do on cross-origin writes, which is what this defends
	// against; rejecting the absent header would break every scripted client.
	rec := httptest.NewRecorder()

	CheckOrigin(nil)(okHandler).ServeHTTP(rec, csrfRequest(http.MethodPost, ""))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 when no Origin is present", rec.Code)
	}
}

func TestOriginWithADifferentSchemeIsRejected(t *testing.T) {
	// http://host and https://host are different origins; accepting the plain
	// one would let a downgraded page write.
	rec := httptest.NewRecorder()

	CheckOrigin(nil)(okHandler).ServeHTTP(rec, csrfRequest(http.MethodPost, "http://contest.university.edu"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a scheme mismatch", rec.Code)
	}
}

func TestOriginIsComparedAgainstTheForwardedHost(t *testing.T) {
	// Behind the reverse proxy the request arrives over plain HTTP while the
	// browser saw HTTPS, so the comparison has to use the forwarded scheme.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://contest.university.edu/api/v1/users", nil)
	req.Host = "contest.university.edu"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", "https://contest.university.edu")

	CheckOrigin(nil)(okHandler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestMalformedOriginIsRejected(t *testing.T) {
	rec := httptest.NewRecorder()

	CheckOrigin(nil)(okHandler).ServeHTTP(rec, csrfRequest(http.MethodPost, "://nonsense"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for an unparseable Origin", rec.Code)
	}
}

// The one request in this product that a *browser* makes directly to the API
// is a chunk of an uploaded dump: everything else that changes state goes
// through a Next server action, which is server-to-server and carries no
// Origin at all. So this rule had never met a real browser write until the
// upload existed, and the first one failed.
//
// Behind the reverse proxy the two agree — Caddy passes the browser's Host
// through, so Origin's host is the request's host. A development stack has no
// Caddy: the browser is on :3000, Next's rewrite forwards /api/* to :8080 and
// replaces Host on the way, and the comparison sees localhost:3000 against
// localhost:8080. The same split is a legitimate production shape too, where
// the interface and the API answer on different names.
//
// A deployment that has that split names its own front origin. Nothing else
// changes: an origin nobody configured is still refused.
func TestAConfiguredFrontOriginIsAcceptedOnADifferentHost(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "http://localhost:8080/api/v1/x", nil)
	req.Host = "localhost:8080"
	req.Header.Set("Origin", "http://localhost:3000")

	CheckOrigin([]string{"http://localhost:3000"})(okHandler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestAnUnconfiguredOriginOnADifferentHostIsStillRefused(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "http://localhost:8080/api/v1/x", nil)
	req.Host = "localhost:8080"
	req.Header.Set("Origin", "http://evil.example")

	CheckOrigin([]string{"http://localhost:3000"})(okHandler).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for an origin nobody configured", rec.Code)
	}
}
