package provisioning

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrNoSchema is a template whose shape has not been worked out yet, or was
// worked out for an older build. Not a failure: it is the ordinary state of
// every contest until the first participant opens their console.
var ErrNoSchema = errors.New("the game's schema is not known yet")

// Schema is the shape of a game database, as the console's schema panel draws
// it (docs/design/preview.html, "SQL-консоль").
//
// Deliberately not the catalogue. A participant is shown tables, their
// columns, each column's type and which table a foreign key points at, and
// nothing else: indexes, constraints, sequences and privileges are how the
// game is built, not what the game is about, and every one of them is
// something the participant can ask the catalogue for themselves in a contest
// that leaves it open.
type Schema struct {
	Tables []Table `json:"tables"`
	// Truncated says the game has more tables, or a table more columns, than
	// this document carries. A flag rather than a silent cut, for the same
	// reason a truncated query result carries one: a short answer presented
	// as a complete one is a wrong answer.
	Truncated bool `json:"truncated,omitempty"`
}

// Table is one relation and its columns, in the order the database reports
// them — which is the order they were declared, and therefore the order the
// person who wrote the game meant them to be read in.
type Table struct {
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
}

// Column is one column, named with the type the participant would have to
// write to compare against it.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Nullable is what makes an explicit NULL in the result table mean
	// something: a column that cannot be null and shows one is a bug in the
	// game, and a participant can only tell the difference if the panel says
	// which is which.
	Nullable bool `json:"nullable,omitempty"`
	// References names the table a foreign key points at, empty when the
	// column points at nothing. The panel draws it as `fk <table>`; the join
	// a participant needs is exactly this, and having to discover it by
	// guessing is not the puzzle the olympiad is setting.
	References string `json:"references,omitempty"`
}

// MaxSchemaTables and MaxSchemaColumns bound the document (CLAUDE.md rule 2).
//
// The init script is an organiser's own SQL, so nothing about the number of
// tables in a game is under this platform's control, and this document is
// both stored as one jsonb value and sent to every participant's browser. The
// figures are far above any olympiad this product is for — the design's own
// example game has seven tables — and far below what would make either of
// those a problem.
const (
	MaxSchemaTables  = 200
	MaxSchemaColumns = 200
)

// SchemaStore is the cache, kept beside the game it describes.
//
// CachedSchema reports ErrNoSchema when there is nothing cached; the version
// it returns is the template build the cached document describes, which is
// what makes a rebuild detectable without a separate invalidation step.
type SchemaStore interface {
	CachedSchema(ctx context.Context, contestID uuid.UUID) (Schema, int, error)
	SaveSchema(ctx context.Context, contestID uuid.UUID, version int, schema Schema) error
}

// SchemaSource is the game cluster, asked what one database actually looks
// like.
type SchemaSource interface {
	ReadSchema(ctx context.Context, database string) (Schema, error)
}

// SchemaReader answers what a contest's game looks like.
//
// Separate from Service, and narrow, because the two answer different
// questions: Service decides which database is yours and makes it exist, and
// this one only describes the one that already does. It needs two of the
// repository's methods and one of the cluster's, so it declares exactly those
// (CLAUDE.md rule 3) rather than taking the wide interfaces Service holds.
type SchemaReader struct {
	store  SchemaStore
	source SchemaSource
}

// NewSchemaReader assembles the reader.
func NewSchemaReader(store SchemaStore, source SchemaSource) *SchemaReader {
	return &SchemaReader{store: store, source: source}
}

// Schema answers what contest's game looks like, reading database only if it
// has to.
//
// Read from an instance rather than from the template, and this is the whole
// reason the cache exists at all. `CREATE DATABASE ... TEMPLATE x` fails
// while anything else is connected to x (SQLSTATE 55006), so a schema read
// against the template would intermittently refuse provisioning for
// everybody, and would do so most often at exactly the moment a contest
// starts and every participant needs a database at once. An instance is a
// byte-for-byte copy of the template and nothing clones from it, so reading
// one is free of that entirely — and, since every instance of a contest is
// the same copy, the answer is worth keeping.
//
// Neither half of the cache is allowed to refuse the answer. A cache that
// cannot be read leaves the cluster to be asked; a cache that cannot be
// written leaves the next caller to ask again. Only the cluster failing is a
// real failure, because then there is genuinely nothing to show — and an
// empty panel would read as "this game has no tables", which is a lie rather
// than an outage.
func (r *SchemaReader) Schema(ctx context.Context, contest Contest, database string) (Schema, error) {
	if cached, version, err := r.store.CachedSchema(ctx, contest.ID); err == nil && version == contest.Version {
		return cached, nil
	}

	schema, err := r.source.ReadSchema(ctx, database)
	if err != nil {
		return Schema{}, fmt.Errorf("read the game's schema: %w", err)
	}
	schema = boundSchema(schema)

	// Best effort on purpose: see the doc above. The caller gets its answer
	// either way, and the next one pays for this read again rather than
	// nobody getting one.
	_ = r.store.SaveSchema(ctx, contest.ID, contest.Version, schema)
	return schema, nil
}

// boundSchema cuts the document down to what MaxSchemaTables and
// MaxSchemaColumns allow, and says whether it had to.
//
// Applied here, before the cache write, rather than at the edge that serves
// it: the bound exists to keep an unbounded value out of one jsonb column as
// much as out of a browser, and a bound applied only on the way out would
// store the whole thing anyway (CLAUDE.md rule 2).
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
