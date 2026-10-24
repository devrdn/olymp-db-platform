package provisioning

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

// ErrNoSchema means no description is stored for the contest's game: it is
// not built yet, or its build stored none.
var ErrNoSchema = errors.New("the game's schema is not known yet")

// Schema is the shape of a game database as the console's schema panel and
// diagram draw it: tables, columns, types, primary keys and foreign keys only.
// Indexes, other constraints and privileges are left to the catalogue.
type Schema struct {
	Tables []Table `json:"tables"`
	// Truncated says the game has more tables, a table more columns or keys,
	// or a key more columns, than this document carries.
	Truncated bool `json:"truncated,omitempty"`
}

// Table is one relation and its columns, in declaration order. A document
// cached before keys were described has neither key list, and is served as
// it is until the game's next build describes it again.
type Table struct {
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
	// PrimaryKey names the primary key's columns in key order; empty when the
	// relation has none.
	PrimaryKey  []string     `json:"primary_key,omitempty"`
	ForeignKeys []ForeignKey `json:"foreign_keys,omitempty"`
}

// ForeignKey is one foreign-key constraint whose referenced table is in the
// public schema. Columns and ReferencedColumns pair up by position, in the
// constraint's order.
type ForeignKey struct {
	Name              string   `json:"name"`
	Columns           []string `json:"columns"`
	ReferencedTable   string   `json:"referenced_table"`
	ReferencedColumns []string `json:"referenced_columns"`
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

// MaxSchemaTables, MaxSchemaColumns and MaxSchemaForeignKeys bound the
// document (CLAUDE.md rule 2). The init script is an organiser's own SQL, and
// the document is stored as one jsonb value and sent to every participant's
// browser. MaxKeyColumns is PostgreSQL's own limit on a key (INDEX_MAX_KEYS);
// a key past it is dropped rather than cut, since half a key is a wrong one.
const (
	MaxSchemaTables      = 200
	MaxSchemaColumns     = 200
	MaxSchemaForeignKeys = 200
	MaxKeyColumns        = 32
	// MaxDocumentForeignKeys bounds the foreign keys of every table together:
	// the per-table cap alone allows 40,000. It sits within the diagram's
	// layout budget.
	MaxDocumentForeignKeys = 1000
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
// The build stores the description, read from the template itself
// (Games.describe), so this is normally a cache read. A miss reads database,
// the participant's own copy, never the template (Games.describe says why). A
// copy's public schema is the template's for everything a participant's role
// can do: their own objects live in `work`, which is not read, and only
// game_author may alter the game's tables. A copy read because the cache could
// not be read is not saved, so it never replaces the build's description.
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
		schema = storable(schema)

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

// storable bounds the document to MaxSchemaTables, MaxSchemaColumns,
// MaxSchemaForeignKeys, MaxDocumentForeignKeys and MaxKeyColumns, and sets
// Truncated if it had to. Both writers of the cache call it first, so the
// stored jsonb is bounded too (CLAUDE.md rule 2).
func storable(schema Schema) Schema {
	if len(schema.Tables) > MaxSchemaTables {
		schema.Tables = schema.Tables[:MaxSchemaTables]
		schema.Truncated = true
	}
	for i := range schema.Tables {
		table := &schema.Tables[i]
		if len(table.Columns) > MaxSchemaColumns {
			table.Columns = table.Columns[:MaxSchemaColumns]
			schema.Truncated = true
		}
		if len(table.PrimaryKey) > MaxKeyColumns {
			table.PrimaryKey = nil
			schema.Truncated = true
		}
		if len(table.ForeignKeys) > MaxSchemaForeignKeys {
			table.ForeignKeys = table.ForeignKeys[:MaxSchemaForeignKeys]
			schema.Truncated = true
		}
		if slices.ContainsFunc(table.ForeignKeys, tooWide) {
			table.ForeignKeys = slices.DeleteFunc(slices.Clone(table.ForeignKeys), tooWide)
			schema.Truncated = true
		}
	}

	left := MaxDocumentForeignKeys
	for i := range schema.Tables {
		table := &schema.Tables[i]
		if len(table.ForeignKeys) > left {
			table.ForeignKeys = table.ForeignKeys[:left]
			schema.Truncated = true
		}
		left -= len(table.ForeignKeys)
	}
	return schema
}

func tooWide(key ForeignKey) bool {
	return len(key.Columns) > MaxKeyColumns || len(key.ReferencedColumns) > MaxKeyColumns
}
