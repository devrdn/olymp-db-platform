package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/clarifications"
	"github.com/devrdn/db-contest/backend/internal/clarifications/clarificationstest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func makeQuestion(t *testing.T, ctx context.Context, contest uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `
		INSERT INTO questions (contest_id, ord)
		VALUES ($1, (SELECT coalesce(max(ord), 0) + 1 FROM questions WHERE contest_id = $1))
		RETURNING id`, contest).Scan(&id)
	if err != nil {
		t.Fatalf("create question: %v", err)
	}
	return id
}

func TestClarificationsHonoursTheRepositoryContract(t *testing.T) {
	clarificationstest.RepositoryContract(t, func(t *testing.T, run func(context.Context, clarificationstest.Target)) {
		withTx(t, func(ctx context.Context) {
			author := makeUser(t, ctx, "clarif-author-"+uuid.NewString()[:8])
			run(ctx, clarificationstest.Target{
				Repo:       NewClarifications(testPool),
				NewContest: func() uuid.UUID { return makeContest(t, ctx, author.ID) },
				NewParticipant: func(contest uuid.UUID) clarificationstest.Participant {
					user := makeUser(t, ctx, "clarif-"+uuid.NewString()[:8])
					return clarificationstest.Participant{
						Registration: makeRegistration(t, ctx, contest, user.ID),
						Login:        user.Login, FullName: user.FullName,
					}
				},
				NewQuestion: func(contest uuid.UUID) uuid.UUID { return makeQuestion(t, ctx, contest) },
				NewHiddenQuestion: func(contest uuid.UUID) uuid.UUID {
					id := makeQuestion(t, ctx, contest)
					exec(t, ctx, `UPDATE questions SET is_visible = false WHERE id = $1`, id)
					return id
				},
				Close: func(registration, question uuid.UUID) {
					exec(t, ctx, `
						INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct, points_awarded)
						VALUES ($1, $2, 1, 'x', true, 1)`, registration, question)
				},
				NewStaff: func() (uuid.UUID, string) {
					user := makeUser(t, ctx, "clarif-staff-"+uuid.NewString()[:8])
					return user.ID, user.Login
				},
				Now: txNow(t, ctx).Truncate(time.Second),
			})
		})
	})
}

// Runs outside a rolled-back transaction: the race is between two
// connections, each asking at the last open place.
func TestConcurrentAsksNeverBothTakeTheLastOpenPlace(t *testing.T) {
	ctx, registration := committedRegistration(t)
	var contest uuid.UUID
	if err := testPool.QueryRow(ctx, `SELECT contest_id FROM registrations WHERE id = $1`, registration).Scan(&contest); err != nil {
		t.Fatal(err)
	}
	repo := NewClarifications(testPool)
	// Every racer dawdles after counting, so without the lock each would
	// count the same open place.
	repo.wrap = func(inner storage.Querier) storage.Querier { return slowCount{inner} }
	const limit, racers = 3, 8
	for range limit - 1 {
		if _, err := repo.Ask(ctx, clarifications.Clarification{
			ContestID: contest, RegistrationID: registration, Body: "q", CreatedAt: time.Now(),
		}, limit, false); err != nil {
			t.Fatal(err)
		}
	}

	var start, done sync.WaitGroup
	start.Add(1)
	errs := make(chan error, racers)
	for range racers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			_, err := repo.Ask(ctx, clarifications.Clarification{
				ContestID: contest, RegistrationID: registration, Body: "race", CreatedAt: time.Now(),
			}, limit, false)
			errs <- err
		}()
	}
	start.Done()
	done.Wait()
	close(errs)

	passed := 0
	for err := range errs {
		switch {
		case err == nil:
			passed++
		case !errors.Is(err, clarifications.ErrTooManyOpen):
			t.Fatalf("a racing ask failed: %v", err)
		}
	}
	if passed != 1 {
		t.Fatalf("%d racing asks took the last open place, want exactly 1", passed)
	}
	if open, err := repo.OpenCount(ctx, registration); err != nil || open != limit {
		t.Fatalf("OpenCount = %d, %v, want %d", open, err, limit)
	}
}

// slowCount pauses after counting a registration's open questions.
type slowCount struct{ storage.Querier }

func (q slowCount) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	row := q.Querier.QueryRow(ctx, sql, args...)
	if strings.Contains(sql, "count(*)") {
		return pausedRow{row}
	}
	return row
}

type pausedRow struct{ pgx.Row }

func (r pausedRow) Scan(dest ...any) error {
	err := r.Row.Scan(dest...)
	time.Sleep(50 * time.Millisecond)
	return err
}

// clarificationPlans records, per statement, the relations read without an
// index and the indexes read.
type clarificationPlans struct {
	storage.Querier
	t       *testing.T
	scanned map[string]bool
	indexes map[string]bool
}

func (q clarificationPlans) explain(ctx context.Context, sql string, args ...any) {
	q.t.Helper()
	var raw []byte
	if err := q.Querier.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+sql, args...).Scan(&raw); err != nil {
		q.t.Fatalf("EXPLAIN %s: %v", sql, err)
	}
	var plans []struct {
		Plan planNode `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil {
		q.t.Fatalf("read the plan: %v", err)
	}
	var walk func(n planNode)
	walk = func(n planNode) {
		if (n.Relation == "clarifications" || n.Relation == "announcements") && !rangeReads[n.NodeType] {
			q.scanned[n.Relation+" ("+n.NodeType+") in "+sql] = true
		}
		if strings.HasPrefix(n.Index, "clarifications_") || strings.HasPrefix(n.Index, "announcements_") {
			if n.IndexCond == "" {
				q.scanned[n.Index+" without a condition in "+sql] = true
			}
			q.indexes[n.Index] = true
		}
		for _, child := range n.Plans {
			walk(child)
		}
	}
	for _, p := range plans {
		walk(p.Plan)
	}
}

