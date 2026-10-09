package provisioning

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// This file reads the table builder's CSV one bounded line at a time and
// writes one row back out. encoding/csv is not used for reading: csv.Reader
// has no ceiling on a field or a record, so one unterminated quote grows its
// buffer for as long as the upload lasts (CLAUDE.md rule 12).

// MaxTableFieldBytes bounds one CSV field: it caps what an unterminated quote
// can make the reader hold before it refuses with ErrTableFieldTooLong.
const MaxTableFieldBytes = 64 << 10

// MaxTableLineBytes bounds one CSV line (header or data row) while the scanner
// looks for its newline. It sits above MaxDefinitionTableColumns fields at
// MaxTableFieldBytes each (about 3.2 MiB), so it is the outer safety net for
// the scan buffer (CLAUDE.md rule 12), not a limit a real row meets.
const MaxTableLineBytes = 4 << 20

// MaxTableDataRows bounds the data rows (header excluded) in one table's CSV.
// gamefile.Limits.MaxFileBytes bounds the bytes; this bounds the count, since
// a file of short rows is large in count long before it is large in bytes.
const MaxTableDataRows = 200_000

// MaxTableDeletedRows bounds how many of a table's rows may be tombstoned
// (CLAUDE.md rule 2); the list is scanned on every window read. A CHECK in
// migration 27 repeats this number, so change both together.
const MaxTableDeletedRows = 10_000

// Why a table's CSV data could not be accepted (CLAUDE.md rule 1).
var (
	// ErrTableHeaderMismatch is a first line that does not name the table's
	// columns in order.
	ErrTableHeaderMismatch = errors.New("the file's header does not match the table's own columns")
	// ErrTableRowFieldCount is a data row whose field count differs from the
	// table's column count.
	ErrTableRowFieldCount = errors.New("a row's field count does not match the table's columns")
	// ErrTableValueInvalid is a field that does not parse as its column's
	// type, or an empty field in a NOT NULL column.
	ErrTableValueInvalid = errors.New("a value does not match its column's type")
	// ErrTableFieldTooLong is one field past MaxTableFieldBytes.
	ErrTableFieldTooLong = errors.New("a field is longer than this platform allows")
	// ErrTableLineTooLong is one line past MaxTableLineBytes.
	ErrTableLineTooLong = errors.New("a line is longer than this platform allows")
	// ErrTableTooManyRows is a file whose data rows exceed MaxTableDataRows.
	ErrTableTooManyRows = errors.New("the table has more rows than this platform allows")
)

// csvField is one parsed CSV field. Null is true when the field was empty and
// unquoted, which is NULL to COPY ... WITH (FORMAT csv); a quoted "" is the
// empty string. csvWriter keeps the same convention on the way out.
type csvField struct {
	Text string
	Null bool
}

// splitCSVLine parses one line (without its newline) into fields, in
// PostgreSQL's CSV dialect. It must agree with the COPY the build runs: a row
// accepted here and refused there fails the contest build, long after the
// upload the organiser could have fixed.
//
// It mirrors CopyReadAttributesCSV. Outside a quoted run, a comma ends the
// field and a quote at any position opens a run; inside one, "" is a literal
// quote and a single quote closes the run. So `1,ab"cd,2` is an unterminated
// quote and `"Bo"bby` is the value Bobby.
//
// One departure: this package's CSV is one row per line throughout, so a
// quote still open at the end of the line is refused (ErrTableValueInvalid)
// rather than continued onto the next line.
func splitCSVLine(line []byte) ([]csvField, error) {
	// One field per comma: exact unless a comma is quoted, a floor otherwise.
	// Saves regrowing the slice on every row of a large file.
	fields := make([]csvField, 0, bytes.Count(line, commaByte)+1)
	i := 0
	for {
		// Fast path for a field with no quote, which is nearly every field:
		// slice it straight into a string. A quote before the delimiter hands
		// the field to the walk below.
		if end := plainFieldEnd(line, i); end >= 0 {
			if end-i > MaxTableFieldBytes {
				return nil, ErrTableFieldTooLong
			}
			fields = append(fields, csvField{Text: string(line[i:end]), Null: end == i})
			if end >= len(line) {
				break
			}
			i = end + 1 // the comma; a field always follows, empty or not
			continue
		}

		var (
			buf     []byte
			inQuote bool
			quoted  bool // this field carried at least one quoted run
		)

	field:
		for i < len(line) {
			b := line[i]
			i++

			switch {
			case b == '"' && inQuote && i < len(line) && line[i] == '"':
				i++ // a doubled quote is one literal quote
				b = '"'
			case b == '"' && inQuote:
				inQuote = false
				continue // the closing quote itself is not data
			case b == '"':
				inQuote, quoted = true, true
				continue // nor is the opening one
			case b == ',' && !inQuote:
				i-- // leave the comma for the step below
				break field
			}

			buf = append(buf, b)
			if len(buf) > MaxTableFieldBytes {
				return nil, ErrTableFieldTooLong
			}
		}

		if inQuote {
			return nil, fmt.Errorf("%w: an opening quote is never closed", ErrTableValueInvalid)
		}
		fields = append(fields, csvField{Text: string(buf), Null: !quoted && len(buf) == 0})

		if i >= len(line) {
			break
		}
		i++ // the comma; a field always follows, including a trailing empty one
	}
	return fields, nil
}

