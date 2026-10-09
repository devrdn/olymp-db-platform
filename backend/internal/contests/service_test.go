package contests_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

// Submit writes against the gate's closing instant; a missing gate must not
// silently become a zero grace.
func TestNewServiceRefusesToAssembleWithoutAGate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewService without a Gate did not panic")
		}
	}()
	contests.NewService(contests.ServiceConfig{})
}

func TestCreateMakesTheAuthorTheOwner(t *testing.T) {
	// Somebody must be able to appoint managers from the first moment.
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

// The recourse when, after the finish, reports are not done or a dispute is
// open.
func TestExtendGraceLengthensAFinishedContestsGracePeriod(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)

	// With no explicit grace the installation default is in force, and the
	// request must clear it.
	grace := conteststest.FixtureDefaultGraceMin + 60
	updated, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, grace)
	if err != nil {
		t.Fatalf("ExtendGrace() = %v", err)
	}
	if updated.Settings.GracePeriodMin != grace {
		t.Fatalf("GracePeriodMin = %d, want %d", updated.Settings.GracePeriodMin, grace)
	}
}

// Reclaim honours archived contests too (§2.4).
func TestExtendGraceAlsoReachesAnArchivedContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusArchived)

	grace := conteststest.FixtureDefaultGraceMin + 60
	if _, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, grace); err != nil {
		t.Fatalf("ExtendGrace() = %v", err)
	}
}

// Other statuses have the ordinary settings route; this must not widen it.
func TestExtendGraceRefusesAnyStatusOtherThanFinishedOrArchived(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)

	_, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, 120)
	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("ExtendGrace() = %v, want ErrNotEditable", err)
	}
}

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

// A stored 0 means "defer to ServiceConfig.DefaultGraceMin"; the first
// explicit value must clear that default.
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

// Compared against the stored zero, 60 minutes would "extend" a 24-hour
// default down to one hour.
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
	stored, err := f.Contests.ByID(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	if stored.Settings.GracePeriodMin != 0 {
		t.Errorf("GracePeriodMin = %d, want unchanged 0", stored.Settings.GracePeriodMin)
	}
}

func TestExtendGraceRefusesAValueEqualToTheInstallationDefault(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)

	_, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, conteststest.FixtureDefaultGraceMin)
	if !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("ExtendGrace() = %v, want ErrInvalidContest", err)
	}
}

// CLAUDE.md rule 2.
func TestExtendGraceRefusesAnUnboundedValue(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)

	_, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, 1_000_000_000)
	if !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("ExtendGrace() = %v, want ErrInvalidContest", err)
	}
}

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
	// The operator response to a power cut.
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

// frozenRunningContest: EndsAt is FixtureNow+2h, so a 130-minute freeze puts
// FreezeAt ten minutes before FixtureNow.
func frozenRunningContest(t *testing.T, f *conteststest.Fixture) contests.Contest {
	t.Helper()
	c := f.SeedContest(contests.StatusRunning)
	freeze := 130
	c.LeaderboardFreezeMin = &freeze
	f.Contests.Put(c)
	return c
}

