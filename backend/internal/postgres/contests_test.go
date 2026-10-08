package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// What a single caller can observe of the contests is the contract every
// contests.Repository answers to, the in-memory one the service tests use
// included (conteststest.ContestRepositoryContract). What the contract leaves
// to this file, for the contests themselves, is what only the real database
// can be asked: the bounds its columns keep when the domain check is not in
// front of them, the default a column fills in, the cascade into the rows that
// hang off a contest, the cover a listing carries, and the locks, which are
// between sessions. The rest of the file is about other interfaces: the
// registrations lookup (Registrations.EnrolledIn) and the scheduler's
// (contests.ScheduleRepository), which has a contract of its own.
func TestContestsHonoursTheRepositoryContract(t *testing.T) {
	conteststest.ContestRepositoryContract(t, func(t *testing.T, run func(context.Context, conteststest.ContestTarget)) {
		withTx(t, func(ctx context.Context) {
			var (
				accounts      = 0
				managers      = NewContestManagers(testPool)
				registrations = NewRegistrations(testPool)
			)
			now := txNow(t, ctx)
			run(ctx, conteststest.ContestTarget{
				Repo: NewContests(testPool),
				NewUser: func() uuid.UUID {
					accounts++
					return makeUser(t, ctx, fmt.Sprintf("contest-contract-%d", accounts)).ID
				},
				Appoint: func(contest, user uuid.UUID, role rbac.ContestRole) {
					if err := managers.Grant(ctx, contests.Manager{
						ContestID: contest, UserID: user, Role: role, GrantedBy: user,
					}); err != nil {
						t.Fatalf("Grant() = %v", err)
					}
				},
				Register: func(contest, user uuid.UUID) {
					if _, err := registrations.Add(ctx, contest, user); err != nil {
						t.Fatalf("Add() = %v", err)
					}
				},
				Outside: context.Background(),
				Now:     func() time.Time { return now },
			})
		})
	})
}

// checkViolation is the SQLSTATE PostgreSQL raises for a CHECK constraint.
const checkViolation = "23514"

// Finding 5: internal/contests.Contest.Validate was the only thing bounding
// duration_min before this migration — a row written by hand, or one that
// predates the check, was not, and Deadline's
// time.Duration(*DurationMin)*time.Minute arithmetic overflows and wraps to a
// deadline in the past well before an int this size otherwise would. This
// goes straight through Exec rather than the repository, the same way a
// hand-written row or a pre-migration one would have reached the table: the
// domain's own Create was never in a position to stop either.
func TestDurationMinIsBoundedAtTheDatabaseToo(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-duration-bound")
		const overTheBound = 10081 // maxDurationMin (contests.go) + 1

		_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`INSERT INTO contests (created_by, timing, duration_min) VALUES ($1, 'individual', $2)`,
			author.ID, overTheBound)

		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != checkViolation {
			t.Fatalf("insert with duration_min = %d: err = %v, want a %s check violation", overTheBound, err, checkViolation)
		}
	})
}

// A duration exactly at the bound is still accepted — this is a ceiling, not
// a tighter limit than the domain's own.
func TestDurationMinAtTheBoundIsAcceptedByTheDatabase(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-duration-at-bound")
		const atTheBound = 10080 // maxDurationMin (contests.go)

		_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`INSERT INTO contests (created_by, timing, duration_min) VALUES ($1, 'individual', $2)`,
			author.ID, atTheBound)
		if err != nil {
			t.Fatalf("insert with duration_min = %d (the bound itself): %v", atTheBound, err)
		}
	})
}

// The picture a contest wears comes back with the contest, because the one
// screen that shows a picture above a story (design spec §10) already reads
// this listing and opens under a timer. A projection is the whole point: a
// second read per row would be the N+1 the languages and translations are
// already written to avoid.
func TestAListingCarriesTheCoverEachContestWears(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-listing-cover")
		dressed := makeContest(t, ctx, author.ID)
		bare := makeContest(t, ctx, author.ID)
		if err := NewCovers(testPool).Save(ctx, aCover(dressed, author.ID)); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		found, _, err := repo.List(ctx, contests.Filter{Limit: 10})
		if err != nil {
			t.Fatalf("List() = %v", err)
		}

		byID := map[uuid.UUID]contests.Contest{}
		for _, c := range found {
			byID[c.ID] = c
		}
		want := aCover(dressed, author.ID)
		if got := byID[dressed]; got.CoverHash != want.Hash {
			t.Errorf("CoverHash = %q, want %q", got.CoverHash, want.Hash)
		}
		if got := byID[dressed]; got.CoverAttribution != want.Attribution {
			t.Errorf("CoverAttribution = %q, want %q", got.CoverAttribution, want.Attribution)
		}
		// Empty, not a failed read: a contest nobody uploaded a picture for
		// wears the drawn cover, and that is an ordinary state.
		if got := byID[bare]; got.CoverHash != "" || got.CoverAttribution != "" {
			t.Errorf("a contest with no uploaded cover read back %q/%q, want both empty",
				got.CoverHash, got.CoverAttribution)
		}
	})
}

