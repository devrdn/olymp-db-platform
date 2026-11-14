package settings_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/settings"
)

func TestEveryProviderIsOnUntilSwitchedOff(t *testing.T) {
	f := newFixture()

	off, err := f.service.DisabledSignInProviders(context.Background())
	if err != nil {
		t.Fatalf("DisabledSignInProviders() = %v", err)
	}
	if len(off) != 0 {
		t.Errorf("DisabledSignInProviders() = %v, want none", off)
	}
}

func TestSwitchingAProviderOffIsStoredAndAudited(t *testing.T) {
	f := newFixture()
	actor := uuid.New()

	if err := f.service.SetSignInProviderEnabled(context.Background(), actor, "google", false); err != nil {
		t.Fatalf("SetSignInProviderEnabled() = %v", err)
	}

	if got := f.repo.values["auth.google.disabled"]; got != "true" {
		t.Errorf("stored %q, want \"true\"", got)
	}
	off, _ := f.service.DisabledSignInProviders(context.Background())
	if len(off) != 1 || !off["google"] {
		t.Errorf("DisabledSignInProviders() = %v, want only google", off)
	}
	if len(f.sink.entries) != 1 {
		t.Fatalf("%d audit entries, want 1", len(f.sink.entries))
	}
	e := f.sink.entries[0]
	if e.Action != audit.ActionSettingsSignInProvider || e.Entity != "settings" || e.ActorID == nil || *e.ActorID != actor {
		t.Errorf("entry %+v, want settings.sign_in_provider on settings by the actor", e)
	}
	if e.Payload["provider"] != "google" || e.Payload["enabled"] != false {
		t.Errorf("payload %v, want provider google, enabled false", e.Payload)
	}
}

func TestSwitchingBackOnRestoresTheProvider(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	_ = f.service.SetSignInProviderEnabled(ctx, uuid.New(), "google", false)

	if err := f.service.SetSignInProviderEnabled(ctx, uuid.New(), "google", true); err != nil {
		t.Fatalf("SetSignInProviderEnabled() = %v", err)
	}

	if got := f.repo.values["auth.google.disabled"]; got != "false" {
		t.Errorf("stored %q, want \"false\"", got)
	}
	if off, _ := f.service.DisabledSignInProviders(ctx); len(off) != 0 {
		t.Errorf("DisabledSignInProviders() = %v, want none", off)
	}
	if len(f.sink.entries) != 2 {
		t.Errorf("%d audit entries, want 2", len(f.sink.entries))
	}
}

func TestASwitchAlreadyInPlaceRecordsNothing(t *testing.T) {
	f := newFixture()
	ctx := context.Background()

	if err := f.service.SetSignInProviderEnabled(ctx, uuid.New(), "google", true); err != nil {
		t.Fatalf("switching on an enabled provider = %v", err)
	}
	_ = f.service.SetSignInProviderEnabled(ctx, uuid.New(), "google", false)
	before := len(f.sink.entries)
	if err := f.service.SetSignInProviderEnabled(ctx, uuid.New(), "google", false); err != nil {
		t.Fatalf("switching off a disabled provider = %v", err)
	}

	if len(f.sink.entries) != before {
		t.Errorf("%d audit entries, want %d", len(f.sink.entries), before)
	}
	if _, stored := f.repo.values["auth.github.disabled"]; stored || len(f.repo.values) != 1 {
		t.Errorf("stored %v, want only google's key", f.repo.values)
	}
}

func TestTheSwitchesStayOutOfTheSettingsCatalogue(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	_ = f.service.SetSignInProviderEnabled(ctx, uuid.New(), "google", false)

	all, _ := f.service.All(ctx)
	public, _ := f.service.Public(ctx)
	for _, values := range []settings.Values{all, public} {
		for key := range values {
			if strings.HasPrefix(key, "auth.") {
				t.Errorf("%q is in the catalogue's answer", key)
			}
		}
	}
	err := f.service.Save(ctx, uuid.New(), settings.Values{"auth.google.disabled": "true"})
	if !errors.Is(err, settings.ErrUnknownKey) {
		t.Errorf("Save(auth key) = %v, want ErrUnknownKey", err)
	}
}

func TestAProviderNameOutOfShapeIsRefused(t *testing.T) {
	for _, name := range []string{"", "Google", strings.Repeat("a", 33), "goo gle", "../x"} {
		f := newFixture()

		err := f.service.SetSignInProviderEnabled(context.Background(), uuid.New(), name, false)

		if !errors.Is(err, settings.ErrUnknownKey) {
			t.Errorf("provider %q: %v, want ErrUnknownKey", name, err)
		}
		if len(f.repo.values) != 0 || len(f.sink.entries) != 0 {
			t.Errorf("provider %q: stored %v and recorded %d entries, want nothing", name, f.repo.values, len(f.sink.entries))
		}
	}
}
