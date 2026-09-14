package contests_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

func TestCreateMakesTheAuthorTheOwner(t *testing.T) {
	// Somebody has to be able to appoint managers from the first moment, and
	// the only person who certainly exists then is the author.
	f := conteststest.NewFixture()
	author := uuid.New()

	created, err := f.Service.Create(context.Background(), contests.CreateCommand{
		ActorID:      author,
		Translations: []contests.Translation{{Lang: "en", Title: "The Library Murder"}},
		Languages:    []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	manager, err := f.Managers.Get(context.Background(), created.ID, author)
	if err != nil {
		t.Fatalf("the author is not staff on their own contest: %v", err)
	}
	if manager.Role != rbac.RoleOwner {
		t.Errorf("author's role = %q, want owner", manager.Role)
	}
}

func TestCreateStoresAReadOnlyPolicy(t *testing.T) {
	// A contest nobody configured must not hand out writes.
	f := conteststest.NewFixture()

	created, err := f.Service.Create(context.Background(), contests.CreateCommand{ActorID: uuid.New()})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	policy, err := f.Policies.ByContest(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("ByContest() = %v", err)
	}
	if policy.Mode != contests.ModeReadOnly {
		t.Errorf("policy mode = %q, want read_only", policy.Mode)
	}
}

func TestCreateDefaultsToInviteOnlyAndSeveralQuestions(t *testing.T) {
	// The safe default: a contest nobody has configured is not open to the
	// whole installation.
	f := conteststest.NewFixture()

	created, err := f.Service.Create(context.Background(), contests.CreateCommand{ActorID: uuid.New()})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if created.Enrollment != contests.EnrollmentInviteOnly {
		t.Errorf("enrollment = %q, want invite_only", created.Enrollment)
	}
	if created.QuestionMode != contests.QuestionModeMulti {
		t.Errorf("question mode = %q, want multi", created.QuestionMode)
	}
	if created.Status != contests.StatusDraft {
		t.Errorf("status = %q, want draft", created.Status)
	}
}

func TestCreateRecordsWhoCreatedTheContest(t *testing.T) {
	f := conteststest.NewFixture()

	if _, err := f.Service.Create(context.Background(), contests.CreateCommand{ActorID: uuid.New()}); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if !f.Audit.Recorded(audit.ActionContestCreate) {
		t.Errorf("audit entries = %v, want a %s", f.Audit.Actions(), audit.ActionContestCreate)
	}
}

func TestCreateRejectsAnUnknownLanguage(t *testing.T) {
	f := conteststest.NewFixture()

	_, err := f.Service.Create(context.Background(), contests.CreateCommand{
		ActorID:   uuid.New(),
		Languages: []contests.ContestLanguage{{Code: "rus", IsDefault: true}},
	})

	if !errors.Is(err, contests.ErrUnknownLanguage) {
		t.Errorf("Create() = %v, want ErrUnknownLanguage", err)
	}
}

func TestCreateRejectsIndividualTimingWithoutADuration(t *testing.T) {
	f := conteststest.NewFixture()

	_, err := f.Service.Create(context.Background(), contests.CreateCommand{
		ActorID: uuid.New(),
		Timing:  contests.TimingIndividual,
	})

	if !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("Create() = %v, want ErrInvalidContest", err)
	}
}

func TestNothingIsWrittenWhenCreateIsRejected(t *testing.T) {
	// The contest, its owner and its policy land together or not at all.
	f := conteststest.NewFixture()

	_, _ = f.Service.Create(context.Background(), contests.CreateCommand{
		ActorID: uuid.New(),
		Timing:  contests.TimingIndividual,
	})

	if got := f.Contests.Count(); got != 0 {
		t.Errorf("stored contests = %d, want 0", got)
	}
}

func TestUpdateRefusesAFinishedContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)

	_, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID:   uuid.New(),
		ContestID: c.ID,
	})

	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("Update() = %v, want ErrNotEditable", err)
	}
}

func TestUpdateRefusesToChangeTheQuestionModeWhileRunning(t *testing.T) {
	// Participants are already answering under the rules they were shown.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)

	_, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID:      uuid.New(),
		ContestID:    c.ID,
		QuestionMode: contests.QuestionModeSingle,
	})

	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("Update() = %v, want ErrNotEditable", err)
	}
}

