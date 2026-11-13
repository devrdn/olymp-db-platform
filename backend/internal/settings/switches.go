package settings

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/audit"
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
