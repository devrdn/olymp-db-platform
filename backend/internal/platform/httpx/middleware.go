// Package httpx provides the HTTP building blocks shared by every route:
// request correlation, panic recovery, access logging and JSON responses.
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
					// With the stack. Without it the line says a panic
					// happened and on which path, which during a contest is
					// the difference between knowing and guessing — and a
					// panic is not the kind of thing that reproduces politely
					// afterwards.
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

// AccessLog records the outcome of every request. Correlation fields are added
// by the logger from the request context.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			rec := NewStatusRecorder(w)

			// Deferred, so a panic that gets past the recoverer still leaves a
			// line. A request that vanished from the log is the one nobody
			// counts, and 500s are what an alert is set on.
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

// ipv6SubjectBits is how much of an IPv6 address names one subscriber. A /64
// is the smallest network a provider hands out, and every host inside it is
// the same caller choosing a different address.
const ipv6SubjectBits = 64

// ClientSubject is the client address as a rate-limit subject: what every
// limiter keyed on an address must use instead of ClientIP.
//
// ClientIP stays exact, because the audit trail and a contest's network
// restriction need the real address. A budget does not: taken host by host,
// one IPv6 subscriber has a fresh budget for every address in their /64.
func ClientSubject(r *http.Request) string {
	return AddressSubject(ClientIP(r))
}

// AddressSubject turns an address into a rate-limit subject. IPv4 is kept
// exact, an IPv4-mapped IPv6 address is read as the IPv4 address it carries,
// and IPv6 is reduced to its /64 in prefix notation. A value that is not an
// address is returned unchanged: it cannot be grouped, and making it empty
// would merge it into whatever bucket an empty subject means to the caller.
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
