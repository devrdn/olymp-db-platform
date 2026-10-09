package api

import (
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
)

// requestMeta puts the request's origin on the context, so every audit entry
// written while serving it records where it came from. HTTP plumbing, so it
// lives here rather than in audit. It must run after the client-IP resolver so
// the address is the proxy-aware one.
func requestMeta(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := audit.WithRequestMeta(r.Context(), httpx.ClientIP(r), r.UserAgent())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
