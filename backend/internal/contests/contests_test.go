package contests_test

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

func TestDraftMovesToPublished(t *testing.T) {
	c := contests.Contest{Status: contests.StatusDraft}

	if err := c.CanTransitionTo(contests.StatusPublished); err != nil {
		t.Errorf("CanTransitionTo(published) = %v, want nil", err)
	}
}

func TestDraftCannotJumpStraightToRunning(t *testing.T) {
	// Starting an unpublished contest would open it to participants who were
	// never shown it, and skip the publish gate entirely.
	c := contests.Contest{Status: contests.StatusDraft}

	if err := c.CanTransitionTo(contests.StatusRunning); !errors.Is(err, contests.ErrInvalidTransition) {
		t.Errorf("CanTransitionTo(running) = %v, want contests.ErrInvalidTransition", err)
	}
}

func TestPublishedGoesBackToDraft(t *testing.T) {
	// Publishing is how an organizer finds out the gate passes; undoing that
	// before anybody starts must not require deleting the contest.
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
	// An organizer publishes to see the contest as participants will, and may
	// still fix a typo; once it runs, changing a question would change the
	// task under people already answering it.
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
	// Extending the window after a power cut, or correcting a network range
	// that turned out to be wrong, are exactly what a running contest needs.
	if !(contests.Contest{Status: contests.StatusRunning}).SettingsEditable() {
		t.Error("SettingsEditable() = false for a running contest, want true")
	}
	if (contests.Contest{Status: contests.StatusFinished}).SettingsEditable() {
		t.Error("SettingsEditable() = true for a finished contest, want false")
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
	// The resolver could not name the caller. Failing open here would turn
	// every proxy misconfiguration into an open door.
	c := contests.Contest{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}}

	if c.AllowsAddress(netip.Addr{}) {
		t.Error("AllowsAddress() = true for an unresolvable address, want false")
	}
}

func TestMappedIPv4AddressMatchesAnIPv4Network(t *testing.T) {
	// A v4 client behind a v6 listener arrives as ::ffff:10.20.30.40; refusing
	// it would lock out a whole lecture hall for a transport detail.
	c := contests.Contest{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}}

	if !c.AllowsAddress(netip.MustParseAddr("::ffff:10.20.30.40")) {
		t.Error("AllowsAddress() = false for a v4-mapped address, want true")
	}
}

// validContest is the smallest contest that satisfies Validate, so each test
// below can state exactly the one thing it breaks.
func validContest() contests.Contest {
	return contests.Contest{
		Status:       contests.StatusDraft,
		Enrollment:   contests.EnrollmentInviteOnly,
		QuestionMode: contests.QuestionModeMulti,
		Timing:       contests.TimingFixed,
		Languages:    []contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ro"}},
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

func TestSpeaksReportsDeclaredLanguagesOnly(t *testing.T) {
	c := validContest()

	if !c.Speaks("ro") {
		t.Error("Speaks(ro) = false for a declared language, want true")
	}
	if c.Speaks("ru") {
		t.Error("Speaks(ru) = true for an undeclared language, want false")
	}
}

func TestValidateAcceptsAWellFormedContest(t *testing.T) {
	if err := validContest().Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestValidateRejectsTwoDefaultLanguages(t *testing.T) {
	// "Which language do we serve when the requested one is missing" must
	// never have two answers.
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
	// A contest is created before its languages are chosen; the publish gate
	// is what insists on them, not the field validator.
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
