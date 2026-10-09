// Package contests owns what an organizer authors and runs: the contest, its
// story, questions and reference answers, its staff and its participants.
//
// It answers what may change and when (lifecycle, publish gate, enrollment,
// network restriction) and whether a participant may act now (Gate.StandingOf).
// It does not answer who may call an operation (internal/rbac) and holds no
// SQL: its storage interfaces are implemented in internal/postgres.
package contests

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Lifecycle statuses. A contest moves forward through them; the only step back
// is unpublishing something that has not started.
const (
	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusRunning   = "running"
	StatusFinished  = "finished"
	StatusArchived  = "archived"
)

// PublicStatuses are the statuses a visitor with no session may be shown:
// every status but the draft. It is the one list repositories filter on, so
// they cannot disagree about what a stranger sees.
var PublicStatuses = []string{StatusPublished, StatusRunning, StatusFinished, StatusArchived}

// Enrollment types decide who creates the registration, and nothing else:
// beyond that point both paths are identical.
const (
	// EnrollmentOpen lets a student sign themselves up.
	EnrollmentOpen = "open"
	// EnrollmentInviteOnly means only staff add participants.
	EnrollmentInviteOnly = "invite_only"
)

// Question modes, see docs/ARCHITECTURE.md §6.1.
const (
	// QuestionModeMulti asks several scored questions.
	QuestionModeMulti = "multi"
	// QuestionModeSingle asks one question carrying the whole contest.
	QuestionModeSingle = "single"
)

// Progression models, see docs/ARCHITECTURE.md §6.1.1. Meaningful only for
// QuestionModeMulti.
const (
	// ProgressionFree lets a participant answer open questions in any order.
	// The default.
	ProgressionFree = "free"
	// ProgressionSequential opens the next question once the previous one is
	// closed: answered correctly or out of attempts. Opening only on a correct
	// answer would trap a stuck participant for the rest of the contest.
	// Enforced by Service.Submit, not only the interface.
	ProgressionSequential = "sequential"
)

// Scoring models, see docs/ARCHITECTURE.md §6.1.1. They decide how a result is
// derived from submissions, not what is stored, so switching mode loses no
// data. Settings a mode ignores stay stored, unapplied, in case it changes back.
const (
	// ScoringPoints sums points_awarded. The default.
	ScoringPoints = "points"
	// ScoringWinner ranks only a winner: whoever first answered the final
	// question correctly. Service.Submit skips the per-attempt penalty here.
	ScoringWinner = "winner"
	// ScoringICPC ranks by questions solved, then by penalty time:
	// ICPCPenaltyMin per wrong attempt on a solved question. Service.Submit
	// writes points_awarded = 0 for every submission.
	ScoringICPC = "icpc"
)

// DefaultICPCPenaltyMin mirrors the migration's column default (000031).
const DefaultICPCPenaltyMin = 20

// maxICPCPenaltyMin bounds Contest.ICPCPenaltyMin (CLAUDE.md rule 2), matching
// the migration's CHECK (icpc_penalty_min BETWEEN 0 AND 240).
const maxICPCPenaltyMin = 240

// Leaderboard labels (docs/ARCHITECTURE.md §10). The table is public, so the
// login is the default and a full name is the organiser's choice.
const (
	LeaderboardNamesLogin    = "login"
	LeaderboardNamesFullName = "full_name"
)

// maxLeaderboardFreezeMin bounds leaderboard_freeze_min (CLAUDE.md rule 2) at
// a week.
const maxLeaderboardFreezeMin = 7 * 24 * 60

// Timing models, see docs/ARCHITECTURE.md §8.
const (
	// TimingFixed gives everybody the same window.
	TimingFixed = "fixed"
	// TimingIndividual gives each participant DurationMin from their own start.
	TimingIndividual = "individual"
)

// maxDurationMin bounds an individual contest's session at a week. Deadline
// multiplies it into int64 nanoseconds, which overflows past about 1.5e8
// minutes into a deadline in the past that locks everybody out.
const maxDurationMin = 7 * 24 * 60

