package gamedb_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
)

func readAllStatements(t *testing.T, src string) []gamedb.Statement {
	t.Helper()

	r := gamedb.NewScriptReader(strings.NewReader(src))
	var out []gamedb.Statement
	for {
		stmt, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next(): %v", err)
		}
		out = append(out, stmt)
	}
}

func TestAnEmptyScriptYieldsNoStatements(t *testing.T) {
	t.Parallel()
	if got := readAllStatements(t, ""); len(got) != 0 {
		t.Fatalf("got %d statements from an empty script, want 0: %+v", len(got), got)
	}
}

// A semicolon means nothing inside a string, identifier or dollar-quote.
func TestASemicolonInsideASingleQuotedLiteralDoesNotEndTheStatement(t *testing.T) {
	t.Parallel()
	const script = `SELECT ';' AS x;`
	got := readAllStatements(t, script)
	if len(got) != 1 || got[0].Text != script {
		t.Fatalf("got %+v, want one statement reading %q", got, script)
	}
}

func TestASemicolonInsideADoubleQuotedIdentifierDoesNotEndTheStatement(t *testing.T) {
	t.Parallel()
	const script = `SELECT 1 AS ";";`
	got := readAllStatements(t, script)
	if len(got) != 1 || got[0].Text != script {
		t.Fatalf("got %+v, want one statement reading %q", got, script)
	}
}

func TestASemicolonInsideAnEmptyTagDollarQuoteDoesNotEndTheStatement(t *testing.T) {
	t.Parallel()
	const script = `CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$SELECT 1; SELECT 2;$$;`
	got := readAllStatements(t, script)
	if len(got) != 1 || got[0].Text != script {
		t.Fatalf("got %+v, want one statement reading %q", got, script)
	}
}

func TestASemicolonInsideANamedTagDollarQuoteDoesNotEndTheStatement(t *testing.T) {
	t.Parallel()
	const script = `CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $body$SELECT 1; SELECT 2;$body$;`
	got := readAllStatements(t, script)
	if len(got) != 1 || got[0].Text != script {
		t.Fatalf("got %+v, want one statement reading %q", got, script)
	}
}

// A tag cannot start with a digit, which tells $1 apart from a dollar-quote.
func TestADollarPlaceholderInAFunctionBodyIsNotMistakenForADollarQuote(t *testing.T) {
	t.Parallel()
	const script = `CREATE FUNCTION f(int) RETURNS int LANGUAGE sql AS $$SELECT $1 + 1;$$;`
	got := readAllStatements(t, script)
	if len(got) != 1 || got[0].Text != script {
		t.Fatalf("got %+v, want one statement reading %q", got, script)
	}
}

func TestADoubledQuoteInsideALiteralIsALiteralQuoteNotTheEnd(t *testing.T) {
	t.Parallel()
	const script = `SELECT 'it''s fine';`
	got := readAllStatements(t, script)
	if len(got) != 1 || got[0].Text != script {
		t.Fatalf("got %+v, want one statement reading %q", got, script)
	}
}

func TestABackslashEscapedQuoteInAnExtendedStringDoesNotEndTheString(t *testing.T) {
	t.Parallel()
	const script = `SELECT E'\'' AS x;`
	got := readAllStatements(t, script)
	if len(got) != 1 || got[0].Text != script {
		t.Fatalf("got %+v, want one statement reading %q", got, script)
	}
}

// The ';' between the inner and outer "*/" is what proves nesting: a reader
// closing on the first "*/" (C's rule) would return it as a second statement.
func TestBlockCommentsNestUnlikeC(t *testing.T) {
	t.Parallel()
	const script = `SELECT /* outer /* inner */ ; still outer */ 1;`
	got := readAllStatements(t, script)
	if len(got) != 1 || got[0].Text != script {
		t.Fatalf("got %+v, want one statement reading %q", got, script)
	}
}

func TestALineCommentHidesASemicolonUntilItsNewline(t *testing.T) {
	t.Parallel()
	const script = "SELECT 1 -- trailing ; comment\n;"
	got := readAllStatements(t, script)
	if len(got) != 1 || got[0].Text != script {
		t.Fatalf("got %+v, want one statement reading %q", got, script)
	}
}

