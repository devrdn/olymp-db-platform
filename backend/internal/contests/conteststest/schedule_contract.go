package conteststest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// ScheduleTarget is a schedule over a contest store, the same store seen as a
// contests.Repository, and the means to seed a contest in a status and
// window.
type ScheduleTarget struct {
	Repo contests.ScheduleRepository
	// Contests is the store Repo reads and moves, for reading back what a
	// move left.
	Contests contests.Repository
	// Seed stores a contest in status with the given window. Either end may
	// be nil; when both are set, startsAt is before endsAt, as the schema
	// requires.
	Seed func(status string, startsAt, endsAt *time.Time) uuid.UUID
	// Now is the store's clock, which decides whether a window has opened or
	// closed.
	Now func() time.Time
}

// ScheduleRepositoryContract is what every contests.ScheduleRepository must
// do; both the in-memory Schedule and postgres.Contests run it. each prepares
// a fresh target for one case, calls run with it, and cleans up. TryLock
// refusing a second holder needs two transactions, so internal/postgres
// tests it across two connections.
//
// A shared database may hold contests a case did not make, so listings are
// checked against the case's own contests only.
func ScheduleRepositoryContract(t *testing.T, each func(t *testing.T, run func(context.Context, ScheduleTarget))) {
	at := func(target ScheduleTarget, d time.Duration) *time.Time {
		v := target.Now().Add(d)
		return &v
	}
	byID := func(t *testing.T, ctx context.Context, target ScheduleTarget, id uuid.UUID) contests.Contest {
		t.Helper()
		c, err := target.Contests.ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		return c
	}
	wantStatus := func(t *testing.T, ctx context.Context, target ScheduleTarget, id uuid.UUID, want, what string) {
		t.Helper()
		if got := byID(t, ctx, target, id).Status; got != want {
			t.Errorf("%s: status = %q, want %q", what, got, want)
		}
	}
	due := func(t *testing.T, ctx context.Context, target ScheduleTarget) []contests.Contest {
		t.Helper()
		found, err := target.Repo.DueToStart(ctx)
		if err != nil {
			t.Fatalf("DueToStart() = %v", err)
		}
		return found
	}
	dueIDs := func(t *testing.T, ctx context.Context, target ScheduleTarget) []uuid.UUID {
		t.Helper()
		found := due(t, ctx, target)
		ids := make([]uuid.UUID, 0, len(found))
		for _, c := range found {
			ids = append(ids, c.ID)
		}
		return ids
	}
	finish := func(t *testing.T, ctx context.Context, target ScheduleTarget, grace time.Duration) []uuid.UUID {
		t.Helper()
		ids, err := target.Repo.AdvanceFinished(ctx, grace)
		if err != nil {
			t.Fatalf("AdvanceFinished(%v) = %v", grace, err)
		}
		return ids
	}
	// matched requires each of want exactly once and none of not. Other
	// identifiers belong to contests the case did not make and are ignored.
	matched := func(t *testing.T, call string, got []uuid.UUID, want, not map[string]uuid.UUID) {
		t.Helper()
		seen := make(map[uuid.UUID]int, len(got))
		for _, id := range got {
			seen[id]++
		}
		for name, id := range want {
			if seen[id] != 1 {
				t.Errorf("%s returned the %s contest %d times, want once", call, name, seen[id])
			}
		}
		for name, id := range not {
			if seen[id] != 0 {
				t.Errorf("%s returned the %s contest, want it left out", call, name)
			}
		}
	}

	t.Run("TryLock wins an uncontested lock", func(t *testing.T) {
		each(t, func(ctx context.Context, target ScheduleTarget) {
			ok, err := target.Repo.TryLock(ctx)
			if err != nil {
				t.Fatalf("TryLock() = %v", err)
			}
			if !ok {
				t.Error("TryLock() = false, want the lock nobody else holds")
			}
		})
	})

	t.Run("DueToStart returns a published contest from the instant its start arrives", func(t *testing.T) {
		each(t, func(ctx context.Context, target ScheduleTarget) {
			atTheInstant := target.Seed(contests.StatusPublished, at(target, 0), nil)
			anHourAgo := target.Seed(contests.StatusPublished, at(target, -time.Hour), nil)

			matched(t, "DueToStart()", dueIDs(t, ctx, target),
				map[string]uuid.UUID{"starting now": atTheInstant, "started an hour ago": anHourAgo}, nil)
		})
	})

	t.Run("DueToStart leaves out a contest whose start is still ahead or unset", func(t *testing.T) {
		// A microsecond is the database clock's finest step.
		each(t, func(ctx context.Context, target ScheduleTarget) {
			matched(t, "DueToStart()", dueIDs(t, ctx, target), nil, map[string]uuid.UUID{
				"starting in a microsecond": target.Seed(contests.StatusPublished, at(target, time.Microsecond), nil),
				"starting in an hour":       target.Seed(contests.StatusPublished, at(target, time.Hour), nil),
				"with no start":             target.Seed(contests.StatusPublished, nil, nil),
			})
		})
	})

	t.Run("DueToStart returns only published contests", func(t *testing.T) {
		each(t, func(ctx context.Context, target ScheduleTarget) {
			not := map[string]uuid.UUID{}
			for _, status := range []string{
				contests.StatusDraft, contests.StatusRunning, contests.StatusFinished, contests.StatusArchived,
			} {
				not[status] = target.Seed(status, at(target, -time.Hour), nil)
			}

			matched(t, "DueToStart()", dueIDs(t, ctx, target), nil, not)
		})
	})

	t.Run("DueToStart returns the row ByID reads and moves nothing", func(t *testing.T) {
		// The scheduler re-runs the publish gate on the result, so it needs
		// languages and titles, and the gate decides whether it moves.
		each(t, func(ctx context.Context, target ScheduleTarget) {
			starts := at(target, -time.Hour)
			id := target.Seed(contests.StatusPublished, starts, nil)
			if err := target.Contests.ReplaceLanguages(ctx, id, []contests.ContestLanguage{
				{Code: "ro"}, {Code: "en", IsDefault: true},
			}); err != nil {
				t.Fatalf("ReplaceLanguages() = %v", err)
			}
			if err := target.Contests.ReplaceTranslations(ctx, id, []contests.Translation{
				{Lang: "en", Title: "The Library Murder", Description: "A body in the stacks."},
			}); err != nil {
				t.Fatalf("ReplaceTranslations() = %v", err)
			}
			before := byID(t, ctx, target, id)

			var got *contests.Contest
			for _, c := range due(t, ctx, target) {
				if c.ID == id {
					found := c
					got = &found
				}
			}
			if got == nil {
				t.Fatal("DueToStart() left out a published contest whose start has passed")
			}

			if got.Status != contests.StatusPublished {
				t.Errorf("Status = %q, want %q", got.Status, contests.StatusPublished)
			}
			if got.StartsAt == nil || !got.StartsAt.Equal(*starts) {
				t.Errorf("StartsAt = %v, want %v", got.StartsAt, *starts)
			}
			wantLanguages := []contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ro"}}
			if !reflect.DeepEqual(got.Languages, wantLanguages) {
				t.Errorf("Languages = %+v, want %+v", got.Languages, wantLanguages)
			}
			wantTitle := contests.Translation{Lang: "en", Title: "The Library Murder", Description: "A body in the stacks."}
			if len(got.Translations) != 1 || got.Translations["en"] != wantTitle {
				t.Errorf("Translations = %+v, want only %+v", got.Translations, wantTitle)
			}
			if !reflect.DeepEqual(*got, before) {
				t.Errorf("DueToStart() row = %+v,\nByID() row = %+v, want the same projection", *got, before)
			}

			if after := byID(t, ctx, target, id); !reflect.DeepEqual(after, before) {
				t.Errorf("after DueToStart() the contest reads %+v, want it untouched: %+v", after, before)
			}
			matched(t, "DueToStart() asked again", dueIDs(t, ctx, target), map[string]uuid.UUID{"due": id}, nil)
		})
	})

	t.Run("a contest SetStatus started is no longer due", func(t *testing.T) {
		each(t, func(ctx context.Context, target ScheduleTarget) {
			id := target.Seed(contests.StatusPublished, at(target, -time.Hour), nil)

			if err := target.Repo.SetStatus(ctx, id, contests.StatusPublished, contests.StatusRunning); err != nil {
				t.Fatalf("SetStatus() = %v", err)
			}

			wantStatus(t, ctx, target, id, contests.StatusRunning, "started contest")
			matched(t, "DueToStart()", dueIDs(t, ctx, target), nil, map[string]uuid.UUID{"started": id})
		})
	})

	t.Run("SetStatus refuses a contest whose status moved after DueToStart read it", func(t *testing.T) {
		// A manual transition got there first; the tick moves on.
		each(t, func(ctx context.Context, target ScheduleTarget) {
			id := target.Seed(contests.StatusPublished, at(target, -time.Hour), nil)
			matched(t, "DueToStart()", dueIDs(t, ctx, target), map[string]uuid.UUID{"due": id}, nil)
			if err := target.Contests.SetStatus(ctx, id, contests.StatusPublished, contests.StatusArchived); err != nil {
				t.Fatalf("the manual SetStatus() = %v", err)
			}

			err := target.Repo.SetStatus(ctx, id, contests.StatusPublished, contests.StatusRunning)

			if !errors.Is(err, contests.ErrStatusChanged) {
				t.Errorf("SetStatus() error = %v, want ErrStatusChanged", err)
			}
			wantStatus(t, ctx, target, id, contests.StatusArchived, "contest moved by hand")
		})
	})

	t.Run("SetStatus of a contest that is not there is ErrNotFound", func(t *testing.T) {
		// Deleted between the read and the write; the tick moves on.
		each(t, func(ctx context.Context, target ScheduleTarget) {
			err := target.Repo.SetStatus(ctx, uuid.New(), contests.StatusPublished, contests.StatusRunning)

			if !errors.Is(err, contests.ErrNotFound) {
				t.Errorf("SetStatus() error = %v, want ErrNotFound", err)
			}
		})
	})

	t.Run("AdvanceFinished finishes a running contest from the instant its end arrives", func(t *testing.T) {
		each(t, func(ctx context.Context, target ScheduleTarget) {
			atTheInstant := target.Seed(contests.StatusRunning, nil, at(target, 0))
			anHourAgo := target.Seed(contests.StatusRunning, at(target, -2*time.Hour), at(target, -time.Hour))

			matched(t, "AdvanceFinished(0)", finish(t, ctx, target, 0),
				map[string]uuid.UUID{"ending now": atTheInstant, "ended an hour ago": anHourAgo}, nil)

			for name, id := range map[string]uuid.UUID{"ending now": atTheInstant, "ended an hour ago": anHourAgo} {
				got := byID(t, ctx, target, id)
				if got.Status != contests.StatusFinished {
					t.Errorf("%s: status = %q, want %q", name, got.Status, contests.StatusFinished)
				}
				if !got.UpdatedAt.Equal(target.Now()) {
					t.Errorf("%s: UpdatedAt = %v, want the store's clock %v", name, got.UpdatedAt, target.Now())
				}
			}
		})
	})

	t.Run("AdvanceFinished leaves running a contest whose end is still ahead or unset", func(t *testing.T) {
		// A contest with no end (individual timing) stays running until an
		// organizer moves it.
		each(t, func(ctx context.Context, target ScheduleTarget) {
			left := map[string]uuid.UUID{
				"ending in a microsecond": target.Seed(contests.StatusRunning, nil, at(target, time.Microsecond)),
				"ending in an hour":       target.Seed(contests.StatusRunning, nil, at(target, time.Hour)),
				"with no end":             target.Seed(contests.StatusRunning, at(target, -24*time.Hour), nil),
			}

			matched(t, "AdvanceFinished(0)", finish(t, ctx, target, 0), nil, left)

			for name, id := range left {
				wantStatus(t, ctx, target, id, contests.StatusRunning, name)
			}
		})
	})

	t.Run("AdvanceFinished closes a contest at its end plus the grace, not before", func(t *testing.T) {
		// Late answers are admitted within the grace; closing inside it
		// would refuse by status what the deadline still allows.
		each(t, func(ctx context.Context, target ScheduleTarget) {
			const grace = 5 * time.Second
			inside := map[string]uuid.UUID{
				"ended two seconds ago": target.Seed(contests.StatusRunning, nil, at(target, -2*time.Second)),
				"whose grace ends in a microsecond": target.Seed(contests.StatusRunning, nil,
					at(target, -grace+time.Microsecond)),
			}
			past := map[string]uuid.UUID{
				"whose grace ends now":  target.Seed(contests.StatusRunning, nil, at(target, -grace)),
				"ended ten seconds ago": target.Seed(contests.StatusRunning, nil, at(target, -10*time.Second)),
			}

			matched(t, "AdvanceFinished(5s)", finish(t, ctx, target, grace), past, inside)

			for name, id := range inside {
				wantStatus(t, ctx, target, id, contests.StatusRunning, name)
			}
			for name, id := range past {
				wantStatus(t, ctx, target, id, contests.StatusFinished, name)
			}
		})
	})

	t.Run("AdvanceFinished finishes only running contests", func(t *testing.T) {
		each(t, func(ctx context.Context, target ScheduleTarget) {
			others := map[string]uuid.UUID{}
			for _, status := range []string{
				contests.StatusDraft, contests.StatusPublished, contests.StatusFinished, contests.StatusArchived,
			} {
				others[status] = target.Seed(status, at(target, -2*time.Hour), at(target, -time.Hour))
			}

			matched(t, "AdvanceFinished(0)", finish(t, ctx, target, 0), nil, others)

			for status, id := range others {
				wantStatus(t, ctx, target, id, status, status+" contest")
			}
		})
	})

	t.Run("AdvanceFinished reports a contest once, on the call that finished it", func(t *testing.T) {
		// The scheduler audits every identifier it is handed, so a repeat
		// would be recorded twice.
		each(t, func(ctx context.Context, target ScheduleTarget) {
			id := target.Seed(contests.StatusRunning, nil, at(target, -time.Hour))

			matched(t, "first AdvanceFinished(0)", finish(t, ctx, target, 0), map[string]uuid.UUID{"overdue": id}, nil)
			matched(t, "second AdvanceFinished(0)", finish(t, ctx, target, 0), nil, map[string]uuid.UUID{"finished": id})

			wantStatus(t, ctx, target, id, contests.StatusFinished, "finished contest")
		})
	})
}
