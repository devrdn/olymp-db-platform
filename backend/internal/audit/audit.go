// Package audit records who did what, from where and when.
//
// The trail answers questions after the fact — why did this participant lose
// access, who changed this question after publication — so entries are written
// in the same transaction as the action they describe. Either both land or
// neither does; an action with no record, or a record of something that was
// rolled back, would both make the trail untrustworthy.
package audit

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/google/uuid"
)

// Action codes. Constants rather than literals so a rename is a compile error
// and the set is discoverable.
const (
	ActionAuthLogin         = "auth.login"
	ActionAuthLoginFailed   = "auth.login_failed"
	ActionAuthLogout        = "auth.logout"
	ActionUserCreate        = "user.create"
	ActionUserUpdate        = "user.update"
	ActionUserBlock         = "user.block"
	ActionUserUnblock       = "user.unblock"
	ActionUserRolesChange   = "user.roles_change"
	ActionUserPasswordReset = "user.password_reset"
	ActionPasswordChange    = "user.password_change"
)

// maxUserAgentLength bounds a header the client controls. The column is kept
// for a year, so an unbounded value is storage someone else gets to spend.
const maxUserAgentLength = 512

// sensitiveKeys never reach the trail, whatever a caller passes in.
var sensitiveKeys = map[string]struct{}{
	"password":         {},
	"new_password":     {},
	"old_password":     {},
	"current_password": {},
	"password_hash":    {},
	"token":            {},
	"secret":           {},
	"session":          {},
	"authorization":    {},
}

// Entry is one line of the trail.
type Entry struct {
	// ActorID is nil for system events such as a scheduled contest transition.
	ActorID   *uuid.UUID
	Action    string
	Entity    string
	EntityID  string
	Payload   map[string]any
	IP        string
	UserAgent string
}

// Sink stores entries. It is satisfied by the PostgreSQL implementation and by
// test doubles.
type Sink interface {
	Append(ctx context.Context, e Entry) error
}

// Recorder validates and sanitises entries before handing them to a sink.
type Recorder struct {
	sink Sink
}

// New returns a recorder writing to sink.
func New(sink Sink) *Recorder {
	return &Recorder{sink: sink}
}

// Record writes one entry.
//
// When the surrounding request runs inside a unit of work, the sink writes
// through the ambient transaction, which is what ties the record to the action.
func (r *Recorder) Record(ctx context.Context, e Entry) error {
	if e.Action == "" {
		return errors.New("audit entry has no action")
	}

	e.Payload = redact(e.Payload)

	// An explicit origin (the login flow resolves its own) wins; everything
	// else inherits the request's.
	if meta, ok := ctx.Value(metaKey{}).(requestMeta); ok {
		if e.IP == "" {
			e.IP = meta.ip
		}
		if e.UserAgent == "" {
			e.UserAgent = meta.userAgent
		}
	}

	if err := r.sink.Append(ctx, e); err != nil {
		return fmt.Errorf("append audit entry: %w", err)
	}
	return nil
}

// metaKey carries the request origin through the context.
type metaKey struct{}

type requestMeta struct {
	ip        string
	userAgent string
}

// WithRequestMeta returns a context carrying the request origin. Record fills
// entries from it when the caller did not set an origin explicitly, so every
// audit write in a request names where the action came from without each call
// site remembering to.
func WithRequestMeta(ctx context.Context, ip, userAgent string) context.Context {
	if len(userAgent) > maxUserAgentLength {
		userAgent = userAgent[:maxUserAgentLength]
	}
	return context.WithValue(ctx, metaKey{}, requestMeta{ip: ip, userAgent: userAgent})
}

// RequestMeta is the HTTP middleware form of WithRequestMeta. It runs after
// the client-IP resolver, so the recorded address is the proxy-aware one.
func RequestMeta(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := WithRequestMeta(r.Context(), httpx.ClientIP(r), r.UserAgent())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// redact returns a copy of the payload with sensitive values removed, at any
// depth. Handlers pass request data straight through, so this is the one place
// that has to be right.
func redact(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}

	out := make(map[string]any, len(payload))
	for key, value := range payload {
		if _, sensitive := sensitiveKeys[key]; sensitive {
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			out[key] = redact(nested)
			continue
		}
		out[key] = value
	}
	return out
}
