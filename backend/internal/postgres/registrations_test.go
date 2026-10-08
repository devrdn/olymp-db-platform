package postgres

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// What a single caller can observe of the registrations is the contract every
// contests.RegistrationRepository answers to, the in-memory one the service
// tests use included (conteststest.RegistrationRepositoryContract). What
// follows it here is what only the real database can be asked: two first
// actions racing for one start time, each of the tables that make a registration
// undeletable, and the combined lookups queryproxy reads.
func TestRegistrationsHonoursTheRepositoryContract(t *testing.T) {
	conteststest.RegistrationRepositoryContract(t, func(t *testing.T, run func(context.Context, conteststest.RegistrationTarget)) {
		withTx(t, func(ctx context.Context) {
			accounts := NewUsers(testPool)
			author := makeUser(t, ctx, "author-reg")
			now := txNow(t, ctx)
			run(ctx, conteststest.RegistrationTarget{
				Repo: NewRegistrations(testPool),
				NewUser: func(login, fullName string) uuid.UUID {
					created, err := accounts.Create(ctx, users.User{
						Login: login, FullName: fullName, Status: users.StatusActive, PasswordHash: "not-a-real-hash",
					})
					if err != nil {
						t.Fatalf("create user %q: %v", login, err)
					}
					return created.ID
				},
				NewContest: func() uuid.UUID { return makeContest(t, ctx, author.ID) },
				// admin is the seeded role that carries contest.admin_all
				// (migration 000006).
				GrantAdminAll: func(user uuid.UUID) {
					if err := accounts.ReplaceRoles(ctx, user, []string{"admin"}); err != nil {
						t.Fatalf("ReplaceRoles() = %v", err)
					}
				},
				RecordWork: func(registration uuid.UUID) {
					exec(t, ctx, `INSERT INTO participant_notes (registration_id, body, updated_at)
						VALUES ($1, 'kept', now())`, registration)
				},
				Now: func() time.Time { return now },
			})
		})
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

// A registration is undeletable once anything of the participant's own hangs
// off it, and in a contest on a shared clock that record is the only thing
// that says so: nothing there ever sets started_at. Each of the five tables
// is asked on its own, because each is a separate way to have worked.
func TestAParticipantWithAnythingRecordedHasWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		put  func(t *testing.T, ctx context.Context, registration uuid.UUID)
	}{
		{"a query", func(t *testing.T, ctx context.Context, registration uuid.UUID) {
			exec(t, ctx, `INSERT INTO query_log (registration_id, request_id, sql_text, status, executed_at)
				VALUES ($1, gen_random_uuid(), 'SELECT 1', 'ok', now())`, registration)
		}},
		{"a note", func(t *testing.T, ctx context.Context, registration uuid.UUID) {
			exec(t, ctx, `INSERT INTO participant_notes (registration_id, body, updated_at)
				VALUES ($1, 'kept', now())`, registration)
		}},
		{"an SQL tab", func(t *testing.T, ctx context.Context, registration uuid.UUID) {
			exec(t, ctx, `INSERT INTO participant_sql_tabs (registration_id, title, body, position, updated_at)
				VALUES ($1, 'Tab 1', 'SELECT 1', 0, now())`, registration)
		}},
		{"a signal", func(t *testing.T, ctx context.Context, registration uuid.UUID) {
			exec(t, ctx, `INSERT INTO participant_events (contest_id, registration_id, kind)
				SELECT contest_id, id, 'page_left' FROM registrations WHERE id = $1`, registration)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withTx(t, func(ctx context.Context) {
				repo := NewRegistrations(testPool)
				author := makeUser(t, ctx, "author-work-"+tc.name)
				student := makeUser(t, ctx, "student-work-"+tc.name)
				contest := makeContest(t, ctx, author.ID)
				p, err := repo.Add(ctx, contest, student.ID)
				if err != nil {
					t.Fatalf("Add() = %v", err)
				}

				clean, err := repo.HasWork(ctx, p.ID)
				if err != nil {
					t.Fatalf("HasWork() = %v", err)
				}
				if clean {
					t.Fatal("a registration with nothing behind it reports work")
				}

				tc.put(t, ctx, p.ID)

				has, err := repo.HasWork(ctx, p.ID)
				if err != nil {
					t.Fatalf("HasWork() = %v", err)
				}
				if !has {
					t.Errorf("HasWork() = false, want true with %s recorded", tc.name)
				}
			})
		})
	}
}

// putReadyTemplate inserts a ready game_templates row directly, the same row
// ForRun's game half and GameInstances.Game both read.
func putReadyTemplate(t *testing.T, ctx context.Context, contest uuid.UUID, database string, version int) {
	t.Helper()
	_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx, `
		INSERT INTO game_templates (contest_id, template_db, version, init_script, status)
		VALUES ($1, $2, $3, 'SELECT 1', 'ready')`, contest, database, version)
	if err != nil {
		t.Fatalf("insert ready template: %v", err)
	}
}

