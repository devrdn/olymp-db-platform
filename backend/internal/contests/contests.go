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

// PublicStatuses are the statuses a visitor with no session may be shown: a
// contest that has been published and everything after it.
//
// Every status there is, less the draft. Written that way round on purpose —
// a new status added to the lifecycle above lands in this list by default and
// has to be taken out deliberately, which is the safe direction for a filter
// that decides what a stranger sees. The other direction has been spelled out
// three times in three repositories, and nothing made the three agree; a
// status added to two of them and forgotten in the third is a contest
// leaking.
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

// Progression models, see docs/ARCHITECTURE.md §6.1.1. Only meaningful when
// QuestionMode is QuestionModeMulti — a single-question contest has no
// "next" question for either one to say anything about.
const (
	// ProgressionFree lets a participant answer any still-open question in
	// any order — today's behaviour, and the default.
	ProgressionFree = "free"
	// ProgressionSequential opens the next question only once the previous
	// one is closed: answered correctly, or every attempt spent. The second
	// condition is the one that matters — opening only on a correct answer
	// would trap a participant who is stuck for the rest of the contest,
	// clock still running. Enforced by Service.Submit, never by the
	// interface alone.
	ProgressionSequential = "sequential"
)

// Scoring models, see docs/ARCHITECTURE.md §6.1.1. Decides how a result is
// derived from submissions, never what is written to them: every submission
// is graded and scored identically in both modes, so switching between them
// mid-contest cannot destroy data.
const (
	// ScoringPoints sums points_awarded across a registration's submissions —
	// today's behaviour, and the default.
	ScoringPoints = "points"
	// ScoringWinner has only a winner: whoever first answered the contest's
	// final question correctly. The per-attempt penalty (questions.penalty_pct)
	// is defined in points and stops meaning anything once points stop being
	// the result, so Service.Submit skips it in this mode — not because the
	// configured percentage is forbidden here, but because a contest's
	// scoring mode may change and the setting must survive that.
	ScoringWinner = "winner"
	// ScoringICPC ranks a registration by how many questions it solved and,
	// to break ties, by how much penalty time solving them cost — the way
	// ICPC itself scores (docs/ARCHITECTURE.md §6.1.1). A question's own points and
	// percentage penalty stay in the data (the mode can be switched back
	// before the contest starts) but mean nothing while this mode is in
	// force: Service.Submit writes points_awarded = 0 for every submission,
	// the same way it skips the penalty in ScoringWinner, and place is
	// decided instead by ICPCPenaltyMin applied per wrong attempt on a
	// question that is eventually solved.
	ScoringICPC = "icpc"
)

// DefaultICPCPenaltyMin is how many minutes one wrong attempt costs a solved
// question in ICPC scoring, when nothing else was chosen — the migration's
// own column default (000031), mirrored here so Service.Create and the test
// stores can apply the identical default without hard-coding 20 twice.
const DefaultICPCPenaltyMin = 20

// maxICPCPenaltyMin bounds Contest.ICPCPenaltyMin (CLAUDE.md rule 2), matching
// the migration's own CHECK (icpc_penalty_min BETWEEN 0 AND 240). 240 minutes
// is already four hours of penalty for a single wrong attempt — far beyond
// anything a real contest window would make survivable to climb back from —
// and staying at a round, generous ceiling rather than an arbitrary one keeps
// the domain and the schema trivially readable as the same rule.
const maxICPCPenaltyMin = 240

// Leaderboard labels: how a participant is named on a table somebody other
// than the contest's staff reads (docs/ARCHITECTURE.md §10). The table is public, which is why the
// login is the default and a full name is something an organiser chooses.
const (
	LeaderboardNamesLogin    = "login"
	LeaderboardNamesFullName = "full_name"
)

// maxLeaderboardFreezeMin bounds leaderboard_freeze_min (CLAUDE.md rule 2): a
// week, the same ceiling a session length has, and far beyond any window a
// freeze could sensibly be measured back across.
const maxLeaderboardFreezeMin = 7 * 24 * 60

// Timing models, see docs/ARCHITECTURE.md §8.
const (
	// TimingFixed gives everybody the same window.
	TimingFixed = "fixed"
	// TimingIndividual gives each participant DurationMin from their own start.
	TimingIndividual = "individual"
)

// maxDurationMin bounds an individual contest's per-participant session.
//
// Deadline (deadline.go) computes time.Duration(*DurationMin) * time.Minute,
// which is arithmetic in int64 nanoseconds: past roughly 1.5e8 minutes it
// overflows and wraps to a deadline in the past, silently locking out every
// participant of the contest that triggered it. A week — 7*24*60 minutes —
// is already far longer than any real-time olympiad sitting, in person or
// online, and staying orders of magnitude below the overflow point rather
// than merely under it is what makes this a bound and not a near miss.
const maxDurationMin = 7 * 24 * 60

