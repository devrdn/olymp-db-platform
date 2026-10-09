package queryrunner

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Result is what a participant gets back.
type Result struct {
	Columns []string
	// ColumnTypes names each column's type as format_type() would. Either
	// empty or parallel to Columns; an entry is empty for a type that cannot
	// be named.
	ColumnTypes []string
	Rows        [][]any
	// Truncated says the answer is longer than what is here, by rows or
	// bytes, so a cut answer is never shown as whole.
	Truncated bool
	// RowsAffected is how many rows a write changed when it answers with a
	// count. Zero for a read and for a write whose RETURNING produced rows.
	RowsAffected int64
	// Duration is the statement's own time, from sending it to reading the
	// last kept row. It excludes connecting, admission and transport, which
	// are the platform's costs, not the participant's query.
	Duration time.Duration
}

// resultAlias names the wrapper's subquery, chosen not to collide with a
// participant's own aliases.
const resultAlias = "db_contest_result"

// limited wraps a query in `SELECT * FROM (...) LIMIT n+1` so the planner can
// stop early; the extra row detects truncation. The closing bracket goes on its
// own line so a trailing line comment cannot swallow it, and trailing
// semicolons are trimmed because a subquery cannot contain one.
func limited(sql string, maxRows int) string {
	return "SELECT * FROM (\n" + trimStatement(sql) + "\n) AS " + resultAlias +
		" LIMIT " + strconv.Itoa(maxRows+1)
}

// trimStatement removes the trailing whitespace and semicolons that a
// statement may legally end with and a subquery may not.
func trimStatement(sql string) string {
	for {
		trimmed := strings.TrimRight(strings.TrimSpace(sql), ";")
		if trimmed == sql {
			return sql
		}
		sql = trimmed
	}
}

// collect reads a result set, stopping at whichever limit comes first. It
// takes the transaction rather than the connection so the query cannot run
// outside the read-only transaction by mistake.
func collect(ctx context.Context, tx pgx.Tx, statement string, limits Limits, write bool) (*Result, error) {
	started := time.Now()

	rows, err := tx.Query(ctx, statement)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	result := &Result{
		ColumnTypes: columnTypes(fields, tx.Conn().TypeMap()),
	}
	for _, field := range fields {
		result.Columns = append(result.Columns, field.Name)
	}

	var size int
	var stoppedEarly bool
	for rows.Next() {
		if len(result.Rows) >= limits.MaxRows {
			// The extra LIMIT row: proof there is more, not part of the answer.
			result.Truncated = true
			stoppedEarly = true
			break
		}

		values, err := rows.Values()
		if err != nil {
			return nil, err
		}

		// Checked before appending, so the last row cannot cross the cap.
		if size += weigh(values); size > limits.MaxBytes {
			result.Truncated = true
			stoppedEarly = true
			break
		}
		result.Rows = append(result.Rows, values)
	}

	if !stoppedEarly {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		// The command tag is only complete once the statement ran to its
		// end; with RETURNING the rows are the answer instead.
		if write && len(result.Columns) == 0 {
			rows.Close()
			result.RowsAffected = rows.CommandTag().RowsAffected()
		}
	}

	result.Duration = time.Since(started)
	return result, nil
}

// weigh estimates how much of the response budget a row spends. It only needs
// to be cheap and proportional; the exact cost would mean encoding first.
func weigh(values []any) int {
	// Per value, for the separators and the key an encoder adds around it.
	const overhead = 8

	total := 0
	for _, value := range values {
		total += overhead
		switch typed := value.(type) {
		case nil:
		case string:
			total += len(typed)
		case []byte:
			total += len(typed)
		default:
			// Numbers, times, booleans: small and bounded, so a fixed cost.
			total += 16
		}
	}
	return total
}
