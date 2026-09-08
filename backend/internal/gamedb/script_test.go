package gamedb_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
)

// readAllStatements drains a ScriptReader, failing the test on any error
// other than the io.EOF that means "no more statements".
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

// A semicolon means nothing while the reader is inside a string, an
// identifier or a dollar-quoted body — this is the one property a naive
// strings.Split(";") does not have, and the whole reason this file exists.
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

// A parameter placeholder must never be mistaken for the start of a
// dollar-quote: PostgreSQL's own tag grammar forbids a tag starting with a
// digit, which is what tells $1 apart from the opening of $1...$1.
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

// An extended string can hold a literal quote escaped with a backslash
// rather than by doubling — the one place a bare backslash inside a string
// actually means something in PostgreSQL (standard_conforming_strings is on
// everywhere else).
func TestABackslashEscapedQuoteInAnExtendedStringDoesNotEndTheString(t *testing.T) {
	t.Parallel()
	const script = `SELECT E'\'' AS x;`
	got := readAllStatements(t, script)
	if len(got) != 1 || got[0].Text != script {
		t.Fatalf("got %+v, want one statement reading %q", got, script)
	}
}

// The semicolon between the inner comment's own "*/" and the outer's real
// one is the part that actually proves nesting: without it, a reader that
// closed on the first "*/" (C's rule, not PostgreSQL's) would still produce
// the same Text for this script by accident — there would be nothing after
// the wrongly-early close but more comment text and the statement's real
// terminator. With a semicolon sitting right there, closing early hands it
// back as a second, truncated statement instead of leaving it inside the
// comment where it belongs.
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

// The same case, but as the second statement of a file that simply was never
// given a final newline — the shape a hand-saved script is very likely to
// have.
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

// The line a build failure is reported against is what the console's viewer
// jumps to, so it has to survive blank lines, comments and multi-line
// statements between the file's start and the one that matters.
func TestEachStatementRemembersTheLineItStartedOn(t *testing.T) {
	t.Parallel()
	const script = "SELECT 1;\n\n-- a comment\nSELECT 2;\nSELECT\n  3;\n"
	got := readAllStatements(t, script)
	// Statement 2 starts on the comment line: Line marks the first
	// non-whitespace byte after the previous ';', and a leading comment is
	// content, not whitespace — the same rule that lets a pg_dump comment
	// stay attached to the COPY it precedes (see copyFromStdinHeader).
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

// pg_dump (recent versions) writes \connect, \restrict and \unrestrict into
// its plain-text output — psql's own commands, never SQL, and PostgreSQL's
// "syntax error at or near \" explains nothing about what actually went
// wrong. The reader has to refuse these itself, by line, before Exec ever
// sees them.
func TestABackslashCommandAtLineStartIsRefusedByLineAndName(t *testing.T) {
	t.Parallel()
	r := gamedb.NewScriptReader(strings.NewReader("SELECT 1;\n\\restrict abc123\nSELECT 2;\n"))

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
	if !strings.Contains(syn.Message, "restrict") {
		t.Fatalf("message = %q, does not name the command", syn.Message)
	}
}

// A backslash that is not the very first byte of its line is not a psql
// command, even when the text right after it reads like one — only column
// zero, outside every quote and comment, means that.
func TestABackslashNotAtLineStartIsNotMistakenForACommand(t *testing.T) {
	t.Parallel()
	const script = "SELECT 1 -- see \\connect below, but this is a comment\n;"
	got := readAllStatements(t, script)
	if len(got) != 1 || got[0].Text != script {
		t.Fatalf("got %+v, want one statement reading %q", got, script)
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

// An open quote, comment or dollar-quote that never closes before EOF is a
// script the reader must refuse, not a statement it silently hands back
// truncated.
func TestAnUnterminatedStringAtEOFIsRefused(t *testing.T) {
	t.Parallel()
	r := gamedb.NewScriptReader(strings.NewReader(`SELECT 'never closes`))
	_, err := r.Next()
	var syn *gamedb.ScriptSyntaxError
	if !errors.As(err, &syn) {
		t.Fatalf("error is %T, want *ScriptSyntaxError: %v", err, err)
	}
}

// The core case: a COPY block's data streams through unmodified — \N stays
// \N, an escaped tab stays escaped, and a row that merely starts with a
// backslash (but is not exactly "\.") is data, not the terminator.
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

// pg_dump always precedes a table's data with a comment block; the reader
// has to see past it to recognise the COPY that follows.
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
	// The comment stays part of CopyHeader (see copyFromStdinHeader's own
	// doc) — PostgreSQL parses a leading comment before a real statement
	// exactly as if it were not there, so the header remains valid SQL to
	// hand to CopyFrom either way.
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

// Next refuses to run again while a COPY block's data has not been drained
// — the reader has no way to skip it itself without duplicating CopyData's
// own job of finding "\.".
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

// A COPY block truncated before its terminator is a malformed file, not
// silently accepted data.
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
