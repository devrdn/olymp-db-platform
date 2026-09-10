package provisioning_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// These tests exercise tablecsv.go through the exported surface
// tabledata_test.go already builds against (tableDataGames, withSuspects,
// beginTableUploadWithContent) — no CORE_DB_DSN needed, the same "parsing
// and boundaries — without a database" reasoning that file's own doc gives.
// tablecsv.go's own line scanner, header check and scalar parser are unexported, so they
// are only reachable this way from outside the package, and this package's
// own test files are black-box throughout (never `package provisioning`).

// TestCompleteTableUploadAcceptsCRLFLineEndings is the fix for the CSV a
// spreadsheet on Windows writes by default: every line ends "\r\n", not
// just "\n". Before the fix, tableLineScanner.next and firstLineIfComplete
// each stripped only the trailing "\n", leaving the header's last column
// read back as `nickname\r` — a byte invisible in the refusal's own text,
// and enough to fail validateHeader on a file whose columns are otherwise
// exactly right.
func TestCompleteTableUploadAcceptsCRLFLineEndings(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	// Every line, including the header, ends "\r\n" — exactly what Excel or
	// Numbers on Windows writes. The third row's last field is quoted and
	// holds a literal CR that is not a line ending at all: tablecsv.go's
	// own doc on splitCSVLine says a field's own bytes are never inspected
	// for a line ending, so this must survive untouched while the CRLF
	// around it is stripped.
	content := "id,name,nickname\r\n" +
		"1,Ann,\r\n" +
		"2,Bob,\"Bo\rbby\"\r\n"
	data := beginTableUploadWithContent(t, service, contest, "suspects", content)

	completed, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID)
	if err != nil {
		t.Fatalf("CompleteTableUpload: %v", err)
	}
	if completed.Lines != 2 {
		t.Fatalf("Lines = %d, want 2", completed.Lines)
	}

	window, err := service.TableDataWindow(t.Context(), contest, "suspects", 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("TableDataWindow: %v", err)
	}
	if len(window.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(window.Rows))
	}
	if window.Rows[0].Fields[1] != "Ann" {
		t.Errorf("row 1's name = %q, want %q (no trailing CR)", window.Rows[0].Fields[1], "Ann")
	}
	if window.Rows[1].Fields[2] != "Bo\rbby" {
		t.Errorf("row 2's nickname = %q, want %q (the quoted field's own CR kept intact)", window.Rows[1].Fields[2], "Bo\rbby")
	}
}

// A quote is special to PostgreSQL's CSV reader wherever it appears in a
// field, not only as its first byte: `CopyReadAttributesCSV` runs a two-state
// loop, and the quote that opens the quoted state is any quote it meets while
// outside one. So `1,ab"cd,2` is not a three-field row with a quote in the
// middle of the second — it opens a quoted field at that quote, swallows the
// rest of the line looking for its close, and ends the COPY with
// `unterminated CSV quoted field`.
//
// splitCSVLine treated a quote as special only in a field's first byte, so
// this row passed validation, was counted, and was shown back to the
// organiser as three good fields — and then failed the build minutes later,
// as an error about their contest rather than about their file. This
// validator exists precisely so a bad value is refused with a row number
// while somebody is still looking at the upload.
func TestCompleteTableUploadRefusesARowPostgreSQLWouldCallAnUnterminatedQuote(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	data := beginTableUploadWithContent(t, service, contest, "suspects",
		"id,name,nickname\n1,ab\"cd,2\n")

	_, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID)
	if !errors.Is(err, provisioning.ErrTableValueInvalid) {
		t.Fatalf("CompleteTableUpload accepted a row COPY will refuse: %v", err)
	}
}

