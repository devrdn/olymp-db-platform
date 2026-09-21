package profile_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/profile"
)

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

// Every refusal is the same one: a contest that does not exist, one somebody
// else is in, and one that has not ended for this participant. The profile
// never says which (design §2.2).
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

// The three ways a contest ends for one participant (design §3): the contest
// itself, their own deadline under an individual timer, and their
// registration being finished or disqualified.
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

// An individual participant who never started is not "finished" while the
// contest runs: there is no deadline to have passed (contests.Deadline).
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
