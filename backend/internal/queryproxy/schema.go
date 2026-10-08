package queryproxy

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
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

// Schema answers what the game looks like, for the console's schema panel,
// to participant in contest: a pair the caller's Access has already admitted
// (the /play/schema handler admits it like every other /play read, and
// observes it). It does not admit them a second time.
//
// The schema is contest content like the story and the questions, so under
// individual timing a successful read starts the participant's clock
// (StartOnRead, which asks the gate again for the start itself) — only once
// it has been read, so a refused read, a hidden schema included, starts
// nothing.
//
// The game and the participant's copy of it come from the combined lookup
// Run uses, asked about the admitted contest and the participant's own
// account: a second round trip after Access's, which is what admitting every
// read in one place costs this endpoint. What that lookup says about the
// registration and the contest is not asked again — the admission already
// spoke for them.
//
// The catalogue flag is checked before the database is provisioned, and
// before anything is read: a contest that hides its schema must not be able
// to be told apart from one whose game is simply slow to answer, and the
// refusal must not cost the cluster a connection either.
func (s *Service) Schema(ctx context.Context, contest contests.Contest, participant contests.Participant, addr netip.Addr) (provisioning.Schema, error) {
	// A build with nothing to answer this has nothing to look up *with*.
	// internal/app builds a second, console-less Service for the participant
	// read endpoints when no Query Runner is deployed, and hands it nils for
	// the collaborators only Run uses — games and databases among them — so
	// it refuses, rather than turning "this deployment has no console" into a
	// panic mid-request.
	if s.schemas == nil {
		return provisioning.Schema{}, ErrSchemaHidden
	}

	// Only the lookup's own failures are read here: a registration gone since
	// Access found it is not_a_participant, anything else is ours.
	lookup, err := s.lookup.ForRun(ctx, contest.ID, participant.UserID)
	if _, err := classifyParticipant(lookup.Participant, err, "look up the contest's game and the participant's copy"); err != nil {
		return provisioning.Schema{}, err
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

	// The participant's own copy, which is what the panel claims to be
	// describing. Ensure may create it, which is work this endpoint pays for
	// on a participant's first visit — and is work their first query would
	// have paid for a moment later anyway.
	database, err := s.databases.EnsureFrom(ctx, game, participant.ID, lookup.Instance, lookup.InstanceErr)
	if err != nil {
		return provisioning.Schema{}, provisionFailure(err)
	}

	schema, err := s.schemas.Schema(ctx, game, database)
	if err != nil {
		return provisioning.Schema{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if _, err := s.StartOnRead(ctx, contest, participant, addr); err != nil {
		return provisioning.Schema{}, err
	}
	return schema, nil
}
