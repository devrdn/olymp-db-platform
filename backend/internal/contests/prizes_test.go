package contests_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
)

func TestPrizeTiersFollowThePlaceDownTheCounts(t *testing.T) {
	places := contests.PrizePlaces{First: 1, Second: 2, Third: 3}
	want := map[int]int{1: 1, 2: 2, 3: 2, 4: 3, 5: 3, 6: 3, 7: 0, 100: 0}

	for place, tier := range want {
		if got := places.Tier(place); got != tier {
			t.Errorf("Tier(%d) = %d, want %d", place, got, tier)
		}
	}
}

// A place shared at a boundary is one place, so every row on it takes the
// better tier: with one first prize, two rows tied at place 1 both get I and
// the next row, at place 3, gets what place 3 earns.
func TestATieStraddlingABoundaryTakesTheBetterTier(t *testing.T) {
	places := contests.PrizePlaces{First: 1, Second: 1, Third: 1}

	for _, tc := range []struct{ place, tier int }{
		{place: 1, tier: 1},
		{place: 2, tier: 2},
		{place: 3, tier: 3},
		{place: 4, tier: 0},
	} {
		if got := places.Tier(tc.place); got != tc.tier {
			t.Errorf("Tier(%d) = %d, want %d", tc.place, got, tc.tier)
		}
	}
}

func TestNoPrizePlacesGiveNobodyATier(t *testing.T) {
	var places contests.PrizePlaces

	for _, place := range []int{0, 1, 2, 3, 1000} {
		if got := places.Tier(place); got != 0 {
			t.Errorf("Tier(%d) = %d, want 0 with no prize places", place, got)
		}
	}
}

// An unplaced row (winner mode, everybody but the winner) is never a prize.
func TestAnUnplacedRowHasNoTier(t *testing.T) {
	places := contests.PrizePlaces{First: 3, Second: 3, Third: 3}

	if got := places.Tier(0); got != 0 {
		t.Errorf("Tier(0) = %d, want 0", got)
	}
}

// A tier with no places is skipped rather than ending the list.
func TestAnEmptyTierIsSkipped(t *testing.T) {
	places := contests.PrizePlaces{First: 0, Second: 2, Third: 1}

	for _, tc := range []struct{ place, tier int }{{1, 2}, {2, 2}, {3, 3}, {4, 0}} {
		if got := places.Tier(tc.place); got != tc.tier {
			t.Errorf("Tier(%d) = %d, want %d", tc.place, got, tc.tier)
		}
	}
}

func TestPrizePlacesAreBoundedAtAThousandEach(t *testing.T) {
	for _, places := range []contests.PrizePlaces{
		{First: -1}, {Second: -1}, {Third: -1},
		{First: 1001}, {Second: 1001}, {Third: 1001},
	} {
		if err := places.Validate(); !errors.Is(err, contests.ErrInvalidContest) {
			t.Errorf("Validate(%+v) = %v, want ErrInvalidContest", places, err)
		}
	}
	if err := (contests.PrizePlaces{First: 1000, Second: 1000, Third: 1000}).Validate(); err != nil {
		t.Errorf("Validate(1000/1000/1000) = %v, want nil", err)
	}
}

func TestANewContestHasNoPrizePlaces(t *testing.T) {
	f := conteststest.NewFixture()

	created, err := f.Service.Create(context.Background(), contests.CreateCommand{ActorID: uuid.New()})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if created.PrizePlaces != (contests.PrizePlaces{}) {
		t.Errorf("PrizePlaces = %+v, want none", created.PrizePlaces)
	}
}

