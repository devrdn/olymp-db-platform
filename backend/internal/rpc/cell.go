package rpc

import (
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"time"

	pb "github.com/devrdn/db-contest/backend/internal/rpc/queryrunnerv1"
)

// cellsFor renders one row as text for the console. NULL is a flag, not an
// empty string: in SQL the two differ.
//
// Cells, flags and texts are one allocation each per row, not three per cell:
// every query renders up to a thousand rows here. Cells are never copied.
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

// render turns one value into what a person reads. driver.Valuer is unwrapped
// because fmt on a driver type (numeric, range, interval) prints its fields,
// not the value.
func render(value any) (text string, null bool) {
	switch typed := value.(type) {
	case nil:
		return "", true
	case string:
		return typed, false
	case []byte:
		// PostgreSQL's bytea spelling, so it can be pasted back into a query.
		return `\x` + hex.EncodeToString(typed), false
	case time.Time:
		return typed.Format(time.RFC3339Nano), false
	case driver.Valuer:
		underlying, err := typed.Value()
		if err != nil {
			// Rendering must not fail a query that already succeeded.
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

// weigh is what a rendered row costs on the wire: the text plus protobuf
// framing. Unlike the runner's estimate over Go values, it sees the rendering.
func weigh(row *pb.Row) int {
	const framing = 8 // per cell: field tags and length prefixes

	total := 0
	for _, cell := range row.GetCells() {
		total += framing + len(cell.GetText())
	}
	return total
}
