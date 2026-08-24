package httpx

import (
	"net/http"
	"net/url"
)

// CheckOrigin rejects state-changing requests that come from another site.
//
// The session lives in a cookie, so a browser attaches it to any request the
// page makes — including one a malicious site triggers. SameSite=Lax is the
// first defence; this is the second, because a single cookie attribute is a
// thin thing to rest an entire authorisation model on.
//
// Safe methods pass untouched, and so does a request with no Origin at all:
// browsers always send it on cross-origin writes, while curl, health probes
// and server-side integrations send nothing. Requiring the header would break
// every non-browser client without stopping the attack it targets.
func CheckOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" {
			Error(w, r, http.StatusForbidden, "cross_origin", "Request origin is not recognised")
			return
		}

		if parsed.Host != r.Host || parsed.Scheme != requestScheme(r) {
			Error(w, r, http.StatusForbidden, "cross_origin", "Request origin is not recognised")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// requestScheme reports the scheme the browser used. Behind the reverse proxy
// the request itself arrives over plain HTTP, so the forwarded header is what
// the Origin has to be compared against.
func requestScheme(r *http.Request) string {
	if isTLS(r) {
		return "https"
	}
	return "http"
}
