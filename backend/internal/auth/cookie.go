package auth

import (
	"net/http"
	"time"
)

// CookieWriter writes and clears the session, device, sign-up and Microsoft
// state cookies. Secure is configured once per deployment rather than
// inferred from a forwarded header, which could silently drop it in
// production.
type CookieWriter struct {
	secure bool
}

func NewCookieWriter(secure bool) CookieWriter {
	return CookieWriter{secure: secure}
}

// SessionName is the session cookie's name. A browser accepts the __Host-
// prefix only from a secure origin, host-only and with Path=/, so no sibling
// host can plant a cookie of that name; without TLS it refuses the prefix.
func (c CookieWriter) SessionName() string {
	if c.secure {
		return "__Host-dbcontest_session"
	}
	return "dbcontest_session"
}

// Set writes the session cookie: HttpOnly keeps the token from scripts, and
// SameSite=Lax blocks cross-site POSTs while letting ordinary links carry the
// session.
func (c CookieWriter) Set(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, c.cookie(c.SessionName(), token, int(ttl.Seconds())))
}

func (c CookieWriter) Clear(w http.ResponseWriter) {
	http.SetCookie(w, c.cookie(c.SessionName(), "", -1))
}

// SetDevice writes the device cookie (see DeviceTrust) with the session
// cookie's protections. Sign-out does not clear it: it ends a session, not the
// fact that this browser is the owner's.
func (c CookieWriter) SetDevice(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, c.cookie(DeviceCookieName, token, int(ttl.Seconds())))
}

// SignupName is the name of the cookie that binds a pending sign-up to the
// browser that started it, prefixed as SessionName is.
func (c CookieWriter) SignupName() string {
	if c.secure {
		return "__Host-dbcontest_signup"
	}
	return "dbcontest_signup"
}

// SetSignup writes the sign-up cookie with the session cookie's protections.
func (c CookieWriter) SetSignup(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, c.cookie(c.SignupName(), token, int(ttl.Seconds())))
}

func (c CookieWriter) ClearSignup(w http.ResponseWriter) {
	http.SetCookie(w, c.cookie(c.SignupName(), "", -1))
}

// MicrosoftStateName is the name of the cookie carrying a Microsoft sign-in
// from its start to its callback, prefixed as SessionName is.
func (c CookieWriter) MicrosoftStateName() string {
	if c.secure {
		return "__Host-dbcontest_ms_state"
	}
	return "dbcontest_ms_state"
}

// SetMicrosoftState writes the state cookie with the session cookie's
// protections. SameSite=Lax still sends it on Microsoft's redirect back,
// which is a top-level GET.
func (c CookieWriter) SetMicrosoftState(w http.ResponseWriter, sealed string, ttl time.Duration) {
	http.SetCookie(w, c.cookie(c.MicrosoftStateName(), sealed, int(ttl.Seconds())))
}

func (c CookieWriter) ClearMicrosoftState(w http.ResponseWriter) {
	http.SetCookie(w, c.cookie(c.MicrosoftStateName(), "", -1))
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
