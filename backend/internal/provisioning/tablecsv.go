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
)

// This file is the table builder's own CSV: reading it in, one bounded line
// at a time, and writing one row back out the same way. Nothing here touches
// a database or a disk — TableDataError and the sentinels below are what a
// caller (tabledata.go) turns a parsing failure into, and formatCSVRow is
// what it hands to gamefile.Store.Append for a row a form submitted.
//
// Why not encoding/csv: csv.Reader has no ceiling on a field or a record —
// one unterminated quote grows its internal buffer for as long as the
// reader keeps handing it bytes, which for an organiser's own upload is
// exactly the untrusted-sized input CLAUDE.md rule 12 is about (the same
// reasoning internal/gamedb/script.go already gives for not using
// bufio.Reader.ReadBytes to find COPY's own line endings). The reader below
// enforces MaxTableLineBytes and MaxTableFieldBytes the instant either is
// crossed, the same shape gamedb's own readDataLine bounds one row of a SQL
// dump. Writing a row back out has no such risk — the fields already passed
// through this bound on the way in, or came from one HTTP request body no
// bigger than a single row — so encoding/csv.Writer is used for that
// direction (formatCSVRow) without reservation.

// MaxTableFieldBytes bounds one CSV field.
//
// Sixty-four kibibytes is generous for anything a detective game's own table
// holds — a witness statement, an address — while bounding what one hostile
// field (an unterminated quote with no comma or newline in sight) can make
// this package hold before it gives up and refuses by name (ErrTableFieldTooLong
// names the row and the column, not just "line too long").
const MaxTableFieldBytes = 64 << 10

// MaxTableLineBytes bounds one CSV line — the header or one data row —
// while the scanner looks for the newline that ends it. Comfortably above
// what MaxDefinitionTableColumns fields at MaxTableFieldBytes each could
// legitimately reach (50 × 64 KiB ≈ 3.2 MiB), so this is the outer safety
// net for the scan buffer (CLAUDE.md rule 12) rather than a bound anyone
// meets by writing a real row.
const MaxTableLineBytes = 4 << 20

// MaxTableDataRows bounds how many data rows (the header does not count) one
// table's CSV may hold.
//
// The design's own example game is a few hundred rows a table; two hundred
// thousand is far past anything a participant would be expected to reason
// about during a timed round, while staying small enough that a boundary
// test can actually write that many lines and finish quickly (TestMaxTableDataRowsBoundary).
// The byte-size ceiling a deployment already applies to the chunked upload
// (gamefile.Limits.MaxFileBytes) bounds the file; this bounds the row count
// on its own, because a CSV of very short rows can be large in count long
// before it is large in bytes.
const MaxTableDataRows = 200_000

// MaxTableDeletedRows bounds how many of a table's rows an organiser may
// tombstone (DeleteTableRow's own doc explains why a delete is a tombstone
// and not a rewrite). Ten thousand is far past anything manual curation of
// example data would ever reach, while keeping the array small enough that
// reading it back, and scanning it on every window read, costs nothing
// worth measuring (CLAUDE.md rule 2 — every list that reaches storage has an
// explicit bound). Migration 27's own CHECK repeats this number in SQL, so
// the two cannot silently disagree.
const MaxTableDeletedRows = 10_000

// Why a table's CSV data could not be accepted. Declared sentinels
// (CLAUDE.md rule 1) so a handler's fail switch can tell an organiser what
// is wrong with their file instead of "internal error".
var (
	// ErrTableHeaderMismatch is a first line that does not name, in order,
	// exactly the columns the table's own definition declares.
	ErrTableHeaderMismatch = errors.New("the file's header does not match the table's own columns")
	// ErrTableRowFieldCount is a data row whose field count does not match
	// the header (and so the table's own column count).
	ErrTableRowFieldCount = errors.New("a row's field count does not match the table's columns")
	// ErrTableValueInvalid is a field that does not parse as its column's
	// type, or an empty field in a column the definition marked NOT NULL.
	ErrTableValueInvalid = errors.New("a value does not match its column's type")
	// ErrTableFieldTooLong is one field past MaxTableFieldBytes.
	ErrTableFieldTooLong = errors.New("a field is longer than this platform allows")
	// ErrTableLineTooLong is one line (header or data row) past
	// MaxTableLineBytes.
	ErrTableLineTooLong = errors.New("a line is longer than this platform allows")
	// ErrTableTooManyRows is a file whose data rows exceed MaxTableDataRows.
	ErrTableTooManyRows = errors.New("the table has more rows than this platform allows")
)

