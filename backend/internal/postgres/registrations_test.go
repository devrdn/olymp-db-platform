package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

func TestParticipantCarriesTheAccountItNames(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-reg")
		student := makeUser(t, ctx, "student-reg")
		id := makeContest(t, ctx, author.ID)

		added, err := repo.Add(ctx, id, student.ID)
		if err != nil {
			t.Fatalf("Add() = %v", err)
		}
		if added.Status != contests.RegistrationRegistered {
			t.Errorf("status = %q, want registered", added.Status)
		}

		got, err := repo.ByUser(ctx, id, student.ID)
		if err != nil {
			t.Fatalf("ByUser() = %v", err)
		}
		if got.Login != "student-reg" {
			t.Errorf("login = %q, want student-reg", got.Login)
		}
	})
}

func TestRegisteringTheSamePersonTwiceIsReportedAsAlreadyEnrolled(t *testing.T) {
	// The unique index is the real guarantee against two staff adding the same
	// student at the same moment; it has to arrive as something the caller can
	// act on rather than an opaque constraint violation.
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-dup")
		student := makeUser(t, ctx, "student-dup")
		id := makeContest(t, ctx, author.ID)
		if _, err := repo.Add(ctx, id, student.ID); err != nil {
			t.Fatalf("Add() = %v", err)
		}

		_, err := repo.Add(ctx, id, student.ID)

		if !errors.Is(err, contests.ErrAlreadyEnrolled) {
			t.Errorf("Add() twice = %v, want ErrAlreadyEnrolled", err)
		}
	})
}

func TestParticipantsAreFilteredByStatus(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-filter")
		staying := makeUser(t, ctx, "student-staying")
		excluded := makeUser(t, ctx, "student-excluded")
		id := makeContest(t, ctx, author.ID)
		if _, err := repo.Add(ctx, id, staying.ID); err != nil {
			t.Fatalf("Add() = %v", err)
		}
		out, err := repo.Add(ctx, id, excluded.ID)
		if err != nil {
			t.Fatalf("Add() = %v", err)
		}
		if err := repo.SetStatus(ctx, out.ID, contests.RegistrationDisqualified); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}

		found, total, err := repo.List(ctx, id, contests.ParticipantFilter{
			Status: contests.RegistrationDisqualified, Limit: 10,
		})
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		if total != 1 || len(found) != 1 || found[0].UserID != excluded.ID {
			t.Errorf("found %d of %d, want only the disqualified participant", len(found), total)
		}
	})
}

// The ordinary case: a participant's first action records now and moves them
// to active.
func TestStartingAParticipantRecordsTheTimeAndMovesThemToActive(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-start")
		student := makeUser(t, ctx, "student-start")
		id := makeContest(t, ctx, author.ID)
		added, err := repo.Add(ctx, id, student.ID)
		if err != nil {
			t.Fatalf("Add() = %v", err)
		}

		now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
		started, err := repo.Start(ctx, added.ID, now)
		if err != nil {
			t.Fatalf("Start() = %v", err)
		}
		if started.StartedAt == nil || !started.StartedAt.Equal(now) {
			t.Errorf("StartedAt = %v, want %v", started.StartedAt, now)
		}
		if started.Status != contests.RegistrationActive {
			t.Errorf("status = %q, want %q", started.Status, contests.RegistrationActive)
		}
	})
}

// A second call must not move the clock: the guard is "started_at IS NULL",
// so the first timestamp survives every call after it — the same property
// the concurrency test below proves under real contention.
func TestStartingATwiceStartedParticipantKeepsTheFirstTime(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-restart")
		student := makeUser(t, ctx, "student-restart")
		id := makeContest(t, ctx, author.ID)
		added, err := repo.Add(ctx, id, student.ID)
		if err != nil {
			t.Fatalf("Add() = %v", err)
		}

		first := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
		if _, err := repo.Start(ctx, added.ID, first); err != nil {
			t.Fatalf("first Start() = %v", err)
		}

		later := first.Add(time.Hour)
		got, err := repo.Start(ctx, added.ID, later)
		if err != nil {
			t.Fatalf("second Start() = %v", err)
		}
		if !got.StartedAt.Equal(first) {
			t.Errorf("StartedAt after a second Start() = %v, want the first time %v, not %v", got.StartedAt, first, later)
		}
	})
}

