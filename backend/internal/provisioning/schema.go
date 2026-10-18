package provisioning

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

// ErrNoSchema is a template whose shape is not cached: a game not built yet,
// or built before builds described their template (Games.describe).
var ErrNoSchema = errors.New("the game's schema is not known yet")

// Schema is the shape of a game database as the console's schema panel draws
// it: tables, columns, types and foreign-key targets only. Indexes,
// constraints and privileges are left to the catalogue.
type Schema struct {
	Tables []Table `json:"tables"`
	// Truncated says the game has more tables, or a table more columns, than
	// this document carries.
	Truncated bool `json:"truncated,omitempty"`
}

// Table is one relation and its columns, in declaration order.
type Table struct {
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
}

// Column is one column, named with the type the participant would have to
// write to compare against it.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable,omitempty"`
	// References names the table a foreign key points at, empty when none.
	References string `json:"references,omitempty"`
}

// MaxSchemaTables and MaxSchemaColumns bound the document (CLAUDE.md rule 2).
// The init script is an organiser's own SQL, and the document is stored as one
// jsonb value and sent to every participant's browser.
const (
	MaxSchemaTables  = 200
	MaxSchemaColumns = 200
)

// SchemaStore is the schema cache. CachedSchema reports ErrNoSchema when
// nothing is cached; the version it returns is the template build the
// document describes, so a rebuild is detected without explicit invalidation.
// SaveSchema writes nothing unless version is the game's current one.
type SchemaStore interface {
	CachedSchema(ctx context.Context, contestID uuid.UUID) (Schema, int, error)
	SaveSchema(ctx context.Context, contestID uuid.UUID, version int, schema Schema) error
}

// SchemaSource reads one database's schema from the game cluster.
type SchemaSource interface {
	ReadSchema(ctx context.Context, database string) (Schema, error)
}

// SchemaReader answers what a contest's game looks like. It is separate from
// Service and declares only the methods it uses (CLAUDE.md Go layout rule 3).
type SchemaReader struct {
	store  SchemaStore
	source SchemaSource
	// reads collapses concurrent misses of one contest into one catalogue read.
	reads singleflight.Group
}

// schemaReadTimeout bounds the shared catalogue read, which is detached from
// the caller's deadline. It sits above the ten-second statement timeout
// gamedb sets, so it bounds dialling a cluster that never answers.
const schemaReadTimeout = 30 * time.Second

// NewSchemaReader assembles the reader.
func NewSchemaReader(store SchemaStore, source SchemaSource) *SchemaReader {
	return &SchemaReader{store: store, source: source}
}

// Schema answers what contest's game looks like, reading database only if it
// has to.
//
// The build caches the description, read from the template before the game is
// ready (Games.describe), so for a game built since, this is a cache read. A
// miss reads database, the participant's own copy, and never the template:
// CREATE DATABASE ... TEMPLATE fails while anything is connected to it
// (SQLSTATE 55006). A copy's public schema is the template's for everything a
// participant's role can do, since their own objects live in `work`, which is
// not read, and only game_author may alter the game's tables. A copy read
// because the cache could not be read is not saved, so it never replaces the
// build's description.
//
// Cache failures never refuse the answer; only a cluster failure does, since
// an empty panel would claim the game has no tables.
//
// At a contest's start every participant misses at once, so misses are
// collapsed into one read per contest and version (singleflight), and a
// rebuild is a separate flight. The cache is checked again inside the flight
// for a caller that arrives after the winner saved. The shared read drops the
// first caller's cancellation, so one participant leaving does not fail the
// others, and takes schemaReadTimeout instead.
func (r *SchemaReader) Schema(ctx context.Context, contest Contest, database string) (Schema, error) {
	if cached, hit, _ := r.cached(ctx, contest); hit {
		return cached, nil
	}

	answer, err, _ := r.reads.Do(fmt.Sprintf("%s@%d", contest.ID, contest.Version), func() (any, error) {
		cached, hit, readable := r.cached(ctx, contest)
		if hit {
			return cached, nil
		}

		shared, cancel := context.WithTimeout(context.WithoutCancel(ctx), schemaReadTimeout)
		defer cancel()

		schema, err := r.source.ReadSchema(shared, database)
		if err != nil {
			return nil, fmt.Errorf("read the game's schema: %w", err)
		}
		schema = boundSchema(schema)

		if readable {
			// Best effort: the next caller reads again if this fails.
			_ = r.store.SaveSchema(shared, contest.ID, contest.Version, schema)
		}
		return schema, nil
	})
	if err != nil {
		return Schema{}, err
	}
	return answer.(Schema), nil
}

// cached returns the cached document when it describes the build the contest
// is running (hit). readable is false when the cache itself failed, as opposed
// to holding nothing for this build.
func (r *SchemaReader) cached(ctx context.Context, contest Contest) (schema Schema, hit, readable bool) {
	cached, version, err := r.store.CachedSchema(ctx, contest.ID)
	switch {
	case errors.Is(err, ErrNoSchema):
		return Schema{}, false, true
	case err != nil:
		return Schema{}, false, false
	case version != contest.Version:
		return Schema{}, false, true
	}
	return cached, true, true
}

// boundSchema cuts the document to MaxSchemaTables and MaxSchemaColumns and
// sets Truncated if it had to. It runs before the cache write so the stored
// jsonb is bounded too (CLAUDE.md rule 2).
func boundSchema(schema Schema) Schema {
	if len(schema.Tables) > MaxSchemaTables {
		schema.Tables = schema.Tables[:MaxSchemaTables]
		schema.Truncated = true
	}
	for i, table := range schema.Tables {
		if len(table.Columns) > MaxSchemaColumns {
			schema.Tables[i].Columns = table.Columns[:MaxSchemaColumns]
			schema.Truncated = true
		}
	}
	return schema
}
