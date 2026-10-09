package contests_test

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"slices"
)

func TestDraftMovesToPublished(t *testing.T) {
	c := contests.Contest{Status: contests.StatusDraft}

	if err := c.CanTransitionTo(contests.StatusPublished); err != nil {
		t.Errorf("CanTransitionTo(published) = %v, want nil", err)
	}
}

func TestDraftCannotJumpStraightToRunning(t *testing.T) {
	// That would skip the publish gate.
	c := contests.Contest{Status: contests.StatusDraft}

	if err := c.CanTransitionTo(contests.StatusRunning); !errors.Is(err, contests.ErrInvalidTransition) {
		t.Errorf("CanTransitionTo(running) = %v, want contests.ErrInvalidTransition", err)
	}
}

func TestPublishedGoesBackToDraft(t *testing.T) {
	// Undoing a publish before anybody starts must not require deleting it.
	c := contests.Contest{Status: contests.StatusPublished}

	if err := c.CanTransitionTo(contests.StatusDraft); err != nil {
		t.Errorf("CanTransitionTo(draft) = %v, want nil", err)
	}
}

func TestRunningCannotBeEditedBackToPublished(t *testing.T) {
	c := contests.Contest{Status: contests.StatusRunning}

	if err := c.CanTransitionTo(contests.StatusPublished); !errors.Is(err, contests.ErrInvalidTransition) {
		t.Errorf("CanTransitionTo(published) = %v, want contests.ErrInvalidTransition", err)
	}
}

func TestArchivedIsTerminal(t *testing.T) {
	c := contests.Contest{Status: contests.StatusArchived}

	for _, status := range []string{contests.StatusDraft, contests.StatusPublished, contests.StatusRunning, contests.StatusFinished} {
		if err := c.CanTransitionTo(status); !errors.Is(err, contests.ErrInvalidTransition) {
			t.Errorf("CanTransitionTo(%s) = %v, want contests.ErrInvalidTransition", status, err)
		}
	}
}

func TestContentIsEditableUntilTheContestStarts(t *testing.T) {
	// Once it runs, changing a question changes the task under people
	// already answering it.
	editable := map[string]bool{
		contests.StatusDraft:     true,
		contests.StatusPublished: true,
		contests.StatusRunning:   false,
		contests.StatusFinished:  false,
		contests.StatusArchived:  false,
	}

	for status, want := range editable {
		if got := (contests.Contest{Status: status}).ContentEditable(); got != want {
			t.Errorf("ContentEditable() for %s = %v, want %v", status, got, want)
		}
	}
}

func TestSettingsStayEditableWhileRunning(t *testing.T) {
	// E.g. extending the window after a power cut, or fixing a network range.
	if !(contests.Contest{Status: contests.StatusRunning}).SettingsEditable() {
		t.Error("SettingsEditable() = false for a running contest, want true")
	}
	if (contests.Contest{Status: contests.StatusFinished}).SettingsEditable() {
		t.Error("SettingsEditable() = true for a finished contest, want false")
	}
}