func TestAStatementWithNoTrailingSemicolonAtEOFIsStillReturned(t *testing.T) {
	t.Parallel()
	const script = `SELECT 1`
	got := readAllStatements(t, script)
	if len(got) != 1 || got[0].Text != script {
		t.Fatalf("got %+v, want one statement reading %q", got, script)
	}
}

// The shape of a hand-saved script.
func TestAFileWithNoFinalNewlineStillYieldsItsLastStatement(t *testing.T) {
	t.Parallel()
	got := readAllStatements(t, "SELECT 1;\nSELECT 2")
	if len(got) != 2 {
		t.Fatalf("got %d statements, want 2: %+v", len(got), got)
	}
	if got[1].Text != "SELECT 2" {
		t.Fatalf("second statement = %q", got[1].Text)
	}
}

// The console's viewer jumps to this line.
func TestEachStatementRemembersTheLineItStartedOn(t *testing.T) {
	t.Parallel()
	const script = "SELECT 1;\n\n-- a comment\nSELECT 2;\nSELECT\n  3;\n"
	got := readAllStatements(t, script)
	// Statement 2 starts on the comment line: a leading comment is content,
	// not whitespace.
	want := []int{1, 3, 5}
	if len(got) != len(want) {
		t.Fatalf("got %d statements, want %d: %+v", len(got), len(want), got)
	}
	for i, line := range want {
		if got[i].Line != line {
			t.Fatalf("statement %d starts at line %d, want %d (%q)", i, got[i].Line, line, got[i].Text)
		}
	}
}

// pg_dump can write \connect, which is psql's, not SQL; PostgreSQL's
// "syntax error at or near \" would explain nothing.
func TestABackslashCommandAtLineStartIsRefusedByLineAndName(t *testing.T) {
	t.Parallel()
	r := gamedb.NewScriptReader(strings.NewReader("SELECT 1;\n\\connect otherdb\nSELECT 2;\n"))

	if _, err := r.Next(); err != nil {
		t.Fatalf("the first statement: %v", err)
	}

	_, err := r.Next()
	if err == nil {
		t.Fatal("a psql meta-command was accepted as SQL")
	}
	var syn *gamedb.ScriptSyntaxError
	if !errors.As(err, &syn) {
		t.Fatalf("error is %T, want *ScriptSyntaxError: %v", err, err)
	}
	if syn.Line != 2 {
		t.Fatalf("line = %d, want 2", syn.Line)
	}
	if !strings.Contains(syn.Message, "connect") {
		t.Fatalf("message = %q, does not name the command", syn.Message)
	}
}

// pg_dump 16.10/17.6/18+ always writes \restrict and \unrestrict, and an
// organiser cannot turn them off.
func TestRestrictAndUnrestrictAreSkippedRatherThanRefused(t *testing.T) {
	t.Parallel()
	const script = "\\restrict abc123\n" +
		"SELECT 1;\n" +
		"\\unrestrict abc123\n"
	got := readAllStatements(t, script)
	if len(got) != 1 {
		t.Fatalf("got %d statements, want 1: %+v", len(got), got)
	}
	if got[0].Text != "SELECT 1;" {
		t.Fatalf("statement text = %q", got[0].Text)
	}
	// The skipped line is not counted: the statement starts on line 2.
	if got[0].Line != 2 {
		t.Fatalf("statement line = %d, want 2", got[0].Line)
	}
}

// Mid-statement, dropping the line would drop the buffered INSERT and run
// a lone ';'.
func TestARestrictInsideAStatementIsRefusedRatherThanDroppingIt(t *testing.T) {
	t.Parallel()
	const script = "INSERT INTO answers VALUES (1, 'secret')\n" +
		"\\restrict abc123\n" +
		";\n"
	r := gamedb.NewScriptReader(strings.NewReader(script))

	_, err := r.Next()
	if err == nil {
		t.Fatal("a \\restrict in the middle of a statement was skipped, taking the statement with it")
	}
	var syn *gamedb.ScriptSyntaxError
	if !errors.As(err, &syn) {
		t.Fatalf("error is %T, want *ScriptSyntaxError: %v", err, err)
	}
	if syn.Line != 2 {
		t.Fatalf("line = %d, want 2", syn.Line)
	}
	if !strings.Contains(syn.Message, "restrict") {
		t.Fatalf("message = %q, does not name the directive", syn.Message)
	}
}

