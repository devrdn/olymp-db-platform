package provisioning_test

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// These tests reach tablecsv.go's unexported parser through the service,
// with the helpers from tabledata_test.go; no database is needed.

// A spreadsheet on Windows ends every line "\r\n"; a stray "\r" on the
// header's last column would fail validateHeader invisibly.
func TestCompleteTableUploadAcceptsCRLFLineEndings(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "id,name,nickname\r\n" +
		"1,Ann,\r\n" +
		"2,Bob,\"Bobby\"\r\n"
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
	if window.Rows[1].Fields[2] != "Bobby" {
		t.Errorf("row 2's nickname = %q, want %q (the quotes stripped, the CRLF too)", window.Rows[1].Fields[2], "Bobby")
	}
}

// This package's CSV is one row per physical line: the row counts, the
// tombstone numbers and the window reads all count lines. A value holding a
// line break would be written as two lines by the form and read as two rows
// by everything after, so both line-break characters are refused for every
// column type, including the quoted CR a spreadsheet could put in a file.
func TestValidateScalarRefusesALineBreakInEveryColumnType(t *testing.T) {
	t.Parallel()
	for name, text := range map[string]string{
		"a newline":         "a\nb",
		"a carriage return": "a\rb",
		"a trailing CRLF":   "a\r\n",
	} {
		for _, typ := range []provisioning.ColumnType{
			provisioning.ColumnText, provisioning.ColumnInteger, provisioning.ColumnNumeric,
			provisioning.ColumnBoolean, provisioning.ColumnDate, provisioning.ColumnTimestamp,
		} {
			err := provisioning.ValidateScalar(text, typ)
			if err == nil {
				t.Fatalf("%s in a %s column was accepted", name, typ)
			}
			if !strings.Contains(err.Error(), "line break") {
				t.Fatalf("%s in a %s column: %q does not say it is the line break", name, typ, err)
			}
		}
	}
}

func TestCompleteTableUploadRefusesAQuotedCarriageReturnInAValue(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	data := beginTableUploadWithContent(t, service, contest, "suspects",
		"id,name,nickname\n1,Ann,\"Bo\rbby\"\n")

	_, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID)
	if !errors.Is(err, provisioning.ErrTableValueInvalid) {
		t.Fatalf("error = %v, want ErrTableValueInvalid", err)
	}
	if !strings.Contains(err.Error(), "row 1") || !strings.Contains(err.Error(), `"nickname"`) {
		t.Fatalf("error = %q, does not name row 1's nickname column", err)
	}
}

// Every value the validator accepts survives being written by the form's
// writer and read back by the parser, as one line with the same fields and
// the same NULL-ness; a value this does not hold for would corrupt the file
// rather than be refused.
func TestEveryAcceptedValueRoundTripsThroughTheWriterAndTheParserAsOneLine(t *testing.T) {
	t.Parallel()
	values := []string{
		"", "plain", "with,comma", `with"quote`, `"`, `""`, `"quoted"`, "a,\"b\",c",
		" padded ", "\t", "Марго", "1_000.5", "2024-01-01 10:00", "x,,y", ",", `\.`,
		strings.Repeat("x", provisioning.MaxTableFieldBytes),
	}
	for _, v := range values {
		if err := provisioning.ValidateScalar(v, provisioning.ColumnText); err != nil {
			t.Fatalf("%q is not a value this test should be using: %v", v, err)
		}
	}

	line, err := provisioning.FormatCSVRow(values)
	if err != nil {
		t.Fatalf("FormatCSVRow: %v", err)
	}
	if strings.ContainsAny(line, "\n\r") {
		t.Fatalf("the row was written with a line break inside it: %q", line)
	}
	fields, err := provisioning.SplitCSVLine([]byte(line))
	if err != nil {
		t.Fatalf("SplitCSVLine(%q): %v", line, err)
	}
	if len(fields) != len(values) {
		t.Fatalf("read back %d fields from %d values: %q", len(fields), len(values), line)
	}
	for i, f := range fields {
		if f.Text != values[i] {
			t.Errorf("field %d read back as %q, written from %q", i, f.Text, values[i])
		}
		if f.Null != (values[i] == "") {
			t.Errorf("field %d read back with Null=%v, written from %q", i, f.Null, values[i])
		}
	}
}

// CLAUDE.md rule 12: a line of MaxTableLineBytes commas is four million
// fields and the parser must not allocate for them, whether the commas are
// fields of their own or all sit inside one quoted field. It is sequential so
// the allocation it measures is its own.
func TestSplitCSVLineAllocatesForNoMoreFieldsThanATableMayHave(t *testing.T) {
	commas := []byte(strings.Repeat(",", provisioning.MaxTableLineBytes))
	quoted := append(append([]byte{'"'}, commas[:len(commas)-2]...), '"')

	for name, tc := range map[string]struct {
		line []byte
		want error
	}{
		"bare commas":   {commas, provisioning.ErrTableRowFieldCount},
		"quoted commas": {quoted, provisioning.ErrTableFieldTooLong},
	} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := provisioning.SplitCSVLine(tc.line)
		runtime.ReadMemStats(&after)

		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: error = %v, want %v", name, err, tc.want)
		}
		// The bound is generous: the point is 100 MiB against a few KiB.
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4<<20 {
			t.Fatalf("%s: parsing allocated %d bytes for a line that is refused anyway", name, allocated)
		}
	}
}