// csvField is one field of a parsed CSV line. Null is true exactly when the
// field was empty and unquoted — PostgreSQL's own COPY ... WITH (FORMAT csv)
// default NULL representation, which is what LoadTableData's own COPY is run
// with (gamedb.Provisioner.LoadTableData), so a quoted empty field ("") is
// kept apart from a bare one as a real empty string rather than NULL.
//
// This is the one convention the whole feature turns on, and csvWriter is its
// other half: a field this package writes empty is written bare, so that what
// AppendTableRow validated as a NULL is the same thing a build loads and the
// same thing this reader would parse back.
type csvField struct {
	Text string
	Null bool
}

// splitCSVLine parses one line (without its trailing newline) into fields.
//
// The dialect is PostgreSQL's own, because the only thing this parser exists
// to do is answer, minutes early and with a row number, the question
// `COPY ... WITH (FORMAT csv)` will answer later on the build. Any disagreement
// between the two is worse than no check at all: a row this accepted and COPY
// refuses is reported to the organiser as a failure of their *contest*,
// hours after the upload they could have fixed it in.
//
// So this mirrors `CopyReadAttributesCSV`: a two-state walk per field, not a
// look at the field's first byte. Outside a quoted run, a comma ends the
// field and a quote — at any position, not only the first — opens one; inside
// one, a doubled `""` is a literal quote and a single one closes the run and
// returns to the outside state, where more bytes may still follow. That last
// part is what makes `1,ab"cd,2` an unterminated quoted field rather than
// three tidy values, and `"Bo"bby` the single value Bobby rather than an
// error. Treating the quote as special only in the first byte got both of
// those wrong, in the direction that hurts: it accepted a row COPY would
// refuse.
//
// The one deliberate departure: a raw newline inside a quoted field. This
// package's CSV is one row per line throughout, from the "row 5" a delete
// refers to down to how a chunked upload's header is found, and a dialect
// that let a field's own bytes decide where a line ends would break every one
// of those — so a quote left open at the end of the line is refused
// (ErrTableValueInvalid names it precisely, since by then the line has
// already been read whole under MaxTableLineBytes) rather than continued onto
// the next.
//
// Null is the same convention as before and as COPY's: a field is NULL
// exactly when it is empty and carried no quotes at all, so `""` is the empty
// string and a bare empty field is absent (csvField's own doc).
func splitCSVLine(line []byte) ([]csvField, error) {
	// One comma per field boundary, so this is the exact field count for every
	// line that has no quoted comma in it and a floor for the rest — enough to
	// keep the slice from being regrown four times for a ten-column row, which
	// on a two-hundred-thousand-row file is most of a million allocations
	// nobody needed.
	fields := make([]csvField, 0, bytes.Count(line, commaByte)+1)
	i := 0
	for {
		// The field with no quote anywhere in it, which is nearly every field
		// of nearly every file: its bytes are already contiguous in the line,
		// so they become one string directly instead of being appended one at
		// a time into a buffer that is then copied into a string. The walk
		// below is what the quoted cases still need, and this hands over to it
		// the moment a '"' turns up before the field's own delimiter — so the
		// two-state parsing splitCSVLine's doc describes is untouched, and
		// `1,ab"cd,2` and `"Bo"bby` still go through it.
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
				i++     // a doubled quote is one literal quote
				b = '"' // ...and the only one of the two that is data
			case b == '"' && inQuote:
				inQuote = false
				continue // the closing quote itself is not data
			case b == '"':
				inQuote, quoted = true, true
				continue // nor is the opening one
			case b == ',' && !inQuote:
				i-- // leave the delimiter for the caller's own step below
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

// commaByte and plainFieldChars are the delimiter and the two bytes that can
// end the "no quote in this field" fast path: the delimiter itself, and the
// quote that means the field is not plain after all.
var commaByte = []byte{','}

const plainFieldChars = ",\""

// plainFieldEnd reports where the field starting at i ends, or -1 when the
// field carries a quote and has to be parsed by the walk in splitCSVLine.
//
// The answer is len(line) for the last field of a line and the index of the
// comma otherwise, which is the same "leave the delimiter for the caller"
// convention the walk uses.
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

// formatCSVRow encodes one row for the file — the counterpart of
// splitCSVLine, used when a row comes from a form rather than an uploaded
// file (AppendTableRow). encoding/csv.Writer is safe here in a way
// splitCSVLine's own doc says it is not for reading: every field already
// passed through MaxTableFieldBytes when it was validated, so there is
// nothing left for an unbounded write to grow without limit.
func formatCSVRow(fields []string) (string, error) {
	var buf bytes.Buffer
	w := newCSVWriter(&buf)
	if err := w.write(fields); err != nil {
		return "", fmt.Errorf("encode the row: %w", err)
	}
	return buf.String(), nil
}

// csvWriter is the small encoder formatCSVRow uses. Hand-rolled rather than
// encoding/csv.Writer for one reason: this package's CSV is strictly one row
// per line (splitCSVLine's own doc), and csv.Writer defaults to writing "\r\n"
// on some platforms' conventions and offers no simple way to pin "\n" without
// also losing control of exactly when quoting happens for the empty-vs-null
// distinction this package's reader draws (csvField.Null).
//
// Quoted in exactly the three cases RFC 4180 quotes — a field containing a
// comma, a quote or a newline — and in no others. An empty field in
// particular is written bare, which is the whole point of the hand-rolled
// writer: a bare empty field is NULL to `COPY ... WITH (FORMAT csv)` and to
// splitCSVLine alike, and `""` is the empty string to both. The only caller
// that ever hands this an empty field is a row from a form
// (Games.AppendTableRow), which validated that field as a NULL — it is
// refused outright in a NOT NULL column — so writing it as `""` would be
// storing the opposite of what was checked: `invalid input syntax for type
// integer: ""` on the build for a nullable integer column, and an empty
// string stored where the organiser meant nothing at all for a nullable text
// one — so that the IS NULL a task asks about matches no row.
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
// MaxTableLineBytes and never holding more of the file than the line
// currently being read — the same shape gamedb.readDataLine bounds one row
// of a SQL dump, reimplemented here rather than imported: gamedb already
// depends on this package (schema.go's SchemaSource), so the reverse import
// would be the cycle Go refuses, and the routine is a dozen lines, not a
// shared protocol the two sides could drift apart on.
type tableLineScanner struct {
	r    *bufio.Reader
	line int // the 1-based number of the line next() is about to return
	// offset is how many bytes of the file lie before the line next() is about
	// to return — the file's own byte offset, so a scanner started at a seek
	// has to be told where it began (newTableLineScannerAt).
	//
	// It exists for TableDataWindow's row marks: an offset is only useful as
	// somewhere to seek back to, and only this loop knows how many bytes each
	// line actually took, trailing newline and CR included.
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

// trimLineEnding strips the newline a line was read up to, and — since a
// file whose lines end "\r\n" is what a spreadsheet on Windows writes by
// default (Excel, Numbers) — a trailing "\r" immediately in front of it as
// well. This only ever removes the byte that terminates the line: a "\r"
// that is data rather than punctuation is never the last byte before the
// line's own newline, because a quoted field's own bytes (splitCSVLine's
// own doc: a field never sees a raw newline) end with the closing quote,
// not with whatever character happened to precede it, so a literal CR
// inside a quoted field is untouched by this.
func trimLineEnding(b []byte) []byte {
	b = bytes.TrimSuffix(b, []byte("\n"))
	return bytes.TrimSuffix(b, []byte("\r"))
}

// next returns the next line's bytes, without its trailing newline, or
// io.EOF once the reader is exhausted. A final line with no trailing newline
// is still returned as data, exactly once, before the next call answers EOF.
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

// firstLineIfComplete looks for the newline that ends r's first line,
// never reading more than MaxTableLineBytes+1 bytes to find it — the bound
// tableLineScanner also enforces, applied here to a file that may still be
// mid-upload (AppendTableChunk's own early header check, tabledata.go). Its
// three-valued answer:
//   - the line has fully arrived: its bytes without the trailing newline,
//     and true;
//   - not enough of the file has arrived yet to contain a newline at all:
//     nil, false, nil — not an error, because a header genuinely may not
//     fit inside one chunk (AppendTableChunk's own doc explains why that is
//     not a reason to refuse);
//   - the bytes read so far already exceed the line bound with still no
//     newline in sight: ErrTableLineTooLong, since no chunk still to come
//     could make an already-too-long line short again.
//
// This cannot reuse tableLineScanner.next(): its io.EOF from the underlying
// reader means "the file is finished, this was its last line", which is
// true once CompleteTableUpload has all of it and false for a file a chunk
// upload is still writing to, where that same io.EOF only means "no more
// bytes have landed yet". Treating the second case as the first is exactly
// the bug this separate, smaller reader exists to avoid: it would read a
// header cut off mid-column-name as if that fragment were the whole thing,
// and refuse a perfectly good file for a header the upload had not finished
// sending.
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
			// Whatever bytes the file currently holds have all been read,
			// with no newline among them: this is not "the file's last
			// line", it is "the file's own writer has not gotten this far
			// yet". Nothing to report either way.
			return nil, false, nil
		default:
			return nil, false, err
		}
	}
}

