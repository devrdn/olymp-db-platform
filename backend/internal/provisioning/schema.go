package provisioning

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
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
	// reads collapses the concurrent misses of one contest into one catalogue
	// read. See Schema's own doc for why a cache alone is not enough.
	reads singleflight.Group
}

// schemaReadTimeout bounds a catalogue read that no longer belongs to any one
// caller.
//
// The shared read is detached from the request that happened to start it (see
// Schema), which also detaches it from that request's deadline — so it needs
// one of its own or a wedged cluster parks a flight, and every later caller
// behind it, forever. Comfortably above the ten seconds
// gamedb.Provisioner.ReadSchema sets as the statement timeout on the
// connection it opens, so what this bounds is the part that timeout cannot:
// dialling a cluster that never answers.
const schemaReadTimeout = 30 * time.Second

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
//
// # The miss is what needs collapsing, not the hit
//
// The cache above answers "one catalogue read per template for the whole
// contest" in steady state and not at all at the moment that matters. At a
// contest's start every participant opens their console inside the same
// minute and every one of them misses, because nothing has been written back
// yet: three hundred fresh connections to three hundred instance databases on
// the game cluster, each borrowing one of that instance's two allowed
// connections, and then three hundred UPDATEs queueing on one game_templates
// row — all to compute the same document, of which two hundred and
// ninety-nine are thrown away.
//
// So the misses of one contest are collapsed into one read. singleflight
// rather than a keyed mutex map because golang.org/x/sync is already a direct
// dependency of this module (internal/users uses errgroup), so it costs no
// new supply chain — and because the map wants writing carefully: entries
// have to be reference-counted or deleted under the same lock that hands them
// out, or it either leaks one mutex per contest ever run or drops a mutex
// somebody is still holding.
//
// Keyed by contest *and* version, so a rebuild mid-contest is a different
// flight rather than one that could hand back the shape the old build had.
// Any instance of one contest is a byte-for-byte copy of the same template,
// which is what makes it sound for one participant's read of their own
// database to answer everybody else's.
//
// The cache is looked at twice, once before the flight and once inside it.
// The outer look is the steady-state path and keeps a warm read off the
// group's lock entirely; the inner one catches the caller who missed, was
// descheduled, and arrived after the winner had already saved — without it
// that caller opens a second connection to ask a question that is now
// answered.
//
// The read inside the flight is the contest's work, not the work of whichever
// request happened to arrive first, so it does not carry that request's
// cancellation: a student navigating away mid-read must not fail the console
// of everyone waiting behind them. It keeps the caller's values and takes a
// deadline of its own (schemaReadTimeout). The cost is that a read started by
// a caller who has already gone still finishes — which is the right way
// round, since finishing is what warms the cache for the next three hundred.
func (r *SchemaReader) Schema(ctx context.Context, contest Contest, database string) (Schema, error) {
	if cached, ok := r.cached(ctx, contest); ok {
		return cached, nil
	}

	answer, err, _ := r.reads.Do(fmt.Sprintf("%s@%d", contest.ID, contest.Version), func() (any, error) {
		if cached, ok := r.cached(ctx, contest); ok {
			return cached, nil
		}

		shared, cancel := context.WithTimeout(context.WithoutCancel(ctx), schemaReadTimeout)
		defer cancel()

		schema, err := r.source.ReadSchema(shared, database)
		if err != nil {
			return nil, fmt.Errorf("read the game's schema: %w", err)
		}
		schema = boundSchema(schema)

		// Best effort on purpose: see the doc above. The caller gets its
		// answer either way, and the next one pays for this read again rather
		// than nobody getting one.
		_ = r.store.SaveSchema(shared, contest.ID, contest.Version, schema)
		return schema, nil
	})
	if err != nil {
		return Schema{}, err
	}
	return answer.(Schema), nil
}

// cached is the cache read both halves of Schema make: the document is only
// an answer when it was read without error *and* describes the build this
// contest is actually running.
func (r *SchemaReader) cached(ctx context.Context, contest Contest) (Schema, bool) {
	cached, version, err := r.store.CachedSchema(ctx, contest.ID)
	if err != nil || version != contest.Version {
		return Schema{}, false
	}
	return cached, true
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
