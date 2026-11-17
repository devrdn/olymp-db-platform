package settings_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func archiveFixture() (*fixture, *clock) {
	f := newFixture()
	c := &clock{now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	f.service = f.service.WithClock(c.Now)
	return f, c
}

func TestTheArchiveIsOffUntilSwitchedOn(t *testing.T) {
	f, _ := archiveFixture()

	on, err := f.service.ArchiveEnabled(context.Background())
	if err != nil || on {
		t.Errorf("ArchiveEnabled() = %v, %v, want false, nil", on, err)
	}
}

func TestSwitchingTheArchiveOnIsStoredAndAudited(t *testing.T) {
	f, _ := archiveFixture()
	actor := uuid.New()

	if err := f.service.SetArchiveEnabled(context.Background(), actor, true); err != nil {
		t.Fatalf("SetArchiveEnabled() = %v", err)
	}

	if got := f.repo.values["archive.enabled"]; got != "true" {
		t.Errorf("stored %q, want \"true\"", got)
	}
	if len(f.sink.entries) != 1 {
		t.Fatalf("%d audit entries, want 1", len(f.sink.entries))
	}
	e := f.sink.entries[0]
	if e.Action != audit.ActionSettingsArchive || e.Entity != "settings" || e.ActorID == nil || *e.ActorID != actor {
		t.Errorf("entry %+v, want settings.archive on settings by the actor", e)
	}
	if e.Payload["enabled"] != true {
		t.Errorf("payload %v, want enabled true", e.Payload)
	}
}

func TestAnArchiveSwitchAlreadyInPlaceRecordsNothing(t *testing.T) {
	f, _ := archiveFixture()

	if err := f.service.SetArchiveEnabled(context.Background(), uuid.New(), false); err != nil {
		t.Fatalf("SetArchiveEnabled(false) = %v", err)
	}

	if len(f.sink.entries) != 0 {
		t.Errorf("%d audit entries, want none", len(f.sink.entries))
	}
	if _, ok := f.repo.values["archive.enabled"]; ok {
		t.Error("a switch already in place was saved")
	}
}

func TestTheArchiveSwitchIsReusedForFiveSeconds(t *testing.T) {
	f, c := archiveFixture()
	ctx := context.Background()
	if on, _ := f.service.ArchiveEnabled(ctx); on {
		t.Fatal("archive on at the start")
	}
	f.repo.values["archive.enabled"] = "true"

	c.now = c.now.Add(settings.ArchiveSwitchTTL - 100*time.Millisecond)
	if on, _ := f.service.ArchiveEnabled(ctx); on {
		t.Error("the change was seen before the cache expired")
	}
	c.now = c.now.Add(100 * time.Millisecond)
	if on, _ := f.service.ArchiveEnabled(ctx); !on {
		t.Error("the change was not seen once the cache expired")
	}
}

func TestSwitchingTheArchiveTakesEffectInThisProcessAtOnce(t *testing.T) {
	f, _ := archiveFixture()
	ctx := context.Background()
	_, _ = f.service.ArchiveEnabled(ctx)

	if err := f.service.SetArchiveEnabled(ctx, uuid.New(), true); err != nil {
		t.Fatalf("SetArchiveEnabled() = %v", err)
	}

	if on, _ := f.service.ArchiveEnabled(ctx); !on {
		t.Error("ArchiveEnabled() = false right after switching on")
	}
}

func TestAFailedArchiveReadIsNotKept(t *testing.T) {
	f, _ := archiveFixture()
	ctx := context.Background()
	f.repo.Err = errors.New("database down")

	if _, err := f.service.ArchiveEnabled(ctx); err == nil {
		t.Fatal("a failed read returned no error")
	}
	f.repo.Err = nil
	f.repo.values["archive.enabled"] = "true"

	on, err := f.service.ArchiveEnabled(ctx)
	if err != nil || !on {
		t.Errorf("ArchiveEnabled() = %v, %v, want true, nil", on, err)
	}
}

func TestAFailedArchiveReadServesTheLastGoodValue(t *testing.T) {
	f, c := archiveFixture()
	ctx := context.Background()
	f.repo.values["archive.enabled"] = "true"
	if on, err := f.service.ArchiveEnabled(ctx); err != nil || !on {
		t.Fatalf("first read = %v, %v, want true, nil", on, err)
	}
	f.repo.Err = errors.New("database down")
	c.now = c.now.Add(settings.ArchiveSwitchTTL)

	on, err := f.service.ArchiveEnabled(ctx)
	if err != nil || !on {
		t.Errorf("ArchiveEnabled() during the outage = %v, %v, want true, nil", on, err)
	}
	f.repo.Err = nil
	f.repo.values["archive.enabled"] = "false"
	if on, _ := f.service.ArchiveEnabled(ctx); !on {
		t.Error("the store was asked again before the kept answer expired")
	}
	c.now = c.now.Add(settings.ArchiveSwitchTTL)
	if on, err := f.service.ArchiveEnabled(ctx); err != nil || on {
		t.Errorf("ArchiveEnabled() once the store is back = %v, %v, want false, nil", on, err)
	}
}

func TestTheArchiveSwitchStaysOutOfTheSettingsCatalogue(t *testing.T) {
	f, _ := archiveFixture()
	ctx := context.Background()

	err := f.service.Save(ctx, uuid.New(), settings.Values{"archive.enabled": "true"})
	if !errors.Is(err, settings.ErrUnknownKey) {
		t.Errorf("Save() = %v, want ErrUnknownKey", err)
	}
	all, _ := f.service.All(ctx)
	if _, ok := all["archive.enabled"]; ok {
		t.Error("All() lists the archive switch")
	}
}