// Update writes icpc_penalty_min the same way Create does — a column added
// after Update's own SQL was last touched is exactly the one a future column
// gets left out of by mistake.
func TestUpdateWritesTheICPCPenalty(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-icpc-penalty")
		id := makeContest(t, ctx, author.ID)

		before, err := repo.ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if before.ICPCPenaltyMin != 20 {
			t.Fatalf("icpc penalty min = %d, want the column's own default of 20", before.ICPCPenaltyMin)
		}

		before.ICPCPenaltyMin = 50
		if err := repo.Update(ctx, before); err != nil {
			t.Fatalf("Update() = %v", err)
		}

		after, err := repo.ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if after.ICPCPenaltyMin != 50 {
			t.Errorf("icpc penalty min = %d, want 50", after.ICPCPenaltyMin)
		}
	})
}

func TestDeletingAContestTakesItsTranslationsWithIt(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewContests(testPool)
		author := makeUser(t, ctx, "author-delete")
		id := makeContest(t, ctx, author.ID)
		if err := repo.ReplaceTranslations(ctx, id, []contests.Translation{
			{Lang: "en", Title: "Gone"},
		}); err != nil {
			t.Fatalf("ReplaceTranslations() = %v", err)
		}

		if err := repo.Delete(ctx, id); err != nil {
			t.Fatalf("Delete() = %v", err)
		}

		var left int
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT count(*) FROM contest_translations WHERE contest_id = $1`, id).Scan(&left); err != nil {
			t.Fatalf("count translations: %v", err)
		}
		if left != 0 {
			t.Errorf("%d translations left behind, want 0", left)
		}
	})
}

func TestEnrolledInReportsOnlyTheCallersOwnRegistrations(t *testing.T) {
	// The flag the catalogue marks its rows with. Reporting somebody else's
	// registration would disclose who takes part in what.
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-on")
		student := makeUser(t, ctx, "student-on")
		other := makeUser(t, ctx, "other-on")

		mine := makeContest(t, ctx, author.ID)
		theirs := makeContest(t, ctx, author.ID)
		if _, err := repo.Add(ctx, mine, student.ID); err != nil {
			t.Fatalf("Add() = %v", err)
		}
		if _, err := repo.Add(ctx, theirs, other.ID); err != nil {
			t.Fatalf("Add() = %v", err)
		}

		on, err := repo.EnrolledIn(ctx, student.ID, []uuid.UUID{mine, theirs})
		if err != nil {
			t.Fatalf("EnrolledIn() = %v", err)
		}

		if !on[mine] {
			t.Error("the student's own registration is not reported")
		}
		if on[theirs] {
			t.Error("another account's registration was reported as this student's")
		}
	})
}

// TestTryLockRefusesASecondHolderUntilTheFirstEndsItsTransaction proves the
// guarantee contests.Scheduler's whole design rests on across two real
// connections, not one: pg_try_advisory_xact_lock is a lock between database
// sessions, and a test that only ever opens one transaction could not tell
// the difference between "this call cannot get the lock" and "this call
// forgot to ask" (CLAUDE.md rule 10 — prove it on the path the deployment
// uses).
func TestTryLockRefusesASecondHolderUntilTheFirstEndsItsTransaction(t *testing.T) {
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	repo := NewContests(testPool)
	ctx := context.Background()

	holding := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)

	go func() {
		firstDone <- storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
			ok, err := repo.TryLock(ctx)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("first caller did not get an uncontested lock")
			}
			close(holding)
			<-release
			return errRollback
		})
	}()

	<-holding
	err := storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
		ok, err := repo.TryLock(ctx)
		if err != nil {
			return err
		}
		if ok {
			t.Error("a second, independent transaction acquired the lock while the first still holds it")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("second TryLock() attempt failed: %v", err)
	}

	close(release)
	if err := <-firstDone; err != nil && !errors.Is(err, errRollback) {
		t.Fatalf("first transaction failed: %v", err)
	}

	err = storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
		ok, err := repo.TryLock(ctx)
		if err != nil {
			return err
		}
		if !ok {
			t.Error("lock is still held after the transaction that acquired it ended")
		}
		return errRollback
	})
	if err != nil && !errors.Is(err, errRollback) {
		t.Fatalf("third TryLock() attempt failed: %v", err)
	}
}

// setSchedule puts a seeded contest (already valid by makeContest's own
// column defaults) into the status and window a schedule case needs, without
// fighting the enumerated CHECK constraints a hand-built contests.Contest
// would have to satisfy on every other field.
func setSchedule(t *testing.T, ctx context.Context, id uuid.UUID, status string, startsAt, endsAt *time.Time) {
	t.Helper()
	_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`UPDATE contests SET status = $2, starts_at = $3, ends_at = $4 WHERE id = $1`,
		id, status, startsAt, endsAt)
	if err != nil {
		t.Fatalf("set contest schedule: %v", err)
	}
}

// What a single caller can observe of the scheduler's reads and moves is
// the contract the in-memory Schedule answers to as well
// (conteststest.ScheduleRepositoryContract). What it leaves to this file is
// TryLock refusing a second holder, which only two transactions can see
// (TestTryLockRefusesASecondHolderUntilTheFirstEndsItsTransaction above).
func TestContestsHonoursTheScheduleRepositoryContract(t *testing.T) {
	conteststest.ScheduleRepositoryContract(t, func(t *testing.T, run func(context.Context, conteststest.ScheduleTarget)) {
		withTx(t, func(ctx context.Context) {
			repo := NewContests(testPool)
			author := makeUser(t, ctx, "author-schedule-contract")
			now := txNow(t, ctx)
			run(ctx, conteststest.ScheduleTarget{
				Repo:     repo,
				Contests: repo,
				Seed: func(status string, startsAt, endsAt *time.Time) uuid.UUID {
					id := makeContest(t, ctx, author.ID)
					setSchedule(t, ctx, id, status, startsAt, endsAt)
					return id
				},
				Now: func() time.Time { return now },
			})
		})
	})
}

// TestLockContestSerialisesConcurrentWriters proves LockContest is a real
// lock between database sessions, not merely decoration: a second caller
// must block until the first transaction holding it ends, across two real
// connections rather than one (CLAUDE.md rule 10 — prove it on the path the
// deployment uses; TestTryLockRefusesASecondHolderUntilTheFirstEndsItsTransaction
// above proves the same thing for the scheduler's advisory lock). This is
// what contests.Service.GrantManager and Service.Enroll/AddParticipants rely
// on to keep a contest's staff and its participants from overlapping no
// matter how two requests for the same contest interleave: the two checks
// live in different tables, so only a lock shared by both directions can
// make one of them wait for the other to finish.
func TestLockContestSerialisesConcurrentWriters(t *testing.T) {
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	ctx := context.Background()
	author := makeUser(t, ctx, "lock-contest-"+uuid.NewString()[:8])
	contest := makeContest(t, ctx, author.ID)
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contest)
	})
	repo := NewContests(testPool)

	holding := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)

	go func() {
		firstDone <- storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
			if err := repo.LockContest(ctx, contest); err != nil {
				return err
			}
			close(holding)
			<-release
			return nil
		})
	}()

	<-holding

	secondAcquired := make(chan error, 1)
	go func() {
		secondAcquired <- storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
			return repo.LockContest(ctx, contest)
		})
	}()

	select {
	case <-secondAcquired:
		t.Fatal("a second caller acquired the lock while the first still holds it")
	case <-time.After(200 * time.Millisecond):
		// Still blocked, as expected.
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first transaction failed: %v", err)
	}

	select {
	case err := <-secondAcquired:
		if err != nil {
			t.Fatalf("second LockContest() = %v, want it to succeed once the first released", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second caller never acquired the lock after the first released it")
	}
}

// TestLockContestDoesNotBlockAForeignKeyInsertReferencingTheContest proves
// LockContest takes a lock weak enough to leave the contest's other
// concurrent writers alone: inserting a row that references this contest by
// foreign key (game_instances.contest_id, here through AddSpare — the same
// insert the pool's own background top-ups make) takes a key-share lock on
// the referenced contests row, and that must not be made to wait behind
// whatever GrantManager, Enroll or AddParticipants is doing with the
// stronger lock this type serialises them on. A `FOR UPDATE` lock would
// block it — exactly what would starve the pool of capacity a participant
// is waiting on for as long as a roster import runs; `FOR NO KEY UPDATE`
// does not conflict with a key-share lock, so this insert completes
// promptly while the first transaction still holds LockContest open.
func TestLockContestDoesNotBlockAForeignKeyInsertReferencingTheContest(t *testing.T) {
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	ctx := context.Background()
	author := makeUser(t, ctx, "lock-fk-"+uuid.NewString()[:8])
	contest := makeContest(t, ctx, author.ID)
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contest)
	})
	repo := NewContests(testPool)
	instances := NewGameInstances(testPool)

	holding := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)

	go func() {
		firstDone <- storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
			if err := repo.LockContest(ctx, contest); err != nil {
				return err
			}
			close(holding)
			<-release
			return nil
		})
	}()

	<-holding

	// A tight deadline stands in for "did not wait behind the lock": if the
	// insert were blocked, it would still be waiting when this expires,
	// long before release is ever closed.
	insertCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	insertErr := instances.AddSpare(insertCtx, contest, "lockfk_"+uuid.NewString()[:12], 1)

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first transaction failed: %v", err)
	}
	if insertErr != nil {
		t.Fatalf("AddSpare() = %v, want the foreign key insert to complete without waiting on LockContest", insertErr)
	}
}