// maxGracePeriodMin bounds settings.grace_period_min (CLAUDE.md rule 2) at 90
// days, well inside what provisioning.Service.Reclaim's make_interval handles.
const maxGracePeriodMin = 90 * 24 * 60

// Errors the domain reports; the HTTP layer maps each to a status code.
var (
	ErrNotFound          = errors.New("contest not found")
	ErrInvalidTransition = errors.New("contest cannot move to that status")
	// ErrStatusChanged is a move decided against a status that has since
	// changed: unlike ErrInvalidTransition, the caller should look again.
	ErrStatusChanged   = errors.New("contest status changed while the request was being decided")
	ErrInvalidContest  = errors.New("contest is not valid")
	ErrUnknownLanguage = errors.New("language is not available")
)

// Contest is one olympiad.
type Contest struct {
	ID           uuid.UUID
	Status       string
	Enrollment   string
	QuestionMode string
	Progression  string
	Scoring      string
	// ICPCPenaltyMin is the minutes one wrong attempt costs a solved question
	// under ScoringICPC. Kept and bounded in every mode.
	ICPCPenaltyMin int
	Timing         string
	// DurationMin is the per-participant session length, set only for
	// TimingIndividual.
	DurationMin *int
	StartsAt    *time.Time
	EndsAt      *time.Time
	// AllowedCIDRs restricts participation to these networks. Empty means no
	// restriction, and it never applies to staff (see AllowsAddress).
	AllowedCIDRs []netip.Prefix
	Settings     Settings
	// LeaderboardFreezeMin is how many minutes before EndsAt the table stops
	// changing for everybody but the staff. Nil is no freeze.
	LeaderboardFreezeMin *int
	// LeaderboardNames is LeaderboardNamesLogin or LeaderboardNamesFullName.
	LeaderboardNames string
	// LeaderboardRevealedAt is when an organiser revealed a frozen table's
	// final state. Set once, never cleared, and never through Update.
	LeaderboardRevealedAt *time.Time
	// Languages has exactly one default.
	Languages []ContestLanguage
	// Translations hold the title and description per language code.
	Translations map[string]Translation
	// CoverHash names the contest's picture; empty means the drawn cover
	// (docs/design/SPEC.md §10.3). Read-only here: covers.Service writes it.
	CoverHash string
	// CoverAttribution credits the picture's author. The publish gate
	// requires one with a CoverHash (§10.1); empty for the drawn cover.
	CoverAttribution string
	CreatedBy        uuid.UUID
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Settings are the tunables stored as jsonb on the contest row. Typed rather
// than a free-form map so an admin request cannot write unbounded or unknown
// keys.
type Settings struct {
	// EnrollmentDeadline closes self-signup early, so provisioning finishes
	// before the start (§4.2). Nil keeps signup open until the start.
	EnrollmentDeadline *time.Time `json:"enrollment_deadline,omitempty"`
	// QueryRateLimitPerMin caps a participant's SQL queries, enforced by
	// queryproxy.Service; zero means the installation default.
	QueryRateLimitPerMin int `json:"query_rate_limit_per_min,omitempty"`
	// GracePeriodMin keeps game databases alive after the finish (§2.4,
	// provisioning.Service.Reclaim). Zero means the installation default, not
	// no grace. Writable after the contest ends through Service.ExtendGrace.
	GracePeriodMin int `json:"grace_period_min,omitempty"`
}

// ContestLanguage is one language a contest is offered in.
type ContestLanguage struct {
	Code string
	// IsDefault marks the language served when the requested one is missing.
	IsDefault bool
}

// Translation is the contest's authored text in one language.
type Translation struct {
	Lang        string
	Title       string
	Description string
}

// allowedTransitions maps a status to the ones reachable from it. Published →
// draft is the only step back, so an organizer can undo a publish before the
// start without deleting the contest.
var allowedTransitions = map[string][]string{
	StatusDraft:     {StatusPublished},
	StatusPublished: {StatusDraft, StatusRunning, StatusArchived},
	StatusRunning:   {StatusFinished},
	StatusFinished:  {StatusArchived},
	StatusArchived:  {},
}

// CanTransitionTo reports whether the contest may move to status.
func (c Contest) CanTransitionTo(status string) error {
	if slices.Contains(allowedTransitions[c.Status], status) {
		return nil
	}
	return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, c.Status, status)
}

