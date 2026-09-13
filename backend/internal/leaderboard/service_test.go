package leaderboard_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
)

// standings is the storage double: it answers every query with the same
// entries and remembers what it was asked.
type standings struct {
	contests *conteststest.Contests
	entries  []leaderboard.Entry
	queries  []leaderboard.Query
}

func (s *standings) Standings(_ context.Context, q leaderboard.Query) ([]leaderboard.Entry, error) {
	s.queries = append(s.queries, q)
	out := s.entries
	if !q.IncludeDisqualified {
		out = nil
		for _, e := range s.entries {
			if !e.Disqualified {
				out = append(out, e)
			}
		}
	}
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (s *standings) MarkRevealed(ctx context.Context, contestID uuid.UUID, at time.Time) (time.Time, bool, error) {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return time.Time{}, false, err
	}
	if c.LeaderboardRevealedAt != nil {
		return *c.LeaderboardRevealedAt, false, nil
	}
	c.LeaderboardRevealedAt = &at
	s.contests.Put(c)
	return at, true, nil
}

type rig struct {
	service   *leaderboard.Service
	contests  *conteststest.Contests
	people    *conteststest.Registrations
	standings *standings
	sink      *conteststest.Sink
	now       time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	f := conteststest.NewFixture()
	r := &rig{contests: f.Contests, people: f.Registrations, sink: conteststest.NewSink(), now: start.Add(time.Hour)}
	r.standings = &standings{contests: f.Contests}
	r.service = leaderboard.NewService(leaderboard.Config{
		Contests:     f.Contests,
		Participants: f.Registrations,
		Standings:    r.standings,
		Audit:        audit.New(r.sink),
		UnitOfWork:   f.UnitOfWork,
		Now:          func() time.Time { return r.now },
		CacheTTL:     10 * time.Second,
		MaxRows:      2000,
	})
	return r
}

func (r *rig) seed(status string, freezeMin *int) contests.Contest {
	return r.contests.Put(contest(status, freezeMin, nil))
}

func TestAFrozenTableIsCutOffAtTheFreezeAndLeavesTheDisqualifiedOut(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, minutes(30))
	r.now = end.Add(-10 * time.Minute)

	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("Public() = %v", err)
	}
	if view.State != leaderboard.StateFrozen {
		t.Errorf("State = %s, want frozen", view.State)
	}
	q := r.standings.queries[0]
	if want := end.Add(-30 * time.Minute); !q.Cutoff.Equal(want) || q.IncludeDisqualified {
		t.Errorf("query = %+v, want cutoff %v without the disqualified", q, want)
	}
}

func TestATableThatHasNotStartedReadsNothing(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusPublished, nil)

	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil || view.State != leaderboard.StateNotStarted || len(view.Rows) != 0 {
		t.Fatalf("Public() = %+v, %v, want an empty not_started table", view, err)
	}
	if len(r.standings.queries) != 0 {
		t.Errorf("standings were read %d times, want none", len(r.standings.queries))
	}
}

func TestADraftAndAMissingContestAreTheSameNotFound(t *testing.T) {
	r := newRig(t)
	draft := r.seed(contests.StatusDraft, nil)

	for _, id := range []uuid.UUID{draft.ID, uuid.New()} {
		if _, err := r.service.Public(context.Background(), id); !errors.Is(err, leaderboard.ErrNotFound) {
			t.Errorf("Public(%s) = %v, want ErrNotFound", id, err)
		}
	}
}

// However many people watch, the database computes a contest's table once
// per TTL.
func TestOneComputationServesEveryViewerWithinTheTTL(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, nil)

	for range 3 {
		if _, err := r.service.Public(context.Background(), c.ID); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.standings.queries) != 1 {
		t.Fatalf("standings were read %d times within the TTL, want 1", len(r.standings.queries))
	}

	r.now = r.now.Add(10 * time.Second)
	if _, err := r.service.Public(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.standings.queries) != 2 {
		t.Errorf("standings were read %d times after the TTL, want 2", len(r.standings.queries))
	}
}

// A live table cached a second before the freeze must not be served after it,
// or the table would still read "live" into the freeze.
func TestALiveTableIsNotServedPastTheFreeze(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, minutes(30))
	freezeAt := end.Add(-30 * time.Minute)

	r.now = freezeAt.Add(-time.Second)
	if _, err := r.service.Public(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	r.now = freezeAt
	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != leaderboard.StateFrozen || len(r.standings.queries) != 2 {
		t.Errorf("State = %s after %d reads, want frozen after 2", view.State, len(r.standings.queries))
	}
}

