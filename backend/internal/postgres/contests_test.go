package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests/conteststest"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
	"github.com/devrdn/olymp-db-platform/backend/internal/rbac"
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
				Registrations: registrations,
				Outside:       context.Background(),
				Now:           func() time.Time { return now },
				SavePolicy: func(p contests.SQLPolicy) {
					if err := NewSQLPolicies(testPool).Save(ctx, p); err != nil {
						t.Fatalf("Save() = %v", err)
					}
				},
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

// Every contest stored before the setting existed, and any row written
// without it, keeps offering the download it always had.
func TestAContestStoredWithoutTheResultDownloadOffersIt(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-result-download")
		var id uuid.UUID
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`INSERT INTO contests (created_by) VALUES ($1) RETURNING id`, author.ID).Scan(&id); err != nil {
			t.Fatalf("insert = %v", err)
		}

		got, err := NewContests(testPool).ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if !got.ResultDownload {
			t.Error("ResultDownload = false, want the column default, true")
		}
	})
}

// Every contest stored before the setting existed, and any row written
// without it, shows the diagram it always showed.
func TestAContestStoredWithoutTheSchemaDiagramShowsIt(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-schema-diagram")
		var id uuid.UUID
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`INSERT INTO contests (created_by) VALUES ($1) RETURNING id`, author.ID).Scan(&id); err != nil {
			t.Fatalf("insert = %v", err)
		}

		got, err := NewContests(testPool).ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if !got.SchemaDiagram {
			t.Error("SchemaDiagram = false, want the column default, true")
		}
	})
}

// Every contest stored before the setting existed awards no prizes.
func TestAContestStoredWithoutPrizePlacesHasNone(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-prize-default")
		var id uuid.UUID
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`INSERT INTO contests (created_by) VALUES ($1) RETURNING id`, author.ID).Scan(&id); err != nil {
			t.Fatalf("insert = %v", err)
		}

		got, err := NewContests(testPool).ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if got.PrizePlaces != (contests.PrizePlaces{}) {
			t.Errorf("PrizePlaces = %+v, want the column defaults, none", got.PrizePlaces)
		}
	})
}

// The CHECK bounds a row written past Contest.Validate, and is no tighter than
// the domain's bound (maxPrizePlaces, prizes.go).
func TestPrizePlacesAreBoundedAtTheDatabaseToo(t *testing.T) {
	for _, column := range []string{"prize_first", "prize_second", "prize_third"} {
		t.Run(column, func(t *testing.T) {
			for value, accepted := range map[int]bool{-1: false, 0: true, 1000: true, 1001: false} {
				withTx(t, func(ctx context.Context) {
					author := makeUser(t, ctx, "author-prize-bound")
					_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
						`INSERT INTO contests (created_by, `+column+`) VALUES ($1, $2)`, author.ID, value)

					var pgErr *pgconn.PgError
					switch {
					case accepted && err != nil:
						t.Errorf("%s = %d: %v, want it accepted", column, value, err)
					case !accepted && (!errors.As(err, &pgErr) || pgErr.Code != checkViolation):
						t.Errorf("%s = %d: err = %v, want a %s check violation", column, value, err, checkViolation)
					}
				})
			}
		})
	}
}

// The reclaim sweep measures a finished contest's grace from updated_at, so a
// display-only save must not move it.
func TestSettingPrizePlacesLeavesTheReclaimClockAlone(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-prize-clock")
		q := storage.QuerierFrom(ctx, testPool)
		var id uuid.UUID
		var before time.Time
		if err := q.QueryRow(ctx, `
			INSERT INTO contests (created_by, status, updated_at)
			VALUES ($1, 'finished', now() - interval '3 days')
			RETURNING id, updated_at`, author.ID).Scan(&id, &before); err != nil {
			t.Fatalf("insert = %v", err)
		}

		repo := NewContests(testPool)
		c, err := repo.ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		c.PrizePlaces = contests.PrizePlaces{First: 1, Second: 2, Third: 3}
		if _, err := repo.SetPrizePlaces(ctx, c); err != nil {
			t.Fatalf("SetPrizePlaces() = %v", err)
		}

		var after time.Time
		if err := q.QueryRow(ctx, `SELECT updated_at FROM contests WHERE id = $1`, id).Scan(&after); err != nil {
			t.Fatalf("read = %v", err)
		}
		if !after.Equal(before) {
			t.Errorf("updated_at = %v, want %v left alone", after, before)
		}
	})
}

