package provisioning

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
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

// SchemaStore is the schema cache, which only a build writes
// (Games.describe). CachedSchema reports ErrNoSchema when nothing is stored;
// the version it returns is the template build the document describes, so a
// rebuild is detected without explicit invalidation.
type SchemaStore interface {
	CachedSchema(ctx context.Context, contestID uuid.UUID) (Schema, int, error)
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
	// copyReads holds one token per copy being read (maxCopyReads).
	copyReads chan struct{}
	log       *slog.Logger
	// unreadable holds the contests whose unreadable cache was reported, so a
	// contest's start does not log once per participant.
	unreadable sync.Map
}

// schemaReadTimeout bounds a copy read, waiting for its turn included. It
// sits above the ten-second statement timeout gamedb sets, so it bounds
// dialling a cluster that never answers.
const schemaReadTimeout = 30 * time.Second

// maxCopyReads bounds the copies read at once. With no description stored
// every participant reads their own at a contest's start, and each read is a
// provisioning connection to the game cluster.
const maxCopyReads = 4

// NewSchemaReader assembles the reader.
func NewSchemaReader(store SchemaStore, source SchemaSource) *SchemaReader {
	return &SchemaReader{
		store: store, source: source,
		copyReads: make(chan struct{}, maxCopyReads),
		log:       slog.New(slog.DiscardHandler),
	}
}

// WithLog reports a cache that cannot be read: until the next build every
// participant's panel is then read from their own copy.
func (r *SchemaReader) WithLog(log *slog.Logger) *SchemaReader {
	r.log = log
	return r
}

// Schema answers what contest's game looks like: the description the build
// read from the template and stored (Games.describe), when it describes the
// build the contest is running.
//
// Otherwise (nothing stored, a game built before builds described their
// templates; a description of another build; or a cache that cannot be read)
// it reads database, the requesting participant's own copy, and answers that
// participant alone. What it reads is never saved and never handed to another
// participant: the organiser's SECURITY DEFINER code run by one participant
// can change their copy's public tables. The template is never read here
// (Games.describe says why). A cluster failure refuses the answer, since an
// empty panel would claim the game has no tables.
func (r *SchemaReader) Schema(ctx context.Context, contest Contest, database string) (Schema, error) {
	cached, version, err := r.store.CachedSchema(ctx, contest.ID)
	if err == nil && version == contest.Version {
		return cached, nil
	}
	if err != nil && !errors.Is(err, ErrNoSchema) {
		if _, reported := r.unreadable.LoadOrStore(contest.ID, struct{}{}); !reported {
			r.log.WarnContext(ctx, "could not read the stored schema; reading each participant's copy until the next build",
				"contest", contest.ID, "error", err)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, schemaReadTimeout)
	defer cancel()
	select {
	case r.copyReads <- struct{}{}:
		defer func() { <-r.copyReads }()
	case <-ctx.Done():
		return Schema{}, fmt.Errorf("wait to read the game's schema: %w", ctx.Err())
	}

	schema, err := r.source.ReadSchema(ctx, database)
	if err != nil {
		return Schema{}, fmt.Errorf("read the game's schema: %w", err)
	}
	return storable(schema), nil
}

// storable bounds the document to MaxSchemaTables, MaxSchemaColumns,
// MaxSchemaForeignKeys, MaxDocumentForeignKeys and MaxKeyColumns, and sets
// Truncated if it had to. The build calls it before storing, so the stored
// jsonb is bounded too (CLAUDE.md rule 2), and the reader before answering
// from a copy.
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
