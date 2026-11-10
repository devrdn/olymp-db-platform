package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The interface reads the session cookie by these names; they are a contract,
// not a detail of this package.
const (
	secureSessionName = "__Host-dbcontest_session"
	plainSessionName  = "dbcontest_session"
)

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %s cookie was set; headers: %v", name, rec.Header())
	return nil
}

func TestSessionCookieIsNotReadableByScript(t *testing.T) {
	rec := httptest.NewRecorder()

	NewCookieWriter(true).Set(rec, "token-value", time.Hour)

	if !sessionCookie(t, rec, secureSessionName).HttpOnly {
		t.Error("the session cookie is not HttpOnly")
	}
}

func TestSessionCookieIsRestrictedToSameSiteRequests(t *testing.T) {
	rec := httptest.NewRecorder()

	NewCookieWriter(true).Set(rec, "token-value", time.Hour)

	if got := sessionCookie(t, rec, secureSessionName).SameSite; got != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", got)
	}
}

func TestSessionCookieIsScopedToTheWholeSite(t *testing.T) {
	rec := httptest.NewRecorder()

	NewCookieWriter(true).Set(rec, "token-value", time.Hour)

	if got := sessionCookie(t, rec, secureSessionName).Path; got != "/" {
		t.Errorf("Path = %q, want /", got)
	}
}

func TestSessionCookieIsSecureWhenConfiguredSo(t *testing.T) {
	rec := httptest.NewRecorder()
	writer := NewCookieWriter(true)

	writer.Set(rec, "token-value", time.Hour)

	if !sessionCookie(t, rec, secureSessionName).Secure {
		t.Error("the cookie is not marked Secure although the deployment is TLS-only")
	}
}

func TestSessionCookieIsNotSecureWhenConfiguredForPlainHTTP(t *testing.T) {
	rec := httptest.NewRecorder()
	writer := NewCookieWriter(false)

	writer.Set(rec, "token-value", time.Hour)

	if sessionCookie(t, rec, plainSessionName).Secure {
		t.Error("the cookie is marked Secure, so a plain-HTTP browser would drop it")
	}
}

func TestASecureSessionCookieIsBoundToThisHost(t *testing.T) {
	rec := httptest.NewRecorder()
	writer := NewCookieWriter(true)

	writer.Set(rec, "token-value", time.Hour)

	cookie := sessionCookie(t, rec, secureSessionName)
	if !cookie.Secure || cookie.Path != "/" || cookie.Domain != "" {
		t.Errorf("cookie = %+v, want Secure, Path=/ and no Domain, which a browser demands of the __Host- prefix", cookie)
	}
	if got := writer.SessionName(); got != secureSessionName {
		t.Errorf("SessionName() = %q, want %q", got, secureSessionName)
	}
}

func TestAPlainHTTPSessionCookieKeepsTheBareName(t *testing.T) {
	rec := httptest.NewRecorder()
	writer := NewCookieWriter(false)

	writer.Set(rec, "token-value", time.Hour)

	sessionCookie(t, rec, plainSessionName)
	if got := writer.SessionName(); got != plainSessionName {
		t.Errorf("SessionName() = %q, want %q: a browser refuses the __Host- prefix without TLS", got, plainSessionName)
	}
}

func TestClearSessionCookieExpiresIt(t *testing.T) {
	rec := httptest.NewRecorder()

	NewCookieWriter(true).Clear(rec)

	// A browser ignores an expiring __Host- cookie that lacks Secure or Path=/.
	cookie := sessionCookie(t, rec, secureSessionName)
	if cookie.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want a negative value that deletes the cookie", cookie.MaxAge)
	}
	if cookie.Value != "" {
		t.Errorf("Value = %q, want it emptied", cookie.Value)
	}
	if !cookie.Secure || cookie.Path != "/" || cookie.Domain != "" {
		t.Errorf("cookie = %+v, want Secure, Path=/ and no Domain", cookie)
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
