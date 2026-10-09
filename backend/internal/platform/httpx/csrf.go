package httpx

import (
	"net/http"
	"net/url"
	"strings"
)

// CheckOrigin rejects state-changing requests that come from another site,
// as a second defence behind SameSite=Lax on the session cookie.
//
// Safe methods pass, and so does a request with no Origin: browsers always
// send it on cross-origin writes, while non-browser clients send none.
// Requiring it would break those clients without stopping the attack.
//
// allowed names front origins this deployment answers for besides its own,
// for setups where the interface and the API have different hosts (a dev
// stack on :3000 and :8080). Empty is the strict default; an origin nobody
// configured is refused.
func CheckOrigin(allowed []string) func(http.Handler) http.Handler {
	// Normalised to scheme and host once, at construction.
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
// the request arrives over plain HTTP, so a trusted forwarded header decides.
func requestScheme(r *http.Request) string {
	if isTLS(r) {
		return "https"
	}
	return "http"
}
