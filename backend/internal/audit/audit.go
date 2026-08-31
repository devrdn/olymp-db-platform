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
	"time"

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

	ActionContestCreate       = "contest.create"
	ActionContestUpdate       = "contest.update"
	ActionContestDelete       = "contest.delete"
	ActionContestStatusChange = "contest.status_change"
	ActionContestLanguages    = "contest.languages_change"
	ActionContestTranslations = "contest.translations_change"
	ActionContestPolicyChange = "contest.policy_change"
	ActionContestStoryChange  = "contest.story_change"
	ActionQuestionCreate      = "contest.question_create"
	ActionQuestionUpdate      = "contest.question_update"
	ActionQuestionDelete      = "contest.question_delete"
	ActionQuestionReorder     = "contest.question_reorder"
	ActionAnswersChange       = "contest.answers_change"
	ActionManagerGrant        = "contest.manager_grant"
	ActionManagerRevoke       = "contest.manager_revoke"

	ActionParticipantAdd        = "participant.add"
	ActionParticipantRemove     = "participant.remove"
	ActionParticipantDisqualify = "participant.disqualify"
	ActionParticipantEnroll     = "participant.enroll"
	// ActionContestAccessDenied records a participant turned away by a
	// contest's network restriction: the same entry that proves the rule works
	// is the signal that somebody tried from an outside device (§7.1).
	ActionContestAccessDenied = "contest.access_denied"
)

// MaxUserAgentLength bounds a header the client controls. The column is kept
// for a year, so an unbounded value is storage someone else gets to spend.
const MaxUserAgentLength = 512

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
	if len(userAgent) > MaxUserAgentLength {
		userAgent = userAgent[:MaxUserAgentLength]
	}
	return context.WithValue(ctx, metaKey{}, requestMeta{ip: ip, userAgent: userAgent})
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

// Record is one entry of the trail as it is read back.
//
// Wider than Entry, which is what a caller writes: reading answers "who, what,
// when, from where", and the actor is a login rather than an identifier
// because a page of UUIDs answers nothing. A system event has no actor at all,
// and says so by leaving both empty.
type Record struct {
	ID      int64
	ActorID *uuid.UUID
	// ActorLogin is empty for a system event, and for an account that has since
	// been deleted — the trail outlives the people in it, which is the point.
	ActorLogin string
	Action     string
	Entity     string
	// EntityID identifies the thing acted upon, and outlives it.
	EntityID string
	// EntityLabel names that thing — a contest's title, an account's login —
	// when it still exists. Empty when it does not: the trail outlives what it
	// describes, and inventing a name for something that is gone would be
	// inventing a record. The identifier is always there for whoever needs it.
	EntityLabel string
	Payload     map[string]any
	IP          string
	UserAgent   string
	CreatedAt   time.Time
}

// Filter selects a page of the trail.
//
// Every field narrows; an empty one does not. The two that matter in practice
// are Actor ("what did this person do") and Entity with EntityID ("what
// happened to this contest"), which is why the table carries an index for each.
type Filter struct {
	Actor    uuid.UUID
	Action   string
	Entity   string
	EntityID string
	// From and To bound created_at, inclusive of From and exclusive of To.
	From  *time.Time
	To    *time.Time
	Limit int
	// Offset pages backwards through history, newest first.
	Offset int
}

// Normalize clamps the page size.
//
// The trail is the largest table in the core database and is kept for a year;
// an unbounded request would ask the server to hold a year of it in memory.
func (f Filter) Normalize() Filter {
	const (
		defaultLimit = 50
		maxLimit     = 200
	)
	if f.Limit <= 0 {
		f.Limit = defaultLimit
	}
	if f.Limit > maxLimit {
		f.Limit = maxLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return f
}

// Reader reads the trail back.
//
// Separate from Sink because the two have nothing in common but a table: one
// is written inside every action's transaction and must never fail silently,
// the other is a paged query behind a permission.
type Reader interface {
	// List returns a page of the trail, newest first, and the total number of
	// entries matching the filter.
	List(ctx context.Context, f Filter) ([]Record, int, error)
}
