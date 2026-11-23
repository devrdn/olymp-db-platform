package queryproxy

import (
	"context"
	"errors"
	"fmt"

	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
)

// ErrSchemaHidden is a contest that closed its catalogues (allow_catalog
// false). Serving the schema here would hand over what the closed catalogue
// and ErrDatabaseDeclined withhold.
var ErrSchemaHidden = errors.New("this contest does not show the game's schema")

// Schemas describes a contest's game (provisioning.SchemaReader).
type Schemas interface {
	Schema(ctx context.Context, contest provisioning.Contest, database string) (provisioning.Schema, error)
}

// WithSchemas supplies the reader behind Service.Schema. A build without it
// answers ErrSchemaHidden rather than panicking, which is the safe direction
// to fail.
func (s *Service) WithSchemas(schemas Schemas) *Service {
	s.schemas = schemas
	return s
}

// Schema describes the contest's game as it was built, for the console's
// schema panel. The caller has already admitted participant in contest
// (participation.Admitter); Schema does not admit them again, and the caller
// starts them once the schema is in hand, so a refused read, a hidden schema
// included, starts nothing. The catalogue flag is checked before provisioning
// or reading anything, so a hidden schema costs the cluster no connection and
// cannot be told apart from a slow one.
func (s *Service) Schema(ctx context.Context, contest contests.Contest, participant contests.Participant) (provisioning.Schema, error) {
	// A Service built without WithSchemas refuses rather than panics.
	if s.schemas == nil {
		return provisioning.Schema{}, ErrSchemaHidden
	}

	lookup, err := s.lookup.ForRun(ctx, contest.ID, participant.UserID)
	if err != nil {
		return provisioning.Schema{}, participation.LookupError(err)
	}
	// A registration removed and added back since admission is a different one,
	// and its instance is not the admitted participant's database.
	if lookup.Participant.ID != participant.ID {
		return provisioning.Schema{}, contests.ErrNotAParticipant
	}

	switch {
	case errors.Is(lookup.GameErr, provisioning.ErrNoGame):
		return provisioning.Schema{}, ErrNoGameYet
	case lookup.GameErr != nil:
		return provisioning.Schema{}, fmt.Errorf("%w: look up the contest's game: %w", ErrUnavailable, lookup.GameErr)
	}
	game := lookup.Game

	if !game.Policy.AllowCatalog {
		return provisioning.Schema{}, ErrSchemaHidden
	}

	database, err := s.databases.EnsureFrom(ctx, game, participant.ID, lookup.Instance, lookup.InstanceErr)
	if err != nil {
		return provisioning.Schema{}, provisionFailure(err)
	}

	schema, err := s.schemas.Schema(ctx, game, database)
	if err != nil {
		return provisioning.Schema{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return schema, nil
}
