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

	updated, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, 120)
	if err != nil {
		t.Fatalf("ExtendGrace() = %v", err)
	}
	if updated.Settings.GracePeriodMin != 120 {
		t.Fatalf("GracePeriodMin = %d, want 120", updated.Settings.GracePeriodMin)
	}
}

// Archived is the other status Reclaim now honours (§2.4), so the same
// recourse has to still reach a contest an organizer already archived.
func TestExtendGraceAlsoReachesAnArchivedContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusArchived)

	if _, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, 60); err != nil {
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

	if _, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, 500); err != nil {
		t.Fatalf("setup ExtendGrace() = %v", err)
	}

	_, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, 100)
	if !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("ExtendGrace() = %v, want ErrInvalidContest", err)
	}
}

// A contest that never configured an explicit grace reads as 0 — "defer to
// the installation default" — and this package has no view of that default
// to compare against, so the first explicit value is accepted outright.
func TestExtendGraceAcceptsTheFirstExplicitValueWhenNoneWasConfigured(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)
	if c.Settings.GracePeriodMin != 0 {
		t.Fatalf("setup: GracePeriodMin = %d, want 0", c.Settings.GracePeriodMin)
	}

	updated, err := f.Service.ExtendGrace(context.Background(), uuid.New(), c.ID, 30)
	if err != nil {
		t.Fatalf("ExtendGrace() = %v", err)
	}
	if updated.Settings.GracePeriodMin != 30 {
		t.Fatalf("GracePeriodMin = %d, want 30", updated.Settings.GracePeriodMin)
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

	if _, err := f.Service.ExtendGrace(context.Background(), actor, c.ID, 180); err != nil {
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