// Every real dump has a header comment block before \restrict, so comments
// must not count as a buffered statement.
func TestARestrictAfterOnlyCommentsIsStillSkipped(t *testing.T) {
	t.Parallel()
	const script = "--\n-- PostgreSQL database dump\n--\n\n" +
		"\\restrict abc123\n" +
		"SELECT 1;\n"
	got := readAllStatements(t, script)
	if len(got) != 1 {
		t.Fatalf("got %d statements, want 1: %+v", len(got), got)
	}
	if got[0].Text != "SELECT 1;" {
		t.Fatalf("statement text = %q", got[0].Text)
	}
}

// Only column zero, outside every quote and comment, marks a command.
func TestABackslashNotAtLineStartIsNotMistakenForACommand(t *testing.T) {
	t.Parallel()
	const script = "SELECT 1 -- see \\connect below, but this is a comment\n;"
	got := readAllStatements(t, script)
	if len(got) != 1 || got[0].Text != script {
		t.Fatalf("got %+v, want one statement reading %q", got, script)
	}
}

// Without a ceiling the word would buffer the rest of the file (CLAUDE.md
// rule 12).
func TestABackslashCommandWordWithNoTerminatorIsRefusedRatherThanBuffered(t *testing.T) {
	t.Parallel()
	r := gamedb.NewScriptReader(strings.NewReader(`\` + strings.Repeat("x", 8<<20)))

	_, err := r.Next()
	var syn *gamedb.ScriptSyntaxError
	if !errors.As(err, &syn) {
		t.Fatalf("error is %T, want *ScriptSyntaxError: %v", err, err)
	}
	// The refusal is where the word survives, so its size shows whether it grew.
	if len(syn.Message) > 512 {
		t.Fatalf("the refusal carries %d bytes of the script back", len(syn.Message))
	}
}

func TestAStatementLongerThanTheBufferIsRefused(t *testing.T) {
	t.Parallel()
	huge := "SELECT '" + strings.Repeat("x", 17<<20) + "';"
	r := gamedb.NewScriptReader(strings.NewReader(huge))

	_, err := r.Next()
	if err == nil {
		t.Fatal("a statement past the buffer bound was accepted")
	}
	var syn *gamedb.ScriptSyntaxError
	if !errors.As(err, &syn) {
		t.Fatalf("error is %T, want *ScriptSyntaxError: %v", err, err)
	}
}

func TestAnUnterminatedStringAtEOFIsRefused(t *testing.T) {
	t.Parallel()
	r := gamedb.NewScriptReader(strings.NewReader(`SELECT 'never closes`))
	_, err := r.Next()
	var syn *gamedb.ScriptSyntaxError
	if !errors.As(err, &syn) {
		t.Fatalf("error is %T, want *ScriptSyntaxError: %v", err, err)
	}
}

// \N and escapes stream unmodified, and a row merely starting with a
// backslash is data, not the terminator.
func TestACopyFromStdinBlockStreamsItsDataAndStopsAtTheTerminator(t *testing.T) {
	t.Parallel()
	const script = "COPY t (a, b) FROM stdin;\n" +
		"1\tfirst\n" +
		"\\N\tsecond\\twith\\tescapes\n" +
		"\\\\not-a-terminator\n" +
		"3\tthird\n" +
		"\\.\n" +
		"SELECT 1;\n"
	r := gamedb.NewScriptReader(strings.NewReader(script))

	stmt, err := r.Next()
	if err != nil {
		t.Fatalf("Next(): %v", err)
	}
	if stmt.CopyHeader != "COPY t (a, b) FROM stdin" {
		t.Fatalf("CopyHeader = %q", stmt.CopyHeader)
	}

	data, err := io.ReadAll(r.CopyData())
	if err != nil {
		t.Fatalf("reading COPY data: %v", err)
	}
	const want = "1\tfirst\n" +
		"\\N\tsecond\\twith\\tescapes\n" +
		"\\\\not-a-terminator\n" +
		"3\tthird\n"
	if string(data) != want {
		t.Fatalf("COPY data = %q, want %q", data, want)
	}

	next, err := r.Next()
	if err != nil {
		t.Fatalf("statement after COPY: %v", err)
	}
	if next.Text != "SELECT 1;" {
		t.Fatalf("statement after COPY = %q", next.Text)
	}
}

func TestACopyFromStdinIsRecognisedAfterALeadingComment(t *testing.T) {
	t.Parallel()
	const script = "--\n-- Data for Name: suspects; Type: TABLE DATA\n--\n\n" +
		"COPY public.suspects (id, name) FROM stdin;\n" +
		"1\tIonescu\n" +
		"\\.\n"
	r := gamedb.NewScriptReader(strings.NewReader(script))

	stmt, err := r.Next()
	if err != nil {
		t.Fatalf("Next(): %v", err)
	}
	// The comment stays in CopyHeader; PostgreSQL ignores it.
	if !strings.HasSuffix(stmt.CopyHeader, "COPY public.suspects (id, name) FROM stdin") {
		t.Fatalf("CopyHeader = %q", stmt.CopyHeader)
	}
	data, err := io.ReadAll(r.CopyData())
	if err != nil {
		t.Fatalf("reading COPY data: %v", err)
	}
	if string(data) != "1\tIonescu\n" {
		t.Fatalf("COPY data = %q", data)
	}
}

// Only the first token decides, and a comment is not one. Matching raw text
// once found `copy` in a comment and `from stdin` in the statement body,
// sending a plain CREATE TABLE to CopyFrom.
func TestOnlyTheFirstRealTokenDecidesWhetherAStatementIsACopyBlock(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		script  string
		areCopy bool
	}{
		{
			name:   "a line comment saying what the script does not do",
			script: "-- We copy the seed rows below rather than reading them from stdin.\nCREATE TABLE t (id int);",
		},
		{
			name:   "a line comment about a later step",
			script: "-- copy these from stdin later\nINSERT INTO guests VALUES (1);",
		},
		{
			name:   "a block comment saying the same thing",
			script: "/* copy rows from stdin */\nCREATE TABLE t (id int);",
		},
		{
			name:   "a string literal that reads like a header",
			script: "CREATE TABLE t (note text DEFAULT 'copy from stdin');",
		},
		{
			name:   "a comment inside a COPY that does not read from stdin",
			script: "COPY t (a) /* was: from stdin */ TO PROGRAM 'cat';",
		},
		{
			name:    "the real thing",
			script:  "COPY public.t (a) FROM stdin;\n\\.\n",
			areCopy: true,
		},
		{
			name:    "the real thing behind pg_dump's own comment block",
			script:  "--\n-- Data for Name: t; Type: TABLE DATA\n--\n\nCOPY public.t (a) FROM stdin;\n\\.\n",
			areCopy: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := gamedb.NewScriptReader(strings.NewReader(tc.script))
			stmt, err := r.Next()
			if err != nil {
				t.Fatalf("Next(): %v", err)
			}
			if isCopy := stmt.CopyHeader != ""; isCopy != tc.areCopy {
				t.Fatalf("CopyHeader = %q (a COPY block: %v), want a COPY block: %v",
					stmt.CopyHeader, isCopy, tc.areCopy)
			}
		})
	}
}

