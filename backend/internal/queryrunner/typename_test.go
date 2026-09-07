package queryrunner

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// The console shows a column's type under its name, beside a schema panel that
// gets its own from PostgreSQL's format_type(). Two spellings of one type
// across two panels of one screen is a defect, so these pin the vocabulary
// rather than whatever the driver happens to call things internally.
func TestATypeIsNamedAsPostgresWouldPrintIt(t *testing.T) {
	types := pgtype.NewMap()

	for name, expectation := range map[string]struct {
		oid  uint32
		want string
	}{
		// Types format_type() prints as their catalogue name.
		"text":  {pgtype.TextOID, "text"},
		"uuid":  {pgtype.UUIDOID, "uuid"},
		"date":  {pgtype.DateOID, "date"},
		"jsonb": {pgtype.JSONBOID, "jsonb"},
		"bytea": {pgtype.ByteaOID, "bytea"},
		// Types format_type() spells out. The driver calls these `timestamptz`,
		// `int4` and `varchar`; the schema panel calls them what is here.
		"timestamptz": {pgtype.TimestamptzOID, "timestamp with time zone"},
		"timestamp":   {pgtype.TimestampOID, "timestamp without time zone"},
		"int4":        {pgtype.Int4OID, "integer"},
		"int8":        {pgtype.Int8OID, "bigint"},
		"int2":        {pgtype.Int2OID, "smallint"},
		"float8":      {pgtype.Float8OID, "double precision"},
		"bool":        {pgtype.BoolOID, "boolean"},
		"varchar":     {pgtype.VarcharOID, "character varying"},
		"bpchar":      {pgtype.BPCharOID, "character"},
		"numeric":     {pgtype.NumericOID, "numeric"},
		// An array. The driver's name for it is `_text`, which is a catalogue
		// spelling no participant has ever typed.
		"text array": {pgtype.TextArrayOID, "text[]"},
		"int4 array": {pgtype.Int4ArrayOID, "integer[]"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := typeName(expectation.oid, types); got != expectation.want {
				t.Fatalf("typeName(%d) = %q, want %q", expectation.oid, got, expectation.want)
			}
		})
	}
}

// A type the connection's map has never heard of — an enum or a domain an
// organiser's own init script declared — must cost the participant nothing.
// Their SELECT ran; not being able to name one of its columns is ours to
// swallow, and an empty name is what the interface omits.
func TestATypeTheDriverDoesNotKnowIsLeftUnnamed(t *testing.T) {
	// Well past every OID PostgreSQL hands out to a built-in type, and past
	// the first user OID too, so nothing registered can answer for it.
	const inventedByAnOrganiser = 987654

	if got := typeName(inventedByAnOrganiser, pgtype.NewMap()); got != "" {
		t.Fatalf("typeName(%d) = %q, want an empty name", inventedByAnOrganiser, got)
	}
}

// The two lists are read together — the interface puts the nth type under the
// nth name — so they have to be the same length, in the same order, always.
func TestEveryColumnGetsATypeInTheSameOrder(t *testing.T) {
	fields := []pgconn.FieldDescription{
		{Name: "full_name", DataTypeOID: pgtype.TextOID},
		{Name: "at", DataTypeOID: pgtype.TimestamptzOID},
		{Name: "mystery", DataTypeOID: 987654},
	}

	got := columnTypes(fields, pgtype.NewMap())
	want := []string{"text", "timestamp with time zone", ""}

	if len(got) != len(want) {
		t.Fatalf("columnTypes() = %v, want %d entries", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("columnTypes()[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// A result with no columns at all — a write answering with a count — must not
// produce a list, so that the layers above carry nothing rather than an empty
// something.
func TestNoColumnsMeansNoTypes(t *testing.T) {
	if got := columnTypes(nil, pgtype.NewMap()); got != nil {
		t.Fatalf("columnTypes(nil) = %#v, want nil", got)
	}
}