// A write decided before an archive that commits while the write waits for
// the row is refused once the lock is released: PostgreSQL re-reads the
// version against the committed row rather than writing into the archived
// contest.
func TestAWriteWaitingOnAnArchiveIsRefusedOnceItCommits(t *testing.T) {
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	ctx := context.Background()
	author := makeUser(t, ctx, "prize-race-"+uuid.NewString()[:8])
	contest := makeContest(t, ctx, author.ID)
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contest)
	})
	if _, err := testPool.Exec(ctx, `UPDATE contests SET status = 'finished' WHERE id = $1`, contest); err != nil {
		t.Fatalf("finish = %v", err)
	}
	repo := NewContests(testPool)
	read, err := repo.ByID(ctx, contest)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}

	archiving := make(chan struct{})
	commit := make(chan struct{})
	// A failure below must still end the archive's transaction, or the
	// cleanup's DELETE waits on its row lock.
	release := sync.OnceFunc(func() { close(commit) })
	t.Cleanup(release)
	archived := make(chan error, 1)
	go func() {
		archived <- storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
			if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
				`UPDATE contests SET status = 'archived' WHERE id = $1`, contest); err != nil {
				return err
			}
			close(archiving)
			<-commit
			return nil
		})
	}()
	<-archiving

	saved := make(chan error, 1)
	go func() {
		decided := read
		decided.PrizePlaces = contests.PrizePlaces{First: 3}
		_, err := repo.SetPrizePlaces(ctx, decided)
		saved <- err
	}()
	select {
	case err := <-saved:
		t.Fatalf("SetPrizePlaces() = %v while the archive still held the row, want it to wait", err)
	case <-time.After(200 * time.Millisecond):
	}

	release()
	if err := <-archived; err != nil {
		t.Fatalf("archive = %v", err)
	}
	select {
	case err := <-saved:
		if !errors.Is(err, contests.ErrContestChanged) {
			t.Errorf("SetPrizePlaces() = %v, want ErrContestChanged once the archive committed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetPrizePlaces() never returned after the archive committed")
	}
	got, err := repo.ByID(ctx, contest)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	if got.PrizePlaces != (contests.PrizePlaces{}) {
		t.Errorf("PrizePlaces = %+v, want the archived contest's counts untouched", got.PrizePlaces)
	}
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
		if _, err := repo.Update(ctx, before); err != nil {
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
			accounts := 0
			run(ctx, conteststest.ScheduleTarget{
				Repo:     repo,
				Contests: repo,
				Seed: func(status string, startsAt, endsAt *time.Time) uuid.UUID {
					id := makeContest(t, ctx, author.ID)
					setSchedule(t, ctx, id, status, startsAt, endsAt)
					return id
				},
				Registrations: NewRegistrations(testPool),
				NewUser: func() uuid.UUID {
					accounts++
					return makeUser(t, ctx, fmt.Sprintf("schedule-contract-%d", accounts)).ID
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

// An organiser's move to finished writes, through the real sink, one
// participant.finish per participation it ended, with no actor and no origin,
// while its own status change keeps the organiser's. The entries go through
// the move's transaction: rolled back with it, none survives.
func TestAnOrganisersFinishRecordsEachParticipationItEndedInItsTransaction(t *testing.T) {
	var contestID uuid.UUID
	withTx(t, func(txCtx context.Context) {
		ctx := audit.WithRequestMeta(txCtx, "203.0.113.7", "organiser-agent")
		suffix := uuid.NewString()[:8]
		author := makeUser(t, ctx, "author-"+suffix)
		contestID = makeContest(t, ctx, author.ID)
		exec(t, ctx, `UPDATE contests SET status = 'running' WHERE id = $1`, contestID)
		registrations := NewRegistrations(testPool)
		started := makeUser(t, ctx, "started-"+suffix)
		registration := makeRegistration(t, ctx, contestID, started.ID)
		if _, _, err := registrations.Start(ctx, registration, txNow(t, ctx).Add(-time.Hour)); err != nil {
			t.Fatalf("Start() = %v", err)
		}
		idle := makeUser(t, ctx, "idle-"+suffix)
		makeRegistration(t, ctx, contestID, idle.ID)

		service := contests.NewService(contests.ServiceConfig{
			Contests:      NewContests(testPool),
			Registrations: registrations,
			Audit:         audit.New(NewAuditSink(testPool)),
			UnitOfWork:    storage.NewUnitOfWork(testPool),
			Gate:          contests.NewGate(0),
			GameReadiness: NewGameInstances(testPool),
			Covers:        NewCovers(testPool),
			Appointees:    NewUsers(testPool),
		})
		if err := service.Transition(ctx, author.ID, contestID, contests.StatusFinished); err != nil {
			t.Fatalf("Transition(finished) = %v", err)
		}

		rows, err := storage.QuerierFrom(ctx, testPool).Query(ctx, `
			SELECT action, actor_id, coalesce(host(ip), ''), coalesce(user_agent, ''), coalesce(payload->>'user_id', '')
			FROM audit_log WHERE entity = 'contest' AND entity_id = $1 ORDER BY id`, contestID.String())
		if err != nil {
			t.Fatalf("read the trail: %v", err)
		}
		defer rows.Close()
		var finishes []string
		for rows.Next() {
			var action, ip, agent, user string
			var actor *uuid.UUID
			if err := rows.Scan(&action, &actor, &ip, &agent, &user); err != nil {
				t.Fatalf("scan the trail: %v", err)
			}
			switch action {
			case audit.ActionContestStatusChange:
				if actor == nil || *actor != author.ID || ip != "203.0.113.7" {
					t.Errorf("the status change names %v from %q, want the organiser %s from 203.0.113.7", actor, ip, author.ID)
				}
			case audit.ActionParticipantFinish:
				if actor != nil || ip != "" || agent != "" {
					t.Errorf("a finish entry names %v from %q/%q, want no actor and no origin", actor, ip, agent)
				}
				finishes = append(finishes, user)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read the trail: %v", err)
		}
		if len(finishes) != 1 || finishes[0] != started.ID.String() {
			t.Errorf("%s entries name %v, want only the participant who started, %s",
				audit.ActionParticipantFinish, finishes, started.ID)
		}
	})

	var survived int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE entity = 'contest' AND entity_id = $1`, contestID.String()).Scan(&survived); err != nil {
		t.Fatalf("read the trail after the rollback: %v", err)
	}
	if survived != 0 {
		t.Errorf("%d entries outlived the rolled-back move, want none", survived)
	}
}

// A row inserted with the column defaults, as the previous release does, must
// pass the domain's checks once read back: Update starts from what the
// repository returns and validates it (security rule 10).
func TestAContestStoredWithColumnDefaultsCanBeUpdatedThroughTheService(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "defaults-"+uuid.NewString()[:8])
		id := makeContest(t, ctx, author.ID)
		service := contests.NewService(contests.ServiceConfig{
			Contests:      NewContests(testPool),
			Registrations: NewRegistrations(testPool),
			Audit:         audit.New(NewAuditSink(testPool)),
			UnitOfWork:    storage.NewUnitOfWork(testPool),
			Gate:          contests.NewGate(0),
			GameReadiness: NewGameInstances(testPool),
			Covers:        NewCovers(testPool),
			Appointees:    NewUsers(testPool),
		})

		updated, err := service.Update(ctx, contests.UpdateCommand{
			ActorID: author.ID, ContestID: id, Progression: contests.ProgressionSequential,
		})
		if err != nil {
			t.Fatalf("Update() = %v", err)
		}
		if updated.Visibility != contests.VisibilityOpen || updated.ArchiveTasks {
			t.Errorf("visibility, archive tasks = %q, %v, want open, false (the column defaults)",
				updated.Visibility, updated.ArchiveTasks)
		}
	})
}

func TestTheSelfRegistrationCheckHoldsInTheDatabase(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "check-"+uuid.NewString()[:8])
		id := makeContest(t, ctx, author.ID)

		_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE contests SET enrollment = 'open', visibility = 'closed' WHERE id = $1`, id)

		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "contests_self_registration_open" {
			t.Errorf("UPDATE = %v, want a check violation of contests_self_registration_open", err)
		}
	})
}

// A row created by the previous release carries only the column defaults.
func TestAContestCreatedBeforeTheReleaseIsOpenWithTasksHidden(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "legacy-"+uuid.NewString()[:8])
		id := makeContest(t, ctx, author.ID)

		got, err := NewContests(testPool).ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if got.Visibility != contests.VisibilityOpen || got.ArchiveTasks {
			t.Errorf("visibility, archive tasks = %q, %v, want open, false", got.Visibility, got.ArchiveTasks)
		}
	})
}

