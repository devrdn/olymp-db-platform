package settings_test

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/google/uuid"
)

func TestAFreshInstallationReadsAsSomethingRatherThanBlanks(t *testing.T) {
	f := newFixture()

	all, err := f.service.All(context.Background())
	if err != nil {
		t.Fatalf("All() = %v", err)
	}

	if all[settings.KeyName] != "DB Contest" {
		t.Errorf("name = %q, want the fallback", all[settings.KeyName])
	}
}

func TestSavedValuesReplaceTheFallback(t *testing.T) {
	ctx := context.Background()
	f := newFixture()

	if err := f.service.Save(ctx, uuid.New(), settings.Values{
		settings.KeyName: "Universitatea Tehnică",
	}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	all, _ := f.service.All(ctx)
	if all[settings.KeyName] != "Universitatea Tehnică" {
		t.Errorf("name = %q, want what was saved", all[settings.KeyName])
	}
}

func TestOnlyTheSettingsMarkedPublicLeaveWithoutASession(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	f.repo.values["installation.smtp_password"] = "hunter2"

	public, err := f.service.Public(ctx)
	if err != nil {
		t.Fatalf("Public() = %v", err)
	}

	if _, leaked := public["installation.smtp_password"]; leaked {
		t.Errorf("a setting nobody declared public was published: %v", public)
	}
	if public[settings.KeyName] == "" {
		t.Error("the installation's name is public and should be there")
	}
}

func TestASettingNothingReadsIsRefused(t *testing.T) {
	f := newFixture()

	err := f.service.Save(context.Background(), uuid.New(), settings.Values{
		"instalation.name": "typo",
	})

	if !errors.Is(err, settings.ErrUnknownKey) {
		t.Errorf("Save() = %v, want it refused for an unknown key", err)
	}
}

func TestAnInvalidValueIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	ctx := context.Background()
	f := newFixture()

	err := f.service.Save(ctx, uuid.New(), settings.Values{
		settings.KeyName:    "Universitatea Tehnică",
		settings.KeyContact: "not an address",
	})

	if !errors.Is(err, settings.ErrInvalid) {
		t.Fatalf("Save() = %v, want ErrInvalid", err)
	}
	// The valid half of a refused save must not land.
	if len(f.repo.values) != 0 {
		t.Errorf("stored %v, want nothing written", f.repo.values)
	}
}

func TestTheNameCannotBeEmptied(t *testing.T) {
	f := newFixture()

	err := f.service.Save(context.Background(), uuid.New(), settings.Values{
		settings.KeyName: "   ",
	})

	if !errors.Is(err, settings.ErrInvalid) {
		t.Errorf("Save() = %v, want an empty name refused", err)
	}
}

func TestSavingRecordsWhatMovedAndWhatItWas(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.service.Save(ctx, uuid.New(), settings.Values{settings.KeyName: "Before"}); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	f.sink.entries = nil

	if err := f.service.Save(ctx, uuid.New(), settings.Values{settings.KeyName: "After"}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	if len(f.sink.entries) != 1 {
		t.Fatalf("recorded %d entries, want one", len(f.sink.entries))
	}
	changes, ok := f.sink.entries[0].Payload["changes"].(map[string]any)
	if !ok {
		t.Fatalf("payload carries no change set: %+v", f.sink.entries[0].Payload)
	}
	moved, ok := changes[settings.KeyName].(map[string]any)
	if !ok {
		t.Fatalf("the name is not in the change set: %+v", changes)
	}
	if moved["from"] != "Before" || moved["to"] != "After" {
		t.Errorf("change = %v, want Before -> After", moved)
	}
}

func TestASaveThatChangesNothingSaysSo(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	if err := f.service.Save(ctx, uuid.New(), settings.Values{settings.KeyName: "Same"}); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	f.sink.entries = nil

	if err := f.service.Save(ctx, uuid.New(), settings.Values{settings.KeyName: "Same"}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	if len(f.sink.entries) != 1 || f.sink.entries[0].Payload["changed"] != false {
		t.Errorf("payload = %+v, want it to say nothing changed", f.sink.entries[0].Payload)
	}
}