// headerFields is the header line a completed CSV must start with —
// table.Columns' own names, in order, exactly as Definition.Tables[].Name
// carries them, before folding or quoting: it is the file's own promise that
// it still describes the table it was made for (this package's own brief),
// so anything other than an exact match is refused.
func headerFields(table TableDefinition) []string {
	names := make([]string, len(table.Columns))
	for i, c := range table.Columns {
		names[i] = c.Name
	}
	return names
}

// validateHeader compares a parsed header line against the table's own
// columns. Checked before a single data row is read (CompleteTableUpload's
// own doc): a file whose header is wrong is refused for that reason alone,
// never for the first row that also happens to be wrong.
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

// validateRow checks one data row's field count against the table's own
// columns and, for every field, that it is within MaxTableFieldBytes and that
// its value parses as that column's type (or is empty and the column allows
// NULL). row is the 1-based data row number (the header does not count —
// tabledata.go's own doc on row numbers explains why they are stable
// identifiers rather than a line count).
//
// The length bound is repeated here rather than left to splitCSVLine, which
// already enforces it while parsing a file: a row from a form never passes
// through that parser at all (Games.AppendTableRow hands the values straight
// over from the decoded request), and the 1 MiB body limit bounds the request
// rather than one field of it — so without this, the form is a way to store a
// field sixteen times the max_field_bytes this service publishes, after which
// every window read of the table refuses to parse the file it created
// (CLAUDE.md rule 2).
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

