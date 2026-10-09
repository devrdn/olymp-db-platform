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

// The shared contract covers what a single caller can observe; the tests
// below cover what only the real database shows: column bounds and defaults,
// cascades, the cover projection and locks between sessions.
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

// The CHECK constraint bounds rows that bypass Contest.Validate; an unbounded
// duration_min overflows Deadline's arithmetic into a past deadline. The
// insert goes through Exec to bypass the domain check.
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

// The database bound must not be tighter than the domain's.
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
		// No upload reads back empty, not as an error.
		if got := byID[bare]; got.CoverHash != "" || got.CoverAttribution != "" {
			t.Errorf("a contest with no uploaded cover read back %q/%q, want both empty",
				got.CoverHash, got.CoverAttribution)
		}
	})
}

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
	// Reporting another account's registration would disclose who takes part
	// in what.
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

// The advisory lock is between sessions, so the test uses two real
// connections (CLAUDE.md rule 10).
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

// setSchedule sets a seeded contest's status and window directly, keeping
// makeContest's valid defaults for every other column.
func setSchedule(t *testing.T, ctx context.Context, id uuid.UUID, status string, startsAt, endsAt *time.Time) {
	t.Helper()
	_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`UPDATE contests SET status = $2, starts_at = $3, ends_at = $4 WHERE id = $1`,
		id, status, startsAt, endsAt)
	if err != nil {
		t.Fatalf("set contest schedule: %v", err)
	}
}

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

// A second LockContest must block until the first transaction ends, across
// two real connections (CLAUDE.md rule 10). GrantManager, Enroll and
// AddParticipants rely on it to keep staff and participants apart.
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

// A foreign-key insert (AddSpare, as the pool's top-ups do) takes a key-share
// lock on the contest row, which FOR NO KEY UPDATE does not block. FOR UPDATE
// would stall the pool for as long as a roster import ran.
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

	// A blocked insert would still be waiting when this deadline expires.
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