// The parser reads one field past the widest table, so a row one column too
// wide is still refused by validateRow with its exact count, and stops
// there; a header that wide is a header mismatch, as any other width is.
func TestSplitCSVLineStopsOneFieldPastTheWidestTable(t *testing.T) {
	t.Parallel()
	widest := strings.Repeat("x,", provisioning.MaxDefinitionTableColumns-1) + "x"
	fields, err := provisioning.SplitCSVLine([]byte(widest))
	if err != nil || len(fields) != provisioning.MaxDefinitionTableColumns {
		t.Fatalf("the widest allowed row: %d fields, %v", len(fields), err)
	}
	fields, err = provisioning.SplitCSVLine([]byte(widest + ",x"))
	if err != nil || len(fields) != provisioning.MaxDefinitionTableColumns+1 {
		t.Fatalf("one field past the widest table: %d fields, %v; want it parsed so its count can be reported", len(fields), err)
	}
	if _, err := provisioning.SplitCSVLine([]byte(widest + ",x,x")); !errors.Is(err, provisioning.ErrTableRowFieldCount) {
		t.Fatalf("two fields past the widest table: %v, want ErrTableRowFieldCount", err)
	}

	// The first chunk's early header check is where such a file is refused.
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)
	content := widest + ",x,x\n1,Ann,\n"
	data, err := service.BeginTableUpload(t.Context(), contest, "suspects", int64(len(content)))
	if err != nil {
		t.Fatalf("begin table upload: %v", err)
	}
	_, err = service.AppendTableChunk(t.Context(), contest, data.ID, 0, strings.NewReader(content))
	if !errors.Is(err, provisioning.ErrTableHeaderMismatch) {
		t.Fatalf("a header wider than any table = %v, want ErrTableHeaderMismatch", err)
	}
}

// To COPY, a quote anywhere in a field opens a quoted run, so `1,ab"cd,2`
// is an unterminated quoted field, not three values.
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

// Text after a closing quote is appended, so `"Bo"bby` is the single value
// Bobby, as in COPY.
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

// <input type="datetime-local"> sends "2024-01-01T10:00" when the seconds
// were never touched, whatever its step.
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

// The content is written straight to the store, bypassing AppendTableChunk's
// early header check, to test the end-of-upload fallback.
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
	// Bring the fake's received-bytes count up to date by hand, or
	// CompleteTableUpload stops at its length check.
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

// Excel's plain "CSV" on a Russian or Romanian Windows writes cp1251.
func TestCompleteTableUploadRefusesTextThatIsNotUTF8(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ content, says string }{
		// "Марго" in cp1251.
		"cp1251":     {"id,name,nickname\n1,\xcc\xe0\xf0\xe3\xee,\n", "UTF-8"},
		"a NUL byte": {"id,name,nickname\n1,Mar\x00got,\n", "NUL"},
	} {
		service, _, _, _ := tableDataGames(t, true)
		contest := uuid.New()
		withSuspects(t, service, contest)
		data := beginTableUploadWithContent(t, service, contest, "suspects", tc.content)

		_, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID)
		if !errors.Is(err, provisioning.ErrTableValueInvalid) {
			t.Fatalf("%s: error = %v, want ErrTableValueInvalid", name, err)
		}
		if !strings.Contains(err.Error(), "row 1") || !strings.Contains(err.Error(), `"name"`) ||
			!strings.Contains(err.Error(), tc.says) {
			t.Fatalf("%s: error = %q, does not name row 1's name column and say %s", name, err, tc.says)
		}
	}
}

// Excel's "CSV UTF-8", which the refusal above recommends, starts with a BOM.
func TestCompleteTableUploadReadsAnExcelUTF8FileWithItsByteOrderMark(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	content := "\xef\xbb\xbfid,name,nickname\n1,Марго,\n"
	data := beginTableUploadWithContent(t, service, contest, "suspects", content)

	if _, err := service.CompleteTableUpload(t.Context(), uuid.New(), contest, data.ID); err != nil {
		t.Fatalf("CompleteTableUpload() = %v, want the file accepted", err)
	}
}

// The JSON body refuses a NUL first; this covers any other route.
func TestAppendTableRowRefusesANULCharacter(t *testing.T) {
	t.Parallel()
	service, _, _, _ := tableDataGames(t, true)
	contest := uuid.New()
	withSuspects(t, service, contest)

	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", "Mar\x00got", ""}); !errors.Is(err, provisioning.ErrTableValueInvalid) {
		t.Fatalf("error = %v, want ErrTableValueInvalid", err)
	}
}

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

// ParseFloat accepts "0x1p-2", which numeric_in refuses, and refuses
// "1e400", which numeric holds; NaN and Infinity must still pass.
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

// CLAUDE.md rule 2 on the form's path: the body limit does not bound one
// field, and an oversized field in the file breaks every later window read.
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

	atBound := strings.Repeat("x", provisioning.MaxTableFieldBytes)
	if _, err := service.AppendTableRow(t.Context(), uuid.New(), contest, "suspects", []string{"1", atBound, ""}); err != nil {
		t.Fatalf("a field of exactly MaxTableFieldBytes was refused: %v", err)
	}
}

// A one-column table keeps the file small enough to run quickly.
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
