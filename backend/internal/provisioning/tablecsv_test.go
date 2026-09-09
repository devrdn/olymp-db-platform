package provisioning_test

import (
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// These two tests exercise tablecsv.go through the exported surface
// tabledata_test.go already builds against (tableDataGames, withSuspects,
// beginTableUploadWithContent) — no CORE_DB_DSN needed, the same "разбор и
// границы — без базы" reasoning that file's own doc gives. tablecsv.go's
// own line scanner, header check and scalar parser are unexported, so they
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