// putPolicy inserts a contest's SQL policy directly.
func putPolicy(t *testing.T, ctx context.Context, contest uuid.UUID, mode string, writable []string) {
	t.Helper()
	_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx, `
		INSERT INTO contest_sql_policies (contest_id, mode, writable_tables, allow_create_view, allow_temp_tables, disk_quota_ratio)
		VALUES ($1, $2, $3, true, true, 8)`, contest, mode, writable)
	if err != nil {
		t.Fatalf("insert sql policy: %v", err)
	}
}

// TestForRunReadsTheParticipantContestAndReadyGameTogether is ForRun's own
// claim: everything queryproxy.Run reads separately through People.ByUser,
// Contests.ByID and GameInstances.Game comes back from the one call, and
// neither part is a stale echo of another — the policy ForRun reports is the
// row this test wrote, not a default GameInstances.Game would have coalesced
// to.
func TestForRunReadsTheParticipantContestAndReadyGameTogether(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-forrun")
		student := makeUser(t, ctx, "student-forrun")
		contestID := makeContest(t, ctx, author.ID)
		makeRegistration(t, ctx, contestID, student.ID)
		putReadyTemplate(t, ctx, contestID, "game_tpl_forrun", 3)
		putPolicy(t, ctx, contestID, string(sqlpolicy.ModeReadWrite), []string{"suspects"})

		got, err := repo.ForRun(ctx, contestID, student.ID)
		if err != nil {
			t.Fatalf("ForRun() = %v", err)
		}
		if got.Participant.UserID != student.ID || got.Participant.ContestID != contestID {
			t.Errorf("participant = %+v, want user %s on contest %s", got.Participant, student.ID, contestID)
		}
		if got.Contest.ID != contestID {
			t.Errorf("contest = %+v, want %s", got.Contest, contestID)
		}
		if got.GameErr != nil {
			t.Fatalf("GameErr = %v, want nil — a ready template exists", got.GameErr)
		}
		if got.Game.Template != "game_tpl_forrun" || got.Game.Version != 3 {
			t.Errorf("game = %+v, want template game_tpl_forrun version 3", got.Game)
		}
		if got.Game.Policy.Mode != sqlpolicy.ModeReadWrite {
			t.Errorf("policy mode = %q, want read_write", got.Game.Policy.Mode)
		}
		if len(got.Game.Policy.WritableTables) != 1 || got.Game.Policy.WritableTables[0] != "suspects" {
			t.Errorf("writable tables = %v, want [suspects]", got.Game.Policy.WritableTables)
		}
		if got.Game.Policy.DiskQuotaRatio != 8 {
			t.Errorf("disk quota ratio = %d, want 8", got.Game.Policy.DiskQuotaRatio)
		}
	})
}

// TestForRunReportsNoGameForAContestWithoutAReadyTemplate proves the half of
// ForRun that lets Run use the participant and the contest before ever
// deciding what a missing game means: both still come back, and only GameErr
// carries provisioning.ErrNoGame — the same sentinel a separate call to
// GameInstances.Game would have returned instead of any participant at all.
func TestForRunReportsNoGameForAContestWithoutAReadyTemplate(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-forrun-nogame")
		student := makeUser(t, ctx, "student-forrun-nogame")
		contestID := makeContest(t, ctx, author.ID)
		makeRegistration(t, ctx, contestID, student.ID)

		got, err := repo.ForRun(ctx, contestID, student.ID)
		if err != nil {
			t.Fatalf("ForRun() = %v, want a nil top-level error — the participant and contest were found", err)
		}
		if !errors.Is(got.GameErr, provisioning.ErrNoGame) {
			t.Errorf("GameErr = %v, want provisioning.ErrNoGame", got.GameErr)
		}
		if got.Participant.UserID != student.ID {
			t.Errorf("participant = %+v, want user %s even with no game yet", got.Participant, student.ID)
		}
		if !errors.Is(got.InstanceErr, provisioning.ErrNoInstance) {
			t.Errorf("InstanceErr = %v, want provisioning.ErrNoInstance — the registration has no copy", got.InstanceErr)
		}
	})
}