var commaByte = []byte{','}

const plainFieldChars = ",\""

// plainFieldEnd returns where the field starting at i ends (the comma's index,
// or len(line) for the last field), or -1 when the field holds a quote and
// needs the walk in splitCSVLine.
func plainFieldEnd(line []byte, i int) int {
	next := bytes.IndexAny(line[i:], plainFieldChars)
	switch {
	case next < 0:
		return len(line)
	case line[i+next] == ',':
		return i + next
	default:
		return -1 // a quote before the delimiter: not this path's field
	}
}

// formatCSVRow encodes one row from a form (AppendTableRow) as a CSV line.
func formatCSVRow(fields []string) (string, error) {
	var buf bytes.Buffer
	w := newCSVWriter(&buf)
	if err := w.write(fields); err != nil {
		return "", fmt.Errorf("encode the row: %w", err)
	}
	return buf.String(), nil
}

// csvWriter writes one row per line and quotes a field only when it holds a
// comma, a quote or a line break. An empty field is written bare, which is
// NULL to COPY and to splitCSVLine alike: AppendTableRow validated it as NULL,
// and writing `""` would store an empty string instead (and fail COPY for a
// nullable integer column). encoding/csv.Writer gives no control over that.
type csvWriter struct{ w io.Writer }

func newCSVWriter(w io.Writer) *csvWriter { return &csvWriter{w: w} }

