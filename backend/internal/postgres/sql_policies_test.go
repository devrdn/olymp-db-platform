package postgres

import (
	"context"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

func TestAContestNobodyConfiguredIsReadOnly(t *testing.T) {
	// The absence of a policy row must not read as "no restrictions".
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-policy-default")
		id := makeContest(t, ctx, author.ID)

		policy, err := NewSQLPolicies(testPool).ByContest(ctx, id)
		if err != nil {
			t.Fatalf("ByContest() = %v", err)
		}

		if policy.Mode != contests.ModeReadOnly {
			t.Errorf("mode = %q, want read_only", policy.Mode)
		}
		if !policy.AllowCatalog {
			t.Error("AllowCatalog = false, want the structural catalogs open by default")
		}
	})
}

func TestPolicySurvivesARoundTrip(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewSQLPolicies(testPool)
		author := makeUser(t, ctx, "author-policy")
		id := makeContest(t, ctx, author.ID)

		if err := repo.Save(ctx, contests.SQLPolicy{
			ContestID:       id,
			Mode:            contests.ModeReadWrite,
			WritableTables:  []string{"notes", "public.evidence"},
			AllowCreateView: true,
			AllowOwnTables:  true,
			AllowCatalog:    false,
			DiskQuotaRatio:  3,
			UpdatedBy:       &author.ID,
		}); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		loaded, err := repo.ByContest(ctx, id)
		if err != nil {
			t.Fatalf("ByContest() = %v", err)
		}
		switch {
		case loaded.Mode != contests.ModeReadWrite:
			t.Errorf("mode = %q, want read_write", loaded.Mode)
		case len(loaded.WritableTables) != 2:
			t.Errorf("writable tables = %v, want two", loaded.WritableTables)
		case !loaded.AllowCreateView || !loaded.AllowOwnTables || loaded.AllowCatalog:
			t.Errorf("flags = %+v, want them as saved", loaded)
		case loaded.DiskQuotaRatio != 3:
			t.Errorf("quota ratio = %d, want 3", loaded.DiskQuotaRatio)
		case loaded.UpdatedBy == nil || *loaded.UpdatedBy != author.ID:
			t.Errorf("updated_by = %v, want %v", loaded.UpdatedBy, author.ID)
		}
	})
}

func TestSavingThePolicyAgainReplacesIt(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewSQLPolicies(testPool)
		author := makeUser(t, ctx, "author-policy-twice")
		id := makeContest(t, ctx, author.ID)
		if err := repo.Save(ctx, contests.SQLPolicy{
			ContestID: id, Mode: contests.ModeReadWrite,
			WritableTables: []string{"notes"}, DiskQuotaRatio: 5,
		}); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		if err := repo.Save(ctx, contests.DefaultSQLPolicy(id)); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		loaded, _ := repo.ByContest(ctx, id)
		if loaded.Mode != contests.ModeReadOnly || len(loaded.WritableTables) != 0 {
			t.Errorf("policy = %+v, want it back to read-only with no writable tables", loaded)
		}
	})
}
