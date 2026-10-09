package contests_test

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

func TestStoryIsStoredInEveryLanguageItWasAuthoredIn(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	_, err := f.Service.SetStory(context.Background(), uuid.New(), c.ID, map[string]string{
		"en": "A body in the stacks.",
		"ro": "Un cadavru între rafturi.",
	})
	if err != nil {
		t.Fatalf("SetStory() = %v", err)
	}

	story, err := f.Service.Story(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("Story() = %v", err)
	}
	if body, ok := story.Body("ro"); !ok || body == "" {
		t.Errorf("Body(ro) = %q, %v; want the Romanian text", body, ok)
	}
}

func TestStoryRefusesALanguageTheInstallationDoesNotOffer(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	_, err := f.Service.SetStory(context.Background(), uuid.New(), c.ID, map[string]string{
		"rus": "Тело между полками.",
	})

	if !errors.Is(err, contests.ErrUnknownLanguage) {
		t.Errorf("SetStory() = %v, want ErrUnknownLanguage", err)
	}
}

func TestStoryCannotChangeOnceTheContestIsRunning(t *testing.T) {
	// Participants are reading it; changing it changes their task mid-contest.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)

	_, err := f.Service.SetStory(context.Background(), uuid.New(), c.ID, map[string]string{
		"en": "A different body entirely.",
	})

	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("SetStory() = %v, want ErrNotEditable", err)
	}
}

func TestChangingTheStoryIsAudited(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	if _, err := f.Service.SetStory(context.Background(), uuid.New(), c.ID,
		map[string]string{"en": "A body in the stacks."}); err != nil {
		t.Fatalf("SetStory() = %v", err)
	}

	if !f.Audit.Recorded(audit.ActionContestStoryChange) {
		t.Errorf("audit entries = %v, want a %s", f.Audit.Actions(), audit.ActionContestStoryChange)
	}
}

func TestAContestWithoutAStoryReportsThatPlainly(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	if _, err := f.Service.Story(context.Background(), c.ID); !errors.Is(err, contests.ErrStoryNotFound) {
		t.Errorf("Story() = %v, want ErrStoryNotFound", err)
	}
}
