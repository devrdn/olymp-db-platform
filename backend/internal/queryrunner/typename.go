package queryrunner

import (
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// Result column types are named as format_type() names them, to match the
// schema panel (internal/gamedb.schemaQuery). Names come from the row
// description's OIDs and the connection's type map, with no catalogue round
// trip. Type modifiers (the 40 in `character varying(40)`) and schema
// qualification are not reproduced.

// formatTypeNames lists the types format_type() prints differently from their
// catalogue name. Any other type keeps its catalogue name.
var formatTypeNames = map[uint32]string{
	pgtype.BitOID:         "bit",
	pgtype.BoolOID:        "boolean",
	pgtype.BPCharOID:      "character",
	pgtype.Float4OID:      "real",
	pgtype.Float8OID:      "double precision",
	pgtype.Int2OID:        "smallint",
	pgtype.Int4OID:        "integer",
	pgtype.Int8OID:        "bigint",
	pgtype.IntervalOID:    "interval",
	pgtype.NumericOID:     "numeric",
	pgtype.TimeOID:        "time without time zone",
	pgtype.TimestampOID:   "timestamp without time zone",
	pgtype.TimestamptzOID: "timestamp with time zone",
	pgtype.TimetzOID:      "time with time zone",
	pgtype.VarbitOID:      "bit varying",
	pgtype.VarcharOID:     "character varying",
}

// columnTypes names the type of every column, one entry per field so the list
// stays parallel to the column names; an unnamed type is an empty entry. The
// column count is bounded by PostgreSQL (1664) and by the result budget.
func columnTypes(fields []pgconn.FieldDescription, types *pgtype.Map) []string {
	if len(fields) == 0 {
		return nil
	}
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		names = append(names, typeName(field.DataTypeOID, types))
	}
	return names
}

// typeName is what PostgreSQL would print for one type OID, or "" for an OID
// the map does not know (such as an organiser's enum). A query never fails
// because a type cannot be named.
func typeName(oid uint32, types *pgtype.Map) string {
	if name, special := formatTypeNames[oid]; special {
		return name
	}

	known, ok := types.TypeForOID(oid)
	if !ok {
		return ""
	}

	// format_type() prints an array as its element's name plus brackets, not
	// the catalogue's `_text`.
	if array, isArray := known.Codec.(*pgtype.ArrayCodec); isArray {
		element := typeName(array.ElementType.OID, types)
		if element == "" {
			return ""
		}
		return element + "[]"
	}
	return known.Name
}