// The freeze is measured back from ends_at, so it lengthens by the same
// amount and the frozen moment stays put.
func TestUpdateExtendsEndsAtAfterTheFreezeWithoutMovingTheFreeze(t *testing.T) {
	f := conteststest.NewFixture()
	c := frozenRunningContest(t, f)
	freezeAt, _ := c.FreezeAt()
	later := c.EndsAt.Add(45 * time.Minute)

	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, EndsAt: &later,
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if updated.EndsAt == nil || !updated.EndsAt.Equal(later) {
		t.Errorf("EndsAt = %v, want %v", updated.EndsAt, later)
	}
	if updated.LeaderboardFreezeMin == nil || *updated.LeaderboardFreezeMin != 130+45 {
		t.Errorf("LeaderboardFreezeMin = %v, want %d", updated.LeaderboardFreezeMin, 130+45)
	}
	reloaded, err := f.Service.ByID(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	if got, ok := reloaded.FreezeAt(); !ok || !got.Equal(freezeAt) {
		t.Errorf("stored FreezeAt = %v, want it to stay at %v", got, freezeAt)
	}
}

// The form and the freeze are in whole minutes; a stored ends_at with seconds
// moves by whole minutes, so the frozen moment stays put to the second.
func TestUpdateExtendsEndsAtAfterTheFreezeKeepsTheStoredSeconds(t *testing.T) {
	f := conteststest.NewFixture()
	c := frozenRunningContest(t, f)
	withSeconds := c.EndsAt.Truncate(time.Minute).Add(17 * time.Second)
	c.EndsAt = &withSeconds
	f.Contests.Put(c)
	freezeAt, _ := c.FreezeAt()

	fromForm := withSeconds.Truncate(time.Minute).Add(time.Hour)
	unchanged := 130 // the form resending the freeze it was given
	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, EndsAt: &fromForm, LeaderboardFreezeMin: &unchanged,
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if want := withSeconds.Add(time.Hour); updated.EndsAt == nil || !updated.EndsAt.Equal(want) {
		t.Errorf("EndsAt = %v, want %v", updated.EndsAt, want)
	}
	if got, ok := updated.FreezeAt(); !ok || !got.Equal(freezeAt) {
		t.Errorf("FreezeAt = %v, want it to stay at %v", got, freezeAt)
	}
}

func TestUpdateAcceptsAnExtensionThatAlreadyCarriesThePairedFreeze(t *testing.T) {
	f := conteststest.NewFixture()
	c := frozenRunningContest(t, f)
	later := c.EndsAt.Add(30 * time.Minute)
	paired := 160

	updated, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, EndsAt: &later, LeaderboardFreezeMin: &paired,
	})
	if err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if updated.LeaderboardFreezeMin == nil || *updated.LeaderboardFreezeMin != paired {
		t.Errorf("LeaderboardFreezeMin = %v, want %d", updated.LeaderboardFreezeMin, paired)
	}
}

// Keeping the frozen moment would need a shorter freeze, which cannot reach
// back past a moment the table was already live for.
func TestUpdateRefusesToMoveEndsAtEarlierOnceTheFreezeIsReached(t *testing.T) {
	f := conteststest.NewFixture()
	c := frozenRunningContest(t, f)
	earlier := c.EndsAt.Add(-5 * time.Minute)

	_, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, EndsAt: &earlier,
	})
	if !errors.Is(err, contests.ErrFreezeAlreadyReached) {
		t.Errorf("Update() = %v, want ErrFreezeAlreadyReached", err)
	}
}

// Only the freeze that keeps the frozen moment in place may accompany an
// extension.
func TestUpdateRefusesAnExtensionWithAFreezeOtherThanThePairedOne(t *testing.T) {
	f := conteststest.NewFixture()
	c := frozenRunningContest(t, f)
	later := c.EndsAt.Add(30 * time.Minute)
	another := 200

	for name, cmd := range map[string]contests.UpdateCommand{
		"another freeze": {LeaderboardFreezeMin: &another},
		"no freeze":      {ClearLeaderboardFreeze: true},
	} {
		t.Run(name, func(t *testing.T) {
			cmd.ActorID, cmd.ContestID, cmd.EndsAt = uuid.New(), c.ID, &later
			_, err := f.Service.Update(context.Background(), cmd)
			if !errors.Is(err, contests.ErrNotEditable) {
				t.Errorf("Update() = %v, want ErrNotEditable", err)
			}
		})
	}
	reloaded, err := f.Service.ByID(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	if !reloaded.EndsAt.Equal(*c.EndsAt) || *reloaded.LeaderboardFreezeMin != 130 {
		t.Errorf("stored contest = ends %v, freeze %v; a refused change was written", reloaded.EndsAt, *reloaded.LeaderboardFreezeMin)
	}
}

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

// The form resends every field, truncated to the minute; an ends_at with
// seconds (set through the API) must not make every unrelated save fail.
func TestUpdateAllowsAnUnrelatedFieldWhenEndsAtIsResentUnchangedAfterTheFreeze(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)
	freeze := 130 // FreezeAt already reached, as above.
	c.LeaderboardFreezeMin = &freeze
	withSeconds := c.EndsAt.Add(17 * time.Second)
	c.EndsAt = &withSeconds
	f.Contests.Put(c)

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
	// Exact: the truncated resent value must not be what gets written.
	if updated.EndsAt == nil || !updated.EndsAt.Equal(withSeconds) {
		t.Errorf("EndsAt = %v, want the exact stored value %v unchanged", updated.EndsAt, withSeconds)
	}
}