func readArchivedAt(t *testing.T, ctx context.Context, id uuid.UUID) *time.Time {
	t.Helper()

	var at *time.Time
	if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
		`SELECT archived_at FROM contests WHERE id = $1`, id).Scan(&at); err != nil {
		t.Fatalf("read archived_at: %v", err)
	}
	return at
}

func TestArchivingAContestStampsArchivedAt(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "archive-"+uuid.NewString()[:8])
		id := makeContest(t, ctx, author.ID)
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE contests SET status = 'finished' WHERE id = $1`, id); err != nil {
			t.Fatalf("finish the contest: %v", err)
		}

		moveStatus(t, ctx, id, contests.StatusArchived)

		at := readArchivedAt(t, ctx, id)
		if now := txNow(t, ctx); at == nil || !at.Equal(now) {
			t.Errorf("archived_at = %v, want the moment of archiving %v", at, now)
		}
	})
}

func TestAContestCreatedArchivedIsStamped(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "archive-"+uuid.NewString()[:8])

		var id uuid.UUID
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`INSERT INTO contests (created_by, status) VALUES ($1, 'archived') RETURNING id`,
			author.ID).Scan(&id); err != nil {
			t.Fatalf("create an archived contest: %v", err)
		}

		at := readArchivedAt(t, ctx, id)
		if now := txNow(t, ctx); at == nil || !at.Equal(now) {
			t.Errorf("archived_at = %v, want the moment of creation %v", at, now)
		}
	})
}

func TestAContestNotArchivedHasNoArchivedAt(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "archive-"+uuid.NewString()[:8])

		for _, status := range []string{"draft", "published", "finished"} {
			id := makeContest(t, ctx, author.ID)
			if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
				`UPDATE contests SET status = $2 WHERE id = $1`, id, status); err != nil {
				t.Fatalf("move the contest to %s: %v", status, err)
			}

			if at := readArchivedAt(t, ctx, id); at != nil {
				t.Errorf("a %s contest has archived_at %v, want none", status, at)
			}
		}
	})
}

// Retention counts from archived_at, so writing the contest after its archive
// must not move it.
func TestArchivedAtSurvivesLaterEdits(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "archive-"+uuid.NewString()[:8])
		id := makeContest(t, ctx, author.ID)

		if !archivedAtKeptThrough(t, ctx, id,
			`UPDATE contests SET visibility = 'closed', updated_at = now() WHERE id = $1`) {
			t.Errorf("archived_at moved after an edit, want it kept at 13 months ago")
		}
	})
}

func TestArchivingAnArchivedContestAgainKeepsItsStamp(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "archive-"+uuid.NewString()[:8])
		id := makeContest(t, ctx, author.ID)

		if !archivedAtKeptThrough(t, ctx, id,
			`UPDATE contests SET status = 'archived', updated_at = now() WHERE id = $1`) {
			t.Errorf("archived_at moved when the archive status was written again, want it kept at 13 months ago")
		}
	})
}

// archivedAtKeptThrough archives the contest, back-dates its stamp by 13
// months, runs edit, and reports whether the stamp stayed where it was put.
func archivedAtKeptThrough(t *testing.T, ctx context.Context, id uuid.UUID, edit string) bool {
	t.Helper()

	q := storage.QuerierFrom(ctx, testPool)
	for _, step := range []string{
		`UPDATE contests SET status = 'archived' WHERE id = $1`,
		`UPDATE contests SET archived_at = now() - interval '13 months' WHERE id = $1`,
		edit,
	} {
		if _, err := q.Exec(ctx, step, id); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
	}

	var kept bool
	if err := q.QueryRow(ctx,
		`SELECT archived_at = now() - interval '13 months' FROM contests WHERE id = $1`,
		id).Scan(&kept); err != nil {
		t.Fatalf("read archived_at: %v", err)
	}
	return kept
}

// afterFirstRead is the contest repository with another writer acting, on its
// own connection, right after the first read: the interleaving a race needs,
// made certain.
type afterFirstRead struct {
	*Contests
	once  sync.Once
	other func(id uuid.UUID) error
	t     *testing.T
}

func (r *afterFirstRead) ByID(ctx context.Context, id uuid.UUID) (contests.Contest, error) {
	c, err := r.Contests.ByID(ctx, id)
	r.once.Do(func() {
		if err := r.other(id); err != nil {
			r.t.Errorf("the other writer = %v", err)
		}
	})
	return c, err
}

// gameReadiness is the publish gate's question about the game.
type gameReadiness interface {
	GameReady(ctx context.Context, contestID uuid.UUID) (bool, error)
}

// readyThen is the game store with another writer acting, on its own
// connection, right after the gate asked whether the game is built.
type readyThen struct {
	*GameInstances
	once  sync.Once
	other func() error
	t     *testing.T
}

func (r *readyThen) GameReady(ctx context.Context, contestID uuid.UUID) (bool, error) {
	ready, err := r.GameInstances.GameReady(ctx, contestID)
	r.once.Do(func() {
		if err := r.other(); err != nil {
			r.t.Errorf("the other writer = %v", err)
		}
	})
	return ready, err
}

// listedThen is the question store with another writer acting, on its own
// connection, right after the first listing, which is the publish gate's.
type listedThen struct {
	*Questions
	once  sync.Once
	other func() error
	t     *testing.T
}

func (r *listedThen) List(ctx context.Context, contestID uuid.UUID) ([]contests.Question, error) {
	qs, err := r.Questions.List(ctx, contestID)
	r.once.Do(func() {
		if err := r.other(); err != nil {
			r.t.Errorf("the other writer = %v", err)
		}
	})
	return qs, err
}

// raceService is the contest service over PostgreSQL with repo and
// questions as its stores. The game cluster is not a race test's business,
// so its readiness is the in-memory games.
func raceService(repo contests.Repository, questions contests.QuestionRepository, games gameReadiness) *contests.Service {
	return contests.NewService(contests.ServiceConfig{
		Contests:      repo,
		Stories:       NewStories(testPool),
		Questions:     questions,
		Registrations: NewRegistrations(testPool),
		Policies:      NewSQLPolicies(testPool),
		Audit:         audit.New(NewAuditSink(testPool)),
		UnitOfWork:    storage.NewUnitOfWork(testPool),
		Gate:          contests.NewGate(0),
		GameReadiness: games,
		Covers:        NewCovers(testPool),
		Appointees:    NewUsers(testPool),
	})
}

// publishableRow commits a draft that passes the publish gate, with the
// games that call it built, and returns it with its one question.
func publishableRow(t *testing.T, ctx context.Context) (id, question uuid.UUID, games *conteststest.Games) {
	t.Helper()
	id = contestRow(t, ctx)
	repo := NewContests(testPool)
	if _, err := testPool.Exec(ctx,
		`UPDATE contests SET starts_at = now() + interval '1 day', ends_at = now() + interval '2 days' WHERE id = $1`,
		id); err != nil {
		t.Fatalf("schedule = %v", err)
	}
	if err := repo.ReplaceLanguages(ctx, id, []contests.ContestLanguage{{Code: "en", IsDefault: true}}); err != nil {
		t.Fatalf("ReplaceLanguages() = %v", err)
	}
	if err := repo.ReplaceTranslations(ctx, id, []contests.Translation{{Lang: "en", Title: "The Library Murder"}}); err != nil {
		t.Fatalf("ReplaceTranslations() = %v", err)
	}
	if _, err := NewStories(testPool).Save(ctx, id, map[string]string{"en": "A body in the stacks."}); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	questions := NewQuestions(testPool)
	if err := storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
		q, err := questions.Create(ctx, contests.Question{
			ContestID: id, Ord: 1, Kind: contests.KindFinal, Points: 10, IsVisible: true,
		})
		if err != nil {
			return err
		}
		question = q.ID
		if err := questions.ReplaceTexts(ctx, q.ID, map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}}); err != nil {
			return err
		}
		return questions.ReplaceAnswers(ctx, q.ID, []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}})
	}); err != nil {
		t.Fatalf("add the question = %v", err)
	}
	games = conteststest.NewGames()
	games.Built(id)
	return id, question, games
}

// An organiser's edit decided against a published contest must not land once
// the scheduler has started it: a question mode change is refused on a
// running contest, and the stale decision would have bypassed that.
func TestAnUpdateDecidedBeforeTheStartIsRefusedOnceTheContestRuns(t *testing.T) {
	ctx := context.Background()
	id := contestRow(t, ctx)
	if _, err := testPool.Exec(ctx, `UPDATE contests SET status = 'published' WHERE id = $1`, id); err != nil {
		t.Fatalf("publish = %v", err)
	}
	repo := NewContests(testPool)
	scheduler := func(id uuid.UUID) error {
		due, err := repo.ByID(context.Background(), id)
		if err != nil {
			return err
		}
		_, err = repo.SetStatus(context.Background(), due, contests.StatusRunning)
		return err
	}
	service := raceService(&afterFirstRead{Contests: repo, other: scheduler, t: t}, NewQuestions(testPool), conteststest.NewGames())

	_, err := service.Update(ctx, contests.UpdateCommand{ContestID: id, QuestionMode: contests.QuestionModeSingle})

	if !errors.Is(err, contests.ErrContestChanged) {
		t.Errorf("Update() = %v, want ErrContestChanged", err)
	}
	got, err := repo.ByID(ctx, id)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	if got.Status != contests.StatusRunning || got.QuestionMode != contests.QuestionModeMulti {
		t.Errorf("status, question mode = %s, %s, want running, multi: the stale edit landed",
			got.Status, got.QuestionMode)
	}
}

// A publish whose gate passed against a row an organiser has since saved
// must not land: here the save took the end date away, and the gate refuses
// a contest with no schedule.
func TestAPublishDecidedBeforeAnEditIsRefused(t *testing.T) {
	ctx := context.Background()
	id, _, games := publishableRow(t, ctx)
	repo := NewContests(testPool)
	organiser := func(id uuid.UUID) error {
		saved, err := repo.ByID(context.Background(), id)
		if err != nil {
			return err
		}
		saved.EndsAt = nil
		_, err = repo.Update(context.Background(), saved)
		return err
	}
	service := raceService(&afterFirstRead{Contests: repo, other: organiser, t: t}, NewQuestions(testPool), games)

	err := service.Transition(ctx, uuid.Nil, id, contests.StatusPublished)

	if !errors.Is(err, contests.ErrContestChanged) {
		t.Errorf("Transition() = %v, want ErrContestChanged", err)
	}
	got, err := repo.ByID(ctx, id)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	if got.Status != contests.StatusDraft || got.EndsAt != nil {
		t.Errorf("status = %s with ends_at %v, want a draft without one: the publish landed past the gate",
			got.Status, got.EndsAt)
	}
}

// The gate reads the content as well as the row: a question deleted after the
// gate counted it must refuse the publish, or the contest goes out with none.
func TestAPublishDecidedBeforeAQuestionIsDeletedIsRefused(t *testing.T) {
	ctx := context.Background()
	id, question, games := publishableRow(t, ctx)
	repo := NewContests(testPool)
	organiser := raceService(repo, NewQuestions(testPool), games)
	deletes := func() error {
		return organiser.DeleteQuestion(context.Background(), uuid.Nil, id, question)
	}
	service := raceService(repo, &listedThen{Questions: NewQuestions(testPool), other: deletes, t: t}, games)

	err := service.Transition(ctx, uuid.Nil, id, contests.StatusPublished)

	if !errors.Is(err, contests.ErrContestChanged) {
		t.Errorf("Transition() = %v, want ErrContestChanged", err)
	}
	if got, err := repo.ByID(ctx, id); err != nil || got.Status != contests.StatusDraft {
		t.Errorf("ByID() = %s, %v, want a draft: the publish landed past the gate", got.Status, err)
	}
}

// A game replaced after the gate saw it built leaves the contest with a game
// still to build: the publish decided against the built one must not land.
func TestAPublishDecidedBeforeTheGameIsReplacedIsRefused(t *testing.T) {
	ctx := context.Background()
	id, _, _ := publishableRow(t, ctx)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO game_templates (contest_id, template_db, init_script, status)
		VALUES ($1, $2, 'CREATE TABLE suspects (name text);', 'ready')`,
		id, "race_"+strings.ReplaceAll(id.String(), "-", "")[:16]); err != nil {
		t.Fatalf("build the game = %v", err)
	}
	repo, templates := NewContests(testPool), NewGameInstances(testPool)
	author, err := repo.ByID(ctx, id)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	games := provisioning.NewGames(templates, nil, templates, repo, storage.NewUnitOfWork(testPool)).
		WithAudit(audit.New(NewAuditSink(testPool)))
	replaces := func() error {
		_, err := games.SetScript(context.Background(), author.CreatedBy, id, "CREATE TABLE witnesses (name text);")
		return err
	}
	service := raceService(repo, NewQuestions(testPool), &readyThen{GameInstances: templates, other: replaces, t: t})

	err = service.Transition(ctx, uuid.Nil, id, contests.StatusPublished)

	if !errors.Is(err, contests.ErrContestChanged) {
		t.Errorf("Transition() = %v, want ErrContestChanged", err)
	}
	if got, err := repo.ByID(ctx, id); err != nil || got.Status != contests.StatusDraft {
		t.Errorf("ByID() = %s, %v, want a draft: the publish landed with the game unbuilt", got.Status, err)
	}
}

