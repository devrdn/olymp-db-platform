package rpc

import (
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"time"

	pb "github.com/devrdn/db-contest/backend/internal/rpc/queryrunnerv1"
)

// cellsFor renders one row for display.
//
// Text, because the only consumer is a console that shows it. NULL is carried
// as a flag rather than as an empty string: in SQL those are different, and a
// participant debugging a left join needs to see which one they have.
//
// The row's cells and the fields they point at are each one allocation for
// the whole row rather than three per cell: every query renders up to a
// thousand rows here, and the collector pays for each object separately. The
// cells are only ever addressed in place, never copied.
func cellsFor(values []any) *pb.Row {
	var (
		cells = make([]pb.Cell, len(values))
		nulls = make([]bool, len(values))
		texts = make([]string, len(values))
		row   = &pb.Row{Cells: make([]*pb.Cell, len(values))}
	)
	for i, value := range values {
		texts[i], nulls[i] = render(value)
		cells[i].IsNull, cells[i].Text = &nulls[i], &texts[i]
		row.Cells[i] = &cells[i]
	}
	return row
}

// render turns one value into what a person reads.
//
// The driver.Valuer case is what stops this printing a struct. The driver
// hands back its own types for anything it has no Go equivalent for —
// numerics, ranges, intervals — and fmt on one of those produces a dump of its
// fields rather than the number that was in the column.
func render(value any) (text string, null bool) {
	switch typed := value.(type) {
	case nil:
		return "", true
	case string:
		return typed, false
	case []byte:
		// The spelling PostgreSQL itself uses for bytea, so that what is shown
		// can be pasted back into a query.
		return `\x` + hex.EncodeToString(typed), false
	case time.Time:
		return typed.Format(time.RFC3339Nano), false
	case driver.Valuer:
		underlying, err := typed.Value()
		if err != nil {
			// Rendering must not fail a query that already succeeded. The
			// fallback is ugly and honest, which beats losing the answer.
			return fmt.Sprint(value), false
		}
		if underlying == nil {
			return "", true
		}
		return render(underlying)
	default:
		return fmt.Sprint(typed), false
	}
}

// weigh is what a rendered row costs on the wire.
//
// The text itself plus a little for the framing protobuf puts around each
// field. Exact enough to be a bound rather than a guess, which the runner's
// own estimate over Go values cannot be: it never sees the rendering.
func weigh(row *pb.Row) int {
	// Per cell, for the field tags and length prefixes around the two fields.
	const framing = 8

	total := 0
	for _, cell := range row.GetCells() {
		total += framing + len(cell.GetText())
	}
	return total
}
