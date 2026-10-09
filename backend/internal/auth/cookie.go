package auth

import (
	"net/http"
	"time"
)

// CookieWriter writes and clears the session cookie. Secure is configured
// once per deployment rather than inferred from a forwarded header, which
// could silently drop it in production.
type CookieWriter struct {
	secure bool
}

func NewCookieWriter(secure bool) CookieWriter {
	return CookieWriter{secure: secure}
}

// Set writes the session cookie: HttpOnly keeps the token from scripts, and
// SameSite=Lax blocks cross-site POSTs while letting ordinary links carry the
// session.
func (c CookieWriter) Set(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, c.cookie(SessionCookieName, token, int(ttl.Seconds())))
}

func (c CookieWriter) Clear(w http.ResponseWriter) {
	http.SetCookie(w, c.cookie(SessionCookieName, "", -1))
}

// SetDevice writes the device cookie (see DeviceTrust) with the session
// cookie's protections. Sign-out does not clear it: it ends a session, not the
// fact that this browser is the owner's.
func (c CookieWriter) SetDevice(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, c.cookie(DeviceCookieName, token, int(ttl.Seconds())))
}

func (c CookieWriter) cookie(name, value string, maxAge int) *http.Cookie {
	// #nosec G124 -- Secure is configured, true outside development; forced on,
	// a local stack without TLS could not sign in. HttpOnly and SameSite are
	// unconditional.
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	}
}
