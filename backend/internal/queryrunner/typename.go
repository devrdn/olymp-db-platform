package queryrunner

import (
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// This file answers one question: what does the console print under a result
// column's name?
//
// It has to be the same vocabulary the schema panel uses one pane to the left,
// which reads `format_type(atttypid, atttypmod)` straight out of the catalogue
// (internal/gamedb.schemaQuery). Two spellings of one type across two panels of
// one screen — `timestamptz` here and `timestamp with time zone` there — is a
// defect a participant reads as two different types.
//
// The name is resolved from the OID the driver already handed back with the row
// description, using the connection's own type map. No second round trip: the
// query has been answered, the participant is waiting, and a catalogue lookup
// per result would put a query on the game cluster for every query a
// participant runs.
//
// Two things this deliberately does not reproduce:
//
//   - Type modifiers. format_type() is given atttypmod as well and prints
//     `character varying(40)`; a row description carries the modifier but
//     decoding it means reimplementing every type's own typmodout function.
//     `character varying` is the truth minus a detail, which beats a wrong
//     detail.
//   - Schema qualification. format_type() qualifies a type that is not on the
//     search path. A participant's own search path is their game database's,
//     so an unqualified name is what they would have typed.

// formatTypeNames is the list of types PostgreSQL's format_type() prints as
// something other than their catalogue name — its own switch, in Go.
//
// Everything absent from this map keeps the name the catalogue has, which is
// what format_type() falls through to and what the driver's type map already
// holds: `text`, `uuid`, `date`, `jsonb`, `bytea` and the rest.
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

// columnTypes names the type of every column, in the order the driver
// described them.
//
// One entry per field and never fewer: the interface reads the two lists
// together, putting the nth type under the nth name, and a list with a gap
// removed would mislabel every column after it. A type that cannot be named
// is an empty entry, which is a hole the interface can leave blank.
//
// The allocation is bounded by the column count, which PostgreSQL bounds at
// 1664 per row and which the runner's own result budget bounds again
// (CLAUDE.md rule 12).
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

// typeName is what PostgreSQL would print for one type OID.
//
// An OID the map does not know returns the empty string. That is the whole
// degradation policy and it is deliberate: a participant's SELECT must never
// fail because we could not name a column's type, and an organiser's init
// script may declare enums and domains whose OIDs exist only in that one game
// database. The rows are the answer; the name above them is a courtesy.
func typeName(oid uint32, types *pgtype.Map) string {
	if name, special := formatTypeNames[oid]; special {
		return name
	}

	known, ok := types.TypeForOID(oid)
	if !ok {
		return ""
	}

	// An array's catalogue name is its element's with an underscore in front —
	// `_text` — which is a spelling no participant has typed. format_type()
	// prints the element's name followed by brackets, and the element is
	// reachable offline through the codec the map already holds.
	if array, isArray := known.Codec.(*pgtype.ArrayCodec); isArray {
		element := typeName(array.ElementType.OID, types)
		if element == "" {
			return ""
		}
		return element + "[]"
	}
	return known.Name
}
