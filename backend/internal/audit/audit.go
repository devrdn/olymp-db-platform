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
	ActionAuthLogin       = "auth.login"
	ActionAuthLoginFailed = "auth.login_failed"
	ActionAuthLogout      = "auth.logout"
	ActionUserCreate      = "user.create"
	ActionUserUpdate      = "user.update"
	ActionUserBlock       = "user.block"
	ActionUserUnblock     = "user.unblock"
	// ActionUserDelete and ActionUserRestore exist because coming back to
	// active is two different events: an account returning from a block was
	// unblocked, one returning from deletion was restored, and the trail has
	// to say which.
	ActionUserDelete        = "user.delete"
	ActionUserRestore       = "user.restore"
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
	// The SQL an author uploads as the game, and the build made from
	// it. Two actions and not one: writing the script is a person
	// deciding something, and the build is what the cluster then did
	// with it — minutes later, possibly failing, with nobody at the
	// keyboard. A trail that folded them together could not answer
	// "was the game that ran the one the organiser wrote".
	ActionGameScriptSet = "contest.game_script_set"
	// ActionGameDefinitionSet records the table builder's own way of writing
	// a game: tables, columns and a primary key saved structurally instead
	// of as SQL (migration 26). Its own action code rather than reusing
	// ActionGameScriptSet, for the same reason ActionGameUploadComplete has
	// one apart from it — the payload the two carry does not overlap (a
	// table count here, a byte count there), so folding them together would
	// leave the trail unable to say which of the three ways built a game
	// without opening the payload.
	ActionGameDefinitionSet  = "contest.game_definition_set"
	ActionGameBuilt          = "contest.game_built"
	ActionContestStoryChange = "contest.story_change"
	ActionQuestionCreate     = "contest.question_create"
	ActionQuestionUpdate     = "contest.question_update"
	ActionQuestionDelete     = "contest.question_delete"
	ActionQuestionReorder    = "contest.question_reorder"
	ActionAnswersChange      = "contest.answers_change"
	ActionManagerGrant       = "contest.manager_grant"
	ActionManagerRevoke      = "contest.manager_revoke"
	// ActionContestPackageExport records a whole contest leaving the
	// installation as a file: its questions, its settings and — the reason
	// this needs a trail at all — its reference answers. Nothing is changed
	// by it, so unlike most of the actions above it is not recorded for the
	// sake of "who changed this"; it is recorded because a full answer key
	// walked out of the door and somebody may later need to know whose
	// account it left through. The payload carries counts only (§9.2).
	ActionContestPackageExport = "contest.package_export"

	ActionParticipantAdd        = "participant.add"
	ActionParticipantRemove     = "participant.remove"
	ActionParticipantDisqualify = "participant.disqualify"
	ActionParticipantEnroll     = "participant.enroll"
	// ActionContestAccessDenied records a participant turned away by a
	// contest's network restriction: the same entry that proves the rule works
	// is the signal that somebody tried from an outside device (§7.1).
	ActionContestAccessDenied = "contest.access_denied"
	// ActionContestStartBlocked records a contest whose starts_at arrived
	// while contests.Scheduler's tick held the lock, but which the same
	// publish gate Service.Transition enforces (CheckPublishable) refused —
	// a story removed, a question deleted, after publication (§8). The
	// contest is left published rather than opened with nothing in it, and
	// this is how an organizer finds out why, instead of from a student's
	// support ticket.
	ActionContestStartBlocked = "contest.start_blocked"
	// ActionGameInstanceReclaim records one participant's database dropped by
	// the background reclaim sweep (§2.4, §4.2): a contest finished, its
	// configured grace period passed, and the database is gone. System-
	// generated (nil actor), the same as ActionContestStartBlocked — nobody
	// asked for this one — and it exists so an organizer who cannot find a
	// database learns from the trail what removed it, and when, rather than
	// filing a support ticket about a missing instance.
	ActionGameInstanceReclaim = "contest.instance_reclaimed"
	// ActionGameInstanceDrop records one database removed because an
	// organizer asked for it — the copy that had gone wrong and had to be
	// remade. Its own action code beside ActionGameInstanceReclaim rather
	// than sharing it: that one is the timer, with no actor, meaning "the
	// grace period ran out"; this one names the person who decided a
	// participant's copy was broken, in the middle of an olympiad. Folding
	// the two together would leave the trail unable to answer which of them
	// took a database away, which is the first question anybody asks.
	ActionGameInstanceDrop = "contest.instance_dropped"
	// ActionGameTemplateReclaim records a contest's template database dropped
	// by the same sweep, once every instance copied from it is already gone
	// (§2.4). The template is the largest single database a contest owns;
	// its own action code rather than reusing ActionGameInstanceReclaim is
	// what lets an organizer searching the trail tell "one participant's
	// copy is gone" from "the whole game is gone" without reading payloads.
	ActionGameTemplateReclaim = "contest.template_reclaimed"
	// ActionGameUploadComplete records an uploaded SQL dump (migration 24)
	// becoming a contest's game: the file an organiser sent in, sealed and
	// checked against what they declared. Its own action code rather than
	// ActionGameScriptSet — that one names the script an organiser wrote in
	// the editor, and the payload the two carry does not overlap (a
	// filename and a line count here; a byte count there), so folding them
	// together would leave the trail unable to say which path produced a
	// game without opening the payload.
	ActionGameUploadComplete = "contest.upload_complete"
	// ActionGameUploadAbort records an upload cancelled before it became
	// anybody's game — by the organiser who started it, or by the
	// abandoned-upload janitor (nil actor, the same convention
	// ActionGameInstanceReclaim uses for the sweep that took a database
	// nobody asked it to).
	ActionGameUploadAbort = "contest.upload_abort"
	// ActionGameTableDataUpload records a CSV file completed for one table
	// of a table-builder game (migration 27) — the table-data counterpart of
	// ActionGameUploadComplete, its own action code for the same reason that
	// one has one apart from ActionGameScriptSet: the payload names a table
	// and a row count, not a whole game's own version.
	ActionGameTableDataUpload = "contest.table_data_upload"
	// ActionGameTableDataUploadAbort records a table's CSV upload cancelled
	// before it completed — ActionGameUploadAbort's own counterpart.
	ActionGameTableDataUploadAbort = "contest.table_data_upload_abort"
	// ActionGameTableDataRowAdd records one row an organiser typed into a
	// form, landing in the same file a chunked upload's own rows do.
	ActionGameTableDataRowAdd = "contest.table_data_row_add"
	// ActionGameTableDataRowDelete records one row tombstoned — never a
	// rewrite of the file itself, DeleteTableRow's own doc explains why.
	ActionGameTableDataRowDelete = "contest.table_data_row_delete"

	// ActionSettingsChange records a change to what the installation calls
	// itself and how it looks. It is entity "settings" with no identifier:
	// there is one of it.
	ActionSettingsChange = "settings.change"
)

