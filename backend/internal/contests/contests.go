// Package contests owns everything an organizer authors and runs: the contest
// itself, its story, its questions and their reference answers, the people who
// staff it, and the people who take part.
//
// It answers "what may be changed, by whom, and when" — the lifecycle rules,
// the publish gate, the enrollment rules and the network restriction. It does
// not answer "who is allowed to call this" (that is internal/rbac, applied by
// the HTTP layer) and it contains no SQL: the storage interfaces are declared
// here in the domain's own terms and implemented in internal/postgres.
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

// Timing models, see docs/ARCHITECTURE.md §8.
const (
	// TimingFixed gives everybody the same window.
	TimingFixed = "fixed"
	// TimingIndividual gives each participant DurationMin from their own start.
	TimingIndividual = "individual"
)

// Errors the domain reports. They are the vocabulary the HTTP layer maps to
// status codes, so each names a distinct situation a client can act on.
var (
	ErrNotFound          = errors.New("contest not found")
	ErrInvalidTransition = errors.New("contest cannot move to that status")
	// ErrStatusChanged reports a move decided against a status that has since
	// moved. Distinct from ErrInvalidTransition, which is about a step that is
	// never legal: this one says the caller was right a moment ago and should
	// look again, which is a different thing to tell a client.
	ErrStatusChanged   = errors.New("contest status changed while the request was being decided")
	ErrInvalidContest  = errors.New("contest is not valid")
	ErrUnknownLanguage = errors.New("language is not available")
)