func TestSequentialActiveOnlyUnderMultiQuestionMode(t *testing.T) {
	cases := []struct {
		name         string
		progression  string
		questionMode string
		want         bool
	}{
		{"sequential and multi", contests.ProgressionSequential, contests.QuestionModeMulti, true},
		{"sequential but single", contests.ProgressionSequential, contests.QuestionModeSingle, false},
		{"free and multi", contests.ProgressionFree, contests.QuestionModeMulti, false},
		{"free and single", contests.ProgressionFree, contests.QuestionModeSingle, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			contest := contests.Contest{Progression: c.progression, QuestionMode: c.questionMode}
			if got := contest.SequentialActive(); got != c.want {
				t.Errorf("SequentialActive() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNoNetworkRestrictionAllowsEverybody(t *testing.T) {
	c := contests.Contest{}

	if !c.AllowsAddress(netip.MustParseAddr("203.0.113.7")) {
		t.Error("AllowsAddress() = false with no restriction configured, want true")
	}
}

func TestAddressInsideAnAllowedNetworkIsAdmitted(t *testing.T) {
	c := contests.Contest{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}}

	if !c.AllowsAddress(netip.MustParseAddr("10.20.30.40")) {
		t.Error("AllowsAddress() = false for an address in the allowed range, want true")
	}
}

func TestAddressOutsideEveryAllowedNetworkIsRefused(t *testing.T) {
	c := contests.Contest{AllowedCIDRs: []netip.Prefix{
		netip.MustParsePrefix("10.20.0.0/16"),
		netip.MustParsePrefix("192.168.1.42/32"),
	}}

	if c.AllowsAddress(netip.MustParseAddr("10.21.0.1")) {
		t.Error("AllowsAddress() = true for an address outside every range, want false")
	}
}

func TestUnknownAddressIsRefusedWhenARestrictionIsInForce(t *testing.T) {
	// Failing open would turn every proxy misconfiguration into an open door.
	c := contests.Contest{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}}

	if c.AllowsAddress(netip.Addr{}) {
		t.Error("AllowsAddress() = true for an unresolvable address, want false")
	}
}

func TestMappedIPv4AddressMatchesAnIPv4Network(t *testing.T) {
	// A v4 client behind a v6 listener arrives as ::ffff:10.20.30.40.
	c := contests.Contest{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}}

	if !c.AllowsAddress(netip.MustParseAddr("::ffff:10.20.30.40")) {
		t.Error("AllowsAddress() = false for a v4-mapped address, want true")
	}
}

func validContest() contests.Contest {
	return contests.Contest{
		Status:       contests.StatusDraft,
		Enrollment:   contests.EnrollmentInviteOnly,
		QuestionMode: contests.QuestionModeMulti,
		Progression:  contests.ProgressionFree,
		Scoring:      contests.ScoringPoints,
		Timing:       contests.TimingFixed,
		Languages:    []contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ro"}},

		LeaderboardNames: contests.LeaderboardNamesLogin,
	}
}

func TestDefaultLanguageIsTheOneMarkedDefault(t *testing.T) {
	if got := validContest().DefaultLanguage(); got != "en" {
		t.Errorf("DefaultLanguage() = %q, want en", got)
	}
}

func TestContestWithoutLanguagesHasNoDefault(t *testing.T) {
	if got := (contests.Contest{}).DefaultLanguage(); got != "" {
		t.Errorf("DefaultLanguage() = %q, want empty", got)
	}
}

func TestLanguageCodesKeepDeclarationOrder(t *testing.T) {
	got := validContest().LanguageCodes()

	if !reflect.DeepEqual(got, []string{"en", "ro"}) {
		t.Errorf("LanguageCodes() = %v, want [en ro]", got)
	}
}

func TestValidateAcceptsAWellFormedContest(t *testing.T) {
	if err := validContest().Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestValidateRejectsTwoDefaultLanguages(t *testing.T) {
	c := validContest()
	c.Languages = []contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ro", IsDefault: true}}

	if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidContest", err)
	}
}

func TestValidateRejectsDeclaredLanguagesWithNoDefault(t *testing.T) {
	c := validContest()
	c.Languages = []contests.ContestLanguage{{Code: "en"}, {Code: "ro"}}

	if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidContest", err)
	}
}

func TestValidateRejectsADuplicatedLanguage(t *testing.T) {
	c := validContest()
	c.Languages = []contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "en"}}

	if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidContest", err)
	}
}

func TestValidateAcceptsAContestWithNoLanguagesYet(t *testing.T) {
	// The publish gate insists on languages, not the field validator.
	c := validContest()
	c.Languages = nil

	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestValidateRejectsIndividualTimingWithoutADuration(t *testing.T) {
	c := validContest()
	c.Timing = contests.TimingIndividual

	if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidContest", err)
	}
}

// Deadline multiplies the minutes into int64 nanoseconds; a large enough
// value wraps to a past deadline and locks out every participant.
func TestValidateRejectsAnIndividualDurationPastTheBound(t *testing.T) {
	c := validContest()
	c.Timing = contests.TimingIndividual
	tooLong := 7*24*60 + 1
	c.DurationMin = &tooLong

	if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidContest", err)
	}
}

func TestValidateAcceptsAnIndividualDurationAtTheBound(t *testing.T) {
	c := validContest()
	c.Timing = contests.TimingIndividual
	atBound := 7 * 24 * 60
	c.DurationMin = &atBound

	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// CLAUDE.md rule 2: grace_period_min reaches make_interval.
func TestValidateRejectsAGracePeriodPastTheBound(t *testing.T) {
	c := validContest()
	c.Settings.GracePeriodMin = 90*24*60 + 1

	if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidContest", err)
	}
}