// Starting a registration that does not exist reports ErrParticipantNotFound,
// the same as every other lookup on a bad id.
func TestStartingAnUnknownRegistrationIsReportedAsNotFound(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)

		if _, err := repo.Start(ctx, uuid.New(), time.Now()); !errors.Is(err, contests.ErrParticipantNotFound) {
			t.Errorf("Start() = %v, want ErrParticipantNotFound", err)
		}
	})
}

// The guarantee finding 1 asks for, proven under real contention rather than
// asserted from the SQL alone: two goroutines racing Start on the very same
// registration, each in its own transaction and its own connection — one
// shared transaction cannot show what happens between connections, which is
// the point of a conditional UPDATE at all. Both must read back the exact
// same start time, and the database row must carry it too: neither
// goroutine may see the other's write partially, and the second comer must
// never move the clock its rival already set.
//
// Runs outside a rolled-back transaction on purpose (see contestWithSpares'
// doc in gameinstances_test.go for why): the point is what two separate
// connections do to one row, which a single enclosing transaction would
// serialize away before the race ever had a chance to happen. The contest is
// deleted afterwards, which cascades to the registration.
func TestStartingConcurrentlyProducesOneStartTimeNotTwo(t *testing.T) {
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	ctx := t.Context()

	author := makeUser(t, ctx, "author-"+uuid.NewString()[:8])
	student := makeUser(t, ctx, "student-"+uuid.NewString()[:8])
	contestID := makeContest(t, ctx, author.ID)
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contestID)
	})
	registrationID := makeRegistration(t, ctx, contestID, student.ID)

	repo := NewRegistrations(testPool)
	const racers = 20

	type outcome struct {
		participant contests.Participant
		err         error
	}
	results := make(chan outcome, racers)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	for i := range racers {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait() // all of them go at once
			now := time.Date(2026, 3, 1, 10, 0, i, 0, time.UTC)
			p, err := repo.Start(context.Background(), registrationID, now)
			results <- outcome{p, err}
		}(i)
	}
	start.Done()
	done.Wait()
	close(results)

	seen := map[time.Time]int{}
	for r := range results {
		if r.err != nil {
			t.Fatalf("Start() = %v", r.err)
		}
		if r.participant.StartedAt == nil {
			t.Fatal("Start() returned a participant with no StartedAt")
		}
		seen[r.participant.StartedAt.UTC()]++
	}

	if len(seen) != 1 {
		t.Fatalf("the %d racers saw %d distinct start times, want exactly 1: %v", racers, len(seen), seen)
	}

	// The row itself agrees with every goroutine's own read: nobody's write
	// silently lost to a later one they never saw.
	final, err := repo.ByUser(context.Background(), contestID, student.ID)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if final.Status != contests.RegistrationActive {
		t.Errorf("status = %q, want %q", final.Status, contests.RegistrationActive)
	}
	for started := range seen {
		if !final.StartedAt.Equal(started) {
			t.Errorf("the stored StartedAt %v does not match what every racer read back %v", final.StartedAt, started)
		}
	}
}

func TestRemovingAParticipantTakesTheRegistrationAway(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-remove")
		student := makeUser(t, ctx, "student-remove")
		id := makeContest(t, ctx, author.ID)
		if _, err := repo.Add(ctx, id, student.ID); err != nil {
			t.Fatalf("Add() = %v", err)
		}

		if err := repo.Remove(ctx, id, student.ID); err != nil {
			t.Fatalf("Remove() = %v", err)
		}

		if _, err := repo.ByUser(ctx, id, student.ID); !errors.Is(err, contests.ErrParticipantNotFound) {
			t.Errorf("ByUser() = %v, want ErrParticipantNotFound", err)
		}
	})
}