// The organizer's recourse when they discover, after the contest already
// finished, that the reports are not done or a dispute is open — the whole
// point of the exception.
func TestExtendGraceLengthensAFinishedContestsGracePeriod(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)

	// c never set an explicit grace, so the grace actually in force is the
	// fixture's installation default (conteststest.FixtureDefaultGraceMin) —
	// the requested value has to clear that, not merely be positive.
	grace := conteststest.FixtureDefaultGraceMin + 60
	updated, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, grace)
	if err != nil {
		t.Fatalf("ExtendGrace() = %v", err)
	}
	if updated.Settings.GracePeriodMin != grace {
		t.Fatalf("GracePeriodMin = %d, want %d", updated.Settings.GracePeriodMin, grace)
	}
}

// Archived is the other status Reclaim now honours (§2.4), so the same
// recourse has to still reach a contest an organizer already archived.
func TestExtendGraceAlsoReachesAnArchivedContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusArchived)

	grace := conteststest.FixtureDefaultGraceMin + 60
	if _, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, grace); err != nil {
		t.Fatalf("ExtendGrace() = %v", err)
	}
}

// Every other status already has its own ordinary settings door; this one
// must not become a second, wider way through it.
func TestExtendGraceRefusesAnyStatusOtherThanFinishedOrArchived(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)

	_, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, 120)
	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("ExtendGrace() = %v, want ErrNotEditable", err)
	}
}

// "Extend" means grow: the one exception a finished contest's settings carry
// must not become a way to shorten a grace an organizer already relied on.
func TestExtendGraceRefusesToShortenAnExplicitlyConfiguredGrace(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)

	first := conteststest.FixtureDefaultGraceMin + 500
	if _, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, first); err != nil {
		t.Fatalf("setup ExtendGrace() = %v", err)
	}

	_, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, first-400)
	if !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("ExtendGrace() = %v, want ErrInvalidContest", err)
	}
}

// A contest that never configured an explicit grace reads as 0 — "defer to
// the installation default" — and the first explicit value it accepts has to
// actually clear that default (ServiceConfig.DefaultGraceMin), not merely be
// positive.
func TestExtendGraceAcceptsTheFirstExplicitValueWhenNoneWasConfigured(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)
	if c.Settings.GracePeriodMin != 0 {
		t.Fatalf("setup: GracePeriodMin = %d, want 0", c.Settings.GracePeriodMin)
	}

	grace := conteststest.FixtureDefaultGraceMin + 30
	updated, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, grace)
	if err != nil {
		t.Fatalf("ExtendGrace() = %v", err)
	}
	if updated.Settings.GracePeriodMin != grace {
		t.Fatalf("GracePeriodMin = %d, want %d", updated.Settings.GracePeriodMin, grace)
	}
}

// The finding this guards against: a contest that never set an explicit
// grace is governed by the installation default, and ExtendGrace used to
// compare a requested value against the stored zero instead of that default
// — so any positive value, including one far below the real default, read as
// an extension. ExtendGrace(…, 60) against a contest defaulting to 24 hours
// used to cut retention to one hour outright; it must be refused instead.
func TestExtendGraceRefusesAValueThatDoesNotClearTheInstallationDefault(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)
	if c.Settings.GracePeriodMin != 0 {
		t.Fatalf("setup: GracePeriodMin = %d, want 0", c.Settings.GracePeriodMin)
	}

	_, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, 60)
	if !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("ExtendGrace() = %v, want ErrInvalidContest", err)
	}
	// And the stored settings must be untouched — a refused call is not a
	// half-applied one.
	stored, err := f.Contests.ByID(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	if stored.Settings.GracePeriodMin != 0 {
		t.Errorf("GracePeriodMin = %d, want unchanged 0", stored.Settings.GracePeriodMin)
	}
}

// A value exactly at the installation default does not extend it either —
// the comparison is "does not extend", the same "<=" the explicit-grace path
// above already uses, applied consistently to the default.
func TestExtendGraceRefusesAValueEqualToTheInstallationDefault(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)

	_, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, conteststest.FixtureDefaultGraceMin)
	if !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("ExtendGrace() = %v, want ErrInvalidContest", err)
	}
}

// A bound reaches this exception the same as everything else CLAUDE.md rule
// 2 asks a stored field to carry.
func TestExtendGraceRefusesAnUnboundedValue(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)

	_, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, 1_000_000_000)
	if !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("ExtendGrace() = %v, want ErrInvalidContest", err)
	}
}

