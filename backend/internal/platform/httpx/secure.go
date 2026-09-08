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
//
// # Why no-store is here and not on the routes that need it most
//
// Nothing this router serves is public. Every route below is behind a session,
// and what comes back is somebody's personal data, a contest's own content, or
// — on GET /contests/{id}/export — every reference answer of an olympiad in one
// file. With no directive at all, a browser and any intermediary are free to
// apply heuristic freshness to a plain 200 GET, which is how an answer key ends
// up in a disk cache on a shared machine in a computer lab, and how a
// participant's own query log ends up in the next person's back button.
//
// Naming the individual routes would be the tighter change and the wrong one.
// The set that needs it is "everything", so opting in per route makes the
// default the unsafe one — the next answer-key-shaped endpoint ships without
// it, which is exactly how this one shipped. A shared header fails safe, and
// the cost is that a genuinely public, cacheable response would have to say so
// for itself. There is no such response here today, and one added later is a
// deliberate override in one place rather than an omission in another.
func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		// no-store rather than no-cache: no-cache permits storing the response
		// and only requires revalidating it, which still leaves the answer key
		// on the disk.
		h.Set("Cache-Control", "no-store")

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