// The form cannot express a move within a minute, so the stored value is kept
// rather than refused: saving the form after the freeze must still succeed.
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

// ICPC penalty minutes are counted from starts_at at read time, so moving it
// mid-run would rescore every fixed-timing participant.
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
	if updated.StartsAt == nil || !updated.StartsAt.Equal(withSeconds) {
		t.Errorf("StartsAt = %v, want the exact stored value %v unchanged", updated.StartsAt, withSeconds)
	}
}

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

// Nothing outside ICPC scoring is keyed off starts_at.
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

// An administrator reads the answer key. Registration refuses one, but the
// permission can be granted after registering; publication is the last
// moment before anybody is let in.
func TestPublishRefusesAContestAnAdministratorIsRegisteredFor(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedPublishableContest()
	admin := f.AddAdministrator("inspector")
	f.Registrations.Put(contests.Participant{ContestID: c.ID, UserID: admin.ID})

	err := f.Service.Transition(context.Background(), uuid.New(), c.ID, contests.StatusPublished)

	if !errors.Is(err, contests.ErrNotPublishable) {
		t.Fatalf("Transition(published) = %v, want ErrNotPublishable", err)
	}
	var notReady *contests.NotPublishableError
	if !errors.As(err, &notReady) {
		t.Fatalf("Transition(published) = %v, want a *contests.NotPublishableError", err)
	}
	// The login tells the organizer whom to take off the roster.
	want := contests.PublishProblem{Code: contests.ProblemStaffRegistered, Detail: "inspector"}
	if !slices.Contains(notReady.Problems, want) {
		t.Errorf("problems = %v, want one of %v", notReady.Problems, want)
	}
}

func TestPublishAcceptsAContestWithOrdinaryParticipants(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedPublishableContest()
	student := f.AddUser("student")
	f.Registrations.Put(contests.Participant{ContestID: c.ID, UserID: student.ID})

	if err := f.Service.Transition(context.Background(), uuid.New(), c.ID, contests.StatusPublished); err != nil {
		t.Fatalf("Transition(published) = %v", err)
	}
}

// docs/design/SPEC.md §10.1. The upload route and a column check also refuse
// this, but not for a row restored from an older dump, repaired by hand, or
// credited with whitespace only.
func TestPublishRefusesAnUploadedCoverWithNobodyCredited(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedPublishableContest()
	f.Covers.Put(c.ID, "   ")

	err := f.Service.Transition(context.Background(), uuid.New(), c.ID, contests.StatusPublished)

	if !errors.Is(err, contests.ErrNotPublishable) {
		t.Fatalf("Transition(published) = %v, want ErrNotPublishable", err)
	}
	var notReady *contests.NotPublishableError
	if !errors.As(err, &notReady) {
		t.Fatalf("Transition(published) = %v, want a *contests.NotPublishableError", err)
	}
	want := contests.PublishProblem{Code: contests.ProblemCoverNeedsAttribution}
	if !slices.Contains(notReady.Problems, want) {
		t.Errorf("problems = %v, want one of %v", notReady.Problems, want)
	}
}

func TestPublishAcceptsACreditedCover(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedPublishableContest()
	f.Covers.Put(c.ID, "Photo: A. Organiser, CC BY 4.0")

	if err := f.Service.Transition(context.Background(), uuid.New(), c.ID, contests.StatusPublished); err != nil {
		t.Fatalf("Transition(published) = %v", err)
	}
}