// Nothing else about a finished contest is reachable through this door: it
// is the grace period or nothing.
func TestExtendGraceRecordsOnlyTheGraceField(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)
	actor := uuid.New()

	if _, err := f.Service.ExtendGrace(context.Background(), actor, c.ID, conteststest.FixtureDefaultGraceMin+180); err != nil {
		t.Fatalf("ExtendGrace() = %v", err)
	}

	if len(f.Audit.Entries) != 1 {
		t.Fatalf("%d audit entries were written, want 1", len(f.Audit.Entries))
	}
	entry := f.Audit.Entries[0]
	if entry.Action != audit.ActionContestUpdate || entry.EntityID != c.ID.String() {
		t.Fatalf("entry = %+v; wrong action or entity", entry)
	}
	changed, ok := entry.Payload["changes"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %+v, no changes map", entry.Payload)
	}
	if _, ok := changed["grace_period_min"]; !ok {
		t.Fatalf("changes = %+v, missing grace_period_min", changed)
	}
	if len(changed) != 1 {
		t.Fatalf("changes = %+v, want only grace_period_min recorded", changed)
	}
}

func TestUpdateExtendsTheWindowOfARunningContest(t *testing.T) {
	// The operator response to a power cut. Refusing it would be a policy that
	// only ever hurts participants.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	later := f.Now.Add(4 * time.Hour)

	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID:   uuid.New(),
		ContestID: c.ID,
		EndsAt:    &later,
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}

	if updated.EndsAt == nil || !updated.EndsAt.Equal(later) {
		t.Errorf("EndsAt = %v, want %v", updated.EndsAt, later)
	}
}

// TestUpdateRefusesToMoveEndsAtOnceTheFreezeIsReached: the public and
// participant leaderboards froze at 11:30 on the strength of a stored
// ends_at, and moving ends_at now would recompute FreezeAt to a later
// moment and read the board as live again — showing, for as long as the
// cache stays stale, results submitted after the freeze that already
// happened.
func TestUpdateRefusesToMoveEndsAtOnceTheFreezeIsReached(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	// SeedContest's EndsAt is FixtureNow+2h; a 130-minute freeze puts
	// FreezeAt ten minutes before FixtureNow — already reached.
	freeze := 130
	c.LeaderboardFreezeMin = &freeze
	f.Contests.Put(c)
	later := f.Now.Add(4 * time.Hour)

	_, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, EndsAt: &later,
	})
	if !errors.Is(err, contests.ErrFreezeAlreadyReached) {
		t.Errorf("Update() = %v, want ErrFreezeAlreadyReached", err)
	}
}

// TestUpdateAllowsExtendingEndsAtBeforeTheFreezeIsReached is the other half
// of the freeze guard: the organizer response to a power cut must keep
// working for as long as the freeze this change protects has not actually
// happened yet.
func TestUpdateAllowsExtendingEndsAtBeforeTheFreezeIsReached(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	// FreezeAt = EndsAt(+2h) - 30min = FixtureNow+90min — not reached yet.
	freeze := 30
	c.LeaderboardFreezeMin = &freeze
	f.Contests.Put(c)
	later := f.Now.Add(4 * time.Hour)

	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, EndsAt: &later,
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if updated.EndsAt == nil || !updated.EndsAt.Equal(later) {
		t.Errorf("EndsAt = %v, want %v", updated.EndsAt, later)
	}
}

// TestUpdateAllowsAnUnrelatedFieldWhenEndsAtIsResentUnchangedAfterTheFreeze
// is the resend case the minute-precision comparison exists for: the
// settings form always resends every field, including ends_at, and a
// contest whose ends_at carries seconds (set through the API rather than
// the form) must not have every save of an unrelated field refused just
// because the resent value, truncated to a minute by the form, does not
// match the stored value byte-for-byte.
func TestUpdateAllowsAnUnrelatedFieldWhenEndsAtIsResentUnchangedAfterTheFreeze(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	freeze := 130 // FreezeAt already reached, as above.
	c.LeaderboardFreezeMin = &freeze
	// A stored ends_at with seconds on it — set through the API directly,
	// never something the settings form itself would have produced.
	withSeconds := c.EndsAt.Add(17 * time.Second)
	c.EndsAt = &withSeconds
	f.Contests.Put(c)

	// The form resends ends_at truncated to the minute, unchanged, while
	// editing an unrelated field.
	resent := withSeconds.Truncate(time.Minute)
	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, EndsAt: &resent,
		LeaderboardNames: contests.LeaderboardNamesFullName,
	})
	if err != nil {
		t.Fatalf("Update() = %v, want the resend of an unchanged ends_at to be harmless", err)
	}
	if updated.LeaderboardNames != contests.LeaderboardNamesFullName {
		t.Errorf("LeaderboardNames = %q, want it to have been saved", updated.LeaderboardNames)
	}
	// The stored deadline itself must survive to the nanosecond: passing the
	// minute-precision comparison must not mean the truncated, resent value
	// is what actually gets written. A save that silently moved the
	// deadline by up to 59 seconds would still show "no change" here if this
	// only checked the same-minute comparison again instead of the exact
	// stored value.
	if updated.EndsAt == nil || !updated.EndsAt.Equal(withSeconds) {
		t.Errorf("EndsAt = %v, want the exact stored value %v unchanged", updated.EndsAt, withSeconds)
	}
}

