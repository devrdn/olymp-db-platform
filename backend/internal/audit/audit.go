// Package audit records who did what, from where and when. Entries are
// written in the same transaction as the action they describe, so either both
// land or neither does. It takes a plain context, never an HTTP request.
package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Action codes.
const (
	ActionAuthLogin       = "auth.login"
	ActionAuthLoginFailed = "auth.login_failed"
	ActionAuthLogout      = "auth.logout"
	ActionUserCreate      = "user.create"
	ActionUserUpdate      = "user.update"
	ActionUserBlock       = "user.block"
	ActionUserUnblock     = "user.unblock"
	// ActionUserDelete and ActionUserRestore are distinct from block and
	// unblock: the trail must say which an account came back from.
	ActionUserDelete        = "user.delete"
	ActionUserRestore       = "user.restore"
	ActionUserRolesChange   = "user.roles_change"
	ActionUserPasswordReset = "user.password_reset"
	ActionPasswordChange    = "user.password_change"
	// ActionUserSignInUnlock records staff clearing an account's sign-in
	// throttling, so the trail explains why a guessing limit did not hold.
	ActionUserSignInUnlock = "user.sign_in_unlock"

	ActionContestCreate       = "contest.create"
	ActionContestUpdate       = "contest.update"
	ActionContestDelete       = "contest.delete"
	ActionContestStatusChange = "contest.status_change"
	ActionContestLanguages    = "contest.languages_change"
	ActionContestTranslations = "contest.translations_change"
	ActionContestPolicyChange = "contest.policy_change"
	// The game script and the build made from it are separate actions: one
	// is a person's decision, the other what the cluster did later, possibly
	// failing.
	ActionGameScriptSet = "contest.game_script_set"
	// ActionGameDefinitionSet records a game written in the table builder
	// rather than as SQL. Each way of building a game has its own code, so
	// the trail tells them apart without opening payloads.
	ActionGameDefinitionSet = "contest.game_definition_set"
	ActionGameBuilt         = "contest.game_built"
	// ActionGameBuildRequested records who asked for a rebuild;
	// ActionGameBuilt records how the build ended.
	ActionGameBuildRequested = "contest.game_build_requested"
	ActionContestStoryChange = "contest.story_change"
	ActionQuestionCreate     = "contest.question_create"
	ActionQuestionUpdate     = "contest.question_update"
	ActionQuestionDelete     = "contest.question_delete"
	ActionQuestionReorder    = "contest.question_reorder"
	ActionAnswersChange      = "contest.answers_change"
	ActionManagerGrant       = "contest.manager_grant"
	ActionManagerRevoke      = "contest.manager_revoke"
	// ActionContestPackageExport records a whole contest, reference answers
	// included, leaving the installation as a file. The payload carries
	// counts only.
	ActionContestPackageExport = "contest.package_export"
	// ActionContestLeaderboardReveal records an organiser revealing a frozen
	// table's final state, which is irreversible.
	ActionContestLeaderboardReveal = "contest.leaderboard_reveal"
	// ActionContestMonitorView records staff viewing what participants did.
	// It is written at most once per 15 minutes per viewer and participant
	// (or contest), or a polling screen would bury the trail.
	ActionContestMonitorView = "contest.monitor_view"
	// ActionContestMonitorExport is recorded on every export, unthrottled.
	ActionContestMonitorExport = "contest.monitor_export"

	ActionParticipantAdd        = "participant.add"
	ActionParticipantRemove     = "participant.remove"
	ActionParticipantDisqualify = "participant.disqualify"
	ActionParticipantEnroll     = "participant.enroll"
	// ActionContestAccessDenied records a participant turned away by a
	// contest's network restriction.
	ActionContestAccessDenied = "contest.access_denied"
	// ActionContestStartBlocked records a scheduled start the publish gate
	// refused (something removed after publication). The contest stays
	// published, and this tells the organizer why.
	ActionContestStartBlocked = "contest.start_blocked"
	// ActionGameInstanceReclaim records one participant's database dropped by
	// the reclaim sweep after the grace period (nil actor).
	ActionGameInstanceReclaim = "contest.instance_reclaimed"
	// ActionGameInstanceDrop records one database an organizer had removed
	// to remake it, distinct from the sweep's reclaim.
	ActionGameInstanceDrop = "contest.instance_dropped"
	// ActionGameTemplateReclaim records a contest's template database dropped
	// by the sweep once every instance copied from it is gone.
	ActionGameTemplateReclaim = "contest.template_reclaimed"
	// ActionGameUploadComplete records an uploaded SQL dump becoming a
	// contest's game.
	ActionGameUploadComplete = "contest.upload_complete"
	// ActionGameUploadAbort records an upload cancelled by its organiser or
	// by the abandoned-upload janitor (nil actor).
	ActionGameUploadAbort = "contest.upload_abort"
	// ActionGameTableDataUpload records a CSV file completed for one table of
	// a table-builder game.
	ActionGameTableDataUpload      = "contest.table_data_upload"
	ActionGameTableDataUploadAbort = "contest.table_data_upload_abort"
	// ActionGameTableDataRowAdd records one row an organiser typed into a
	// form.
	ActionGameTableDataRowAdd = "contest.table_data_row_add"
	// ActionGameTableDataRowDelete records one row tombstoned.
	ActionGameTableDataRowDelete = "contest.table_data_row_delete"

	// ActionSettingsChange is entity "settings" with no identifier: there is
	// one of it.
	ActionSettingsChange = "settings.change"
)