func TestValidateAcceptsAGracePeriodAtTheBound(t *testing.T) {
	c := validContest()
	c.Settings.GracePeriodMin = 90 * 24 * 60

	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestValidateRejectsFixedTimingCarryingADuration(t *testing.T) {
	// A stray duration nothing reads is a setting an organizer would trust.
	c := validContest()
	minutes := 90
	c.DurationMin = &minutes

	if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidContest", err)
	}
}

func TestValidateRejectsAWindowThatEndsBeforeItStarts(t *testing.T) {
	c := validContest()
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	end := start.Add(-time.Hour)
	c.StartsAt, c.EndsAt = &start, &end

	if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidContest", err)
	}
}

func TestValidateRejectsAnUnknownEnrollmentType(t *testing.T) {
	c := validContest()
	c.Enrollment = "everyone"

	if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("Validate() = %v, want contests.ErrInvalidContest", err)
	}
}

func TestValidateRejectsAFreezeOutsideItsBounds(t *testing.T) {
	// CLAUDE.md rule 2. Zero is not "no freeze"; nil is.
	for _, freeze := range []int{0, -5, 10081} {
		c := validContest()
		c.LeaderboardFreezeMin = &freeze
		if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
			t.Errorf("freeze %d: Validate() = %v, want ErrInvalidContest", freeze, err)
		}
	}
}

// Such a freeze would leave the table empty for the whole contest.
func TestValidateRejectsAFreezeThatIsNotShorterThanTheWindow(t *testing.T) {
	c := validContest()
	start := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	c.StartsAt, c.EndsAt = &start, &end

	freeze := 120
	c.LeaderboardFreezeMin = &freeze
	if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("freeze equal to the window: Validate() = %v, want ErrInvalidContest", err)
	}

	freeze = 119
	if err := c.Validate(); err != nil {
		t.Errorf("freeze one minute shorter than the window: Validate() = %v, want nil", err)
	}
}

func TestValidateRejectsAnUnknownLeaderboardLabel(t *testing.T) {
	for _, label := range []string{"", "email", "FULL_NAME"} {
		c := validContest()
		c.LeaderboardNames = label
		if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
			t.Errorf("label %q: Validate() = %v, want ErrInvalidContest", label, err)
		}
	}
}

func TestValidateAcceptsICPCScoring(t *testing.T) {
	c := validContest()
	c.Scoring = contests.ScoringICPC

	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// CLAUDE.md rule 2; matches the migration's CHECK (BETWEEN 0 AND 240).
func TestValidateRejectsAnICPCPenaltyOutsideItsBounds(t *testing.T) {
	for _, penalty := range []int{-1, 241} {
		c := validContest()
		c.ICPCPenaltyMin = penalty
		if err := c.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
			t.Errorf("penalty %d: Validate() = %v, want ErrInvalidContest", penalty, err)
		}
	}
}

func TestValidateAcceptsAnICPCPenaltyAtItsBounds(t *testing.T) {
	for _, penalty := range []int{0, 240} {
		c := validContest()
		c.ICPCPenaltyMin = penalty
		if err := c.Validate(); err != nil {
			t.Errorf("penalty %d: Validate() = %v, want nil", penalty, err)
		}
	}
}

// Written against the lifecycle, not a list: a new status must decide here
// whether a stranger sees it.
func TestOnlyADraftIsKeptFromAStranger(t *testing.T) {
	all := []string{
		contests.StatusDraft,
		contests.StatusPublished,
		contests.StatusRunning,
		contests.StatusFinished,
		contests.StatusArchived,
	}

	for _, status := range all {
		public := slices.Contains(contests.PublicStatuses, status)
		if status == contests.StatusDraft && public {
			t.Error("a draft is visible to a stranger")
		}
		if status != contests.StatusDraft && !public {
			t.Errorf("%q is not in PublicStatuses; if that is deliberate, say so here", status)
		}
	}
	if len(contests.PublicStatuses) != len(all)-1 {
		t.Errorf("PublicStatuses has %d entries for %d statuses: a status was added without a decision",
			len(contests.PublicStatuses), len(all))
	}
}