// TestUpdateKeepsTheStoredEndsAtWhenAnExplicitMoveStaysInTheSameMinute is the
// regression this guards against directly: an explicit attempt to move
// ends_at from one second within a minute to another, after the freeze has
// been reached, must not silently succeed at moving the deadline by however
// many seconds separate the two — the settings form cannot express that
// distinction, so the service must not let it through by accident. Keeping
// the stored value (rather than refusing outright) is the chosen behaviour:
// an organiser saving the form after the freeze sees their save succeed, not
// a spurious conflict over a difference of seconds they never intended.
func TestUpdateKeepsTheStoredEndsAtWhenAnExplicitMoveStaysInTheSameMinute(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	freeze := 130 // FreezeAt already reached.
	c.LeaderboardFreezeMin = &freeze
	stored := c.EndsAt.Truncate(time.Minute) // exactly 12:00:00, say.
	c.EndsAt = &stored
	f.Contests.Put(c)

	movedWithinMinute := stored.Add(59 * time.Second) // 12:00:59.
	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, EndsAt: &movedWithinMinute,
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if updated.EndsAt == nil || !updated.EndsAt.Equal(stored) {
		t.Errorf("EndsAt = %v, want it to stay at the stored %v rather than move within the minute", updated.EndsAt, stored)
	}

	reloaded, err := f.Service.ByID(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	if reloaded.EndsAt == nil || !reloaded.EndsAt.Equal(stored) {
		t.Errorf("stored EndsAt = %v, want %v", reloaded.EndsAt, stored)
	}
}

// TestUpdateRefusesToChangeStartsAtForICPCScoringWhileRunning: ICPC penalty
// minutes are counted from starts_at at read time (postgres/leaderboard.go),
// never stored with a submission, so moving starts_at mid-run would
// retroactively rescore every fixed-timing participant's penalty — the same
// "no path may rescore a result nobody can see the reason for"
// checkRunningChange already enforces for the penalty setting itself.
func TestUpdateRefusesToChangeStartsAtForICPCScoringWhileRunning(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	c.Scoring = contests.ScoringICPC
	f.Contests.Put(c)
	earlier := f.Now.Add(-2 * time.Hour)

	_, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, StartsAt: &earlier,
	})
	if !errors.Is(err, contests.ErrICPCStartLocked) {
		t.Errorf("Update() = %v, want ErrICPCStartLocked", err)
	}
}

// TestUpdateAllowsAnUnrelatedFieldWhenStartsAtIsResentUnchangedOnICPC is the
// starts_at half of the resend case: an ICPC contest whose starts_at carries
// seconds must still accept an unrelated save when the form resends
// starts_at at its own minute precision.
func TestUpdateAllowsAnUnrelatedFieldWhenStartsAtIsResentUnchangedOnICPC(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	c.Scoring = contests.ScoringICPC
	withSeconds := c.StartsAt.Add(42 * time.Second)
	c.StartsAt = &withSeconds
	f.Contests.Put(c)

	resent := withSeconds.Truncate(time.Minute)
	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, StartsAt: &resent,
		LeaderboardNames: contests.LeaderboardNamesFullName,
	})
	if err != nil {
		t.Fatalf("Update() = %v, want the resend of an unchanged starts_at to be harmless", err)
	}
	if updated.LeaderboardNames != contests.LeaderboardNamesFullName {
		t.Errorf("LeaderboardNames = %q, want it to have been saved", updated.LeaderboardNames)
	}
	// Same exactness requirement as the ends_at case: the stored starts_at
	// must survive to the nanosecond, not merely stay within the same
	// minute as before.
	if updated.StartsAt == nil || !updated.StartsAt.Equal(withSeconds) {
		t.Errorf("StartsAt = %v, want the exact stored value %v unchanged", updated.StartsAt, withSeconds)
	}
}