// The other half of the same dialect, and the reason the fix is "match
// PostgreSQL" rather than "refuse anything with a quote in it": a quote that
// closes and is followed by more bytes is not an error to PostgreSQL either
// — the loop simply goes back to its unquoted state and keeps appending. So
// `"Bo"bby` is the single value Bobby, in COPY and here alike.
func TestCompleteTableUploadReadsAClosedQuoteFollowedByMoreTextAsOneValue(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	data := beginTableUploadWithContent(t, service, contest, "suspects",
		"id,name,nickname\n1,Ann,\"Bo\"bby\n")
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); err != nil {
		t.Fatalf("CompleteTableUpload: %v", err)
	}

	window, err := service.TableDataWindow(t.Context(), contest, "suspects", 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("TableDataWindow: %v", err)
	}
	if len(window.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(window.Rows))
	}
	if window.Rows[0].Fields[2] != "Bobby" {
		t.Errorf("nickname = %q, want %q — the same value COPY would load", window.Rows[0].Fields[2], "Bobby")
	}
}

// witnessTable is a table with a timestamp column — none of tabledata_test.go's
// own helpers need one, so this test defines its own.
func witnessTable() provisioning.TableDefinition {
	return provisioning.TableDefinition{
		Name: "witnesses",
		Columns: []provisioning.ColumnDefinition{
			{Name: "id", Type: provisioning.ColumnInteger},
			{Name: "seen_at", Type: provisioning.ColumnTimestamp},
		},
		PrimaryKey: []string{"id"},
	}
}

// TestAppendTableRowAcceptsATimestampWithNoSecondsField is the fix for
// <input type="datetime-local" step={1}>: a browser's own picker still
// serialises that value without a seconds component whenever the organiser
// never touched the seconds field, "2024-01-01T10:00" rather than
// "2024-01-01T10:00:00" — regardless of step. validTimestamp's own accepted
// forms did not include that shape, so choosing a time in the widget and
// clicking "add row" refused with a message pointing at a form
// (YYYY-MM-DD HH:MM:SS) the widget cannot be made to send.
func TestAppendTableRowAcceptsATimestampWithNoSecondsField(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest,
		provisioning.Definition{Tables: []provisioning.TableDefinition{witnessTable()}}); err != nil {
		t.Fatalf("save the definition: %v", err)
	}

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "witnesses", []string{"1", "2024-01-01T10:00"}); err != nil {
		t.Fatalf("AppendTableRow with a seconds-less timestamp: %v", err)
	}
}

// The bounds and refusals tablecsv.go owns, reached through the exported
// methods that run them.
//
// They used to live in tabledata_test.go, because that is where the method
// each one calls is declared. But the rule each one is about — the header
// must name the table's columns, a row's field count must match, a value
// must parse as its column's type, a field must fit MaxTableFieldBytes, a
// file must fit MaxTableDataRows — belongs to this file's own source, and
// somebody changing one of those checks opens this file first (CLAUDE.md Go
// layout rule 5).

// TestCompleteTableUploadRefusesAMismatchedHeaderBeforeValidatingAnyRow is
// the brief's own requirement for the full, end-of-upload pass: a header
// that does not name the table's own columns is refused, and refused for
// that reason alone — the row after it, itself invalid for an unrelated
// reason (a non-numeric id), is never even reached.
//
// The content is written straight to the store rather than through
// service.AppendTableChunk (beginTableUploadWithContent's own helper), the
// same way TestCompleteTableUploadRefusesTheDeclaredLengthNotMatchingWhatArrived
// does: AppendTableChunk now catches most mismatched headers itself, on the
// very first chunk (TestAppendTableChunkRefusesAMismatchedHeaderOnTheFirstChunk
// below) — this test is about the fallback for a file that reached the
// store some other way, so it must not go through the same early check it
// is not testing.
func TestCompleteTableUploadRefusesAMismatchedHeaderBeforeValidatingAnyRow(t *testing.T) {
	t.Parallel()
	service, store, _, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "id,name,extra\nnot-a-number,A,x\n" // wrong header AND a row that would also fail
	data, err := service.BeginTableUpload(t.Context(), contest, "suspects", int64(len(content)))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := files.Append(data.ID.String(), 0, strings.NewReader(content)); err != nil {
		t.Fatalf("append directly: %v", err)
	}
	// Written straight to the store rather than through AppendTableChunk, so
	// the fake repository's own bookkeeping of how many bytes have arrived
	// is brought up to date by hand — otherwise CompleteTableUpload would
	// stop at its length check, never reaching the header check this test
	// is actually about.
	store.mu.Lock()
	seeded := store.tableData[data.ID]
	seeded.ReceivedBytes = int64(len(content))
	store.tableData[data.ID] = seeded
	store.mu.Unlock()

	_, err = service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID)
	if !errors.Is(err, provisioning.ErrTableHeaderMismatch) {
		t.Fatalf("error = %v, want ErrTableHeaderMismatch", err)
	}
	if errors.Is(err, provisioning.ErrTableValueInvalid) {
		t.Fatal("the row's own mistake was reported — the header should have stopped this first")
	}
}