// ContentEditable reports whether the story, questions and answers may still
// change. The line is the start, not the publication: once running, an edit
// would change the task under people already answering it.
func (c Contest) ContentEditable() bool {
	return c.Status == StatusDraft || c.Status == StatusPublished
}

// FreezeAt is the moment the leaderboard freezes: LeaderboardFreezeMin before
// EndsAt. ok is false when the contest has no freeze or no end to measure it
// from.
func (c Contest) FreezeAt() (time.Time, bool) {
	if c.LeaderboardFreezeMin == nil || c.EndsAt == nil {
		return time.Time{}, false
	}
	return c.EndsAt.Add(-time.Duration(*c.LeaderboardFreezeMin) * time.Minute), true
}

// FreezeFitsWindow reports whether the freeze begins after the window opens.
// No freeze always fits; a freeze without a complete window never does.
func (c Contest) FreezeFitsWindow() bool {
	if c.LeaderboardFreezeMin == nil {
		return true
	}
	freezeAt, ok := c.FreezeAt()
	return ok && c.StartsAt != nil && c.StartsAt.Before(freezeAt)
}

// SettingsEditable reports whether the contest's own fields may still change.
// Wider than ContentEditable: a running contest may need its window extended
// or a network range corrected.
func (c Contest) SettingsEditable() bool {
	return !c.Ended()
}

// Ended reports a contest over for everybody: finished or archived. A single
// participant's time is Deadline's question.
func (c Contest) Ended() bool {
	return c.Status == StatusFinished || c.Status == StatusArchived
}

// SequentialActive reports whether sequential progression (§6.1.1) governs
// this contest; it means nothing in single-question mode. Every sequential
// check uses this method so the rule lives in one place.
func (c Contest) SequentialActive() bool {
	return c.Progression == ProgressionSequential && c.QuestionMode == QuestionModeMulti
}