func TestAParticipantSeesTheSharedTableAndWhichRowIsTheirs(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, nil)
	user := uuid.New()
	me, err := r.people.Add(context.Background(), c.ID, user)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.service.Public(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	view, own, err := r.service.ForParticipant(context.Background(), c.ID, user)
	if err != nil {
		t.Fatalf("ForParticipant() = %v", err)
	}
	if own != me.ID || view.State != leaderboard.StateLive {
		t.Errorf("own = %s, state = %s; want %s, live", own, view.State, me.ID)
	}
	if len(r.standings.queries) != 1 {
		t.Errorf("standings were read %d times, want the participant to share the public computation", len(r.standings.queries))
	}

	if _, _, err := r.service.ForParticipant(context.Background(), c.ID, uuid.New()); !errors.Is(err, leaderboard.ErrNotAParticipant) {
		t.Errorf("ForParticipant(stranger) = %v, want ErrNotAParticipant", err)
	}
}

// The staff table is the one that ignores the freeze, and it says what
// everybody else is being shown.
func TestTheStaffTableIsLiveAndSaysWhatOthersSee(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusRunning, minutes(30))
	r.now = end.Add(-10 * time.Minute)

	view, err := r.service.Live(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("Live() = %v", err)
	}
	q := r.standings.queries[0]
	if !q.Cutoff.Equal(r.now) || !q.IncludeDisqualified {
		t.Errorf("query = %+v, want cutoff now including the disqualified", q)
	}
	if view.Shown.State != leaderboard.StateFrozen {
		t.Errorf("Shown.State = %s, want frozen", view.Shown.State)
	}
}

func TestRevealRefusesWhatHasNothingToReveal(t *testing.T) {
	r := newRig(t)
	running := r.seed(contests.StatusRunning, minutes(30))
	unfrozen := r.seed(contests.StatusFinished, nil)

	for _, id := range []uuid.UUID{running.ID, unfrozen.ID} {
		if _, err := r.service.Reveal(context.Background(), uuid.New(), id); !errors.Is(err, leaderboard.ErrNotRevealable) {
			t.Errorf("Reveal(%s) = %v, want ErrNotRevealable", id, err)
		}
	}
	if len(r.sink.Entries) != 0 {
		t.Errorf("audit entries = %d, want none for a refusal", len(r.sink.Entries))
	}
}

func TestRevealOpensTheFinalTableAndIsRecordedOnce(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusFinished, minutes(30))
	r.now = end.Add(time.Hour)

	// Warm the cache with the frozen table, which the reveal must replace.
	if view, _ := r.service.Public(context.Background(), c.ID); view.State != leaderboard.StateFrozen {
		t.Fatalf("setup: State = %s, want frozen", view.State)
	}

	first, err := r.service.Reveal(context.Background(), uuid.New(), c.ID)
	if err != nil {
		t.Fatalf("Reveal() = %v", err)
	}
	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil || view.State != leaderboard.StateFinal {
		t.Fatalf("after reveal Public() = %s, %v, want final", view.State, err)
	}

	r.now = r.now.Add(time.Minute)
	second, err := r.service.Reveal(context.Background(), uuid.New(), c.ID)
	if err != nil || !second.Equal(first) {
		t.Errorf("second Reveal() = %v, %v, want the first moment %v", second, err, first)
	}
	if len(r.sink.Entries) != 1 || r.sink.Entries[0].Action != audit.ActionContestLeaderboardReveal {
		t.Fatalf("audit entries = %+v, want exactly one reveal", r.sink.Entries)
	}
	if len(r.sink.Loose) != 0 {
		t.Errorf("the reveal was recorded outside its transaction")
	}
}

func TestATableLongerThanTheBoundSaysItWasCut(t *testing.T) {
	r := newRig(t)
	r.service = leaderboard.NewService(leaderboard.Config{
		Contests: r.contests, Participants: r.people, Standings: r.standings,
		Audit: audit.New(r.sink), UnitOfWork: &conteststest.UnitOfWork{},
		Now: func() time.Time { return r.now }, CacheTTL: time.Second, MaxRows: 2,
	})
	c := r.seed(contests.StatusRunning, nil)
	r.standings.entries = []leaderboard.Entry{entry("a", 3, at(1)), entry("b", 2, at(2)), entry("c", 1, at(3))}

	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Truncated || len(view.Rows) != 2 {
		t.Errorf("Truncated = %v with %d rows, want true with 2", view.Truncated, len(view.Rows))
	}
}