// validateScalar reports whether text is a value PostgreSQL would accept for
// t, using Go's own parsers as a fast, honest pre-check — not a promise that
// PostgreSQL will agree in every last case (numeric's precision rules are its
// own, not reimplemented here), only that a value obviously wrong for its
// column's type is refused with the row and column that named it rather than
// however many minutes into a build PostgreSQL's own COPY would take to say
// the same thing.
func validateScalar(text string, t ColumnType) error {
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

// scanNumericDigits consumes, from *at, one or more decimal digits optionally
// grouped with a single underscore between any two of them — PostgreSQL 16's
// own digit separator — and reports how many digits it took.
//
// Verified against a live PostgreSQL 16 instance rather than assumed:
// '1_000'::numeric is 1000, but '1__000', '_1000' and '1000_' are all
// refused, because an underscore must sit strictly between two digits — never
// lead, trail, or double. Verified again on every run rather than once by
// hand: TestAScriptSavedInTheCoreDatabaseAgreesWithPostgreSQLAboutEveryValueForm
// (game_integration_test.go) casts each of those four forms on the game
// cluster and fails if this file and the database have stopped agreeing.
// That is exactly what the second case below encodes:
// an underscore is consumed only together with the digit that must follow it,
// and only once a digit has already been seen.
//
// Zero means there were no digits here at all, and *at is left where it was.
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

// validNumericLiteral reports whether text is a value PostgreSQL's own
// numeric_in would accept for a numeric column — checked as syntax, not
// evaluated as a 64-bit float. strconv.ParseFloat is the wrong tool for this
// column type for three separate reasons: it rounds to 64 bits of mantissa
// where numeric keeps arbitrary precision exactly; it reports an
// out-of-range exponent (e.g. "1e400") as an error where numeric simply
// holds the value; and it accepts a hexadecimal float literal ("0x1p-2")
// that numeric_in has never accepted. A value ParseFloat waved through for
// any of those three reasons is exactly the failure this whole pre-check
// exists to avoid: an upload accepted here and refused later, minutes into a
// COPY, as a build failure rather than an upload one.
//
// Whitespace is trimmed the same way numeric_in itself trims it. NaN
// (unsigned only — '+NaN' and '-NaN' are both refused) and, since
// PostgreSQL 14, signed Infinity/Inf are accepted case-insensitively as the
// type's own special values. This was verified against a live PostgreSQL 16
// instance (this platform's own target, deploy/docker-compose.yml) rather
// than assumed from the type's older behaviour: 'NaN', 'Infinity', 'Inf',
// '-Infinity' and '+Inf' all cast to numeric on it — and go on doing so,
// because that check now runs by itself
// (TestAScriptSavedInTheCoreDatabaseAgreesWithPostgreSQLAboutEveryValueForm,
// game_integration_test.go). Every form this function accepts is cast on the
// real cluster there, so a value waved through here that COPY would refuse —
// the failed build this whole pre-check exists to prevent — fails a test
// instead of a contest.
//
// Written out rather than expressed as a regular expression, which is what it
// used to be. This is the hottest routine of the upload's own validation pass:
// two hundred thousand rows of ten columns measured at 178 ms and 166 MB of
// garbage, and 75 ms of that 178 was this one call — 188 ns a time, four
// hundred thousand times — inside the HTTP request that completes the upload,
// on the process serving the olympiad. The grammar is a dozen lines of
// character tests; a regular expression bought nothing here but the cost of an
// engine.
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

	// The decimal syntax numeric_in accepts for an ordinary value: digits with
	// an optional fractional part (5, 5., 5.5), or a fractional part on its own
	// (.5), then an optional exponent. Deliberately not strconv.ParseFloat, for
	// the three reasons above — and note that this refuses the hexadecimal float
	// literal (0x1p-2) ParseFloat accepts and numeric_in never has, simply by
	// never having a case that consumes an 'x'.
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
	// Anything left over is a byte numeric_in would not have accepted either —
	// including any byte above ASCII, which no case above can consume.
	return at == len(body)
}

