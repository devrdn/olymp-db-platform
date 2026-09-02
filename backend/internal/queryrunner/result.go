package queryrunner

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Result is what a participant gets back.
type Result struct {
	Columns []string
	Rows    [][]any
	// Truncated says the answer is longer than what is here — by rows, by
	// bytes, or both. A flag rather than a silent cut: a participant reading
	// nine hundred rows of a nine thousand row answer and not being told has
	// been given a wrong answer, not a shortened one.
	Truncated bool
}

// resultAlias names the wrapper's subquery. Deliberately unlikely to collide
// with anything a participant writes, since it shares a namespace with their
// own aliases.
const resultAlias = "db_contest_result"

// limited wraps a query so the server stops early.
//
// `SELECT * FROM (…) q LIMIT n+1`: the extra row is how truncation is detected
// without counting the whole answer. The planner can use the limit, which is
// the point — reading a thousand rows of a ten million row scan and hanging up
// still makes the server do the scan.
//
// Two details are load-bearing and both were found by trying them:
//
//   - the closing bracket must be on its own line, because a query ending in a
//     line comment otherwise swallows it and nothing parses;
//   - trailing semicolons have to go, because the checker allows `SELECT 1;`
//     and a semicolon inside a subquery does not parse.
//
// EXPLAIN is never wrapped: it cannot appear inside a subquery at all. Its
// result is cut while reading instead, which is enough — a plan is small.
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

// collect reads a result set, stopping at whichever limit comes first.
//
// It takes the transaction rather than the connection. Both work today, since
// a pgx transaction is bound to the connection that began it — but only one of
// them says so, and the day a connection comes from a pool the other would
// send the query outside the read-only transaction without a word.
func collect(ctx context.Context, tx pgx.Tx, statement string, limits Limits) (*Result, error) {
	rows, err := tx.Query(ctx, statement)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := &Result{}
	for _, field := range rows.FieldDescriptions() {
		result.Columns = append(result.Columns, field.Name)
	}

	var size int
	var stoppedEarly bool
	for rows.Next() {
		if len(result.Rows) >= limits.MaxRows {
			// The row the extra LIMIT fetched: proof there is more, and not
			// part of the answer.
			result.Truncated = true
			stoppedEarly = true
			break
		}

		values, err := rows.Values()
		if err != nil {
			return nil, err
		}

		// Checked before appending, so the cap is a ceiling rather than a
		// threshold that the last row is allowed to cross. A single row larger
		// than the whole budget therefore yields nothing but the flag, which
		// is the honest answer: there is no prefix of that row to show.
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
	}
	return result, nil
}

// weigh estimates how much of the response budget a row spends.
//
// An estimate on purpose: the exact cost is whatever the encoder produces, and
// measuring that would mean encoding the answer before deciding whether to
// keep it. What this has to be is proportional and cheap, so that a thousand
// rows of a megabyte each are stopped and a thousand small ones are not.
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
			// Numbers, times, booleans: bounded and small. Counting them as a
			// fixed cost keeps this a size check rather than a reflection pass
			// over every value of every row.
			total += 16
		}
	}
	return total
}