// actions lists every action code declared above; TestEveryActionIsListed
// checks it against the source.
var actions = []string{
	ActionAuthLogin, ActionAuthLoginFailed, ActionAuthLogout,
	ActionUserCreate, ActionUserUpdate, ActionUserBlock, ActionUserUnblock,
	ActionUserDelete, ActionUserRestore, ActionUserRolesChange,
	ActionUserPasswordReset, ActionPasswordChange, ActionUserSignInUnlock,

	ActionContestCreate, ActionContestUpdate, ActionContestDelete,
	ActionContestStatusChange, ActionContestLanguages, ActionContestTranslations,
	ActionContestPolicyChange, ActionGameScriptSet, ActionGameDefinitionSet, ActionGameBuilt,
	ActionGameBuildRequested,
	ActionContestStoryChange, ActionQuestionCreate,
	ActionQuestionUpdate, ActionQuestionDelete, ActionQuestionReorder,
	ActionAnswersChange, ActionManagerGrant, ActionManagerRevoke,
	ActionContestPackageExport, ActionContestLeaderboardReveal,
	ActionContestMonitorView, ActionContestMonitorExport,

	ActionParticipantAdd, ActionParticipantRemove, ActionParticipantDisqualify,
	ActionParticipantEnroll, ActionContestAccessDenied, ActionContestStartBlocked,
	ActionGameInstanceReclaim, ActionGameTemplateReclaim, ActionGameInstanceDrop,
	ActionGameUploadComplete, ActionGameUploadAbort,
	ActionGameTableDataUpload, ActionGameTableDataUploadAbort,
	ActionGameTableDataRowAdd, ActionGameTableDataRowDelete,

	ActionSettingsChange,
}

var actionSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(actions))
	for _, a := range actions {
		set[a] = struct{}{}
	}
	return set
}()

// Actions lists every action this installation can record, for the trail's
// filter and its validation.
func Actions() []string {
	return append([]string(nil), actions...)
}

// IsAction reports whether code names a real action.
func IsAction(code string) bool {
	_, ok := actionSet[code]
	return ok
}

// MaxUserAgentLength bounds a header the client controls (CLAUDE.md rule 2).
const MaxUserAgentLength = 512

// storableUserAgent drops invalid UTF-8 and NUL characters and cuts to
// MaxUserAgentLength bytes on a character boundary. PostgreSQL would refuse
// either, failing the audited write.
func storableUserAgent(userAgent string) string {
	userAgent = strings.ReplaceAll(strings.ToValidUTF8(userAgent, ""), "\x00", "")
	if len(userAgent) <= MaxUserAgentLength {
		return userAgent
	}
	cut := MaxUserAgentLength
	for cut > 0 && !utf8.RuneStart(userAgent[cut]) {
		cut--
	}
	return userAgent[:cut]
}

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

type Entry struct {
	// ActorID is nil for system events.
	ActorID   *uuid.UUID
	Action    string
	Entity    string
	EntityID  string
	Payload   map[string]any
	IP        string
	UserAgent string
}

// Sink stores entries.
type Sink interface {
	Append(ctx context.Context, e Entry) error
	// AppendMany stores entries in one statement.
	AppendMany(ctx context.Context, entries []Entry) error
}

type Recorder struct {
	sink Sink
}

func New(sink Sink) *Recorder {
	return &Recorder{sink: sink}
}

// Record writes one entry. Inside a unit of work the sink writes through the
// ambient transaction, tying the record to the action.
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

// RecordMany writes entries that belong to one operation. One bad entry
// fails the whole call: a partial trail is worse than none.
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

// prepareEntry validates and sanitises one entry and fills in the request's
// origin when the entry does not name its own.
func prepareEntry(ctx context.Context, e Entry) (Entry, error) {
	if e.Action == "" {
		return Entry{}, errors.New("audit entry has no action")
	}

	e.Payload = redact(e.Payload)

	// An explicit origin (the login flow resolves its own) wins.
	if meta, ok := ctx.Value(metaKey{}).(requestMeta); ok {
		if e.IP == "" {
			e.IP = meta.ip
		}
		if e.UserAgent == "" {
			e.UserAgent = meta.userAgent
		}
	}
	e.UserAgent = storableUserAgent(e.UserAgent)
	return e, nil
}

type metaKey struct{}

type requestMeta struct {
	ip        string
	userAgent string
}

// WithRequestMeta returns a context carrying the request origin, which Record
// uses when an entry does not set its own.
func WithRequestMeta(ctx context.Context, ip, userAgent string) context.Context {
	return context.WithValue(ctx, metaKey{}, requestMeta{ip: ip, userAgent: storableUserAgent(userAgent)})
}

// redact returns a copy of the payload with sensitive values removed, at any
// depth. Handlers pass request data straight through.
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

// Record is one entry of the trail as it is read back, with the actor's login
// rather than only an identifier. A system event leaves both empty.
type Record struct {
	ID      int64
	ActorID *uuid.UUID
	// ActorLogin is empty for a system event and for a deleted account.
	ActorLogin string
	Action     string
	Entity     string
	EntityID   string
	// EntityLabel names the entity (a title, a login) while it exists, and
	// is empty once it is gone.
	EntityLabel string
	Payload     map[string]any
	IP          string
	UserAgent   string
	CreatedAt   time.Time
}

// Filter selects a page of the trail. Every non-empty field narrows; Actor and
// Entity with EntityID are each backed by an index (CLAUDE.md rule 7).
type Filter struct {
	Actor    uuid.UUID
	Action   string
	Entity   string
	EntityID string
	// From is inclusive, To exclusive.
	From   *time.Time
	To     *time.Time
	Limit  int
	Offset int
}

// Normalize clamps the page size: the trail is the largest table in the core
// database.
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

// Reader reads the trail back. It is separate from Sink, which writes inside
// every action's transaction.
type Reader interface {
	// List returns a page, newest first, and the total matching the filter.
	List(ctx context.Context, f Filter) ([]Record, int, error)
}