// A statement after the header on the same line cannot run; dropping it
// silently would build a game missing part of its data.
func TestAStatementAfterACopyHeaderOnTheSameLineIsRefusedRatherThanDropped(t *testing.T) {
	t.Parallel()
	const script = "COPY t (a) FROM stdin; SELECT setval('t_id_seq', 100);\n" +
		"1\n" +
		"\\.\n"
	r := gamedb.NewScriptReader(strings.NewReader(script))

	stmt, err := r.Next()
	if err == nil {
		t.Fatalf("the trailing statement was dropped silently; Next() returned %+v", stmt)
	}
	var syn *gamedb.ScriptSyntaxError
	if !errors.As(err, &syn) {
		t.Fatalf("error is %T, want *ScriptSyntaxError: %v", err, err)
	}
	if syn.Line != 1 {
		t.Fatalf("line = %d, want 1", syn.Line)
	}
}

func TestTrailingWhitespaceAfterACopyHeaderIsNotAStatement(t *testing.T) {
	t.Parallel()
	r := gamedb.NewScriptReader(strings.NewReader("COPY t (a) FROM stdin;  \t\r\n1\n\\.\n"))

	stmt, err := r.Next()
	if err != nil {
		t.Fatalf("Next(): %v", err)
	}
	if stmt.CopyHeader == "" {
		t.Fatalf("CopyHeader is empty for %q", stmt.Text)
	}
	data, err := io.ReadAll(r.CopyData())
	if err != nil {
		t.Fatalf("reading COPY data: %v", err)
	}
	if string(data) != "1\n" {
		t.Fatalf("COPY data = %q, want the one row", data)
	}
}

