package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie was set; headers: %v", SessionCookieName, rec.Header())
	return nil
}

func TestSessionCookieIsNotReadableByScript(t *testing.T) {
	// The single most valuable property of the cookie: an XSS bug on the page
	// still cannot read the session token.
	rec := httptest.NewRecorder()

	NewCookieWriter(true).Set(rec, "token-value", time.Hour)

	if !sessionCookie(t, rec).HttpOnly {
		t.Error("the session cookie is not HttpOnly")
	}
}

func TestSessionCookieIsRestrictedToSameSiteRequests(t *testing.T) {
	// SameSite is the first line against cross-site request forgery; the
	// Origin check on mutating requests is the second.
	rec := httptest.NewRecorder()

	NewCookieWriter(true).Set(rec, "token-value", time.Hour)

	if got := sessionCookie(t, rec).SameSite; got != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", got)
	}
}

func TestSessionCookieIsScopedToTheWholeSite(t *testing.T) {
	rec := httptest.NewRecorder()

	NewCookieWriter(true).Set(rec, "token-value", time.Hour)

	if got := sessionCookie(t, rec).Path; got != "/" {
		t.Errorf("Path = %q, want /", got)
	}
}

func TestSessionCookieIsSecureWhenConfiguredSo(t *testing.T) {
	// Whether the deployment is served over TLS is a deployment fact, not
	// something to infer per request from a header the proxy may not set.
	rec := httptest.NewRecorder()
	writer := NewCookieWriter(true)

	writer.Set(rec, "token-value", time.Hour)

	if !sessionCookie(t, rec).Secure {
		t.Error("the cookie is not marked Secure although the deployment is TLS-only")
	}
}

func TestSessionCookieIsNotSecureWhenConfiguredForPlainHTTP(t *testing.T) {
	// A Secure cookie is dropped by the browser over plain HTTP, which would
	// make a local stack without a certificate impossible to log into.
	rec := httptest.NewRecorder()
	writer := NewCookieWriter(false)

	writer.Set(rec, "token-value", time.Hour)

	if sessionCookie(t, rec).Secure {
		t.Error("the cookie is marked Secure, so a plain-HTTP browser would drop it")
	}
}

func TestClearSessionCookieExpiresIt(t *testing.T) {
	rec := httptest.NewRecorder()

	NewCookieWriter(true).Clear(rec)

	cookie := sessionCookie(t, rec)
	if cookie.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want a negative value that deletes the cookie", cookie.MaxAge)
	}
	if cookie.Value != "" {
		t.Errorf("Value = %q, want it emptied", cookie.Value)
	}
}
