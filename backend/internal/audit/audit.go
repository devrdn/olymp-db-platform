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
	"net"
	"net/http"

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

	if err := r.sink.Append(ctx, e); err != nil {
		return fmt.Errorf("append audit entry: %w", err)
	}
	return nil
}

// FromRequest fills in the caller's address and agent.
func FromRequest(r *http.Request, e Entry) Entry {
	e.IP = clientIP(r)

	agent := r.UserAgent()
	if len(agent) > maxUserAgentLength {
		agent = agent[:maxUserAgentLength]
	}
	e.UserAgent = agent

	return e
}

// clientIP extracts the peer address. It reads RemoteAddr only: a forwarded
// header is client-supplied, and the proxy-aware resolution belongs in one
// place rather than being re-derived per call site.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// Store nothing rather than junk: the column is typed `inet`.
		return ""
	}
	if net.ParseIP(host) == nil {
		return ""
	}
	return host
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
