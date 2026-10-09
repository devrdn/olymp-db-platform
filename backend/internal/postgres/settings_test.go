package postgres

import (
	"context"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/google/uuid"
)

func TestSettingsSurviveARoundTrip(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewSettings(testPool)
		admin := makeUser(t, ctx, "settings-admin")

		if err := repo.Save(ctx, admin.ID, settings.Values{
			settings.KeyName:    "Universitatea Tehnică",
			settings.KeyContact: "olimpiada@example.edu",
		}); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		all, err := repo.All(ctx)
		if err != nil {
			t.Fatalf("All() = %v", err)
		}
		if all[settings.KeyName] != "Universitatea Tehnică" {
			t.Errorf("name = %q", all[settings.KeyName])
		}
		if all[settings.KeyContact] != "olimpiada@example.edu" {
			t.Errorf("contact = %q", all[settings.KeyContact])
		}
	})
}

func TestSavingASettingTwiceReplacesItRatherThanFailing(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewSettings(testPool)
		admin := makeUser(t, ctx, "settings-twice")

		for _, name := range []string{"Before", "After"} {
			if err := repo.Save(ctx, admin.ID, settings.Values{settings.KeyName: name}); err != nil {
				t.Fatalf("Save(%q) = %v", name, err)
			}
		}

		all, _ := repo.All(ctx)
		if all[settings.KeyName] != "After" {
			t.Errorf("name = %q, want the later value", all[settings.KeyName])
		}
	})
}

func TestASystemActorLeavesNoAuthorRatherThanAnInventedOne(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewSettings(testPool)

		if err := repo.Save(ctx, uuid.Nil, settings.Values{settings.KeyName: "Seeded"}); err != nil {
			t.Fatalf("Save() = %v", err)
		}

		// Read through the transaction: the row is not committed yet.
		var author *uuid.UUID
		err := storage.QuerierFrom(ctx, testPool).
			QueryRow(ctx, `SELECT updated_by FROM settings WHERE key = $1`, settings.KeyName).
			Scan(&author)
		if err != nil {
			t.Fatalf("read updated_by: %v", err)
		}
		if author != nil {
			t.Errorf("updated_by = %v, want it left empty", author)
		}
	})
}