// validBoolean matches PostgreSQL's own accepted spellings for a boolean
// literal (case-insensitive prefixes of true/false, plus the single-letter
// and numeral forms), per its own documentation of boolean input.
func validBoolean(text string) bool {
	switch strings.ToLower(text) {
	case "true", "t", "yes", "y", "on", "1":
		return true
	case "false", "f", "no", "n", "off", "0":
		return true
	}
	return false
}

// timestampLayouts are the shapes validTimestamp accepts — the ISO form
// PostgreSQL's own output uses, with or without fractional seconds and with
// either a space or a "T" between the date and the time. Deliberately not
// every format PostgreSQL's input parser accepts (it accepts many): a
// generated or organiser-curated CSV is expected to use one of these, and a
// narrower accepted set is a friendlier refusal than a silently-misparsed
// date under a looser one.
//
// The two without a seconds field exist for one caller, not CSV files:
// <input type="datetime-local"> (game-builder-table.tsx) hands its own value
// straight to appendTableRowAction, and a browser's own serialisation of
// that value omits ":ss" whenever the organiser never touched the seconds
// sub-field, regardless of the input's own step attribute — verified
// against a live browser rather than assumed, since MDN's own wording on
// this is easy to misread as "step=1 always keeps it". Refusing a value the
// widget itself cannot be made to send would leave setting a nonzero second
// as the only way around a 400 an organiser has no reason to expect.
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