// TestUpdateKeepsTheStoredStartsAtWhenAnExplicitMoveStaysInTheSameMinute is
// the starts_at half of the same regression: on a running ICPC contest, an
// explicit move from one second within a minute to another must not
// silently rescore every fixed-timing participant's penalty by however many
// seconds separate the two.
func TestUpdateKeepsTheStoredStartsAtWhenAnExplicitMoveStaysInTheSameMinute(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	c.Scoring = contests.ScoringICPC
	stored := c.StartsAt.Truncate(time.Minute)
	c.StartsAt = &stored
	f.Contests.Put(c)

	movedWithinMinute := stored.Add(59 * time.Second)
	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, StartsAt: &movedWithinMinute,
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if updated.StartsAt == nil || !updated.StartsAt.Equal(stored) {
		t.Errorf("StartsAt = %v, want it to stay at the stored %v rather than move within the minute", updated.StartsAt, stored)
	}

	reloaded, err := f.Service.ByID(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	if reloaded.StartsAt == nil || !reloaded.StartsAt.Equal(stored) {
		t.Errorf("stored StartsAt = %v, want %v", reloaded.StartsAt, stored)
	}
}

// TestUpdateAllowsChangingStartsAtOutsideICPCScoringWhileRunning proves the
// new starts_at guard is scoped to ICPC: a points or winner contest has
// nothing keyed off starts_at the way ICPC's penalty formula is, so extending
// or correcting the window's start stays the ordinary "fix it after a power
// cut" operation SettingsEditable exists for.
func TestUpdateAllowsChangingStartsAtOutsideICPCScoringWhileRunning(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	earlier := f.Now.Add(-2 * time.Hour)

	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, StartsAt: &earlier,
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if updated.StartsAt == nil || !updated.StartsAt.Equal(earlier) {
		t.Errorf("StartsAt = %v, want %v", updated.StartsAt, earlier)
	}
}

func TestPublishRefusesAnIncompleteContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	err := f.Service.Transition(context.Background(), uuid.New(), c.ID, contests.StatusPublished)

	if !errors.Is(err, contests.ErrNotPublishable) {
		t.Errorf("Transition(published) = %v, want ErrNotPublishable", err)
	}
}

func TestPublishAcceptsACompleteContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedPublishableContest()

	if err := f.Service.Transition(context.Background(), uuid.New(), c.ID, contests.StatusPublished); err != nil {
		t.Fatalf("Transition(published) = %v", err)
	}

	reloaded, _ := f.Service.ByID(context.Background(), c.ID)
	if reloaded.Status != contests.StatusPublished {
		t.Errorf("status = %q, want published", reloaded.Status)
	}
}

func TestStartingAnUnpublishedContestIsRefused(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedPublishableContest()

	err := f.Service.Transition(context.Background(), uuid.New(), c.ID, contests.StatusRunning)

	if !errors.Is(err, contests.ErrInvalidTransition) {
		t.Errorf("Transition(running) = %v, want ErrInvalidTransition", err)
	}
}

func TestDeleteRefusesAContestThatWasPublished(t *testing.T) {
	// Once people could see it, it is a record: archiving is how it goes away.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)

	err := f.Service.Delete(context.Background(), uuid.New(), c.ID)

	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("Delete() = %v, want ErrNotEditable", err)
	}
}

func TestDeleteRemovesADraft(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	if err := f.Service.Delete(context.Background(), uuid.New(), c.ID); err != nil {
		t.Fatalf("Delete() = %v", err)
	}

	if _, err := f.Service.ByID(context.Background(), c.ID); !errors.Is(err, contests.ErrNotFound) {
		t.Errorf("ByID() after delete = %v, want ErrNotFound", err)
	}
}

func TestPolicyCannotChangeWhileTheContestIsRunning(t *testing.T) {
	// Participants would end up with different powers depending on when they
	// connected, and the template grants would no longer match the validator.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)

	policy := contests.DefaultSQLPolicy(c.ID)
	policy.Mode = contests.ModeReadWrite
	err := f.Service.SetPolicy(context.Background(), uuid.New(), c.ID, policy)

	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("SetPolicy() = %v, want ErrNotEditable", err)
	}
}

func TestPolicyChangesBeforeTheStart(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)

	policy := contests.DefaultSQLPolicy(c.ID)
	policy.Mode = contests.ModeReadWrite
	policy.WritableTables = []string{"notes"}
	if err := f.Service.SetPolicy(context.Background(), uuid.New(), c.ID, policy); err != nil {
		t.Fatalf("SetPolicy() = %v", err)
	}

	stored, _ := f.Service.Policy(context.Background(), c.ID)
	if stored.Mode != contests.ModeReadWrite {
		t.Errorf("stored mode = %q, want read_write", stored.Mode)
	}
}