func TestNextRefusesWhileACopyBlocksDataIsUnread(t *testing.T) {
	t.Parallel()
	r := gamedb.NewScriptReader(strings.NewReader("COPY t FROM stdin;\n1\n\\.\nSELECT 1;\n"))
	if _, err := r.Next(); err != nil {
		t.Fatalf("Next(): %v", err)
	}
	if _, err := r.Next(); err == nil {
		t.Fatal("Next ran again with the previous COPY block's data unread")
	}
}

func TestACopyBlockWithNoTerminatorIsRefused(t *testing.T) {
	t.Parallel()
	r := gamedb.NewScriptReader(strings.NewReader("COPY t FROM stdin;\n1\tfirst\n"))
	if _, err := r.Next(); err != nil {
		t.Fatalf("Next(): %v", err)
	}
	_, err := io.ReadAll(r.CopyData())
	var syn *gamedb.ScriptSyntaxError
	if !errors.As(err, &syn) {
		t.Fatalf("error is %T, want *ScriptSyntaxError: %v", err, err)
	}
}

// endlessBytes yields one byte forever; under io.LimitReader it stands in
// for COPY data with no newline.
type endlessBytes byte

func (b endlessBytes) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

// CLAUDE.md rule 12: a never-ending data line is refused with a line number,
// not buffered.
func TestALineOfCopyDataPastTheBoundIsRefused(t *testing.T) {
	t.Parallel()
	src := io.MultiReader(
		strings.NewReader("COPY public.t (a) FROM stdin;\n"),
		io.LimitReader(endlessBytes('x'), 17<<20), // one line, no '\n' in sight
		strings.NewReader("\n\\.\n"),
	)
	r := gamedb.NewScriptReader(src)

	if _, err := r.Next(); err != nil {
		t.Fatalf("Next(): %v", err)
	}
	_, err := io.Copy(io.Discard, r.CopyData())
	if err == nil {
		t.Fatal("a COPY data line past the bound was read in full instead of refused")
	}
	var syn *gamedb.ScriptSyntaxError
	if !errors.As(err, &syn) {
		t.Fatalf("error is %T, want *ScriptSyntaxError: %v", err, err)
	}
	if syn.Line != 2 {
		t.Fatalf("line = %d, want 2 (the first data line)", syn.Line)
	}
}

// A long row is ordinary (one text or bytea column), so the bound must sit
// well above it.
func TestALongLineOfCopyDataUnderTheBoundIsStreamedWhole(t *testing.T) {
	t.Parallel()
	const wide = 1 << 20 // 1 MiB in one column
	src := io.MultiReader(
		strings.NewReader("COPY public.t (a) FROM stdin;\n"),
		io.LimitReader(endlessBytes('x'), wide),
		strings.NewReader("\n\\.\n"),
	)
	r := gamedb.NewScriptReader(src)

	if _, err := r.Next(); err != nil {
		t.Fatalf("Next(): %v", err)
	}
	n, err := io.Copy(io.Discard, r.CopyData())
	if err != nil {
		t.Fatalf("reading COPY data: %v", err)
	}
	if n != wide+1 { // the row's own bytes plus its newline
		t.Fatalf("streamed %d bytes, want %d", n, wide+1)
	}
}

