package profile_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/profile"
)

func TestNewServiceRefusesToAssembleWithoutAGate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewService without a Gate did not panic")
		}
	}()
	profile.NewService(profile.Config{})
}

func TestOpenAdmitsAContestThatHasEndedForTheCaller(t *testing.T) {
	r := newRig(t)
	c, p := r.seed(t, contests.StatusFinished)

	access, err := r.service.Open(t.Context(), c.ID, r.user)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	if access.Contest.ID != c.ID || access.Participant.ID != p.ID {
		t.Errorf("Open() resolved %+v", access)
	}
}

func TestOpenRefusesEverythingThatIsNotTheCallersFinishedContest(t *testing.T) {
	cases := map[string]func(t *testing.T, r *rig) (uuid.UUID, uuid.UUID){
		"a contest that does not exist": func(_ *testing.T, r *rig) (uuid.UUID, uuid.UUID) {
			return uuid.New(), r.user
		},
		"a contest the caller is not in": func(t *testing.T, r *rig) (uuid.UUID, uuid.UUID) {
			c, _ := r.seed(t, contests.StatusFinished)
			return c.ID, uuid.New()
		},
		"a contest still running for the caller": func(t *testing.T, r *rig) (uuid.UUID, uuid.UUID) {
			r.now = start.Add(time.Hour)
			c, _ := r.seed(t, contests.StatusRunning)
			return c.ID, r.user
		},
		"a contest that has not started": func(t *testing.T, r *rig) (uuid.UUID, uuid.UUID) {
			r.now = start.Add(-time.Hour)
			c, _ := r.seed(t, contests.StatusPublished)
			return c.ID, r.user
		},
		"a draft": func(t *testing.T, r *rig) (uuid.UUID, uuid.UUID) {
			r.now = start.Add(-time.Hour)
			c, _ := r.seed(t, contests.StatusDraft)
			return c.ID, r.user
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			contestID, userID := arrange(t, r)
			if _, err := r.service.Open(t.Context(), contestID, userID); !errors.Is(err, profile.ErrNotFound) {
				t.Errorf("Open() = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestOpenAdmitsOnEveryWayAContestEndsForOneParticipant(t *testing.T) {
	t.Run("the individual deadline has passed while the contest runs", func(t *testing.T) {
		r := newRig(t)
		r.now = start.Add(90 * time.Minute)
		c, p := r.seed(t, contests.StatusRunning)
		duration := 30
		c.Timing, c.DurationMin = contests.TimingIndividual, &duration
		r.contests.Put(c)
		started := start.Add(10 * time.Minute)
		p.StartedAt = &started
		r.people.Put(p)

		if _, err := r.service.Open(t.Context(), c.ID, r.user); err != nil {
			t.Errorf("Open() = %v, want the report of a participant whose own hour is up", err)
		}
	})

	for _, status := range []string{contests.RegistrationFinished, contests.RegistrationDisqualified} {
		t.Run("the registration is "+status, func(t *testing.T) {
			r := newRig(t)
			r.now = start.Add(time.Hour)
			c, p := r.seed(t, contests.StatusRunning)
			p.Status = status
			r.people.Put(p)

			if _, err := r.service.Open(t.Context(), c.ID, r.user); err != nil {
				t.Errorf("Open() = %v, want the report", err)
			}
		})
	}
}

func TestOpenRefusesAnIndividualParticipantWhoNeverStarted(t *testing.T) {
	r := newRig(t)
	r.now = start.Add(time.Hour)
	c, _ := r.seed(t, contests.StatusRunning)
	duration := 30
	c.Timing, c.DurationMin = contests.TimingIndividual, &duration
	r.contests.Put(c)

	if _, err := r.service.Open(t.Context(), c.ID, r.user); !errors.Is(err, profile.ErrNotFound) {
		t.Errorf("Open() = %v, want ErrNotFound", err)
	}
}

func TestSummaryIsOneRead(t *testing.T) {
	r := newRig(t)
	r.store.summary = profile.Summary{Contests: 4, Finished: 3, Queries: 120, Solved: 9}

	got, err := r.service.Summary(t.Context(), r.user)
	if err != nil {
		t.Fatalf("Summary() = %v", err)
	}
	if got != r.store.summary {
		t.Errorf("Summary() = %+v, want %+v", got, r.store.summary)
	}
	if r.store.reads["summary"] != 1 {
		t.Errorf("the store was read %d times, want one", r.store.reads["summary"])
	}
}

func TestOpenWaitsForTheGraceAfterTheDeadline(t *testing.T) {
	for name, given := range map[string]struct {
		now  time.Time
		want error
	}{
		"at exactly the deadline":          {now: end, want: profile.ErrNotFound},
		"an instant before deadline+grace": {now: end.Add(grace - time.Nanosecond), want: profile.ErrNotFound},
		"at deadline+grace":                {now: end.Add(grace), want: nil},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.now = given.now
			c, _ := r.seed(t, contests.StatusRunning)

			if _, err := r.service.Open(t.Context(), c.ID, r.user); !errors.Is(err, given.want) {
				t.Errorf("Open() = %v, want %v", err, given.want)
			}
		})
	}
}

func TestOpenAdmitsAnIndividualParticipantWhoNeverStartedOnceTheWindowCloses(t *testing.T) {
	r := newRig(t)
	r.now = end
	c, _ := r.seed(t, contests.StatusRunning)
	duration := 30
	c.Timing, c.DurationMin = contests.TimingIndividual, &duration
	r.contests.Put(c)

	if _, err := r.service.Open(t.Context(), c.ID, r.user); err != nil {
		t.Errorf("Open() = %v, want the report: too late to start is over", err)
	}
}

func TestOpenRefusesAContestThatNeverRanHoweverLate(t *testing.T) {
	for _, status := range []string{contests.StatusPublished, "paused"} {
		t.Run(status, func(t *testing.T) {
			r := newRig(t)
			r.now = end.Add(time.Hour)
			c, _ := r.seed(t, status)

			if _, err := r.service.Open(t.Context(), c.ID, r.user); !errors.Is(err, profile.ErrNotFound) {
				t.Errorf("Open() = %v, want ErrNotFound", err)
			}
		})
	}
}
