package profile_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/profile"
)

var (
	start = time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	end   = start.Add(3 * time.Hour)
)

// grace is the rig's DEADLINE_GRACE, the one its gate is built with.
const grace = 5 * time.Second

// store counts how often each read was made, so tests can assert one
// aggregating query.
type store struct {
	summary    profile.Summary
	enrolments []profile.Enrolment
	activity   profile.Activity
	reads      map[string]int
	lastLimit  int
}

func newStore() *store { return &store{reads: map[string]int{}} }

func (s *store) Summary(context.Context, uuid.UUID) (profile.Summary, error) {
	s.reads["summary"]++
	return s.summary, nil
}

func (s *store) Enrolments(_ context.Context, _ uuid.UUID, limit int) ([]profile.Enrolment, error) {
	s.reads["enrolments"]++
	s.lastLimit = limit
	out := s.enrolments
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *store) Activity(context.Context, uuid.UUID) (profile.Activity, error) {
	s.reads["activity"]++
	return s.activity, nil
}

type results struct {
	own  map[uuid.UUID]leaderboard.Own
	asks int
}

func (r *results) Own(_ context.Context, contestID, _ uuid.UUID) (leaderboard.Own, error) {
	r.asks++
	own, ok := r.own[contestID]
	if !ok {
		return leaderboard.Own{}, leaderboard.ErrNotAParticipant
	}
	return own, nil
}

type attempts struct {
	answers monitor.Answers
	asks    int
}

func (a *attempts) Answers(context.Context, uuid.UUID, uuid.UUID) (monitor.Answers, error) {
	a.asks++
	return a.answers, nil
}

type rig struct {
	service  *profile.Service
	store    *store
	results  *results
	attempts *attempts
	contests *conteststest.Contests
	people   *conteststest.Registrations
	user     uuid.UUID
	now      time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	f := conteststest.NewFixture()
	r := &rig{
		store: newStore(), results: &results{own: map[uuid.UUID]leaderboard.Own{}}, attempts: &attempts{},
		contests: f.Contests, people: f.Registrations, user: uuid.New(), now: end.Add(time.Hour),
	}
	r.service = profile.NewService(profile.Config{
		Store:        r.store,
		Contests:     r.contests,
		Participants: r.people,
		Results:      r.results,
		Attempts:     r.attempts,
		Now:          func() time.Time { return r.now },
		Gate:         contests.NewGate(grace),
	})
	return r
}

func (r *rig) seed(t *testing.T, status string) (contests.Contest, contests.Participant) {
	t.Helper()
	s, e := start, end
	c := r.contests.Put(contests.Contest{
		Status: status, Scoring: contests.ScoringPoints, Timing: contests.TimingFixed,
		StartsAt: &s, EndsAt: &e, LeaderboardNames: contests.LeaderboardNamesLogin,
		Languages:    []contests.ContestLanguage{{Code: "en", IsDefault: true}},
		Translations: map[string]contests.Translation{"en": {Lang: "en", Title: "The Library Murder"}},
	})
	p, err := r.people.Add(t.Context(), c.ID, r.user)
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}