// maxGracePeriodMin bounds settings.grace_period_min — CLAUDE.md rule 2: a
// field that reaches storage needs an explicit bound, and this one governs
// how long a finished contest's game databases outlive it (§2.4), read by
// provisioning.Service.Reclaim through make_interval. 90 days is far beyond
// any dispute or report an organizer would extend it for, and staying well
// under the point make_interval's own arithmetic could misbehave at is the
// same reasoning maxDurationMin above already applies to a duration in
// minutes.
const maxGracePeriodMin = 90 * 24 * 60

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
	// Progression decides the order questions may be answered in (see
	// ProgressionFree, ProgressionSequential).
	Progression string
	// Scoring decides how a result is derived from submissions (see
	// ScoringPoints, ScoringWinner, ScoringICPC).
	Scoring string
	// ICPCPenaltyMin is how many minutes one wrong attempt costs a solved
	// question when Scoring is ScoringICPC (see ScoringICPC's own doc).
	// Meaningless in every other mode but always present and always bounded,
	// so a contest that switches back to icpc later has a value ready rather
	// than a fresh default nobody chose.
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
	// Languages are the languages this contest is offered in, exactly one of
	// which is the default.
	Languages []ContestLanguage
	// Translations hold the title and description per language code.
	Translations map[string]Translation
	// CoverHash names the picture this contest wears, and is empty for one
	// wearing the drawn cover instead (design spec §10.3). Read-only: the
	// cover is written through covers.Service, which owns both the row and
	// the file, and a Contest carries it only so a listing can answer with
	// it — the play screen puts a picture above the story and may not spend
	// a second request on one hash.
	CoverHash string
	// CoverAttribution credits whoever made that picture. §10.1 makes the
	// line part of the publish gate, so a non-empty CoverHash always arrives
	// with one; empty means there is nobody to credit, because the cover is
	// drawn and its author is us.
	CoverAttribution string
	CreatedBy        uuid.UUID
	CreatedAt        time.Time
	UpdatedAt        time.Time
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
	// installation default. Read by queryproxy.Service, which enforces it
	// ahead of the query journal — not by the Query Runner, which has no
	// notion of one contest's settings and enforces only the installation's
	// own QUERY_PER_MINUTE.
	QueryRateLimitPerMin int `json:"query_rate_limit_per_min,omitempty"`
	// GracePeriodMin keeps game databases alive after the finish, so somebody
	// who lost their connection at the buzzer is not wiped out immediately.
	// Read by provisioning.Service.Reclaim (§2.4); zero defers to the
	// installation's own default rather than meaning "no grace at all" — a
	// grace of zero would reclaim a just-finished contest's databases on the
	// very next tick. The one field of a finished (or archived) contest that
	// stays writable past SettingsEditable's own line — see Service.
	// ExtendGrace, and its own doc for why.
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

// FreezeAt is the moment the leaderboard freezes: LeaderboardFreezeMin before
// EndsAt. ok is false when the contest has no freeze or no end to measure it
// from.
func (c Contest) FreezeAt() (time.Time, bool) {
	if c.LeaderboardFreezeMin == nil || c.EndsAt == nil {
		return time.Time{}, false
	}
	return c.EndsAt.Add(-time.Duration(*c.LeaderboardFreezeMin) * time.Minute), true
}

// FreezeFitsWindow reports whether the freeze begins after the window opens,
// so the table is not frozen before anybody could have answered. A contest
// with no freeze always fits; one with a freeze but no complete window does
// not, because there is nothing to measure it against.
func (c Contest) FreezeFitsWindow() bool {
	if c.LeaderboardFreezeMin == nil {
		return true
	}
	freezeAt, ok := c.FreezeAt()
	return ok && c.StartsAt != nil && c.StartsAt.Before(freezeAt)
}

// SettingsEditable reports whether the contest's own fields may still change.
//
// Wider than ContentEditable on purpose: extending the window after a power
// cut, or correcting a network range that turned out to be wrong, are exactly
// the operations a running contest needs.
func (c Contest) SettingsEditable() bool {
	return !c.Ended()
}

// Ended reports a contest that is over for everybody: finished, or archived
// after it finished. Each participant may have finished earlier, by their
// own deadline — that is Deadline's question, not this one.
func (c Contest) Ended() bool {
	return c.Status == StatusFinished || c.Status == StatusArchived
}

// SequentialActive reports whether sequential progression (§6.1.1) actually
// governs answering this contest.
//
// Progression alone is not enough to ask: ProgressionSequential is
// meaningless at QuestionModeSingle, where the one question has nothing
// before it to wait on. Submit (submission.go) and Reader.Questions
// (participant_view.go) both key their own sequential gating off this one
// method rather than each repeating the two-field comparison — they agreed
// with each other only because the publish gate happens to force a
// single-mode contest down to exactly one question, and a rule that two
// places restate is a rule that can drift the moment either one is edited
// without the other.
func (c Contest) SequentialActive() bool {
	return c.Progression == ProgressionSequential && c.QuestionMode == QuestionModeMulti
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
	// Enrolled narrows that set to one half or the other: true for the
	// contests the person is on, false for the rest of what is offered to
	// them. Nil leaves the whole visible set, which is what a catalogue wants.
	//
	// It narrows and never widens — the visibility rule above still decides
	// what may be seen at all, so this cannot become a way to ask about
	// somebody else's registrations.
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
	// LockContest takes an exclusive, transaction-scoped lock on this
	// contest row, held until the surrounding transaction ends. It exists to
	// serialise writes that live in two different tables and must not race:
	// GrantManager checks the roster before appointing staff, and
	// Enroll/AddParticipants check the staff list before registering a
	// participant, so that a contest's owner or manager can never also end
	// up its participant no matter how the two requests interleave. Must run
	// inside a unit of work; an implementation refuses otherwise rather than
	// silently doing nothing.
	LockContest(ctx context.Context, id uuid.UUID) error
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
