// Package httpx provides the HTTP building blocks shared by every route:
// request correlation, panic recovery, access logging, JSON responses, the
// Origin check and the one trust boundary for forwarded headers. It imports
// no domain package.
package httpx

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/google/uuid"
)

// RequestIDHeader carries the correlation identifier between the frontend, the
// Core API and the Query Runner.
const RequestIDHeader = "X-Request-Id"

type Middleware func(http.Handler) http.Handler

// RequestID ensures every request carries a correlation identifier. An
// incoming header is reused only when it is a well-formed UUID, so client
// input cannot forge log lines or corrupt correlation.
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
// panic value is logged, never sent to the client.
func Recoverer(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					// With the stack: a panic rarely reproduces afterwards.
					log.ErrorContext(r.Context(), "recovered from panic",
						"panic", v,
						"method", r.Method,
						"path", r.URL.Path,
						"stack", string(debug.Stack()),
					)
					Error(w, r, http.StatusInternalServerError, CodeInternalError, "Internal server error")
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog records the outcome of every request.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			rec := NewStatusRecorder(w)

			// Deferred, so a panic that gets past the recoverer still leaves a
			// line.
			defer func() {
				log.InfoContext(r.Context(), "http request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", rec.Status(),
					"bytes", rec.BytesWritten(),
					"duration_ms", time.Since(started).Milliseconds(),
				)
			}()

			next.ServeHTTP(rec, r)
		})
	}
}

// ClientIP returns the address the request came from: IPResolver's answer
// when its middleware ran, otherwise the TCP peer. It never reads forwarded
// headers itself (CLAUDE.md rule 9).
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

// ipv6SubjectBits is how much of an IPv6 address names one subscriber: a /64
// is the smallest network a provider hands out.
const ipv6SubjectBits = 64

// ClientSubject is the client address as a rate-limit subject, which every
// address-keyed limiter must use instead of ClientIP: otherwise one IPv6
// subscriber gets a fresh budget per address in their /64. ClientIP stays
// exact for the audit trail and network restrictions (CLAUDE.md rule 9).
func ClientSubject(r *http.Request) string {
	return AddressSubject(ClientIP(r))
}

// AddressSubject turns an address into a rate-limit subject: IPv4 (including
// IPv4-mapped IPv6) is kept exact, IPv6 is reduced to its /64. A value that is
// not an address is returned unchanged, not emptied, so it does not merge into
// another bucket.
func AddressSubject(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(ipv6SubjectBits)
	if err != nil {
		return ip
	}
	return prefix.String()
}
