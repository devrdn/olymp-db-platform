package queryproxy

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// Why a reset was not done. The gate's refusals and the account's rate are
// passed through as Run passes them.
var (
	// ErrResetReadOnly is a contest whose policy writes nothing, so a
	// participant's database is always as the template made it.
	ErrResetReadOnly = errors.New("a read-only contest's database never changes, so there is nothing to reset")
	// ErrResetTooOften is a registration past ResetsPerWindow.
	ErrResetTooOften = errors.New("this participant has reset their database as often as allowed for now")
)

// Each reset drops the participant's database and copies the whole template
// through the copy slots the spare pool shares, so the bound counts presses:
// three in ten minutes undoes a mistake and a retry or two, and holds one
// participant to eighteen copies an hour however fast they press.
const (
	ResetsPerWindow = 3
	ResetWindow     = 10 * time.Minute
)

// ResetCommand is one participant asking for their database back as the
// contest started it. Like Command it names no database.
type ResetCommand struct {
	ContestID uuid.UUID
	UserID    uuid.UUID
	// Address is checked against the contest's network restriction.
	Address netip.Addr
	// Session is monitor.SessionTag, never the token.
	Session   string
	UserAgent string
}

// Reset remakes the caller's own database from the contest's template. The
// two rate checks come first, as in Run: the account's shared budget before
// any lookup, then this registration's resets before the gate, so every
// refusal after the lookup, a read-only contest's included, spends a press.
//
// A registration with no database yet is answered as done: the first query
// copies a fresh one anyway. A reset starts no clock, since whatever made the
// database already did.
func (s *Service) Reset(ctx context.Context, cmd ResetCommand) error {
	if err := s.rate.Admit(cmd.UserID.String(), s.perMinuteDefault); err != nil {
		return err
	}

	lookup, lookupErr := s.lookup.ForRun(ctx, cmd.ContestID, cmd.UserID)
	participant, err := classifyParticipant(lookup.Participant, lookupErr, "look up the participant and the contest")
	if err != nil {
		return err
	}
	if err := s.resets.Admit(participant.ID.String(), ResetsPerWindow); err != nil {
		return ErrResetTooOften
	}

	contest := lookup.Contest
	if err := s.admit(contest, participant, cmd.Address); err != nil {
		return err
	}
	if s.watcher != nil {
		s.watcher.Observe(ctx, monitor.Visit{
			Contest: contest.ID, Registration: participant.ID,
			Address: cmd.Address, Session: cmd.Session, UserAgent: cmd.UserAgent,
		})
	}

	switch {
	case errors.Is(lookup.GameErr, provisioning.ErrNoGame):
		return ErrNoGameYet
	case lookup.GameErr != nil:
		return fmt.Errorf("%w: look up the contest's game: %w", ErrUnavailable, lookup.GameErr)
	}
	if lookup.Game.Policy.Mode != sqlpolicy.ModeReadWrite {
		return ErrResetReadOnly
	}

	switch err := s.databases.Reset(ctx, cmd.UserID, lookup.Game, participant.ID); {
	case errors.Is(err, provisioning.ErrNoInstance):
		return nil
	case err != nil:
		return provisionFailure(err)
	}
	return nil
}
