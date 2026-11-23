package contests

import (
	"net/netip"
	"time"
)

// Patch is a change to a contest's own settings, the one shape Create and
// Update take. A field left at its zero value (an empty string, a nil pointer,
// a nil slice) is not part of the change: Update keeps the stored value and
// Create keeps DefaultContest's. Pointers carry the settings whose zero is a
// real value, such as no penalty or a download turned off.
type Patch struct {
	Enrollment   string
	QuestionMode string
	Progression  string
	Scoring      string
	Timing       string
	DurationMin  *int
	StartsAt     *time.Time
	EndsAt       *time.Time
	// AllowedCIDRs replaces the network restriction; a non-nil empty slice
	// clears it.
	AllowedCIDRs []netip.Prefix
	// Settings replaces every tunable at once.
	Settings *Settings
	// LeaderboardFreezeMin sets the freeze; ClearLeaderboardFreeze removes it,
	// since nil already means "not part of the change", and wins over it.
	LeaderboardFreezeMin   *int
	ClearLeaderboardFreeze bool
	LeaderboardNames       string
	ICPCPenaltyMin         *int
	ResultDownload         *bool
	SchemaDiagram          *bool
}

// DefaultContest is a new draft with every setting at its default. The
// leaderboard labels people by full name because the table is public and a
// self-registered login is an email address. The ICPC penalty, the result
// download and the schema diagram match their column defaults, so a new
// contest behaves like one stored before the setting existed.
func DefaultContest() Contest {
	return Contest{
		Status:           StatusDraft,
		Enrollment:       EnrollmentInviteOnly,
		Visibility:       VisibilityClosed,
		QuestionMode:     QuestionModeMulti,
		Progression:      ProgressionFree,
		Scoring:          ScoringPoints,
		Timing:           TimingFixed,
		LeaderboardNames: LeaderboardNamesFullName,
		ICPCPenaltyMin:   DefaultICPCPenaltyMin,
		ResultDownload:   true,
		SchemaDiagram:    true,
	}
}

// ApplyTo returns c with p's changes and nothing checked; the caller
// validates the result. Self-registration always opens the contest
// (checkVisibility), so a contest left taking it is made visible.
func (p Patch) ApplyTo(c Contest) Contest {
	replace(&c.Enrollment, p.Enrollment)
	replace(&c.QuestionMode, p.QuestionMode)
	replace(&c.Progression, p.Progression)
	replace(&c.Scoring, p.Scoring)
	replace(&c.Timing, p.Timing)
	replace(&c.DurationMin, p.DurationMin)
	replace(&c.StartsAt, p.StartsAt)
	replace(&c.EndsAt, p.EndsAt)
	if p.AllowedCIDRs != nil {
		c.AllowedCIDRs = p.AllowedCIDRs
	}
	replaceWith(&c.Settings, p.Settings)
	if p.ClearLeaderboardFreeze {
		c.LeaderboardFreezeMin = nil
	} else {
		replace(&c.LeaderboardFreezeMin, p.LeaderboardFreezeMin)
	}
	replace(&c.LeaderboardNames, p.LeaderboardNames)
	replaceWith(&c.ICPCPenaltyMin, p.ICPCPenaltyMin)
	replaceWith(&c.ResultDownload, p.ResultDownload)
	replaceWith(&c.SchemaDiagram, p.SchemaDiagram)

	if c.Enrollment == EnrollmentOpen {
		c.Visibility = VisibilityOpen
	}
	return c
}

// replace writes v over *dst unless v is the zero value, which a Patch uses
// for "not part of the change".
func replace[T comparable](dst *T, v T) {
	var zero T
	if v != zero {
		*dst = v
	}
}

// replaceWith is replace for a setting whose zero is meaningful, carried as a
// pointer that is nil when not part of the change.
func replaceWith[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

// auditFields is the part of a contest the audit trail may hold; every Patch
// field must reach it (TestEveryContestSettingHasItsDefaultAndIsSetAndLeftAloneByThePatch).
// Authored text is excluded: the trail records which titles changed, never
// the text (§9.2).
func (c Contest) auditFields() map[string]any {
	return map[string]any{
		"enrollment":               c.Enrollment,
		"visibility":               c.Visibility,
		"archive_tasks":            c.ArchiveTasks,
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
		"trusted_domain_only":      c.Settings.TrustedDomainOnly,
		"leaderboard_freeze_min":   c.LeaderboardFreezeMin,
		"leaderboard_names":        c.LeaderboardNames,
		"icpc_penalty_min":         c.ICPCPenaltyMin,
		"result_download":          c.ResultDownload,
		"schema_diagram":           c.SchemaDiagram,
	}
}

// cidrStrings renders a network list for the audit trail.
func cidrStrings(prefixes []netip.Prefix) []string {
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		out = append(out, prefix.String())
	}
	return out
}
