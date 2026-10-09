package httpx

import (
	"net/http"
	"strings"
)

// hstsValue pins the browser to TLS for a year, including subdomains.
const hstsValue = "max-age=31536000; includeSubDomains"

// SecureHeaders applies the response hardening every API route needs: no
// content sniffing, no framing, and HSTS only over TLS (over plain HTTP it
// would lock developers out of a local stack).
//
// Cache-Control: no-store is set for every route, not per route, because
// nearly everything served is private (personal data, answer keys on export)
// and a heuristically cached 200 GET lands on a shared lab machine's disk. A
// public, cacheable response must opt out explicitly.
func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		// no-store, not no-cache: no-cache still stores the response.
		h.Set("Cache-Control", "no-store")

		if isTLS(r) {
			h.Set("Strict-Transport-Security", hstsValue)
		}

		next.ServeHTTP(w, r)
	})
}

// isTLS reports whether the request reached the edge over TLS: a TLS
// connection, or X-Forwarded-Proto from a peer IPResolver trusts (CLAUDE.md
// rule 9). requestScheme builds the Origin check from this, so an untrusted
// header must not turn http into https and loosen it. A proxy not named in
// TRUSTED_PROXIES is treated as plain HTTP, the strict answer.
func isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !forwardedTrusted(r) {
		return false
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