func TestSetLanguagesRefusesOnceTheContestIsRunning(t *testing.T) {
	// Adding a language mid-contest would leave everything authored in it
	// empty for whoever picked it.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)

	err := f.Service.SetLanguages(context.Background(), uuid.New(), c.ID,
		[]contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ro"}})

	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("SetLanguages() = %v, want ErrNotEditable", err)
	}
}

func TestUnknownContestIsReportedAsNotFound(t *testing.T) {
	f := conteststest.NewFixture()

	if _, err := f.Service.ByID(context.Background(), uuid.New()); !errors.Is(err, contests.ErrNotFound) {
		t.Errorf("ByID() = %v, want ErrNotFound", err)
	}
}

func TestAContestCanBeSwitchedBackToAFixedWindow(t *testing.T) {
	// The session length belongs to individual timing. Switching to a fixed
	// window has to take it away, or the change is unreachable: the client has
	// no way to send "no duration", and the contest refuses to validate with a
	// duration it is not allowed to carry.
	f := conteststest.NewFixture()
	minutes := 90
	c := f.Contests.Put(contests.Contest{
		Status:       contests.StatusDraft,
		Enrollment:   contests.EnrollmentInviteOnly,
		QuestionMode: contests.QuestionModeMulti,
		Progression:  contests.ProgressionFree,
		Scoring:      contests.ScoringPoints,
		Timing:       contests.TimingIndividual,
		DurationMin:  &minutes,
	})

	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID:   uuid.New(),
		ContestID: c.ID,
		Timing:    contests.TimingFixed,
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}

	if updated.DurationMin != nil {
		t.Errorf("duration = %v, want it cleared with the timing model", *updated.DurationMin)
	}
}

func TestStartingRefusesAContestWhoseContentWasTakenApartAfterPublishing(t *testing.T) {
	// Content stays editable while published, deliberately — an organizer
	// publishes to see the contest as participants will, and may still fix a
	// typo. That leaves a window: publish, remove the story, start. The gate
	// has to hold at the moment participants are actually let in.
	f := conteststest.NewFixture()
	c := f.SeedPublishableContest()
	if err := f.Service.Transition(context.Background(), uuid.New(), c.ID, contests.StatusPublished); err != nil {
		t.Fatalf("Transition(published) = %v", err)
	}
	if err := f.Stories.Delete(context.Background(), c.ID); err != nil {
		t.Fatalf("Delete() = %v", err)
	}

	err := f.Service.Transition(context.Background(), uuid.New(), c.ID, contests.StatusRunning)

	if !errors.Is(err, contests.ErrNotPublishable) {
		t.Errorf("Transition(running) = %v, want ErrNotPublishable", err)
	}
}

// changesIn returns the recorded change set of the last entry with that action.
func changesIn(t *testing.T, f *conteststest.Fixture, action string) map[string]any {
	t.Helper()

	for i := len(f.Audit.Entries) - 1; i >= 0; i-- {
		if f.Audit.Entries[i].Action != action {
			continue
		}
		changes, ok := f.Audit.Entries[i].Payload["changes"].(map[string]any)
		if !ok {
			t.Fatalf("%s payload = %v, want a changes map", action, f.Audit.Entries[i].Payload)
		}
		return changes
	}
	t.Fatalf("no %s entry among %v", action, f.Audit.Actions())
	return nil
}

func TestAnEditRecordsWhatMovedAndWhatItWas(t *testing.T) {
	// "Who moved the deadline, and what was it before" is the question asked
	// months later. Recording the new state alone cannot answer the second
	// half, and recording every field cannot answer the first.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	later := f.Now.Add(9 * time.Hour)

	if _, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID:    uuid.New(),
		ContestID:  c.ID,
		EndsAt:     &later,
		Enrollment: contests.EnrollmentOpen,
	}); err != nil {
		t.Fatalf("Update() = %v", err)
	}

	changes := changesIn(t, f, audit.ActionContestUpdate)

	enrollment, ok := changes["enrollment"].(map[string]any)
	if !ok {
		t.Fatalf("changes = %v, want the enrollment recorded", changes)
	}
	if enrollment["from"] != contests.EnrollmentInviteOnly || enrollment["to"] != contests.EnrollmentOpen {
		t.Errorf("enrollment = %v, want invite_only → open", enrollment)
	}
	if _, present := changes["ends_at"]; !present {
		t.Errorf("changes = %v, want the moved deadline recorded", changes)
	}
}

