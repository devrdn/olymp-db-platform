package queryproxy

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
	"github.com/devrdn/olymp-db-platform/backend/internal/queryrunner"
	"github.com/devrdn/olymp-db-platform/backend/internal/sqlpolicy"
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
	// ContestID is the identifier as the caller wrote it, as in Command.
	ContestID string
	UserID    uuid.UUID
	// Address is checked against the contest's network restriction.
	Address netip.Addr
	// Session is monitor.SessionTag, never the token.
	Session   string
	UserAgent string
}

// resetBudget is the reset's budget as admission charges it: the account's
// query rate, shared with the console, before any lookup, then this
// registration's resets before the gate, so every refusal after the lookup, a
// read-only contest's included, spends a press.
type resetBudget struct{ s *Service }

func (b resetBudget) Charge(_ context.Context, account uuid.UUID) error {
	return b.s.rate.Admit(account.String(), b.s.perMinuteDefault)
}

func (b resetBudget) ChargeRegistration(_ context.Context, participant contests.Participant, _ contests.Contest) error {
	if err := b.s.resets.Admit(participant.ID.String(), ResetsPerWindow); err != nil {
		return ErrResetTooOften
	}
	return nil
}

// Reset remakes the caller's own database from the contest's template, once
// admission has spent resetBudget and asked the gate. Like Run it refuses once
// nothing is left to answer, after the checks made from values in hand.
//
// A registration with no database yet is answered as done: the first query
// copies a fresh one anyway. A reset starts no clock, since whatever made the
// database already did.
func (s *Service) Reset(ctx context.Context, cmd ResetCommand) error {
	admission, lookup, err := s.admit(ctx, resetBudget{s}, cmd.ContestID, cmd.UserID, cmd.Address, cmd.Session, cmd.UserAgent)
	if err != nil {
		return err
	}
	participant := admission.Participant

	switch {
	case errors.Is(lookup.GameErr, provisioning.ErrNoGame):
		return ErrNoGameYet
	case lookup.GameErr != nil:
		return fmt.Errorf("%w: look up the contest's game: %w", ErrUnavailable, lookup.GameErr)
	}
	if lookup.Game.Policy.Mode != sqlpolicy.ModeReadWrite {
		return ErrResetReadOnly
	}
	if s.answerable != nil {
		left, err := s.answerable.AnswerableLeft(ctx, admission.Contest.ID, participant.ID)
		if err != nil {
			return fmt.Errorf("%w: work out what is still answerable: %w", ErrUnavailable, err)
		}
		if !left {
			return ErrNothingLeftToAnswer
		}
	}

	switch err := s.databases.Reset(ctx, cmd.UserID, lookup.Game, participant.ID); {
	case errors.Is(err, provisioning.ErrNoInstance):
		return nil
	case ctx.Err() != nil && errors.Is(err, context.Canceled):
		// Most often a second press that gave up waiting for the first.
		return fmt.Errorf("%w: waiting for the reset: %w", queryrunner.ErrCanceled, err)
	case err != nil:
		return provisionFailure(err)
	}
	return nil
}
