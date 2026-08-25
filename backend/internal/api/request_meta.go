package api

import (
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
)

// requestMeta puts the request's origin on the context, so every audit entry
// written while serving it names where the action came from without each call
// site having to remember.
//
// It lives here rather than in package audit because it is HTTP plumbing: the
// audit domain should not have to know that requests, headers or proxies
// exist. It must run after the client-IP resolver, so the address recorded is
// the proxy-aware one.
func requestMeta(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := audit.WithRequestMeta(r.Context(), httpx.ClientIP(r), r.UserAgent())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