// They change only what the table shows, so they stay open while the contest
// runs and after it finishes.
func TestPrizePlacesChangeAtEveryStatusButArchived(t *testing.T) {
	for _, status := range []string{
		contests.StatusDraft, contests.StatusPublished, contests.StatusRunning, contests.StatusFinished,
	} {
		t.Run(status, func(t *testing.T) {
			f := conteststest.NewFixture()
			c := f.SeedContest(status)
			want := contests.PrizePlaces{First: 1, Second: 2, Third: 3}

			updated, err := f.Service.SetPrizePlaces(context.Background(), uuid.New(), c.ID, want)
			if err != nil {
				t.Fatalf("SetPrizePlaces() = %v", err)
			}
			if updated.PrizePlaces != want {
				t.Errorf("returned PrizePlaces = %+v, want %+v", updated.PrizePlaces, want)
			}
			stored, _ := f.Contests.ByID(context.Background(), c.ID)
			if stored.PrizePlaces != want {
				t.Errorf("stored PrizePlaces = %+v, want %+v", stored.PrizePlaces, want)
			}
		})
	}
}

func TestPrizePlacesAreRefusedOnceArchived(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusArchived)

	_, err := f.Service.SetPrizePlaces(context.Background(), uuid.New(), c.ID, contests.PrizePlaces{First: 1})
	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("SetPrizePlaces() = %v, want ErrNotEditable", err)
	}
	stored, _ := f.Contests.ByID(context.Background(), c.ID)
	if stored.PrizePlaces != (contests.PrizePlaces{}) {
		t.Errorf("stored PrizePlaces = %+v, want them untouched", stored.PrizePlaces)
	}
}

// The narrow door opens nothing else: a finished contest's other settings stay
// as they were.
func TestSettingPrizePlacesOnAFinishedContestChangesNothingElse(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)
	before, _ := f.Contests.ByID(context.Background(), c.ID)

	if _, err := f.Service.SetPrizePlaces(context.Background(), uuid.New(), c.ID, contests.PrizePlaces{Third: 4}); err != nil {
		t.Fatalf("SetPrizePlaces() = %v", err)
	}

	// UpdatedAt included: the reclaim sweep measures the grace from it.
	after, _ := f.Contests.ByID(context.Background(), c.ID)
	after.PrizePlaces = before.PrizePlaces
	if fmt.Sprintf("%+v", after) != fmt.Sprintf("%+v", before) {
		t.Errorf("contest = %+v\nwant only the prize places changed from %+v", after, before)
	}
}

// The general update stays frozen once the contest ends; prize places do not
// reopen it.
func TestTheGeneralUpdateStaysRefusedOnceFinished(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)

	_, err := f.Service.Update(context.Background(), contests.UpdateCommand{
		ActorID: uuid.New(), ContestID: c.ID, Enrollment: contests.EnrollmentOpen,
	})
	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("Update() = %v, want ErrNotEditable", err)
	}
}

func TestPrizePlacesOutOfBoundsAreRefused(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusRunning)

	_, err := f.Service.SetPrizePlaces(context.Background(), uuid.New(), c.ID, contests.PrizePlaces{Second: 1001})
	if !errors.Is(err, contests.ErrInvalidContest) {
		t.Errorf("SetPrizePlaces() = %v, want ErrInvalidContest", err)
	}
}

func TestSettingPrizePlacesOfAContestThatIsNotThereIsNotFound(t *testing.T) {
	f := conteststest.NewFixture()

	_, err := f.Service.SetPrizePlaces(context.Background(), uuid.New(), uuid.New(), contests.PrizePlaces{First: 1})
	if !errors.Is(err, contests.ErrNotFound) {
		t.Errorf("SetPrizePlaces() = %v, want ErrNotFound", err)
	}
}

func TestAPrizePlacesChangeIsAuditedAsAContestUpdate(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusFinished)

	if _, err := f.Service.SetPrizePlaces(context.Background(), uuid.New(), c.ID,
		contests.PrizePlaces{First: 1, Second: 0, Third: 2}); err != nil {
		t.Fatalf("SetPrizePlaces() = %v", err)
	}

	changes := changesIn(t, f, audit.ActionContestUpdate)
	for field, to := range map[string]int{"prize_first": 1, "prize_third": 2} {
		change, ok := changes[field].(map[string]any)
		if !ok || change["from"] != 0 || change["to"] != to {
			t.Errorf("%s change = %v, want 0 → %d", field, changes[field], to)
		}
	}
	if _, moved := changes["prize_second"]; moved {
		t.Errorf("changes = %v, want prize_second left out since it did not move", changes)
	}
}