// CopyFrom sends each Read as one unbuffered CopyData message, so a Read
// returning one row is one syscall per row. The row size divides nothing in
// particular, so passing requires packing rows.
func TestOneReadOfCopyDataFillsTheCallersBuffer(t *testing.T) {
	t.Parallel()
	// The buffer CopyFrom offers, and an ordinary row length.
	const copyFromBuffer = 65531
	row := strings.Repeat("x", 99) + "\n"
	rows := (copyFromBuffer / len(row)) * 4

	var script strings.Builder
	script.WriteString("COPY public.t (a) FROM stdin;\n")
	for range rows {
		script.WriteString(row)
	}
	script.WriteString("\\.\n")

	r := gamedb.NewScriptReader(strings.NewReader(script.String()))
	if _, err := r.Next(); err != nil {
		t.Fatalf("Next(): %v", err)
	}

	data := r.CopyData()
	buf := make([]byte, copyFromBuffer)
	n, err := data.Read(buf)
	if err != nil {
		t.Fatalf("first Read: %v", err)
	}
	// The last row may be split, but one row per Read must never happen.
	if n <= copyFromBuffer-len(row) {
		t.Fatalf("one Read returned %d of %d bytes — about %d rows, not a full buffer",
			n, copyFromBuffer, n/len(row))
	}

	rest, err := io.ReadAll(data)
	if err != nil {
		t.Fatalf("reading the rest: %v", err)
	}
	if total := n + len(rest); total != rows*len(row) {
		t.Fatalf("streamed %d bytes, want %d", total, rows*len(row))
	}
	if got := string(buf[:n]) + string(rest); got != strings.Repeat(row, rows) {
		t.Fatal("the data that came back is not the data that went in")
	}
}

// TestARealPgDumpFileIsReadInFull reads an actual `pg_dump --no-owner
// --no-privileges` dump (PostgreSQL 16.15), \restrict pair included, rather
// than a hand-made script.
func TestARealPgDumpFileIsReadInFull(t *testing.T) {
	t.Parallel()

	f, err := os.Open("testdata/pg_dump_16_languages.sql")
	if err != nil {
		t.Fatalf("open testdata: %v", err)
	}
	defer f.Close()

	r := gamedb.NewScriptReader(f)

	// Each statement by its start line, so a dropped, split or invented one
	// shows up as a line: eleven SET, one set_config, CREATE TABLE, the COPY
	// header and ALTER TABLE. The first starts at 7 because the header comment
	// belongs to it; \restrict (line 4) and \unrestrict (line 62) add none.
	wantLines := []int{7, 11, 12, 13, 14, 15, 16, 17, 18, 19, 21, 23, 25, 38, 49}
	const wantCopyAt = 38

	var (
		gotLines []int
		copyRows []byte
		sawCopy  bool
	)
	for {
		stmt, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next(): %v", err)
		}
		gotLines = append(gotLines, stmt.Line)
		if stmt.CopyHeader == "" {
			continue
		}
		if stmt.Line != wantCopyAt {
			t.Fatalf("the COPY block was recognised at line %d, want %d", stmt.Line, wantCopyAt)
		}
		if !strings.Contains(stmt.CopyHeader, "COPY public.languages") {
			t.Fatalf("CopyHeader = %q", stmt.CopyHeader)
		}
		data, err := io.ReadAll(r.CopyData())
		if err != nil {
			t.Fatalf("reading COPY data: %v", err)
		}
		copyRows, sawCopy = data, true
	}

	if !slices.Equal(gotLines, wantLines) {
		t.Fatalf("statements start on lines %v, want %v", gotLines, wantLines)
	}
	if !sawCopy {
		t.Fatal("the COPY public.languages block was never seen")
	}
	const wantRows = "en\tEnglish\tEnglish\tt\t10\n" +
		"ro\tRomanian\tRomână\tt\t20\n" +
		"ru\tRussian\tРусский\tt\t30\n"
	if string(copyRows) != wantRows {
		t.Fatalf("COPY data = %q, want %q", copyRows, wantRows)
	}
}

// The reader assumes standard_conforming_strings=on. With it off,
// `('a\'); SELECT 1;` is one literal to the server and two statements here,
// and the script's SET would override the connection's. Refused by line.
func TestASetThatTurnsOffStandardConformingStringsIsRefusedByLine(t *testing.T) {
	t.Parallel()
	r := gamedb.NewScriptReader(strings.NewReader(
		"SET statement_timeout = 0;\nSET standard_conforming_strings = off;\nSELECT 1;\n"))

	if _, err := r.Next(); err != nil {
		t.Fatalf("the first statement: %v", err)
	}

	_, err := r.Next()
	if err == nil {
		t.Fatal("a script was allowed to move the server out of the dialect this reader parses in")
	}
	var syn *gamedb.ScriptSyntaxError
	if !errors.As(err, &syn) {
		t.Fatalf("error is %T, want *ScriptSyntaxError: %v", err, err)
	}
	if syn.Line != 2 {
		t.Fatalf("line = %d, want 2", syn.Line)
	}
	if !strings.Contains(syn.Message, "standard_conforming_strings") {
		t.Fatalf("message = %q, does not name the setting", syn.Message)
	}
}

