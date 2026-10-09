package queryrunner

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// Names must match format_type(), which the schema panel shows, not the
// driver's internal names.
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
		// Types format_type() spells out differently from the driver.
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
		// The driver's name for this is `_text`.
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

// An organiser's enum or domain is unknown to the type map; it gets an empty
// name and the query still succeeds.
func TestATypeTheDriverDoesNotKnowIsLeftUnnamed(t *testing.T) {
	// Past every built-in OID, so nothing registered answers for it.
	const inventedByAnOrganiser = 987654

	if got := typeName(inventedByAnOrganiser, pgtype.NewMap()); got != "" {
		t.Fatalf("typeName(%d) = %q, want an empty name", inventedByAnOrganiser, got)
	}
}

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

// A write answering with a count has no columns and gets nil, not an empty list.
func TestNoColumnsMeansNoTypes(t *testing.T) {
	if got := columnTypes(nil, pgtype.NewMap()); got != nil {
		t.Fatalf("columnTypes(nil) = %#v, want nil", got)
	}
}
