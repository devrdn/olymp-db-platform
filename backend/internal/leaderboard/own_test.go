package leaderboard_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
)

func ownEntries(caller uuid.UUID) []leaderboard.Entry {
	scored := start.Add(time.Hour)
	return []leaderboard.Entry{
		{Registration: uuid.New(), Login: "ahead", Points: 30, Solved: 3, LastScoredAt: &scored},
		{Registration: caller, Login: "caller", Points: 20, Solved: 2, LastScoredAt: &scored},
		{Registration: uuid.New(), Login: "behind", Points: 10, Solved: 1, LastScoredAt: &scored},
		{Registration: uuid.New(), Login: "excluded", Points: 40, Solved: 4, LastScoredAt: &scored, Disqualified: true},
	}
}

func TestOwnCarriesThePlaceOfAnOpenTable(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusFinished, nil)
	caller := uuid.New()
	r.standings.entries = ownEntries(caller)

	own, err := r.service.Own(context.Background(), c.ID, caller)
	if err != nil {
		t.Fatalf("Own() = %v", err)
	}
	if !own.Open || own.State != leaderboard.StateFinal {
		t.Fatalf("Own() = %+v, want an open final table", own)
	}
	if own.Place != 2 || own.Participants != 3 {
		t.Errorf("place %d of %d, want 2 of 3", own.Place, own.Participants)
	}
	if own.Row.Points != 20 || own.Row.Solved != 2 {
		t.Errorf("row = %+v, want the caller's own 20 points and 2 solved", own.Row)
	}
}

func TestOwnHidesThePlaceWhileTheTableIsFrozen(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusFinished, minutes(30))
	caller := uuid.New()
	r.standings.entries = ownEntries(caller)

	own, err := r.service.Own(context.Background(), c.ID, caller)
	if err != nil {
		t.Fatalf("Own() = %v", err)
	}
	if own.Open || own.State != leaderboard.StateFrozen {
		t.Fatalf("Own() = %+v, want a frozen table that is not open", own)
	}
	if own.Place != 0 || own.Participants != 0 {
		t.Errorf("place %d of %d, want neither", own.Place, own.Participants)
	}
	if own.Row.Points != 20 || own.Row.Solved != 2 {
		t.Errorf("row = %+v, want the caller's own numbers regardless of the freeze", own.Row)
	}
	last := r.standings.queries[len(r.standings.queries)-1]
	if !last.Cutoff.Equal(r.now) {
		t.Errorf("cutoff %v, want now (%v)", last.Cutoff, r.now)
	}
}

func TestOwnAnswersADisqualifiedParticipantWithTheirOwnNumbers(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusFinished, nil)
	caller := uuid.New()
	entries := ownEntries(caller)
	entries[1].Disqualified = true
	r.standings.entries = entries

	own, err := r.service.Own(context.Background(), c.ID, caller)
	if err != nil {
		t.Fatalf("Own() = %v", err)
	}
	if own.Open || own.Place != 0 || own.Row.Points != 20 {
		t.Errorf("Own() = %+v, want the caller's own numbers and no place", own)
	}
}

func TestOwnRefusesSomebodyWhoIsNotOnTheContest(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusFinished, nil)
	r.standings.entries = ownEntries(uuid.New())

	if _, err := r.service.Own(context.Background(), c.ID, uuid.New()); !errors.Is(err, leaderboard.ErrNotAParticipant) {
		t.Errorf("Own() = %v, want ErrNotAParticipant", err)
	}
	if _, err := r.service.Own(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, leaderboard.ErrNotFound) {
		t.Errorf("Own() of a contest that does not exist = %v, want ErrNotFound", err)
	}
}

func TestOwnReadsTheSameTableTheContestServes(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusFinished, nil)
	caller := uuid.New()
	r.standings.entries = ownEntries(caller)

	view, err := r.service.Public(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("Public() = %v", err)
	}
	reads := r.standings.callCount()
	own, err := r.service.Own(context.Background(), c.ID, caller)
	if err != nil {
		t.Fatalf("Own() = %v", err)
	}
	if got := r.standings.callCount(); got != reads {
		t.Errorf("the standings were read %d more times; the cached table should serve this", got-reads)
	}
	for _, row := range view.Rows {
		if row.Registration != caller {
			continue
		}
		if row.Place != own.Place || row.Points != own.Row.Points || row.Solved != own.Row.Solved {
			t.Errorf("the table says %+v and the report says %+v", row, own.Row)
		}
		return
	}
	t.Fatal("the caller is not on the table at all")
}

func TestOwnInWinnerModeReportsOnlyTheWinnersPlace(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusFinished, nil)
	c.Scoring = contests.ScoringWinner
	r.contests.Put(c)
	winner, loser := uuid.New(), uuid.New()
	final := start.Add(30 * time.Minute)
	scored := start.Add(time.Hour)
	r.standings.entries = []leaderboard.Entry{
		{Registration: loser, Login: "loser", Points: 30, Solved: 3, LastScoredAt: &scored},
		{Registration: winner, Login: "winner", Points: 10, Solved: 1, LastScoredAt: &scored, FinalAt: &final},
	}

	won, err := r.service.Own(context.Background(), c.ID, winner)
	if err != nil {
		t.Fatalf("Own() = %v", err)
	}
	if !won.Open || won.Place != 1 || !won.Row.Winner {
		t.Errorf("the winner reads %+v, want first place and the winner's mark", won)
	}

	lost, err := r.service.Own(context.Background(), c.ID, loser)
	if err != nil {
		t.Fatalf("Own() = %v", err)
	}
	if lost.Place != 0 || lost.Row.Winner {
		t.Errorf("a non-winner reads %+v, want no place and no mark", lost)
	}
	if lost.Row.Points != 30 {
		t.Errorf("a non-winner reads %+v, want their own numbers all the same", lost.Row)
	}
}

func TestOwnInWinnerModeSaysNothingWhileTheTableIsFrozen(t *testing.T) {
	r := newRig(t)
	c := r.seed(contests.StatusFinished, minutes(30))
	c.Scoring = contests.ScoringWinner
	r.contests.Put(c)
	winner := uuid.New()
	final := start.Add(30 * time.Minute)
	r.standings.entries = []leaderboard.Entry{
		{Registration: winner, Login: "winner", Points: 10, Solved: 1, FinalAt: &final},
	}

	own, err := r.service.Own(context.Background(), c.ID, winner)
	if err != nil {
		t.Fatalf("Own() = %v", err)
	}
	if own.Open || own.Place != 0 || own.Row.Winner {
		t.Errorf("Own() = %+v, want no place and no winner's mark while frozen", own)
	}
	if own.Row.Points != 10 {
		t.Errorf("Own() = %+v, want the caller's own numbers", own.Row)
	}
}