// AllowsAddress reports whether a participant at addr may take part. An empty
// list means no restriction. Staff are never checked (§7.1), so a mistyped
// range cannot lock an administrator out.
func (c Contest) AllowsAddress(addr netip.Addr) bool {
	if len(c.AllowedCIDRs) == 0 {
		return true
	}
	if !addr.IsValid() {
		// Unknown address under a restriction fails closed, or a proxy
		// misconfiguration opens the contest.
		return false
	}

	// A v4 client behind a v6 listener arrives as ::ffff:10.20.30.40, which
	// matches no v4 prefix until it is unmapped.
	addr = addr.Unmap()
	for _, prefix := range c.AllowedCIDRs {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// DefaultLanguage returns the language served when the requested one is
// missing, or an empty string when the contest declares none.
func (c Contest) DefaultLanguage() string {
	for _, l := range c.Languages {
		if l.IsDefault {
			return l.Code
		}
	}
	return ""
}

// LanguageCodes returns the declared language codes, in declaration order.
func (c Contest) LanguageCodes() []string {
	codes := make([]string, 0, len(c.Languages))
	for _, l := range c.Languages {
		codes = append(codes, l.Code)
	}
	return codes
}

// Validate checks the fields a contest must always satisfy. It mirrors the
// table's CHECK constraints so the client is told which field is wrong instead
// of getting a 500.
func (c Contest) Validate() error {
	if !slices.Contains([]string{StatusDraft, StatusPublished, StatusRunning, StatusFinished, StatusArchived}, c.Status) {
		return fmt.Errorf("%w: unknown status %q", ErrInvalidContest, c.Status)
	}
	if !slices.Contains([]string{EnrollmentOpen, EnrollmentInviteOnly}, c.Enrollment) {
		return fmt.Errorf("%w: unknown enrollment type %q", ErrInvalidContest, c.Enrollment)
	}
	if !slices.Contains([]string{QuestionModeMulti, QuestionModeSingle}, c.QuestionMode) {
		return fmt.Errorf("%w: unknown question mode %q", ErrInvalidContest, c.QuestionMode)
	}
	if !slices.Contains([]string{ProgressionFree, ProgressionSequential}, c.Progression) {
		return fmt.Errorf("%w: unknown progression %q", ErrInvalidContest, c.Progression)
	}
	if !slices.Contains([]string{ScoringPoints, ScoringWinner, ScoringICPC}, c.Scoring) {
		return fmt.Errorf("%w: unknown scoring mode %q", ErrInvalidContest, c.Scoring)
	}
	if c.ICPCPenaltyMin < 0 || c.ICPCPenaltyMin > maxICPCPenaltyMin {
		return fmt.Errorf("%w: icpc_penalty_min of %d is outside 0..%d",
			ErrInvalidContest, c.ICPCPenaltyMin, maxICPCPenaltyMin)
	}

	switch c.Timing {
	case TimingFixed:
		if c.DurationMin != nil {
			return fmt.Errorf("%w: a fixed contest has no per-participant duration", ErrInvalidContest)
		}
	case TimingIndividual:
		if c.DurationMin == nil || *c.DurationMin <= 0 {
			return fmt.Errorf("%w: an individual contest needs a positive duration", ErrInvalidContest)
		}
		if *c.DurationMin > maxDurationMin {
			return fmt.Errorf("%w: duration_min of %d exceeds the %d-minute bound",
				ErrInvalidContest, *c.DurationMin, maxDurationMin)
		}
	default:
		return fmt.Errorf("%w: unknown timing %q", ErrInvalidContest, c.Timing)
	}

	if c.StartsAt != nil && c.EndsAt != nil && !c.StartsAt.Before(*c.EndsAt) {
		return fmt.Errorf("%w: the contest must end after it starts", ErrInvalidContest)
	}
	if c.Settings.QueryRateLimitPerMin < 0 || c.Settings.GracePeriodMin < 0 {
		return fmt.Errorf("%w: limits must not be negative", ErrInvalidContest)
	}
	if c.Settings.GracePeriodMin > maxGracePeriodMin {
		return fmt.Errorf("%w: grace_period_min of %d exceeds the %d-minute bound",
			ErrInvalidContest, c.Settings.GracePeriodMin, maxGracePeriodMin)
	}

	if !slices.Contains([]string{LeaderboardNamesLogin, LeaderboardNamesFullName}, c.LeaderboardNames) {
		return fmt.Errorf("%w: unknown leaderboard label %q", ErrInvalidContest, c.LeaderboardNames)
	}
	if freeze := c.LeaderboardFreezeMin; freeze != nil {
		if *freeze < 1 || *freeze > maxLeaderboardFreezeMin {
			return fmt.Errorf("%w: leaderboard_freeze_min of %d is outside 1..%d",
				ErrInvalidContest, *freeze, maxLeaderboardFreezeMin)
		}
		// Checked here when the window is known; the publish gate checks it
		// again, because the window can move after the freeze was saved.
		if !c.FreezeFitsWindow() && c.StartsAt != nil && c.EndsAt != nil {
			return fmt.Errorf("%w: a freeze of %d minutes is not shorter than the window",
				ErrInvalidContest, *freeze)
		}
	}

	return validateLanguages(c.Languages)
}

// validateLanguages requires unique codes and exactly one default. An empty set
// is legal until the publish gate.
func validateLanguages(langs []ContestLanguage) error {
	seen := make(map[string]struct{}, len(langs))
	defaults := 0

	for _, l := range langs {
		code := strings.TrimSpace(l.Code)
		if code == "" {
			return fmt.Errorf("%w: a language code must not be empty", ErrInvalidContest)
		}
		if _, duplicate := seen[code]; duplicate {
			return fmt.Errorf("%w: language %q listed twice", ErrInvalidContest, code)
		}
		seen[code] = struct{}{}
		if l.IsDefault {
			defaults++
		}
	}

	if len(langs) > 0 && defaults != 1 {
		return fmt.Errorf("%w: exactly one language must be the default, got %d", ErrInvalidContest, defaults)
	}
	return nil
}

// Filter selects a page of contests.
type Filter struct {
	// Query matches a substring of any translated title.
	Query  string
	Status string
	// ManagedBy limits the result to contests the user owns or manages.
	ManagedBy uuid.UUID
	// VisibleTo limits the result to contests the participant is registered
	// for, plus open ones still accepting signups.
	VisibleTo uuid.UUID
	// Enrolled splits the VisibleTo set: true for contests the person is on,
	// false for the rest, nil for both. It only narrows that set.
	Enrolled *bool
	Limit    int
	Offset   int
}

// Normalize clamps the page size so a client cannot ask for the whole table.
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

// Repository stores contests, their languages and their titles. Languages and
// translations are replaced as a whole because the set must be consistent.
type Repository interface {
	Create(ctx context.Context, c Contest) (Contest, error)
	// ByID returns a contest with its languages and translations, or
	// ErrNotFound.
	ByID(ctx context.Context, id uuid.UUID) (Contest, error)
	// List returns a page of contests and the total matching the filter.
	List(ctx context.Context, f Filter) ([]Contest, int, error)
	// Update saves the contest's own fields; status is not among them.
	Update(ctx context.Context, c Contest) error
	// SetStatus moves the contest from status from to status to, and reports
	// ErrStatusChanged if a concurrent request moved it since the caller read
	// it.
	SetStatus(ctx context.Context, id uuid.UUID, from, to string) error
	// Delete removes a contest and everything hanging off it.
	Delete(ctx context.Context, id uuid.UUID) error
	// ReplaceLanguages sets the contest's languages to exactly these.
	ReplaceLanguages(ctx context.Context, id uuid.UUID, langs []ContestLanguage) error
	// ReplaceTranslations sets the contest's titles to exactly these.
	ReplaceTranslations(ctx context.Context, id uuid.UUID, translations []Translation) error
	// LockContest takes an exclusive lock on the contest row until the
	// transaction ends. It serialises GrantManager against Enroll and
	// AddParticipants so staff and roster checks cannot interleave and make a
	// manager a participant. Must run inside a unit of work; refuses otherwise.
	LockContest(ctx context.Context, id uuid.UUID) error
}

// auditFields is the part of a contest the audit trail may hold. A new field
// is not recorded unless added here. Authored text is excluded: the trail
// records which titles changed, never the text (§9.2).
func (c Contest) auditFields() map[string]any {
	return map[string]any{
		"enrollment":               c.Enrollment,
		"question_mode":            c.QuestionMode,
		"progression":              c.Progression,
		"scoring":                  c.Scoring,
		"timing":                   c.Timing,
		"duration_min":             c.DurationMin,
		"starts_at":                c.StartsAt,
		"ends_at":                  c.EndsAt,
		"allowed_cidrs":            cidrStrings(c.AllowedCIDRs),
		"enrollment_deadline":      c.Settings.EnrollmentDeadline,
		"query_rate_limit_per_min": c.Settings.QueryRateLimitPerMin,
		"grace_period_min":         c.Settings.GracePeriodMin,
		"leaderboard_freeze_min":   c.LeaderboardFreezeMin,
		"leaderboard_names":        c.LeaderboardNames,
		"icpc_penalty_min":         c.ICPCPenaltyMin,
	}
}