// TestCompleteTableUploadNamesTheRowWhoseFieldCountDoesNotMatch is the
// brief's other named requirement: a row with the wrong field count is
// refused with its own row number, not silently skipped.
func TestCompleteTableUploadNamesTheRowWhoseFieldCountDoesNotMatch(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "id,name,nickname\n1,Margot,\n2,Duplicate\n" // row 2 has only 2 fields
	data := beginTableUploadWithContent(t, service, contest, "suspects", content)

	_, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID)
	if !errors.Is(err, provisioning.ErrTableRowFieldCount) {
		t.Fatalf("error = %v, want ErrTableRowFieldCount", err)
	}
	if !strings.Contains(err.Error(), "row 2") {
		t.Fatalf("error = %q, does not name row 2", err)
	}
}

// TestCompleteTableUploadNamesTheRowAndColumnOfAValueThatDoesNotParse checks
// the second named refusal: a value that does not fit its column's type
// names the row and the column, not just "invalid".
func TestCompleteTableUploadNamesTheRowAndColumnOfAValueThatDoesNotParse(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "id,name,nickname\n1,Margot,\nnot-a-number,Someone,\n"
	data := beginTableUploadWithContent(t, service, contest, "suspects", content)

	_, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID)
	if !errors.Is(err, provisioning.ErrTableValueInvalid) {
		t.Fatalf("error = %v, want ErrTableValueInvalid", err)
	}
	if !strings.Contains(err.Error(), "row 2") || !strings.Contains(err.Error(), `"id"`) {
		t.Fatalf("error = %q, does not name row 2's id column", err)
	}
}

// A row that does not match the table's own columns is refused before
// anything is written — a value with the wrong number of fields, or one
// that will not parse as its column's type, must not reach the file at all.
func TestAppendTableRowRefusesAValueThatDoesNotMatchItsColumn(t *testing.T) {
	t.Parallel()
	service, _, _, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"not-a-number", "A", ""}); !errors.Is(err, provisioning.ErrTableValueInvalid) {
		t.Fatalf("error = %v, want ErrTableValueInvalid", err)
	}
	ids, err := files.UploadIDs(anyAge())
	if err != nil {
		t.Fatalf("list the table data volume: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("a refused row still left %d file(s) on disk", len(ids))
	}
}