// Contest is one olympiad.
type Contest struct {
	ID     uuid.UUID
	Status string
	// Enrollment decides who may create a registration (see EnrollmentOpen).
	Enrollment string
	// QuestionMode decides whether the contest asks one question or several.
	QuestionMode string
	Timing       string
	// DurationMin is the per-participant session length, set only for
	// TimingIndividual.
	DurationMin *int
	StartsAt    *time.Time
	EndsAt      *time.Time
	// AllowedCIDRs restricts participation to these networks. Empty means no
	// restriction, and it never applies to staff (see AllowsAddress).
	AllowedCIDRs []netip.Prefix
	Settings     Settings
	// Languages are the languages this contest is offered in, exactly one of
	// which is the default.
	Languages []ContestLanguage
	// Translations hold the title and description per language code.
	Translations map[string]Translation
	CreatedBy    uuid.UUID
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Settings are the tunables stored as jsonb on the contest row.
//
// A typed struct rather than a free-form map: the column is written straight
// from an admin request, and an untyped blob would be both an unbounded write
// and a set of keys nobody can enumerate afterwards.
type Settings struct {
	// EnrollmentDeadline closes self-signup early, so a provisioning queue is
	// not still working when the contest starts (see §4.2). Nil keeps signup
	// open until the contest starts.
	EnrollmentDeadline *time.Time `json:"enrollment_deadline,omitempty"`
	// QueryRateLimitPerMin caps a participant's SQL queries; zero means the
	// installation default. The game loop reads it.
	QueryRateLimitPerMin int `json:"query_rate_limit_per_min,omitempty"`
	// GracePeriodMin keeps game databases alive after the finish, so somebody
	// who lost their connection at the buzzer is not wiped out immediately.
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

// allowedTransitions maps a status to the ones reachable from it.
//
// Published → draft is the only step back, and it exists because publishing is
// how an organizer finds out the gate passes: undoing that before anybody has
// started must not require deleting the contest.
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
// change.
//
// The line is the start, not the publication: an organizer publishes to see
// the contest as participants will, and may still fix a typo. Once it is
// running, changing a question would change the task under people already
// answering it.
func (c Contest) ContentEditable() bool {
	return c.Status == StatusDraft || c.Status == StatusPublished
}

// SettingsEditable reports whether the contest's own fields may still change.
//
// Wider than ContentEditable on purpose: extending the window after a power
// cut, or correcting a network range that turned out to be wrong, are exactly
// the operations a running contest needs.
func (c Contest) SettingsEditable() bool {
	return c.Status != StatusFinished && c.Status != StatusArchived
}

// AllowsAddress reports whether a participant at addr may take part.
//
// An empty list means no restriction. Staff are never checked against it —
// see §7.1: an administrator who mistypes a range must not be able to lock
// themselves out of the contest they are configuring.
func (c Contest) AllowsAddress(addr netip.Addr) bool {
	if len(c.AllowedCIDRs) == 0 {
		return true
	}
	if !addr.IsValid() {
		// The resolver could not name the caller. With a restriction in force,
		// "unknown" has to mean "no": failing open here would turn every proxy
		// misconfiguration into an open door.
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

// Speaks reports whether the contest declares the language.
func (c Contest) Speaks(code string) bool {
	return slices.Contains(c.LanguageCodes(), code)
}

// Validate checks the fields a contest must always satisfy.
//
// It mirrors the table's CHECK constraints rather than trusting them: the
// database is the guarantee, but a violated constraint reaches the client as
// an opaque 500, and an organizer filling in a form deserves to be told which
// field is wrong.
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

	switch c.Timing {
	case TimingFixed:
		if c.DurationMin != nil {
			return fmt.Errorf("%w: a fixed contest has no per-participant duration", ErrInvalidContest)
		}
	case TimingIndividual:
		if c.DurationMin == nil || *c.DurationMin <= 0 {
			return fmt.Errorf("%w: an individual contest needs a positive duration", ErrInvalidContest)
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

	return validateLanguages(c.Languages)
}

// validateLanguages enforces the one rule the set has to satisfy: exactly one
// default, so the fallback language is never ambiguous. An empty set is legal —
// a contest is created before its languages are chosen, and it is the publish
// gate that insists on them.
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
	// ManagedBy limits the result to contests the user owns or manages. It is
	// how an organizer's list stays their own without the repository needing
	// to know anything about permissions.
	ManagedBy uuid.UUID
	// VisibleTo limits the result to what a participant may see: contests they
	// are registered for, plus open ones still accepting signups.
	VisibleTo uuid.UUID
	Limit     int
	Offset    int
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

// Repository stores contests, the languages they are offered in and their
// authored titles.
//
// Translations and languages are replaced as a whole rather than patched one
// key at a time: it is the set that has to be consistent (exactly one default,
// a title for every declared language), and a half-applied set is precisely
// what the publish gate would then have to guess about.
type Repository interface {
	Create(ctx context.Context, c Contest) (Contest, error)
	// ByID returns a contest with its languages and translations, or
	// ErrNotFound.
	ByID(ctx context.Context, id uuid.UUID) (Contest, error)
	// List returns a page of contests and the total matching the filter.
	List(ctx context.Context, f Filter) ([]Contest, int, error)
	// Update saves the contest's own fields; status is not among them.
	Update(ctx context.Context, c Contest) error
	// SetStatus moves the contest along its lifecycle, but only from the
	// status the caller decided against.
	//
	// The expectation is a parameter rather than a convention because the
	// decision and the write are two statements: the gate that permits a move
	// runs against a contest read moments earlier, and between the two a
	// concurrent request may have moved it. Reporting ErrStatusChanged makes
	// the second writer look again instead of overwriting a state it never
	// examined.
	SetStatus(ctx context.Context, id uuid.UUID, from, to string) error
	// Delete removes a contest and everything hanging off it.
	Delete(ctx context.Context, id uuid.UUID) error
	// ReplaceLanguages sets the contest's languages to exactly these.
	ReplaceLanguages(ctx context.Context, id uuid.UUID, langs []ContestLanguage) error
	// ReplaceTranslations sets the contest's titles to exactly these.
	ReplaceTranslations(ctx context.Context, id uuid.UUID, translations []Translation) error
}

// auditFields is the part of a contest that may be written to the audit trail.
//
// One list, next to the type, rather than repeated wherever a change is
// recorded: it reads as a decision about what the trail may hold, and a field
// added to the contest is either added here deliberately or not recorded at
// all. Authored text is absent on purpose — the trail records that titles
// changed and in which languages, never the titles (§9.2).
func (c Contest) auditFields() map[string]any {
	return map[string]any{
		"enrollment":               c.Enrollment,
		"question_mode":            c.QuestionMode,
		"timing":                   c.Timing,
		"duration_min":             c.DurationMin,
		"starts_at":                c.StartsAt,
		"ends_at":                  c.EndsAt,
		"allowed_cidrs":            cidrStrings(c.AllowedCIDRs),
		"enrollment_deadline":      c.Settings.EnrollmentDeadline,
		"query_rate_limit_per_min": c.Settings.QueryRateLimitPerMin,
		"grace_period_min":         c.Settings.GracePeriodMin,
	}
}
