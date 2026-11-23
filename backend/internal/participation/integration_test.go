package participation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage/storagetest"
	"github.com/devrdn/olymp-db-platform/backend/internal/postgres"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests admit against the real repositories, because the fakes hide
// what the schema does, such as a NULL started_at or the once-only start in
// the repository's WHERE clause (CLAUDE.md rule 10). Without CORE_DB_DSN they
// skip.

// integrationPool opens the test database named by CORE_DB_DSN, or skips.
// storagetest refuses a non-test database, which matters here: these fixtures
// are committed, not rolled back.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := storagetest.OpenCore(context.Background(), nil)
	if err != nil {
		t.Fatalf("open the test database: %v", err)
	}
	if pool == nil {
		t.Skip("set CORE_DB_DSN to run this test against a real database")
	}
	t.Cleanup(pool.Close)
	return pool
}

func makeUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, login string) uuid.UUID {
	t.Helper()
	created, err := postgres.NewUsers(pool).Create(ctx, users.User{
		Login: login, FullName: login, Status: users.StatusActive, PasswordHash: "not-a-real-hash",
	})
	if err != nil {
		t.Fatalf("create user %q: %v", login, err)
	}
	return created.ID
}

// makeContest inserts a running contest with direct SQL, skipping the
// lifecycle the contests service enforces, and deletes it when the test ends.
func makeContest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, author uuid.UUID, timing string, durationMin *int, startsAt *time.Time, endsAt time.Time) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO contests (created_by, status, timing, duration_min, starts_at, ends_at)
		VALUES ($1, 'running', $2, $3, $4, $5)
		RETURNING id`, author, timing, durationMin, startsAt, endsAt).Scan(&id)
	if err != nil {
		t.Fatalf("create contest: %v", err)
	}
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(clean, `DELETE FROM contests WHERE id = $1`, id)
	})
	return id
}

func realAdmitter(pool *pgxpool.Pool, now func() time.Time) (*participation.Admitter, *postgres.Registrations) {
	registrations := postgres.NewRegistrations(pool)
	return participation.New(participation.Config{
		Lookup: registrations, People: registrations, Gate: contests.NewGate(5 * time.Second),
		Audit: audit.New(postgres.NewAuditSink(pool)), UnitOfWork: storage.NewUnitOfWork(pool),
		Now: now,
	}), registrations
}

func read(account, contest uuid.UUID) participation.Request {
	return participation.Request{Contest: contest.String(), Account: account}
}

// A stranger, a contest that does not exist, a disqualified participant and a
// contest past its end are refused against the real schema.
func TestAdmissionAgainstTheRealSchemaAnswersTheGatesOwnStandingCheck(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()

	author := makeUser(t, ctx, pool, "author-"+uuid.NewString()[:8])
	enrolled := makeUser(t, ctx, pool, "enrolled-"+uuid.NewString()[:8])
	stranger := makeUser(t, ctx, pool, "stranger-"+uuid.NewString()[:8])
	disqualified := makeUser(t, ctx, pool, "disq-"+uuid.NewString()[:8])

	farFuture := time.Now().Add(24 * time.Hour)
	running := makeContest(t, ctx, pool, author, contests.TimingFixed, nil, nil, farFuture)
	elsewhere := makeContest(t, ctx, pool, author, contests.TimingFixed, nil, nil, farFuture)
	notRunningAnymore := makeContest(t, ctx, pool, author, contests.TimingFixed, nil, nil, time.Now().Add(-time.Minute))

	admitter, registrations := realAdmitter(pool, nil)
	for _, enrol := range []struct{ contest, account uuid.UUID }{
		{running, enrolled}, {elsewhere, stranger}, {running, disqualified}, {notRunningAnymore, enrolled},
	} {
		if _, err := registrations.Add(ctx, enrol.contest, enrol.account); err != nil {
			t.Fatalf("Add() = %v", err)
		}
	}
	disqualifiedReg, err := registrations.ByUser(ctx, running, disqualified)
	if err != nil {
		t.Fatalf("ByUser() = %v", err)
	}
	if err := registrations.SetStatus(ctx, disqualifiedReg.ID, contests.RegistrationDisqualified); err != nil {
		t.Fatalf("SetStatus() = %v", err)
	}
	reads := participation.NewReads(0)

	if _, err := admitter.Admit(ctx, reads, read(enrolled, running)); err != nil {
		t.Fatalf("an enrolled participant of a running contest was refused: %v", err)
	}
	for name, given := range map[string]struct {
		request participation.Request
		want    error
	}{
		// The same refusal as a contest that does not exist, so the answer
		// never confirms the contest exists.
		"a stranger to this contest":    {read(stranger, running), contests.ErrNotAParticipant},
		"a contest that does not exist": {read(enrolled, uuid.New()), contests.ErrNotAParticipant},
		"a disqualified participant":    {read(disqualified, running), contests.ErrNotAParticipant},
		// Past ends_at although nothing flipped the status to finished.
		"a contest past its own deadline": {read(enrolled, notRunningAnymore), contests.ErrDeadlinePassed},
	} {
		if _, err := admitter.Admit(ctx, reads, given.request); !errors.Is(err, given.want) {
			t.Errorf("%s: Admit() = %v, want %v", name, err, given.want)
		}
	}
}

// postgres.Registrations hands admission a contest without its titles and
// descriptions, whether it is acting or waiting, but with its languages.
func TestTheRealLookupHandsAdmissionAContestWithoutTranslations(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()

	author := makeUser(t, ctx, pool, "author-"+uuid.NewString()[:8])
	student := makeUser(t, ctx, pool, "student-"+uuid.NewString()[:8])
	contestID := makeContest(t, ctx, pool, author, contests.TimingFixed, nil, nil, time.Now().Add(24*time.Hour))
	contestStore := postgres.NewContests(pool)
	if err := contestStore.ReplaceLanguages(ctx, contestID, []contests.ContestLanguage{{Code: "en", IsDefault: true}}); err != nil {
		t.Fatalf("ReplaceLanguages() = %v", err)
	}
	if err := contestStore.ReplaceTranslations(ctx, contestID, []contests.Translation{
		{Lang: "en", Title: "Night in the archive", Description: "A long brief."},
	}); err != nil {
		t.Fatalf("ReplaceTranslations() = %v", err)
	}
	admitter, registrations := realAdmitter(pool, nil)
	if _, err := registrations.Add(ctx, contestID, student); err != nil {
		t.Fatalf("Add() = %v", err)
	}

	for name, opts := range map[string][]participation.Option{
		"acting":  nil,
		"waiting": {participation.MayWait()},
	} {
		admission, err := admitter.Admit(ctx, participation.NewReads(0), read(student, contestID), opts...)
		if err != nil {
			t.Fatalf("%s: Admit() = %v", name, err)
		}
		if len(admission.Contest.Translations) != 0 {
			t.Errorf("%s: translations = %v, want none", name, admission.Contest.Translations)
		}
		if admission.Contest.DefaultLanguage() != "en" {
			t.Errorf("%s: default language = %q, want en", name, admission.Contest.DefaultLanguage())
		}
	}
}

// The first read of content starts the clock, the events channel never does,
// and a later read holding the participant as it was before the start, as a
// racing read would, moves nothing. Under individual timing the start is the
// participant's clock; under fixed timing it makes the registration active.
func TestTheFirstReadStartsTheParticipantOnceAndTheEventsChannelNever(t *testing.T) {
	for _, timing := range []string{contests.TimingIndividual, contests.TimingFixed} {
		t.Run(timing, func(t *testing.T) {
			pool := integrationPool(t)
			ctx := context.Background()

			author := makeUser(t, ctx, pool, "author-"+uuid.NewString()[:8])
			student := makeUser(t, ctx, pool, "student-"+uuid.NewString()[:8])
			clock := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
			opened := clock.Add(-time.Hour)
			var duration *int
			if timing == contests.TimingIndividual {
				sixty := 60
				duration = &sixty
			}
			contestID := makeContest(t, ctx, pool, author, timing, duration, &opened, clock.Add(24*time.Hour))
			admitter, registrations := realAdmitter(pool, func() time.Time { return clock })
			if _, err := registrations.Add(ctx, contestID, student); err != nil {
				t.Fatalf("Add() = %v", err)
			}
			reads := participation.NewReads(0)

			if _, err := admitter.Admit(ctx, reads, read(student, contestID), participation.MayWait()); err != nil {
				t.Fatalf("Admit(MayWait) = %v", err)
			}
			if stored, err := registrations.ByUser(ctx, contestID, student); err != nil || stored.StartedAt != nil || stored.Status != contests.RegistrationRegistered {
				t.Fatalf("after the events channel: %+v, err = %v; want registered, no start", stored, err)
			}

			first, err := admitter.Admit(ctx, reads, read(student, contestID))
			if err != nil {
				t.Fatalf("Admit() = %v", err)
			}
			racing, err := admitter.Admit(ctx, reads, read(student, contestID))
			if err != nil {
				t.Fatalf("Admit() = %v", err)
			}
			if err := first.Start(ctx); err != nil {
				t.Fatalf("Start() = %v", err)
			}
			stored, err := registrations.ByUser(ctx, contestID, student)
			if err != nil || stored.StartedAt == nil || !stored.StartedAt.Equal(clock) || stored.Status != contests.RegistrationActive {
				t.Fatalf("after the first read: %+v, err = %v; want active, started at %v", stored, err, clock)
			}

			clock = clock.Add(7 * time.Minute)
			if err := racing.Start(ctx); err != nil {
				t.Fatalf("the racing read's Start() = %v", err)
			}
			again, err := registrations.ByUser(ctx, contestID, student)
			if err != nil || again.StartedAt == nil || !again.StartedAt.Equal(*stored.StartedAt) || again.Status != contests.RegistrationActive {
				t.Fatalf("after a second read: %+v, err = %v; want active, started at %v unchanged", again, err, stored.StartedAt)
			}
		})
	}
}

// The start's audit entry goes through the real transaction and sink: one
// entry for the read that recorded the start, with the caller's address from
// the request, and none for a racing read that lost it.
func TestAFirstReadAuditsTheStartOnceWithTheCallersAddress(t *testing.T) {
	pool := integrationPool(t)
	ctx := audit.WithRequestMeta(context.Background(), "203.0.113.9", "integration-test")

	author := makeUser(t, ctx, pool, "author-"+uuid.NewString()[:8])
	student := makeUser(t, ctx, pool, "student-"+uuid.NewString()[:8])
	clock := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	contestID := makeContest(t, ctx, pool, author, contests.TimingFixed, nil, nil, clock.Add(24*time.Hour))
	admitter, registrations := realAdmitter(pool, func() time.Time { return clock })
	if _, err := registrations.Add(ctx, contestID, student); err != nil {
		t.Fatalf("Add() = %v", err)
	}

	var admissions []*participation.Admission
	for range 2 {
		admission, err := admitter.Admit(ctx, participation.NewReads(0), read(student, contestID))
		if err != nil {
			t.Fatalf("Admit() = %v", err)
		}
		admissions = append(admissions, admission)
	}
	for i, admission := range admissions {
		if err := admission.Start(ctx); err != nil {
			t.Fatalf("read %d: Start() = %v", i+1, err)
		}
	}

	rows, err := pool.Query(ctx, `
		SELECT actor_id, host(ip) FROM audit_log
		WHERE action = $1 AND entity = 'contest' AND entity_id = $2`,
		audit.ActionParticipantStart, contestID.String())
	if err != nil {
		t.Fatalf("read the trail: %v", err)
	}
	type entry struct {
		actor   uuid.UUID
		address string
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (entry, error) {
		var e entry
		err := row.Scan(&e.actor, &e.address)
		return e, err
	})
	if err != nil {
		t.Fatalf("scan the trail: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d %s entries after two reads, want 1", len(entries), audit.ActionParticipantStart)
	}
	if entries[0].actor != student || entries[0].address != "203.0.113.9" {
		t.Errorf("entry = %+v, want actor %s from 203.0.113.9", entries[0], student)
	}
}