// meanwhileThen is the contest repository with another writer starting, on
// its own connection, right after the first read. The read returns once that
// writer finishes or has waited a while, so a writer that blocks on a lock the
// caller holds is left to finish after the caller commits.
type meanwhileThen struct {
	*Contests
	once  sync.Once
	other func() error
	done  chan error
}

func (r *meanwhileThen) ByID(ctx context.Context, id uuid.UUID) (contests.Contest, error) {
	c, err := r.Contests.ByID(ctx, id)
	r.once.Do(func() {
		go func() { r.done <- r.other() }()
		select {
		case err := <-r.done:
			r.done <- err
		case <-time.After(300 * time.Millisecond):
		}
	})
	return c, err
}

// Two authors adding a question at the same moment both succeed, the second
// with the next number: content edits serialise on the contest, they do not
// refuse each other.
func TestTwoQuestionsAddedAtOnceBothLandInOrder(t *testing.T) {
	ctx := context.Background()
	id := contestRow(t, ctx)
	repo := NewContests(testPool)
	other := raceService(repo, NewQuestions(testPool), conteststest.NewGames())
	adds := func() error {
		_, err := other.AddQuestion(context.Background(), contests.QuestionCommand{ContestID: id, Points: 1})
		return err
	}
	meanwhile := &meanwhileThen{Contests: repo, other: adds, done: make(chan error, 1)}
	service := raceService(meanwhile, NewQuestions(testPool), conteststest.NewGames())

	_, err := service.AddQuestion(ctx, contests.QuestionCommand{ContestID: id, Points: 2})

	if err != nil {
		t.Errorf("the first AddQuestion() = %v, want it to land", err)
	}
	select {
	case err := <-meanwhile.done:
		if err != nil {
			t.Errorf("the second AddQuestion() = %v, want it to land", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second AddQuestion() never returned")
	}
	questions, err := NewQuestions(testPool).List(ctx, id)
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	var ords []int
	for _, q := range questions {
		ords = append(ords, q.Ord)
	}
	slices.Sort(ords)
	if !slices.Equal(ords, []int{1, 2}) {
		t.Errorf("question numbers = %v, want 1 and 2", ords)
	}
}

// A policy save decided on a published contest must not land on the running
// one: the template's grants are built from the policy, and would stop
// matching what the validator enforces.
func TestAPolicyDecidedBeforeTheStartDoesNotLandOnTheRunningContest(t *testing.T) {
	ctx := context.Background()
	id := contestRow(t, ctx)
	if _, err := testPool.Exec(ctx, `UPDATE contests SET status = 'published' WHERE id = $1`, id); err != nil {
		t.Fatalf("publish = %v", err)
	}
	if err := NewSQLPolicies(testPool).Save(ctx, contests.DefaultSQLPolicy(id)); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	repo := NewContests(testPool)
	start := func() error {
		due, err := repo.ByID(context.Background(), id)
		if err != nil {
			return err
		}
		_, err = repo.SetStatus(context.Background(), due, contests.StatusRunning)
		return err
	}
	meanwhile := &meanwhileThen{Contests: repo, other: start, done: make(chan error, 1)}
	service := raceService(meanwhile, NewQuestions(testPool), conteststest.NewGames())
	author, err := repo.ByID(ctx, id)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	policy := contests.DefaultSQLPolicy(id)
	policy.Mode = contests.ModeReadWrite

	saveErr := service.SetPolicy(ctx, author.CreatedBy, id, policy)

	select {
	case <-meanwhile.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the start never returned")
	}
	got, err := repo.ByID(ctx, id)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	if got.Status == contests.StatusRunning && got.SQLMode == contests.ModeReadWrite {
		t.Errorf("the contest runs under the policy saved for it while published (SetPolicy() = %v)", saveErr)
	}
}

// editableUntilRead is the game store reporting the game editable, then
// starting the contest on its own connection, as a start committed between a
// replacement's check and its write would.
type editableUntilRead struct {
	*GameInstances
	once  sync.Once
	start func() error
	t     *testing.T
}

func (r *editableUntilRead) GameEditable(ctx context.Context, contestID uuid.UUID) (bool, error) {
	editable, err := r.GameInstances.GameEditable(ctx, contestID)
	r.once.Do(func() {
		if err := r.start(); err != nil {
			r.t.Errorf("the start = %v", err)
		}
	})
	return editable, err
}

// A game replacement decided while the contest was published must not land
// once it runs: every participant's copy would go stale mid-contest.
func TestAGameReplacementDecidedBeforeTheStartIsRefused(t *testing.T) {
	ctx := context.Background()
	id := contestRow(t, ctx)
	if _, err := testPool.Exec(ctx, `UPDATE contests SET status = 'published' WHERE id = $1`, id); err != nil {
		t.Fatalf("publish = %v", err)
	}
	repo, templates := NewContests(testPool), NewGameInstances(testPool)
	author, err := repo.ByID(ctx, id)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	start := func() error {
		due, err := repo.ByID(context.Background(), id)
		if err != nil {
			return err
		}
		_, err = repo.SetStatus(context.Background(), due, contests.StatusRunning)
		return err
	}
	games := provisioning.NewGames(templates, nil, &editableUntilRead{GameInstances: templates, start: start, t: t},
		repo, storage.NewUnitOfWork(testPool)).WithAudit(audit.New(NewAuditSink(testPool)))

	_, err = games.SetScript(ctx, author.CreatedBy, id, "CREATE TABLE suspects (name text);")

	if !errors.Is(err, provisioning.ErrGameNotEditable) {
		t.Errorf("SetScript() = %v, want ErrGameNotEditable", err)
	}
	if _, err := templates.TemplateStatus(ctx, id); !errors.Is(err, provisioning.ErrNoGame) {
		t.Errorf("TemplateStatus() = %v, want no game stored on the running contest", err)
	}
}

// A build request decided while the contest was published must not land once
// it runs, for the same reason a replacement must not. The real RequestBuild
// and contest row share one transaction (CLAUDE.md rule 10).
func TestABuildRequestDecidedBeforeTheStartIsRefused(t *testing.T) {
	ctx := context.Background()
	id := contestRow(t, ctx)
	if _, err := testPool.Exec(ctx, `UPDATE contests SET status = 'published' WHERE id = $1`, id); err != nil {
		t.Fatalf("publish = %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO game_templates (contest_id, template_db, init_script, status)
		VALUES ($1, $2, 'CREATE TABLE suspects (name text);', 'ready')`,
		id, "race_"+strings.ReplaceAll(id.String(), "-", "")[:16]); err != nil {
		t.Fatalf("build the game = %v", err)
	}
	repo, templates := NewContests(testPool), NewGameInstances(testPool)
	author, err := repo.ByID(ctx, id)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	start := func() error {
		due, err := repo.ByID(context.Background(), id)
		if err != nil {
			return err
		}
		_, err = repo.SetStatus(context.Background(), due, contests.StatusRunning)
		return err
	}
	games := provisioning.NewGames(templates, nil, &editableUntilRead{GameInstances: templates, start: start, t: t},
		repo, storage.NewUnitOfWork(testPool)).WithAudit(audit.New(NewAuditSink(testPool)))

	_, err = games.RequestBuild(ctx, author.CreatedBy, id)

	if !errors.Is(err, provisioning.ErrGameNotEditable) {
		t.Errorf("RequestBuild() = %v, want ErrGameNotEditable", err)
	}
	if got, err := templates.TemplateStatus(ctx, id); err != nil || got.Status != provisioning.TemplateReady {
		t.Errorf("TemplateStatus() = %s, %v, want the running contest's game left ready", got.Status, err)
	}
}

// The scheduler's finish reads no row to go stale: its condition sits in its
// own UPDATE, which PostgreSQL re-evaluates against the committed row. An
// extension committed while the finish waits for the row leaves the contest
// running. The finish rolls back, so no other contest in the shared database
// is finished by it.
func TestAFinishWaitingOnAnExtensionLeavesTheContestRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := contestRow(t, ctx)
	if _, err := testPool.Exec(ctx, `
		UPDATE contests SET status = 'running', starts_at = now() - interval '2 hours', ends_at = now() - interval '1 hour'
		WHERE id = $1`, id); err != nil {
		t.Fatalf("run the contest = %v", err)
	}
	repo := NewContests(testPool)
	read, err := repo.ByID(ctx, id)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}

	extending := make(chan struct{})
	commit := make(chan struct{})
	release := sync.OnceFunc(func() { close(commit) })
	extended := make(chan error, 1)
	go func() {
		extended <- storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
			later := time.Now().Add(time.Hour)
			read.EndsAt = &later
			if _, err := repo.Update(ctx, read); err != nil {
				return err
			}
			close(extending)
			<-commit
			return nil
		})
	}()
	// Registered after contestRow's cleanup, so it runs first: the extension
	// ends before the cleanup's DELETE needs its row.
	t.Cleanup(func() {
		release()
		<-extended
	})
	select {
	case <-extending:
	case err := <-extended:
		extended <- err
		t.Fatalf("extension = %v before it held the row", err)
	}

	type outcome struct {
		finishedOurs bool
		err          error
	}
	finishing := make(chan outcome, 1)
	go func() {
		var got outcome
		err := storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
			finished, err := repo.AdvanceFinished(ctx, 0)
			if err != nil {
				return err
			}
			for _, c := range finished {
				got.finishedOurs = got.finishedOurs || c.ID == id
			}
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			got.err = err
		}
		finishing <- got
	}()
	select {
	case <-finishing:
		t.Fatal("AdvanceFinished() returned while the extension still held the row, want it to wait")
	case <-time.After(200 * time.Millisecond):
	}

	release()
	if err := <-extended; err != nil {
		extended <- err
		t.Fatalf("extension = %v", err)
	}
	extended <- nil
	got := <-finishing
	switch {
	case got.err != nil:
		t.Fatalf("AdvanceFinished() = %v", got.err)
	case got.finishedOurs:
		t.Error("AdvanceFinished() finished the contest its extension had moved past now")
	}
}
