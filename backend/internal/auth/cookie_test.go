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
	rec := httptest.NewRecorder()

	NewCookieWriter(true).Set(rec, "token-value", time.Hour)

	if !sessionCookie(t, rec).HttpOnly {
		t.Error("the session cookie is not HttpOnly")
	}
}

func TestSessionCookieIsRestrictedToSameSiteRequests(t *testing.T) {
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
	rec := httptest.NewRecorder()
	writer := NewCookieWriter(true)

	writer.Set(rec, "token-value", time.Hour)

	if !sessionCookie(t, rec).Secure {
		t.Error("the cookie is not marked Secure although the deployment is TLS-only")
	}
}

func TestSessionCookieIsNotSecureWhenConfiguredForPlainHTTP(t *testing.T) {
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

func TestTheDeviceCookieIsHttpOnlySameSiteAndLongLived(t *testing.T) {
	rec := httptest.NewRecorder()

	NewCookieWriter(true).SetDevice(rec, "device-token", 30*24*time.Hour)

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == DeviceCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("no %s cookie was set", DeviceCookieName)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Errorf("cookie = %+v, want HttpOnly, Secure as configured, SameSite=Lax, Path=/", cookie)
	}
	if cookie.MaxAge != int((30 * 24 * time.Hour).Seconds()) {
		t.Errorf("MaxAge = %d, want thirty days", cookie.MaxAge)
	}
	if cookie.Value != "device-token" {
		t.Errorf("Value = %q", cookie.Value)
	}
}
