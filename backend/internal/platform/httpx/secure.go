package httpx

import "net/http"

// hstsValue pins the browser to TLS for a year, including subdomains.
const hstsValue = "max-age=31536000; includeSubDomains"

// SecureHeaders applies the response hardening that every API route needs.
//
// The API serves JSON only, so it forbids content sniffing and framing
// outright. HSTS is sent only when the request actually arrived over TLS:
// announcing it over plain HTTP has no effect in browsers and would lock
// developers out of a local stack that has no certificate.
func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")

		if isTLS(r) {
			h.Set("Strict-Transport-Security", hstsValue)
		}

		next.ServeHTTP(w, r)
	})
}

// isTLS reports whether the request reached the edge over TLS. The forwarded
// header is only meaningful because the reverse proxy is the sole ingress and
// overwrites it; nothing security-critical depends on this value.
func isTLS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