func (q clarificationPlans) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.explain(ctx, sql, args...)
	return q.Querier.Query(ctx, sql, args...)
}

func (q clarificationPlans) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	q.explain(ctx, sql, args...)
	return q.Querier.QueryRow(ctx, sql, args...)
}

// Every read a route offers is a range of the index migration 000049 made
// for it (CLAUDE.md rule 7), measured on an olympiad's worth of rows across
// several contests.
func TestEveryClarificationReadIsARangeOfItsIndex(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		q := storage.QuerierFrom(ctx, testPool)
		author := makeUser(t, ctx, "clarif-plan-"+uuid.NewString()[:8])
		var contest, registration uuid.UUID
		for i := range 6 {
			c := makeContest(t, ctx, author.ID)
			if _, err := q.Exec(ctx, `
				WITH people AS (
					INSERT INTO users (login, full_name, password_hash)
					SELECT 'clarif-plan-' || $1::text || '-' || n, 'P', 'x' FROM generate_series(1, 300) n
					RETURNING id
				), regs AS (
					INSERT INTO registrations (contest_id, user_id) SELECT $2, id FROM people RETURNING id
				)
				INSERT INTO clarifications (contest_id, registration_id, body, created_at)
				SELECT $2, r.id, 'q', now() - make_interval(secs => n)
				FROM regs r CROSS JOIN generate_series(1, 4) n`, uuid.NewString()[:8], c); err != nil {
				t.Fatalf("load contest %d: %v", i, err)
			}
			// A statement of its own: an UPDATE in the same WITH would not see
			// the rows its sibling inserted.
			if _, err := q.Exec(ctx, `
				UPDATE clarifications
				SET answer = 'a', answered_at = created_at + interval '1 minute',
				    visibility = CASE WHEN random() < 0.1 THEN 'public' ELSE 'private' END
				WHERE contest_id = $1 AND random() < 0.7`, c); err != nil {
				t.Fatalf("answer in contest %d: %v", i, err)
			}
			// A contest posts a few dozen; the table holds every contest's.
			notes := 1000
			if i == 5 {
				notes = 30
			}
			if _, err := q.Exec(ctx, `
				INSERT INTO announcements (contest_id, body, created_at)
				SELECT $1, 'note', now() - make_interval(mins => n) FROM generate_series(1, $2::int) n`, c, notes); err != nil {
				t.Fatal(err)
			}
			contest = c
		}
		if err := q.QueryRow(ctx, `SELECT id FROM registrations WHERE contest_id = $1 LIMIT 1`, contest).Scan(&registration); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"users", "registrations", "clarifications", "announcements"} {
			if _, err := q.Exec(ctx, "ANALYZE "+table); err != nil {
				t.Fatal(err)
			}
		}

		plans := clarificationPlans{t: t, scanned: map[string]bool{}, indexes: map[string]bool{}}
		repo := NewClarifications(testPool)
		repo.wrap = func(inner storage.Querier) storage.Querier {
			plans.Querier = inner
			return plans
		}
		latest := clarifications.Cursor{At: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), ID: uuid.Max}
		if _, err := repo.Feed(ctx, contest, registration, latest, 51, true); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.OpenCount(ctx, registration); err != nil {
			t.Fatal(err)
		}
		for _, state := range []clarifications.State{clarifications.StateOpen, clarifications.StateAnswered} {
			page, err := repo.Queue(ctx, contest, state, 50, uuid.Nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) == 0 {
				t.Fatalf("the %s queue is empty; the fixture seeds both states", state)
			}
			if _, err := repo.Queue(ctx, contest, state, 50, page[len(page)-1].ID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := repo.Counts(ctx, contest); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repo.Announcements(ctx, contest, 50, 0); err != nil {
			t.Fatal(err)
		}

		for scan := range plans.scanned {
			t.Errorf("read without an index range: %s", scan)
		}
		for _, index := range []string{
			"clarifications_registration_posted_idx", "clarifications_public_posted_idx",
			"clarifications_open_idx", "clarifications_answered_idx", "announcements_contest_time_idx",
		} {
			if !plans.indexes[index] {
				t.Errorf("no read used %s; read %v", index, plans.indexes)
			}
		}
	})
}