func TestAnEditDoesNotRecordFieldsTheFormMerelyResent(t *testing.T) {
	// The settings form sends everything it holds. If all of it were recorded,
	// every save would read as a rewrite of the contest and bury the one line
	// that actually moved.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	if _, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID:    uuid.New(),
		ContestID:  c.ID,
		Enrollment: contests.EnrollmentInviteOnly,
		Timing:     contests.TimingFixed,
		StartsAt:   c.StartsAt,
		EndsAt:     c.EndsAt,
	}); err != nil {
		t.Fatalf("Update() = %v", err)
	}

	for i := len(f.Audit.Entries) - 1; i >= 0; i-- {
		if f.Audit.Entries[i].Action != audit.ActionContestUpdate {
			continue
		}
		if f.Audit.Entries[i].Payload["changed"] != false {
			t.Errorf("payload = %v, want it to say nothing changed", f.Audit.Entries[i].Payload)
		}
		return
	}
	t.Fatalf("no %s entry at all", audit.ActionContestUpdate)
}

func TestChangingTheSQLPolicyRecordsWhatWasLoosened(t *testing.T) {
	// The one setting that decides how much power a participant gets. What it
	// was before is the whole question after an incident.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	policy := contests.DefaultSQLPolicy(c.ID)
	policy.Mode = contests.ModeReadWrite
	policy.WritableTables = []string{"notes"}
	policy.AllowCreateView = true
	if err := f.Service.SetPolicy(context.Background(), uuid.New(), c.ID, policy); err != nil {
		t.Fatalf("SetPolicy() = %v", err)
	}

	changes := changesIn(t, f, audit.ActionContestPolicyChange)

	mode, ok := changes["mode"].(map[string]any)
	if !ok || mode["from"] != contests.ModeReadOnly || mode["to"] != contests.ModeReadWrite {
		t.Errorf("mode = %v, want read_only → read_write", changes["mode"])
	}
	if _, present := changes["allow_create_view"]; !present {
		t.Errorf("changes = %v, want the loosened flag recorded", changes)
	}
}

func TestChangingTheLanguagesRecordsTheSetItReplaced(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	if err := f.Service.SetLanguages(context.Background(), uuid.New(), c.ID,
		[]contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ro"}}); err != nil {
		t.Fatalf("SetLanguages() = %v", err)
	}

	changes := changesIn(t, f, audit.ActionContestLanguages)

	languages, ok := changes["languages"].(map[string]any)
	if !ok {
		t.Fatalf("changes = %v, want the languages recorded", changes)
	}
	if _, ok := languages["from"].([]string); !ok {
		t.Errorf("from = %v, want the previous set", languages["from"])
	}
}

func TestAStatusChangeRecordsItTheSameWayAsEverythingElse(t *testing.T) {
	// It already carried from/to under its own keys. One shape for every
	// change is what lets the panel render them without knowing the action.
	f := conteststest.NewFixture()
	c := f.SeedPublishableContest()

	if err := f.Service.Transition(context.Background(), uuid.New(), c.ID,
		contests.StatusPublished); err != nil {
		t.Fatalf("Transition() = %v", err)
	}

	changes := changesIn(t, f, audit.ActionContestStatusChange)

	status, ok := changes["status"].(map[string]any)
	if !ok || status["from"] != contests.StatusDraft || status["to"] != contests.StatusPublished {
		t.Errorf("status = %v, want draft → published", changes["status"])
	}
}

func TestChangingTheTitlesRecordsTheLanguagesButNotTheText(t *testing.T) {
	// It recorded nothing at all — an entry that says only that somebody
	// touched the titles. The languages are the part worth keeping; the titles
	// themselves are authored text, and the trail is not a version history.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	if err := f.Service.SetTranslations(context.Background(), uuid.New(), c.ID,
		[]contests.Translation{{Lang: "en", Title: "The Library Murder"}}); err != nil {
		t.Fatalf("SetTranslations() = %v", err)
	}

	for i := len(f.Audit.Entries) - 1; i >= 0; i-- {
		entry := f.Audit.Entries[i]
		if entry.Action != audit.ActionContestTranslations {
			continue
		}
		if fmt.Sprint(entry.Payload) == "map[]" || entry.Payload == nil {
			t.Fatalf("payload = %v, want the languages named", entry.Payload)
		}
		if strings.Contains(fmt.Sprint(entry.Payload), "Library Murder") {
			t.Fatalf("payload = %v, want no authored text in the trail", entry.Payload)
		}
		return
	}
	t.Fatalf("no %s entry", audit.ActionContestTranslations)
}

