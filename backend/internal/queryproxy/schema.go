package queryproxy

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// ErrSchemaHidden is a contest that closed its catalogues.
//
// The same policy flag, and the same reasoning, as ErrDatabaseDeclined above:
// a contest whose organiser set allow_catalog to false meant the shape of the
// game to be discovered by playing it. Serving that shape from an endpoint of
// our own would hand over exactly what closing the catalogue withholds — and
// would make the care ErrDatabaseDeclined takes over PostgreSQL's own words
// pointless, since anybody could simply ask this instead.
var ErrSchemaHidden = errors.New("this contest does not show the game's schema")

// Schemas describes a contest's game. provisioning.SchemaReader is the
// implementation; this façade only needs the one question.
type Schemas interface {
	Schema(ctx context.Context, contest provisioning.Contest, database string) (provisioning.Schema, error)
}

// WithSchemas supplies the reader behind Service.Schema.
//
// An option rather than a constructor argument because the schema panel is
// not what this façade is for: every deployment sets it, and a build that
// forgot to answers ErrSchemaHidden rather than panicking mid-contest — the
// same thing a contest that deliberately closed its catalogues says, which is
// the safe direction for a missing wire to fail in.
func (s *Service) WithSchemas(schemas Schemas) *Service {
	s.schemas = schemas
	return s
}

// Schema answers what the game looks like, for the console's schema panel.
//
// The same admission every other participant-facing read requires (Access):
// registered and not disqualified or finished, the contest open to them,
// their address allowed. Their own clock is not started by asking — Access
// never starts one, and opening a screen is not the deliberate action §8
// means by starting.
//
// The catalogue flag is checked before the database is provisioned, and
// before anything is read: a contest that hides its schema must not be able
// to be told apart from one whose game is simply slow to answer, and the
// refusal must not cost the cluster a connection either.
func (s *Service) Schema(ctx context.Context, contestID, userID uuid.UUID, addr netip.Addr) (provisioning.Schema, error) {
	participant, _, err := s.Access(ctx, contestID, userID, addr)
	if err != nil {
		return provisioning.Schema{}, err
	}

	// Before anything is looked up, because a build with nothing to answer
	// this has nothing to look up *with*. internal/app builds a second,
	// console-less Service for the participant read endpoints when no Query
	// Runner is deployed, and hands it nils for the three collaborators only
	// Run uses — games and databases among them. Asking those first would
	// turn "this deployment has no console" into a panic mid-request.
	if s.schemas == nil {
		return provisioning.Schema{}, ErrSchemaHidden
	}

	game, err := s.games.Game(ctx, contestID)
	switch {
	case errors.Is(err, provisioning.ErrNoGame):
		return provisioning.Schema{}, ErrNoGameYet
	case err != nil:
		return provisioning.Schema{}, fmt.Errorf("%w: look up the contest's game: %w", ErrUnavailable, err)
	}

	if !game.Policy.AllowCatalog {
		return provisioning.Schema{}, ErrSchemaHidden
	}

	// The participant's own copy, which is what the panel claims to be
	// describing. Ensure may create it, which is work this endpoint pays for
	// on a participant's first visit — and is work their first query would
	// have paid for a moment later anyway.
	database, err := s.databases.Ensure(ctx, game, participant.ID)
	if err != nil {
		return provisioning.Schema{}, provisionFailure(err)
	}

	schema, err := s.schemas.Schema(ctx, game, database)
	if err != nil {
		return provisioning.Schema{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return schema, nil
}
