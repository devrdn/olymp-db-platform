package auth

import (
	"net/http"
	"time"
)

// CookieWriter writes and clears the session cookie.
//
// Whether the deployment is served over TLS is a property of the deployment,
// so it is configured once rather than inferred per request from a forwarded
// header the reverse proxy may or may not set. Getting that inference wrong
// silently drops the Secure attribute in production, which is exactly the
// mistake worth designing out.
type CookieWriter struct {
	secure bool
}

// NewCookieWriter returns a writer that marks cookies Secure when the
// deployment is served over TLS.
func NewCookieWriter(secure bool) CookieWriter {
	return CookieWriter{secure: secure}
}

// Set writes the session cookie.
//
// HttpOnly keeps the token out of reach of any script on the page, which is
// the reason it lives in a cookie rather than in local storage. SameSite=Lax
// blocks the cross-site POSTs that CSRF relies on while still allowing an
// ordinary link into the application to carry the session.
func (c CookieWriter) Set(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, c.cookie(token, int(ttl.Seconds())))
}

// Clear deletes the cookie, so a browser holding a dead session stops sending
// it on every request.
func (c CookieWriter) Clear(w http.ResponseWriter) {
	http.SetCookie(w, c.cookie("", -1))
}

func (c CookieWriter) cookie(value string, maxAge int) *http.Cookie {
	// #nosec G124 -- Secure is deliberately configured rather than hard-coded.
	// It defaults to true outside development (see platform/config); forcing
	// it on would make a local stack without a certificate impossible to sign
	// into, because browsers silently drop Secure cookies over plain HTTP.
	// HttpOnly and SameSite, which have no such trade-off, are unconditional.
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   c.secure,
		SameSite: http.SameSiteLaxMode,
	}
}
