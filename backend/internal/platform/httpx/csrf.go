package httpx

import (
	"net/http"
	"net/url"
	"strings"
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
//
// `allowed` names front origins this deployment answers for, in addition to
// its own. Empty is the strict default and the shape the compose deployment
// uses: Caddy passes the browser's Host through, so the API's own host is the
// origin the page came from and nothing else has to be said.
//
// It exists because that identity is not universal. A development stack has
// no Caddy — the browser is on :3000 and Next's rewrite forwards /api/* to
// :8080, replacing Host on the way — and a production deployment may put the
// interface and the API on different names. In both, a browser's write is
// same-site to the person using it and cross-origin to this comparison. The
// answer is for the deployment to say which origin that is, not for the check
// to guess: an origin nobody configured is still refused.
func CheckOrigin(allowed []string) func(http.Handler) http.Handler {
	// Normalised once, at construction: an origin is a scheme and a host, and
	// comparing anything else (a path, a trailing slash, a case difference in
	// the scheme) would compare noise.
	permitted := make(map[string]struct{}, len(allowed))
	for _, origin := range allowed {
		if parsed, err := url.Parse(origin); err == nil && parsed.Host != "" {
			permitted[strings.ToLower(parsed.Scheme)+"://"+strings.ToLower(parsed.Host)] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
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
				Error(w, r, http.StatusForbidden, CodeCrossOrigin, "Request origin is not recognised")
				return
			}

			sameSite := parsed.Host == r.Host && parsed.Scheme == requestScheme(r)
			_, named := permitted[strings.ToLower(parsed.Scheme)+"://"+strings.ToLower(parsed.Host)]
			if !sameSite && !named {
				Error(w, r, http.StatusForbidden, CodeCrossOrigin, "Request origin is not recognised")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
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