// Moving the freeze while the contest runs would either open the live table
// for a moment or hide a table participants already saw; neither is a setting.
func TestUpdateRefusesToMoveTheFreezeWhileRunning(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)

	freeze := 30
	settings := c.Settings
	_, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, Settings: &settings,
		LeaderboardFreezeMin: &freeze,
	})
	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("Update() = %v, want ErrNotEditable", err)
	}
}

// The label is the organiser's choice about names, not a property of the
// result, and it may change while the contest runs.
func TestUpdateChangesTheLeaderboardLabelWhileRunning(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)

	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, LeaderboardNames: contests.LeaderboardNamesFullName,
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if updated.LeaderboardNames != contests.LeaderboardNamesFullName {
		t.Errorf("LeaderboardNames = %q, want %q", updated.LeaderboardNames, contests.LeaderboardNamesFullName)
	}
}

// A draft sets its freeze, and clears it again: "no freeze" has to be
// sayable, not only "a different freeze".
func TestUpdateSetsAndClearsTheFreezeBeforeTheContestStarts(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	freeze := 30
	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, LeaderboardFreezeMin: &freeze,
	})
	if err != nil || updated.LeaderboardFreezeMin == nil || *updated.LeaderboardFreezeMin != 30 {
		t.Fatalf("set: Update() = %+v, %v", updated.LeaderboardFreezeMin, err)
	}

	updated, err = f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, ClearLeaderboardFreeze: true,
	})
	if err != nil || updated.LeaderboardFreezeMin != nil {
		t.Fatalf("clear: Update() = %+v, %v", updated.LeaderboardFreezeMin, err)
	}
}

// The ICPC scoring mode (docs/superpowers/specs/2026-09-13-icpc-scoring-design.md).

// A contest that never mentions the penalty gets the same 20 minutes the
// column default would give it, so a seed created before an organizer ever
// opens the ICPC settings still has a sane value.
func TestCreateDefaultsTheICPCPenaltyToTwenty(t *testing.T) {
	f := conteststest.NewFixture()

	created, err := f.Service.Create(context.Background(), contests.CreateCommand{ActorID: uuid.New()})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if created.ICPCPenaltyMin != 20 {
		t.Errorf("ICPCPenaltyMin = %d, want 20", created.ICPCPenaltyMin)
	}
}

func TestCreateHonoursAnExplicitICPCPenalty(t *testing.T) {
	f := conteststest.NewFixture()
	penalty := 15

	created, err := f.Service.Create(context.Background(), contests.CreateCommand{
		ActorID: uuid.New(), ICPCPenaltyMin: &penalty,
	})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if created.ICPCPenaltyMin != 15 {
		t.Errorf("ICPCPenaltyMin = %d, want 15", created.ICPCPenaltyMin)
	}
}

// UpdateCommand.ICPCPenaltyMin is a pointer for the same reason
// LeaderboardFreezeMin's setting half is: nil has to mean "leave it alone",
// and the field's own zero value (no penalty at all) is a configuration an
// organizer can mean.
func TestUpdateLeavesTheICPCPenaltyAloneWhenNotMentioned(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	c.ICPCPenaltyMin = 30
	f.Contests.Put(c)

	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, Enrollment: contests.EnrollmentOpen,
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if updated.ICPCPenaltyMin != 30 {
		t.Errorf("ICPCPenaltyMin = %d, want it to survive an unrelated update", updated.ICPCPenaltyMin)
	}
}

func TestUpdateSetsTheICPCPenalty(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	penalty := 45

	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, ICPCPenaltyMin: &penalty,
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if updated.ICPCPenaltyMin != 45 {
		t.Errorf("ICPCPenaltyMin = %d, want 45", updated.ICPCPenaltyMin)
	}
}

// The penalty is applied per submission, at the moment of answering; moving
// it mid-run would make earlier answers disagree with later ones about how
// much a wrong attempt cost, for a reason no participant could see — the
// same reasoning that already refuses a scoring-mode change while running.
func TestUpdateRefusesToChangeTheICPCPenaltyWhileRunning(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	penalty := 30

	_, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, ICPCPenaltyMin: &penalty,
	})
	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("Update() = %v, want ErrNotEditable", err)
	}
}
