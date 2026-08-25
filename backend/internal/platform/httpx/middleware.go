// Package httpx provides the HTTP building blocks shared by every route:
// request correlation, panic recovery, access logging and JSON responses.
package httpx

import (
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/google/uuid"
)

// RequestIDHeader carries the correlation identifier between the frontend, the
// Core API and the Query Runner.
const RequestIDHeader = "X-Request-Id"

// Middleware is the standard decorator shape used across the service.
type Middleware func(http.Handler) http.Handler

// RequestID ensures every request carries a correlation identifier. An
// incoming header is reused only when it is a well-formed UUID: arbitrary
// client input must never reach the logs or the query journal, where it could
// forge log lines or corrupt correlation.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if _, err := uuid.Parse(id); err != nil {
			id = uuid.NewString()
		}

		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id)))
	})
}

// Recoverer converts a panic in a downstream handler into a 500 response. The
// panic value is logged but never sent to the client, which would leak
// internal details.
func Recoverer(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					log.ErrorContext(r.Context(), "recovered from panic",
						"panic", v,
						"method", r.Method,
						"path", r.URL.Path,
					)
					Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog records the outcome of every request. Correlation fields are added
// by the logger from the request context.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			rec := NewStatusRecorder(w)

			next.ServeHTTP(rec, r)

			log.InfoContext(r.Context(), "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.Status(),
				"bytes", rec.BytesWritten(),
				"duration_ms", time.Since(started).Milliseconds(),
			)
		})
	}
}

// ClientIP returns the address the request came from.
//
// When the IPResolver middleware ran, this is the proxy-aware resolution it
// stored; otherwise it falls back to the TCP peer. The fallback never reads
// forwarded headers — a header is client-supplied unless a trusted proxy
// vouched for it, and that judgement lives in IPResolver, in one place.
func ClientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(clientIPKey{}).(string); ok && ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	return host
}