func TestPublishAcceptsAContestWithNoUploadedCover(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedPublishableContest()

	if err := f.Service.Transition(context.Background(), uuid.New(), c.ID, contests.StatusPublished); err != nil {
		t.Fatalf("Transition(published) = %v", err)
	}
}

func TestTransitionTriggersThePoolWhenPublishingOrStarting(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedPublishableContest()

	if err := f.Service.Transition(context.Background(), uuid.New(), c.ID, contests.StatusPublished); err != nil {
		t.Fatalf("Transition(published) = %v", err)
	}
	if len(f.PoolTrigger.Triggered) != 1 || f.PoolTrigger.Triggered[0] != c.ID {
		t.Fatalf("triggered after publishing = %v, want exactly [%s]", f.PoolTrigger.Triggered, c.ID)
	}

	if err := f.Service.Transition(context.Background(), uuid.New(), c.ID, contests.StatusRunning); err != nil {
		t.Fatalf("Transition(running) = %v", err)
	}
	if len(f.PoolTrigger.Triggered) != 2 || f.PoolTrigger.Triggered[1] != c.ID {
		t.Fatalf("triggered after starting = %v, want a second entry for %s", f.PoolTrigger.Triggered, c.ID)
	}
	if f.PoolTrigger.TriggeredWhileOpen != 0 {
		t.Fatalf("the trigger fired while Transition's own transaction was still open, want it fired after commit")
	}
}

func TestTransitionDoesNotTriggerThePoolForFinishingOrArchiving(t *testing.T) {
	f := conteststest.NewFixture()
	running := f.SeedContest(contests.StatusRunning)
	published := f.SeedPublishableContest()
	if err := f.Service.Transition(context.Background(), uuid.New(), published.ID, contests.StatusPublished); err != nil {
		t.Fatalf("Transition(published) = %v", err)
	}
	f.PoolTrigger.Triggered = nil // only the two transitions below are under test.

	if err := f.Service.Transition(context.Background(), uuid.New(), running.ID, contests.StatusFinished); err != nil {
		t.Fatalf("Transition(finished) = %v", err)
	}
	if err := f.Service.Transition(context.Background(), uuid.New(), published.ID, contests.StatusArchived); err != nil {
		t.Fatalf("Transition(archived) = %v", err)
	}
	if len(f.PoolTrigger.Triggered) != 0 {
		t.Fatalf("triggered = %v, want none for finishing or archiving", f.PoolTrigger.Triggered)
	}
}

func TestTransitionDoesNotTriggerThePoolOnRefusal(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	if err := f.Service.Transition(context.Background(), uuid.New(), c.ID, contests.StatusPublished); !errors.Is(err, contests.ErrNotPublishable) {
		t.Fatalf("Transition(published) = %v, want ErrNotPublishable", err)
	}
	if len(f.PoolTrigger.Triggered) != 0 {
		t.Fatalf("triggered = %v, want none for a refused transition", f.PoolTrigger.Triggered)
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
	// Once people could see it, it is a record: it is archived instead.
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
	// Powers would depend on when a participant connected, and the template
	// grants would no longer match the validator.
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
	// Everything in a language added mid-contest would be empty.
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
	// The client cannot send "no duration", and fixed timing refuses one, so
	// switching must drop it.
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
	// Content stays editable while published, so the gate must hold again at
	// start.
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
	// "Who moved the deadline, and what was it before" needs both halves.
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
	// The form resends everything; recording it all would bury what moved.
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
	// After an incident, the previous policy is the whole question.
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
	// One shape lets the panel render any change without knowing the action.
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
	// The trail is not a version history for authored text.
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

// Moving the freeze while running would either briefly open the live table or
// hide one participants saw.
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

// The label is about names, not the result.
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

// Matches the column default.
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

// Nil means "leave it alone"; zero is a real setting (no penalty).
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

// Earlier and later answers would disagree about what a wrong attempt cost.
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