// actions lists every action code declared above.
//
// This is the list audit_test.go's TestEveryActionIsListed reads audit.go's
// own source to check against: a constant added to the block above without
// being added here is exactly the omission that left `user.delete` reaching
// the trail with no wording in any language and no way for the filter to
// offer it.
var actions = []string{
	ActionAuthLogin, ActionAuthLoginFailed, ActionAuthLogout,
	ActionUserCreate, ActionUserUpdate, ActionUserBlock, ActionUserUnblock,
	ActionUserDelete, ActionUserRestore, ActionUserRolesChange,
	ActionUserPasswordReset, ActionPasswordChange,

	ActionContestCreate, ActionContestUpdate, ActionContestDelete,
	ActionContestStatusChange, ActionContestLanguages, ActionContestTranslations,
	ActionContestPolicyChange, ActionGameScriptSet, ActionGameDefinitionSet, ActionGameBuilt,
	ActionContestStoryChange, ActionQuestionCreate,
	ActionQuestionUpdate, ActionQuestionDelete, ActionQuestionReorder,
	ActionAnswersChange, ActionManagerGrant, ActionManagerRevoke,
	ActionContestPackageExport,

	ActionParticipantAdd, ActionParticipantRemove, ActionParticipantDisqualify,
	ActionParticipantEnroll, ActionContestAccessDenied, ActionContestStartBlocked,
	ActionGameInstanceReclaim, ActionGameTemplateReclaim, ActionGameInstanceDrop,
	ActionGameUploadComplete, ActionGameUploadAbort,
	ActionGameTableDataUpload, ActionGameTableDataUploadAbort,
	ActionGameTableDataRowAdd, ActionGameTableDataRowDelete,

	ActionSettingsChange,
}

// actionSet backs IsAction. Built once from actions rather than kept as a
// second hand-written list, so the two cannot say something different.
var actionSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(actions))
	for _, a := range actions {
		set[a] = struct{}{}
	}
	return set
}()

// Actions lists every action this installation can record.
//
// Enumerable so the layers above do not have to be trusted to keep up: the
// HTTP layer serves this to the filter so it can offer the whole vocabulary
// rather than whatever happens to be on the current page, and validates an
// ?action= filter against it rather than silently returning an empty page for
// a code that was never real. The interface translates each of these in
// every language it speaks, checked by a test of its own.
func Actions() []string {
	return append([]string(nil), actions...)
}

// IsAction reports whether code names a real action, for validating a filter
// before it reaches the query.
func IsAction(code string) bool {
	_, ok := actionSet[code]
	return ok
}

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
	// AppendMany stores entries in one statement. A bulk operation writes one
	// entry per account, and a round trip each would undo the reason the
	// operation is batched at all.
	AppendMany(ctx context.Context, entries []Entry) error
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
	prepared, err := prepareEntry(ctx, e)
	if err != nil {
		return err
	}
	if err := r.sink.Append(ctx, prepared); err != nil {
		return fmt.Errorf("append audit entry: %w", err)
	}
	return nil
}

// RecordMany writes entries that belong to one operation, such as the
// accounts a single bulk action touched. One bad entry fails the whole call,
// the same way one bad entry fails Record: a partial trail for one operation
// is worse than none.
func (r *Recorder) RecordMany(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}

	prepared := make([]Entry, len(entries))
	for i, e := range entries {
		p, err := prepareEntry(ctx, e)
		if err != nil {
			return err
		}
		prepared[i] = p
	}

	if err := r.sink.AppendMany(ctx, prepared); err != nil {
		return fmt.Errorf("append audit entries: %w", err)
	}
	return nil
}

// prepareEntry validates and sanitises one entry before it reaches a sink:
// the empty-action check, payload redaction, and inheriting the request's IP
// and user agent when the entry does not name its own. Record and RecordMany
// both call it, so the single and batch paths cannot drift apart.
func prepareEntry(ctx context.Context, e Entry) (Entry, error) {
	if e.Action == "" {
		return Entry{}, errors.New("audit entry has no action")
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
	return e, nil
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