// TestAppendTableRowValidatesNumericAsDecimalSyntaxNotAsAFloat is the other
// defect this package's numeric column check had: a PostgreSQL numeric is an
// exact decimal of arbitrary precision, and strconv.ParseFloat is not a
// stand-in for it in either direction.
//
//   - "0x1p-2" is a hexadecimal float literal ParseFloat happily parses to
//     0.25 — and numeric_in has never accepted one (checked against a live
//     PostgreSQL 16 instance, this platform's own target). A value that
//     clears this check and only fails inside PostgreSQL's own COPY,
//     minutes into a build, is exactly the failure this whole pre-check
//     exists to prevent.
//   - "1e400" is a value numeric holds exactly, arbitrary precision being
//     the type's entire point, but ParseFloat reports a range error for it
//     because it does not fit a 64-bit float — refusing an organiser's
//     perfectly good row for a limit that belongs to Go's float type, not
//     to the column's own.
//   - "NaN", numeric's own special value (and, since PostgreSQL 14,
//     signed Infinity/Inf, also checked against that live instance), must
//     keep working now that the check is decimal syntax rather than a
//     float parse.
func TestAppendTableRowValidatesNumericAsDecimalSyntaxNotAsAFloat(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, provisioning.Definition{
		Tables: []provisioning.TableDefinition{{
			Name: "prices",
			Columns: []provisioning.ColumnDefinition{
				{Name: "id", Type: provisioning.ColumnInteger},
				{Name: "amount", Type: provisioning.ColumnNumeric},
			},
		}},
	}); err != nil {
		t.Fatalf("save the definition: %v", err)
	}

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "prices", []string{"1", "0x1p-2"}); !errors.Is(err, provisioning.ErrTableValueInvalid) {
		t.Fatalf("hex float literal: error = %v, want ErrTableValueInvalid", err)
	}
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "prices", []string{"2", "1e400"}); err != nil {
		t.Fatalf("a value numeric holds exactly was refused as if it had to fit a 64-bit float: %v", err)
	}
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "prices", []string{"3", "NaN"}); err != nil {
		t.Fatalf("NaN, numeric's own special value, was refused: %v", err)
	}
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "prices", []string{"4", "-Infinity"}); err != nil {
		t.Fatalf("-Infinity, numeric's own special value since PostgreSQL 14, was refused: %v", err)
	}
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "prices", []string{"5", "1_000.5"}); err != nil {
		t.Fatalf("an underscore digit separator, valid decimal syntax, was refused: %v", err)
	}
}

// TestAppendTableRowRefusesAFieldPastTheFieldBound is CLAUDE.md rule 2 on
// the path a form takes: the request body limit bounds the whole request,
// not one field of it, so without a check here a single value can be sixteen
// times the max_field_bytes this service publishes to its own clients. Once
// such a value is in the file, every window read of that table refuses with
// ErrTableFieldTooLong for ever — the rows cannot be looked at, and the row
// count the screen shows drops to zero.
func TestAppendTableRowRefusesAFieldPastTheFieldBound(t *testing.T) {
	t.Parallel()
	service, _, _, files := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	tooLong := strings.Repeat("x", provisioning.MaxTableFieldBytes+1)
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects",
		[]string{"1", tooLong, ""}); !errors.Is(err, provisioning.ErrTableFieldTooLong) {
		t.Fatalf("error = %v, want ErrTableFieldTooLong", err)
	}
	ids, err := files.UploadIDs(anyAge())
	if err != nil {
		t.Fatalf("list the table data volume: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("a refused row still left %d file(s) on disk", len(ids))
	}

	// Exactly at the bound is still accepted: the refusal is one byte past
	// it, not near it.
	atBound := strings.Repeat("x", provisioning.MaxTableFieldBytes)
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", atBound, ""}); err != nil {
		t.Fatalf("a field of exactly MaxTableFieldBytes was refused: %v", err)
	}
}

// TestCompleteTableUploadRefusesOneRowPastMaxTableDataRows is the boundary
// test for the row-count bound: MaxTableDataRows rows complete, one more is
// refused. A one-column table keeps the file small enough that both halves
// of this test run in well under a second.
func TestCompleteTableUploadRefusesOneRowPastMaxTableDataRows(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, provisioning.Definition{
		Tables: []provisioning.TableDefinition{{
			Name:    "codes",
			Columns: []provisioning.ColumnDefinition{{Name: "id", Type: provisioning.ColumnInteger}},
		}},
	}); err != nil {
		t.Fatalf("save the definition: %v", err)
	}

	var atLimit strings.Builder
	atLimit.WriteString("id\n")
	for i := 0; i < provisioning.MaxTableDataRows; i++ {
		atLimit.WriteString("1\n")
	}
	data := beginTableUploadWithContent(t, service, contest, "codes", atLimit.String())
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); err != nil {
		t.Fatalf("exactly MaxTableDataRows rows was refused: %v", err)
	}

	overLimit := atLimit.String() + "1\n"
	data2 := beginTableUploadWithContent(t, service, contest, "codes", overLimit)
	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data2.ID); !errors.Is(err, provisioning.ErrTableTooManyRows) {
		t.Fatalf("error = %v, want ErrTableTooManyRows", err)
	}
}