func (w *csvWriter) write(fields []string) error {
	for i, f := range fields {
		if i > 0 {
			if _, err := io.WriteString(w.w, ","); err != nil {
				return err
			}
		}
		if strings.ContainsAny(f, ",\"\n\r") {
			quoted := `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
			if _, err := io.WriteString(w.w, quoted); err != nil {
				return err
			}
			continue
		}
		if _, err := io.WriteString(w.w, f); err != nil {
			return err
		}
	}
	return nil
}

// tableLineScanner reads a CSV file one line at a time, bounded at
// MaxTableLineBytes. It duplicates gamedb.readDataLine because gamedb imports
// this package, so importing gamedb here would be a cycle.
type tableLineScanner struct {
	r    *bufio.Reader
	line int // the 1-based number of the line next() is about to return
	// offset is the file byte offset of the line next() is about to return,
	// for TableDataWindow's row marks to seek back to. A scanner started after
	// a seek must be told where it began (newTableLineScannerAt).
	offset int64
}

func newTableLineScanner(r io.Reader) *tableLineScanner {
	return &tableLineScanner{r: bufio.NewReaderSize(r, 64<<10)}
}

// newTableLineScannerAt is newTableLineScanner for a file already seeked to
// offset, so that the offsets it reports are the file's and not the reader's.
func newTableLineScannerAt(r io.Reader, offset int64) *tableLineScanner {
	s := newTableLineScanner(r)
	s.offset = offset
	return s
}

// trimLineEnding strips the newline and a "\r" right before it, which
// spreadsheets on Windows write. A CR that is data sits inside a quoted field,
// which ends with its closing quote, so it is never stripped.
func trimLineEnding(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	return bytes.TrimSuffix(b, []byte("\r"))
}

// next returns the next line without its newline, or io.EOF at the end. A
// final line with no trailing newline is still returned once.
func (s *tableLineScanner) next() ([]byte, error) {
	var line []byte
	for {
		chunk, err := s.r.ReadSlice('\n')
		if len(line)+len(chunk) > MaxTableLineBytes {
			return nil, ErrTableLineTooLong
		}
		switch {
		case err == nil:
			s.line++
			s.offset += int64(len(line) + len(chunk))
			if len(line) == 0 {
				return trimLineEnding(chunk), nil
			}
			return trimLineEnding(append(line, chunk...)), nil
		case errors.Is(err, bufio.ErrBufferFull):
			line = append(line, chunk...)
			continue
		case errors.Is(err, io.EOF):
			if len(chunk) == 0 && len(line) == 0 {
				return nil, io.EOF
			}
			s.line++
			s.offset += int64(len(line) + len(chunk))
			return append(line, chunk...), nil
		default:
			return nil, err
		}
	}
}

// firstLineIfComplete returns r's first line once its newline has arrived,
// reading at most MaxTableLineBytes+1 bytes. It serves a file still being
// uploaded (AppendTableChunk's early header check):
//   - the newline has arrived: the line without it, and true;
//   - no newline yet: nil, false, nil, since a header may span chunks;
//   - past the bound with no newline: ErrTableLineTooLong.
//
// tableLineScanner.next() cannot do this: it treats io.EOF as the end of the
// file, while mid-upload EOF only means no more bytes have landed, so a header
// cut mid-name would be read as complete and refused.
func firstLineIfComplete(r io.Reader) ([]byte, bool, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(line)+len(chunk) > MaxTableLineBytes {
			return nil, false, ErrTableLineTooLong
		}
		switch {
		case err == nil:
			return trimLineEnding(append(line, chunk...)), true, nil
		case errors.Is(err, bufio.ErrBufferFull):
			line = append(line, chunk...)
			continue
		case errors.Is(err, io.EOF):
			// No newline yet: the upload has not reached it.
			return nil, false, nil
		default:
			return nil, false, err
		}
	}
}

// utf8BOM is the byte-order mark Excel's "CSV UTF-8" writes at the very
// start of the file.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// splitHeaderLine is splitCSVLine for the first line, which may start with a
// byte-order mark. Left in, the mark becomes part of the first column's name
// and the header is refused for a difference no one can see. Only the header
// carries one, and the build skips the header line.
func splitHeaderLine(line []byte) ([]csvField, error) {
	return splitCSVLine(bytes.TrimPrefix(line, utf8BOM))
}

// headerFields is the header a CSV must start with: the table's column names,
// in order, as the definition declares them. Only an exact match is accepted.
func headerFields(table TableDefinition) []string {
	names := make([]string, len(table.Columns))
	for i, c := range table.Columns {
		names[i] = c.Name
	}
	return names
}

// validateHeader compares a parsed header with the table's columns. It runs
// before any data row is read, so a wrong header is reported as such.
func validateHeader(fields []csvField, table TableDefinition) error {
	want := headerFields(table)
	if len(fields) != len(want) {
		return fmt.Errorf("%w: the file has %d column(s), the table has %d",
			ErrTableHeaderMismatch, len(fields), len(want))
	}
	for i, f := range fields {
		if f.Null || f.Text != want[i] {
			return fmt.Errorf("%w: column %d is %q, want %q", ErrTableHeaderMismatch, i+1, f.Text, want[i])
		}
	}
	return nil
}

// validateRow checks one data row's field count, field lengths and value
// types against the table. row is the 1-based data row number, header
// excluded.
//
// The length check repeats splitCSVLine's because a row from a form
// (AppendTableRow) never passes through the parser, and the 1 MiB body limit
// does not bound a single field (CLAUDE.md rule 2).
func validateRow(fields []csvField, table TableDefinition, row int64) error {
	if len(fields) != len(table.Columns) {
		return fmt.Errorf("%w: row %d has %d field(s), the table has %d columns",
			ErrTableRowFieldCount, row, len(fields), len(table.Columns))
	}
	for i, f := range fields {
		col := table.Columns[i]
		if len(f.Text) > MaxTableFieldBytes {
			return fmt.Errorf("%w: row %d, column %q is %d bytes, the limit is %d",
				ErrTableFieldTooLong, row, col.Name, len(f.Text), MaxTableFieldBytes)
		}
		if f.Null {
			if !col.Nullable {
				return fmt.Errorf("%w: row %d, column %q is empty but is not nullable",
					ErrTableValueInvalid, row, col.Name)
			}
			continue
		}
		if err := validateScalar(f.Text, col.Type); err != nil {
			return fmt.Errorf("%w: row %d, column %q: %s", ErrTableValueInvalid, row, col.Name, err)
		}
	}
	return nil
}

// validateScalar is a fast pre-check that PostgreSQL would accept text for t,
// so an obviously wrong value is refused with its row and column rather than
// failing COPY during the build. It is not exhaustive (numeric precision, for
// one, is left to PostgreSQL).
//
// Every column, text included, refuses bytes that are not UTF-8 (Excel's
// plain "CSV" in a Russian or Romanian locale is cp1251) and NUL, as
// PostgreSQL does.
func validateScalar(text string, t ColumnType) error {
	if !utf8.ValidString(text) {
		return errors.New("is not UTF-8 text; save the file as CSV UTF-8 and upload it again")
	}
	if strings.ContainsRune(text, 0) {
		return errors.New("holds a NUL character, which PostgreSQL cannot store in any column")
	}
	switch t {
	case ColumnText:
		return nil
	case ColumnInteger:
		if _, err := strconv.ParseInt(text, 10, 32); err != nil {
			return fmt.Errorf("%q is not a whole number that fits a 32-bit integer", text)
		}
	case ColumnNumeric:
		if !validNumericLiteral(text) {
			return fmt.Errorf("%q is not a valid numeric literal", text)
		}
	case ColumnBoolean:
		if !validBoolean(text) {
			return fmt.Errorf("%q is not one of PostgreSQL's own boolean spellings (true/false/t/f/yes/no/y/n/1/0)", text)
		}
	case ColumnDate:
		if _, err := time.Parse("2006-01-02", text); err != nil {
			return fmt.Errorf("%q is not a date in YYYY-MM-DD form", text)
		}
	case ColumnTimestamp:
		if !validTimestamp(text) {
			return fmt.Errorf("%q is not a timestamp in YYYY-MM-DD HH:MM:SS form (the seconds may be left off)", text)
		}
	default:
		return fmt.Errorf("column type %q is not one this platform supports", t)
	}
	return nil
}

// scanNumericDigits consumes digits from *at and returns how many it took.
// It allows PostgreSQL 16's digit separator, a single underscore strictly
// between two digits: '1_000' is valid, '1__000', '_1000' and '1000_' are
// not. On zero digits *at is left where it was.
// TestAScriptSavedInTheCoreDatabaseAgreesWithPostgreSQLAboutEveryValueForm
// checks these forms against a live cluster.
func scanNumericDigits(s string, at *int) int {
	i, digits := *at, 0
	for i < len(s) {
		switch {
		case s[i] >= '0' && s[i] <= '9':
			i, digits = i+1, digits+1
		case s[i] == '_' && digits > 0 && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			i, digits = i+2, digits+1
		default:
			*at = i
			return digits
		}
	}
	*at = i
	return digits
}

// validNumericLiteral reports whether numeric_in would accept text, checked
// as syntax. strconv.ParseFloat is wrong for this: it rounds to 64 bits,
// refuses exponents such as "1e400" that numeric holds, and accepts hex
// floats ("0x1p-2") that numeric_in refuses.
//
// Whitespace is trimmed as numeric_in trims it. Unsigned NaN and signed
// Infinity/Inf are accepted case-insensitively.
// TestAScriptSavedInTheCoreDatabaseAgreesWithPostgreSQLAboutEveryValueForm
// casts every accepted form on a real PostgreSQL 16 cluster.
//
// Hand-written rather than a regular expression: this is the hottest call of
// upload validation, and the regexp was 75 ms of 178 ms on 200,000 rows of
// ten columns.
func validNumericLiteral(text string) bool {
	s := strings.TrimSpace(text)
	if s == "" {
		return false
	}
	if strings.EqualFold(s, "nan") {
		return true
	}
	body := s
	if body[0] == '+' || body[0] == '-' {
		body = body[1:]
	}
	if strings.EqualFold(body, "inf") || strings.EqualFold(body, "infinity") {
		return true
	}

	// Digits with an optional fraction (5, 5., 5.5) or a fraction alone (.5),
	// then an optional exponent. No case consumes an 'x', so hex floats fail.
	at := 0
	whole := scanNumericDigits(body, &at)
	fraction := 0
	if at < len(body) && body[at] == '.' {
		at++
		fraction = scanNumericDigits(body, &at)
	}
	if whole == 0 && fraction == 0 {
		return false
	}
	if at < len(body) && (body[at] == 'e' || body[at] == 'E') {
		at++
		if at < len(body) && (body[at] == '+' || body[at] == '-') {
			at++
		}
		if scanNumericDigits(body, &at) == 0 {
			return false
		}
	}
	// Any byte left over, including any non-ASCII byte, is not numeric.
	return at == len(body)
}

// validBoolean accepts PostgreSQL's common boolean spellings, case-insensitively.
func validBoolean(text string) bool {
	switch strings.ToLower(text) {
	case "true", "t", "yes", "y", "on", "1":
		return true
	case "false", "f", "no", "n", "off", "0":
		return true
	}
	return false
}

// timestampLayouts are the ISO shapes validTimestamp accepts. The set is
// narrower than PostgreSQL's input parser: a refusal is friendlier than a
// misparsed date.
//
// The layouts without seconds serve <input type="datetime-local">, which
// omits ":ss" whenever the organiser never touched the seconds, whatever its
// step attribute says.
var timestampLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999",
	"2006-01-02T15:04:05.999999",
	"2006-01-02 15:04",
	"2006-01-02T15:04",
}

func validTimestamp(text string) bool {
	for _, layout := range timestampLayouts {
		if _, err := time.Parse(layout, text); err == nil {
			return true
		}
	}
	return false
}