// The participant's own copy comes back with the rest, as GameInstances.Of
// would have read it, so Ensure has nothing left to ask in the common case.
func TestForRunReadsTheParticipantsOwnInstance(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-forrun-instance")
		student := makeUser(t, ctx, "student-forrun-instance")
		contestID := makeContest(t, ctx, author.ID)
		registration := makeRegistration(t, ctx, contestID, student.ID)
		putReadyTemplate(t, ctx, contestID, "game_tpl_forrun_instance", 3)
		instances := NewGameInstances(testPool)
		if err := instances.Assign(ctx, contestID, registration, "game_db_forrun_instance", 2); err != nil {
			t.Fatalf("Assign() = %v", err)
		}

		got, err := repo.ForRun(ctx, contestID, student.ID)
		if err != nil {
			t.Fatalf("ForRun() = %v", err)
		}
		want, err := instances.Of(ctx, registration)
		if err != nil {
			t.Fatalf("Of() = %v", err)
		}
		if got.InstanceErr != nil || got.Instance != want {
			t.Errorf("instance = %+v, %v; want %+v as Of reads it", got.Instance, got.InstanceErr, want)
		}
	})
}

// ForAccess is ForRun without the game: the same participant and contest,
// and the same single answer for a stranger and for a contest that does not
// exist.
func TestForAccessReadsTheParticipantAndTheContestTogether(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-foraccess")
		student := makeUser(t, ctx, "student-foraccess")
		stranger := makeUser(t, ctx, "stranger-foraccess")
		contestID := makeContest(t, ctx, author.ID)
		makeRegistration(t, ctx, contestID, student.ID)

		participant, contest, err := repo.ForAccess(ctx, contestID, student.ID)
		if err != nil {
			t.Fatalf("ForAccess() = %v", err)
		}
		byUser, err := repo.ByUser(ctx, contestID, student.ID)
		if err != nil {
			t.Fatalf("ByUser() = %v", err)
		}
		if !reflect.DeepEqual(participant, byUser) {
			t.Errorf("participant = %+v, want %+v as ByUser reads it", participant, byUser)
		}
		byID, err := NewContests(testPool).ByID(ctx, contestID)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if !reflect.DeepEqual(contest, byID) {
			t.Errorf("contest = %+v, want %+v as ByID reads it", contest, byID)
		}

		if _, _, err := repo.ForAccess(ctx, contestID, stranger.ID); !errors.Is(err, contests.ErrParticipantNotFound) {
			t.Errorf("ForAccess(stranger) = %v, want ErrParticipantNotFound", err)
		}
		if _, _, err := repo.ForAccess(ctx, uuid.New(), student.ID); !errors.Is(err, contests.ErrParticipantNotFound) {
			t.Errorf("ForAccess(unknown contest) = %v, want ErrParticipantNotFound", err)
		}
	})
}

// A template still building, never having reached 'ready', reads the same
// way as no template at all — the same WHERE t.status = 'ready' that already
// governed GameInstances.Game, now joined instead of queried separately.
func TestForRunReportsNoGameForATemplateStillBuilding(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-forrun-building")
		student := makeUser(t, ctx, "student-forrun-building")
		contestID := makeContest(t, ctx, author.ID)
		makeRegistration(t, ctx, contestID, student.ID)
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx, `
			INSERT INTO game_templates (contest_id, template_db, version, init_script, status)
			VALUES ($1, 'game_tpl_building2', 1, 'SELECT 1', 'building')`, contestID); err != nil {
			t.Fatalf("insert building template: %v", err)
		}

		got, err := repo.ForRun(ctx, contestID, student.ID)
		if err != nil {
			t.Fatalf("ForRun() = %v", err)
		}
		if !errors.Is(got.GameErr, provisioning.ErrNoGame) {
			t.Errorf("GameErr = %v, want provisioning.ErrNoGame for a template still building", got.GameErr)
		}
	})
}

// TestForRunReportsNotAParticipantForAnUnregisteredUser is the same answer
// People.ByUser already gives a caller who never registered — ForRun must
// not invent a different one just because it also reads the contest and the
// game in the same round trip.
func TestForRunReportsNotAParticipantForAnUnregisteredUser(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		author := makeUser(t, ctx, "author-forrun-unreg")
		stranger := makeUser(t, ctx, "stranger-forrun")
		contestID := makeContest(t, ctx, author.ID)

		if _, err := repo.ForRun(ctx, contestID, stranger.ID); !errors.Is(err, contests.ErrParticipantNotFound) {
			t.Errorf("ForRun() = %v, want ErrParticipantNotFound", err)
		}
	})
}

// A contest id naming nothing at all reads the same way as one nobody
// registered for: the INNER JOIN against contests means there is no separate
// "no such contest" case to invent (see ForRun's own doc).
func TestForRunReportsNotAParticipantForAnUnknownContest(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewRegistrations(testPool)
		stranger := makeUser(t, ctx, "stranger-forrun-nocontest")

		if _, err := repo.ForRun(ctx, uuid.New(), stranger.ID); !errors.Is(err, contests.ErrParticipantNotFound) {
			t.Errorf("ForRun() = %v, want ErrParticipantNotFound — the same answer a never-registered user gets", err)
		}
	})
}