// On is the dialect already assumed, and every modern pg_dump writes it.
// RESET is refused: it defers to the cluster's configuration.
func TestSettingStandardConformingStringsOnIsLeftAlone(t *testing.T) {
	t.Parallel()
	for _, statement := range []string{
		"SET standard_conforming_strings = on;",
		"SET standard_conforming_strings TO 'on';",
		"set session standard_conforming_strings = true;",
	} {
		got := readAllStatements(t, statement)
		if len(got) != 1 {
			t.Fatalf("%q gave %d statements, want 1", statement, len(got))
		}
	}

	r := gamedb.NewScriptReader(strings.NewReader("RESET standard_conforming_strings;"))
	if _, err := r.Next(); err == nil {
		t.Fatal("RESET standard_conforming_strings was accepted")
	}
}

// The third case holds the second's bytes in a hundredth of the statements,
// separating per-statement cost from scanning cost.
//
// Run with `go test -bench Reader -benchmem ./internal/gamedb/`.
func BenchmarkScriptReaderCopyDump(b *testing.B) {
	benchmarkScriptReader(b, copyDumpBytes(1<<24))
}

func BenchmarkScriptReaderInsertDump(b *testing.B) {
	benchmarkScriptReader(b, insertDumpBytes(1<<24, 1))
}

func BenchmarkScriptReaderWideInserts(b *testing.B) {
	benchmarkScriptReader(b, insertDumpBytes(1<<24, 1000))
}

func benchmarkScriptReader(b *testing.B, script string) {
	b.SetBytes(int64(len(script)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r := gamedb.NewScriptReader(strings.NewReader(script))
		for {
			stmt, err := r.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				b.Fatalf("reading the script: %v", err)
			}
			if stmt.CopyHeader == "" {
				continue
			}
			if _, err := io.Copy(io.Discard, r.CopyData()); err != nil {
				b.Fatalf("draining the COPY block: %v", err)
			}
		}
	}
}

// copyDumpBytes is roughly bytes of a pg_dump COPY block.
func copyDumpBytes(size int) string {
	var b strings.Builder
	b.WriteString("COPY public.guests (id, full_name, city) FROM stdin;\n")
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, "%d\tIonescu Vasile %d\tChisinau\n", i, i)
	}
	b.WriteString("\\.\n")
	return b.String()
}

// insertDumpBytes is roughly bytes of the same data as INSERTs, with
// rowsPerStatement rows in each (1 is what pg_dump --inserts writes).
func insertDumpBytes(size, rowsPerStatement int) string {
	var b strings.Builder
	for i := 0; b.Len() < size; {
		b.WriteString("INSERT INTO public.guests (id, full_name, city) VALUES")
		for row := 0; row < rowsPerStatement; row++ {
			if row > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "\n\t(%d, 'Ionescu Vasile %d', 'Chisinau')", i, i)
			i++
		}
		b.WriteString(";\n")
	}
	return b.String()
}

// Both refusals name the line in the shape the upload console parses
// (game-upload.tsx). The regular expression is repeated here because
// nothing else holds the two sides together.
func TestBothScriptFailuresNameTheLineInTheShapeTheConsoleParses(t *testing.T) {
	// The console's regular expression.
	console := regexp.MustCompile(`(?i)^line (\d+):`)

	for name, err := range map[string]error{
		"the reader's own refusal": &gamedb.ScriptSyntaxError{Line: 42, Message: "a psql meta-command"},
		"PostgreSQL's refusal": &gamedb.ScriptError{
			Line: 42, Message: `syntax error at or near "SELCT"`, SQLState: "42601",
		},
	} {
		t.Run(name, func(t *testing.T) {
			found := console.FindStringSubmatch(err.Error())
			if found == nil {
				t.Fatalf("the console cannot find a line number in %q", err.Error())
			}
			if found[1] != "42" {
				t.Fatalf("the console reads line %q, want 42, from %q", found[1], err.Error())
			}
		})
	}

	// An unlocated error carries no prefix.
	unlocated := (&gamedb.ScriptError{Message: "the cluster is out of disk"}).Error()
	if console.MatchString(unlocated) {
		t.Fatalf("an unlocated failure offered the console a line to jump to: %q", unlocated)
	}
}
