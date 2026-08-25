package contests_test

import (
	"context"
	"errors"
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
