package settings

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/google/uuid"
)

// providerName is the shape of a sign-in provider's id; the package does not
// know which providers exist.
var providerName = regexp.MustCompile(`^[a-z]{1,32}$`)

// SignInProviderKey is where a provider's switch is stored: "true" is off,
// anything else or nothing is on. It is outside the Catalogue, so neither the
// settings screen nor Save can read or write it.
func SignInProviderKey(provider string) string {
	return "auth." + provider + ".disabled"
}

// DisabledSignInProviders reads the switches from the repository on every
// call; the sign-in routes must not honour an answer older than the
// administrator's click.
func (s *Service) DisabledSignInProviders(ctx context.Context) (map[string]bool, error) {
	stored, err := s.repo.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("read sign-in switches: %w", err)
	}
	off := map[string]bool{}
	for key, value := range stored {
		if provider, ok := switchedProvider(key); ok && value == "true" {
			off[provider] = true
		}
	}
	return off, nil
}

// switchedProvider is the provider a switch key belongs to; false when the
// key is not a switch.
func switchedProvider(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, "auth.")
	if !ok {
		return "", false
	}
	provider, ok := strings.CutSuffix(rest, ".disabled")
	return provider, ok && providerName.MatchString(provider)
}

// SetSignInProviderEnabled switches one provider on or off and records it in
// one transaction. A switch already in that position writes and records
// nothing.
func (s *Service) SetSignInProviderEnabled(ctx context.Context, actorID uuid.UUID, provider string, enabled bool) error {
	if !providerName.MatchString(provider) {
		return fmt.Errorf("%w: %q", ErrUnknownKey, provider)
	}
	off, err := s.DisabledSignInProviders(ctx)
	if err != nil {
		return err
	}
	if off[provider] == !enabled {
		return nil
	}

	value := "true"
	if enabled {
		value = "false"
	}
	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.Save(ctx, actorID, Values{SignInProviderKey(provider): value}); err != nil {
			return fmt.Errorf("save sign-in switch: %w", err)
		}
		return s.record(ctx, actorID, audit.ActionSettingsSignInProvider, map[string]any{"provider": provider, "enabled": enabled})
	})
}

// KeyArchiveEnabled is where the archive switch is stored: "true" is on,
// anything else or nothing is off. It is outside the Catalogue, so neither
// the settings screen nor Save can read or write it.
const KeyArchiveEnabled = "archive.enabled"

// ArchiveSwitchTTL is how long a process reuses its last read of the archive
// switch; every archive request asks, and one read per request is a cost
// with no observable gain.
const ArchiveSwitchTTL = 5 * time.Second

// archiveCache is the last answer for the archive switch. The zero value
// holds none.
type archiveCache struct {
	mu     sync.Mutex
	on     bool
	readAt time.Time
	read   bool
}

// ArchiveEnabled reports whether the contest archive is on. The answer is
// kept for ArchiveSwitchTTL, so another process sees a change within that
// time and this one at once (SetArchiveEnabled). A failed read serves the
// last answer read for another ArchiveSwitchTTL, so an outage costs one
// attempt per interval rather than one per request; it is an error only when
// this process has never read the switch.
func (s *Service) ArchiveEnabled(ctx context.Context) (bool, error) {
	c := &s.archive
	c.mu.Lock()
	defer c.mu.Unlock()

	now := s.now()
	if c.read && now.Sub(c.readAt) < ArchiveSwitchTTL {
		return c.on, nil
	}
	stored, err := s.repo.All(ctx)
	if err != nil {
		if c.read {
			c.readAt = now
			return c.on, nil
		}
		return false, fmt.Errorf("read archive switch: %w", err)
	}
	c.on, c.readAt, c.read = stored[KeyArchiveEnabled] == "true", now, true
	return c.on, nil
}

// SetArchiveEnabled switches the archive on or off and records it in one
// transaction. It compares against the stored value, not the cache, and a
// switch already in that position writes and records nothing.
func (s *Service) SetArchiveEnabled(ctx context.Context, actorID uuid.UUID, enabled bool) error {
	stored, err := s.repo.All(ctx)
	if err != nil {
		return fmt.Errorf("read archive switch: %w", err)
	}
	if (stored[KeyArchiveEnabled] == "true") == enabled {
		return nil
	}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.Save(ctx, actorID, Values{KeyArchiveEnabled: strconv.FormatBool(enabled)}); err != nil {
			return fmt.Errorf("save archive switch: %w", err)
		}
		return s.record(ctx, actorID, audit.ActionSettingsArchive, map[string]any{"enabled": enabled})
	})
	if err != nil {
		return err
	}

	c := &s.archive
	c.mu.Lock()
	c.on, c.readAt, c.read = enabled, s.now(), true
	c.mu.Unlock()
	return nil
}
