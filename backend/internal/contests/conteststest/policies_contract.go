package conteststest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// PolicyTarget is a store holding no policies yet, and the means to create
// the contest and account a policy names.
type PolicyTarget struct {
	Store      contests.PolicyStore
	NewContest func() uuid.UUID
	NewUser    func() uuid.UUID
	// Now is the store's clock, which stamps UpdatedAt.
	Now func() time.Time
}

// PolicyStoreContract is what every contests.PolicyStore must do; both the
// in-memory Policies and postgres.SQLPolicies run it. each prepares a fresh
// target for one case, calls run with it, and cleans up.
//
// What the database refuses by constraint is left out: SQLPolicy.Validate
// rules those policies out before a write. A missing contest is the
// exception, since no validation catches it, and is reported as ErrNotFound.
func PolicyStoreContract(t *testing.T, each func(t *testing.T, run func(context.Context, PolicyTarget))) {
	save := func(t *testing.T, ctx context.Context, target PolicyTarget, p contests.SQLPolicy) {
		t.Helper()
		if err := target.Store.Save(ctx, p); err != nil {
			t.Fatalf("Save() = %v", err)
		}
	}
	load := func(t *testing.T, ctx context.Context, target PolicyTarget, contest uuid.UUID) contests.SQLPolicy {
		t.Helper()
		p, err := target.Store.ByContest(ctx, contest)
		if err != nil {
			t.Fatalf("ByContest() = %v", err)
		}
		return p
	}
	// sameAs compares writable tables in order, and UpdatedAt against the
	// store's clock rather than want.
	sameAs := func(t *testing.T, got, want contests.SQLPolicy, updatedAt time.Time, what string) {
		t.Helper()
		if got.ContestID != want.ContestID {
			t.Errorf("%s: contest = %v, want %v", what, got.ContestID, want.ContestID)
		}
		if got.Mode != want.Mode {
			t.Errorf("%s: mode = %q, want %q", what, got.Mode, want.Mode)
		}
		if !slices.Equal(got.WritableTables, want.WritableTables) {
			t.Errorf("%s: writable tables = %v, want %v", what, got.WritableTables, want.WritableTables)
		}
		if got.AllowCreateView != want.AllowCreateView || got.AllowOwnTables != want.AllowOwnTables ||
			got.AllowTempTables != want.AllowTempTables || got.AllowCatalog != want.AllowCatalog {
			t.Errorf("%s: flags (view, own, temp, catalog) = (%t, %t, %t, %t), want (%t, %t, %t, %t)", what,
				got.AllowCreateView, got.AllowOwnTables, got.AllowTempTables, got.AllowCatalog,
				want.AllowCreateView, want.AllowOwnTables, want.AllowTempTables, want.AllowCatalog)
		}
		if got.DiskQuotaRatio != want.DiskQuotaRatio {
			t.Errorf("%s: disk quota ratio = %d, want %d", what, got.DiskQuotaRatio, want.DiskQuotaRatio)
		}
		switch {
		case want.UpdatedBy == nil && got.UpdatedBy != nil:
			t.Errorf("%s: updated by = %v, want nobody", what, *got.UpdatedBy)
		case want.UpdatedBy != nil && got.UpdatedBy == nil:
			t.Errorf("%s: updated by = nobody, want %v", what, *want.UpdatedBy)
		case want.UpdatedBy != nil && *got.UpdatedBy != *want.UpdatedBy:
			t.Errorf("%s: updated by = %v, want %v", what, *got.UpdatedBy, *want.UpdatedBy)
		}
		if !got.UpdatedAt.Equal(updatedAt) {
			t.Errorf("%s: UpdatedAt = %v, want %v", what, got.UpdatedAt, updatedAt)
		}
	}

	t.Run("a contest nobody configured is read-only with the catalogs open", func(t *testing.T) {
		each(t, func(ctx context.Context, target PolicyTarget) {
			contest := target.NewContest()

			got := load(t, ctx, target, contest)

			sameAs(t, got, contests.SQLPolicy{
				ContestID:      contest,
				Mode:           contests.ModeReadOnly,
				WritableTables: []string{},
				AllowCatalog:   true,
				DiskQuotaRatio: 5,
			}, time.Time{}, "default")
		})
	})

	t.Run("a saved policy comes back with every field", func(t *testing.T) {
		each(t, func(ctx context.Context, target PolicyTarget) {
			contest, author := target.NewContest(), target.NewUser()
			want := contests.SQLPolicy{
				ContestID: contest,
				Mode:      contests.ModeReadWrite,
				// Not in alphabetical order, so a store that sorts them shows.
				WritableTables:  []string{"public.evidence", "notes"},
				AllowCreateView: true,
				AllowOwnTables:  true,
				AllowTempTables: true,
				AllowCatalog:    false,
				DiskQuotaRatio:  3,
				UpdatedBy:       &author,
			}

			save(t, ctx, target, want)

			sameAs(t, load(t, ctx, target, contest), want, target.Now(), "loaded")
		})
	})

	t.Run("each flag is kept on its own", func(t *testing.T) {
		each(t, func(ctx context.Context, target PolicyTarget) {
			// One flag at a time, so a store that swapped or ignored one
			// shows.
			flags := []struct {
				name string
				set  func(*contests.SQLPolicy)
			}{
				{"create view", func(p *contests.SQLPolicy) { p.AllowCreateView = true }},
				{"own tables", func(p *contests.SQLPolicy) { p.AllowOwnTables = true }},
				{"temp tables", func(p *contests.SQLPolicy) { p.AllowTempTables = true }},
				{"catalog", func(p *contests.SQLPolicy) { p.AllowCatalog = true }},
			}
			for _, f := range flags {
				contest := target.NewContest()
				want := contests.SQLPolicy{ContestID: contest, Mode: contests.ModeReadWrite, DiskQuotaRatio: 2}
				f.set(&want)

				save(t, ctx, target, want)

				got := load(t, ctx, target, contest)
				if got.AllowCreateView != want.AllowCreateView || got.AllowOwnTables != want.AllowOwnTables ||
					got.AllowTempTables != want.AllowTempTables || got.AllowCatalog != want.AllowCatalog {
					t.Errorf("only %s set: flags (view, own, temp, catalog) = (%t, %t, %t, %t), want (%t, %t, %t, %t)", f.name,
						got.AllowCreateView, got.AllowOwnTables, got.AllowTempTables, got.AllowCatalog,
						want.AllowCreateView, want.AllowOwnTables, want.AllowTempTables, want.AllowCatalog)
				}
			}
		})
	})

	t.Run("saving a policy without writable tables or an author reads back without them", func(t *testing.T) {
		each(t, func(ctx context.Context, target PolicyTarget) {
			contest := target.NewContest()
			want := contests.SQLPolicy{ContestID: contest, Mode: contests.ModeReadOnly, AllowCatalog: true, DiskQuotaRatio: 7}

			save(t, ctx, target, want)

			got := load(t, ctx, target, contest)
			sameAs(t, got, want, target.Now(), "loaded")
			if got.WritableTables == nil {
				t.Error("loaded writable tables are nil, want an empty list")
			}
		})
	})

	t.Run("saving again replaces the whole policy", func(t *testing.T) {
		each(t, func(ctx context.Context, target PolicyTarget) {
			contest, author := target.NewContest(), target.NewUser()
			save(t, ctx, target, contests.SQLPolicy{
				ContestID: contest, Mode: contests.ModeReadWrite,
				WritableTables:  []string{"notes", "public.evidence"},
				AllowCreateView: true, AllowOwnTables: true, AllowTempTables: true,
				DiskQuotaRatio: 9, UpdatedBy: &author,
			})

			want := contests.DefaultSQLPolicy(contest)
			save(t, ctx, target, want)

			sameAs(t, load(t, ctx, target, contest), want, target.Now(), "loaded")
		})
	})

	t.Run("saving the same policy again changes nothing observable", func(t *testing.T) {
		each(t, func(ctx context.Context, target PolicyTarget) {
			contest, author := target.NewContest(), target.NewUser()
			want := contests.SQLPolicy{
				ContestID: contest, Mode: contests.ModeReadWrite,
				WritableTables: []string{"notes"}, DiskQuotaRatio: 4, UpdatedBy: &author,
			}
			save(t, ctx, target, want)

			save(t, ctx, target, want)

			sameAs(t, load(t, ctx, target, contest), want, target.Now(), "loaded")
		})
	})

	t.Run("each contest keeps its own policy", func(t *testing.T) {
		each(t, func(ctx context.Context, target PolicyTarget) {
			configured, untouched := target.NewContest(), target.NewContest()
			save(t, ctx, target, contests.SQLPolicy{
				ContestID: configured, Mode: contests.ModeReadWrite,
				WritableTables: []string{"notes"}, AllowOwnTables: true, DiskQuotaRatio: 2,
			})

			sameAs(t, load(t, ctx, target, untouched), contests.DefaultSQLPolicy(untouched), time.Time{}, "the other contest")

			want := contests.SQLPolicy{ContestID: untouched, Mode: contests.ModeReadOnly, DiskQuotaRatio: 8}
			save(t, ctx, target, want)
			sameAs(t, load(t, ctx, target, untouched), want, target.Now(), "the other contest once configured")
			if got := load(t, ctx, target, configured); got.Mode != contests.ModeReadWrite || got.DiskQuotaRatio != 2 {
				t.Errorf("the first contest = %+v, want it as it was saved", got)
			}
		})
	})

	t.Run("saving the policy of a contest that is not there is reported", func(t *testing.T) {
		each(t, func(ctx context.Context, target PolicyTarget) {
			err := target.Store.Save(ctx, contests.DefaultSQLPolicy(uuid.New()))

			if !errors.Is(err, contests.ErrNotFound) {
				t.Errorf("Save() for an unknown contest error = %v, want ErrNotFound", err)
			}
		})
	})

	t.Run("the lists a caller holds are not the store's", func(t *testing.T) {
		each(t, func(ctx context.Context, target PolicyTarget) {
			contest := target.NewContest()
			tables := []string{"notes", "evidence"}
			save(t, ctx, target, contests.SQLPolicy{
				ContestID: contest, Mode: contests.ModeReadWrite, WritableTables: tables, DiskQuotaRatio: 5,
			})

			// Writing to a slice after Save or a read is not saving.
			tables[0] = "scribbled"
			loaded := load(t, ctx, target, contest).WritableTables
			if len(loaded) < 2 {
				t.Fatalf("WritableTables = %v, want the two saved", loaded)
			}
			loaded[1] = "overwritten"

			if got := load(t, ctx, target, contest).WritableTables; !slices.Equal(got, []string{"notes", "evidence"}) {
				t.Errorf("writable tables = %v, want [notes evidence]", got)
			}
		})
	})
}
