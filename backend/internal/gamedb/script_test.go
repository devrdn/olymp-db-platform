package gamedb_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
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

// pg_dump can write \connect into its plain-text output — psql's own
// command, never SQL, and PostgreSQL's "syntax error at or near \" explains
// nothing about what actually went wrong. The reader has to refuse it
// itself, by line, before Exec ever sees it. (\restrict and \unrestrict are
// the one pair of backslash commands this does *not* apply to — see
// TestRestrictAndUnrestrictAreSkippedRatherThanRefused.)
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

// \restrict and \unrestrict are the exception: pg_dump 16.10/17.6/18 and
// newer write \restrict <token> right after the dump header and
// \unrestrict <token> as the very last line, unconditionally and with no
// flag to suppress it (see script.go's own comment on this case for why
// letting just this pair through is not a weaker check than refusing every
// other backslash command). The reader must skip the line silently rather
// than refuse the script — an organiser exporting a dump with a stock
// pg_dump has no way to remove it.
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
	// The skipped \restrict line must not be counted: the statement starts
	// on line 2, not line 1.
	if got[0].Line != 2 {
		t.Fatalf("statement line = %d, want 2", got[0].Line)
	}
}

// Skipping the directive is only safe where pg_dump writes it: between two
// statements, with nothing of a statement buffered yet. A file assembled by
// hand, or two dumps concatenated, can put it in the middle of one — and
// dropping the line there would drop everything read before it, executing a
// lone ';' instead of the INSERT. A build that "succeeded" without part of
// its data is the one outcome worse than a refusal.
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

// The other side of the same boundary, and the layout every real dump has:
// pg_dump writes its header comment block *before* the \restrict line, so
// "nothing accumulated" has to mean "nothing but whitespace and comments" —
// counting a comment as content would refuse every dump the tool produces
// (TestARealPgDumpFileIsReadInFull is the same claim against the real file).
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

// The other unbounded reader in this file, and the one with no ceiling at
// all until this test: readPsqlCommandWord accumulated bytes until it met a
// space, a tab or a newline, so a line that opens with a backslash and never
// meets one took the rest of the file — up to GAME_UPLOAD_MAX_FILE_BYTES of
// it — into a single []byte inside the API process serving participants, and
// then copied it a second time into the refusal's own message (CLAUDE.md rule
// 12: the bound belongs where the bytes arrive).
func TestABackslashCommandWordWithNoTerminatorIsRefusedRatherThanBuffered(t *testing.T) {
	t.Parallel()
	r := gamedb.NewScriptReader(strings.NewReader(`\` + strings.Repeat("x", 8<<20)))

	_, err := r.Next()
	var syn *gamedb.ScriptSyntaxError
	if !errors.As(err, &syn) {
		t.Fatalf("error is %T, want *ScriptSyntaxError: %v", err, err)
	}
	// The refusal is the only place the word survives to, so its size is what
	// says whether the word itself was ever allowed to grow.
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

// What decides a COPY block is the statement's first *token*, and a comment
// is not one.
//
// The pattern that used to answer this looked at the statement's raw text,
// where an alternative matching a line comment can be backtracked into: RE2
// stopped it halfway through the comment, found `copy` inside the comment
// itself, and then reached across the newline into the statement's own body
// for `from stdin`. The price is not a missed COPY but the opposite — an
// organiser's perfectly good CREATE TABLE handed to pgconn.PgConn.CopyFrom,
// which answers with an error that is not a *pgconn.PgError at all, so the
// author is told "internal error, ask an administrator" about SQL that works
// (CLAUDE.md rule 1).
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

// The same reasoning the \restrict case in this file already applies, at the
// one other place the reader throws bytes away: after a COPY header the rest
// of its line was discarded outright, so a file writing
// `COPY t FROM stdin; SELECT setval(...);` lost the second statement without
// a word. "A game that built successfully without part of its data is worse
// than one that refused" — script.go's own sentence, for the identical case.
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

// The whitespace pg_dump itself can leave after the header's semicolon is
// not a statement, and must not be refused as one.
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

// endlessBytes yields the same byte for ever. It stands in for COPY data
// that never reaches a newline without putting a multi-megabyte string
// literal in the test binary — and, wrapped in an io.LimitReader, it is also
// how the test below stays deterministic instead of running until something
// runs out of memory.
type endlessBytes byte

func (b endlessBytes) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

// The bound COPY data is read under (CLAUDE.md rule 12). A dump whose data
// line never ends is untrusted input like any other, and the reader must
// refuse it with a line number rather than grow one buffer until the API
// process — the same one serving the olympiad, since the build runs as a
// background task inside it — is killed for the memory.
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

// The other side of that bound: a long row is ordinary in a real dump — one
// text or bytea column is enough — so the limit must be well clear of what a
// dump legitimately contains, and a line under it must stream through
// untouched.
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

// One Read is one CopyData message on the wire and one write(2) into the
// socket: pgconn.PgConn.CopyFrom offers this exact buffer and hands whatever
// comes back straight to pgproto3, which flushes and writes it unbuffered.
// So a Read that returns one hundred-byte row because that is the first row
// it read is a dump sent to PostgreSQL one syscall at a time — thirty million
// of them for a three-gigabyte dump, in the process that is also serving the
// olympiad.
//
// Asserted as "the buffer comes back nearly full" rather than as a count of
// syscalls, because the buffer is the only thing this side controls; the row
// size below is deliberately a divisor of nothing in particular, so passing
// requires actually packing rows rather than getting lucky.
func TestOneReadOfCopyDataFillsTheCallersBuffer(t *testing.T) {
	t.Parallel()
	// The buffer pgconn.PgConn.CopyFrom actually offers, and a row length no
	// wider than a dump's ordinary ones.
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
	// A whole row may not fit at the end, so the last partial one is split
	// rather than left out: what must never happen is coming back after one
	// row of a block that has thousands left.
	if n <= copyFromBuffer-len(row) {
		t.Fatalf("one Read returned %d of %d bytes — about %d rows, not a full buffer",
			n, copyFromBuffer, n/len(row))
	}

	// And the block still streams through byte for byte.
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

// TestARealPgDumpFileIsReadInFull is the regression this package was
// missing: every earlier test constructs its own script by hand, so all of
// them agreed with what the reader expects a dump to look like. This one
// instead reads an actual `pg_dump --no-owner --no-privileges` text-format
// dump (PostgreSQL 16.15, one table) byte for byte off disk — the
// \restrict / \unrestrict pair included, since that is what the real tool
// writes and organisers cannot turn off. If the reader ever refuses this
// file again, this test is the one that will say so.
func TestARealPgDumpFileIsReadInFull(t *testing.T) {
	t.Parallel()

	f, err := os.Open("testdata/pg_dump_16_languages.sql")
	if err != nil {
		t.Fatalf("open testdata: %v", err)
	}
	defer f.Close()

	r := gamedb.NewScriptReader(f)

	// Every statement the file contains, by the source line it starts on —
	// which is both the count and the identity of each one, so a statement
	// dropped, split or invented shows up as a line rather than as a number
	// nobody can place. Reading down testdata/pg_dump_16_languages.sql:
	// eleven SET, one SELECT pg_catalog.set_config, CREATE TABLE, the COPY
	// header, and ALTER TABLE ... ADD CONSTRAINT — fifteen. The first starts
	// at 7 rather than 10 because pg_dump's "Dumped from database version"
	// comment block belongs to the statement that follows it (Statement.Text
	// keeps a leading comment). The \restrict on line 4 and the \unrestrict
	// on line 62 must add none of their own, which is what this file is
	// here to prove; blank lines between SETs must not add any either.
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

// The lexer above reads a script in exactly one dialect: the one
// standard_conforming_strings=on defines, where a backslash inside '...' is
// an ordinary character and only E'...' processes escapes (see
// isExtendedStringPrefix's own doc). That is an assumption about the *server*,
// not about the file, and a dump can move the server out from under it: a
// pg_dump taken from a database that had the setting off writes
// `SET standard_conforming_strings = off;` at the top, and from that
// statement onward the two disagree about where a literal ends.
//
// The disagreement is not cosmetic. `INSERT INTO notes VALUES ('a\'); SELECT
// 1;` is two statements to this reader and, to a server with the setting off,
// one unterminated literal that swallows the second — so at best the build
// fails with a message about the wrong line, and at worst two statements are
// executed as text neither the author nor this reader intended. Pinning the
// setting on the build connection does not help: the script's own SET runs
// after ours and wins.
//
// So the reader refuses it, by name and with the line, which is the same
// answer it gives a psql meta-command a dump left in — a fact about the file
// that a person can act on (re-export it), not a verdict PostgreSQL could
// have explained.
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

// The form every modern pg_dump writes says on, which is the dialect this
// reader already assumes — nothing to refuse, and refusing it would turn away
// every correctly exported dump there is. RESET is refused, though: it hands
// the setting back to whatever the cluster's own configuration says, which
// this process does not decide and cannot read from here.
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

// The three shapes a game's own bytes reach this reader in, measured against
// each other rather than in the abstract: the point of the third case is that
// it holds the same bytes as the second in a hundredth of the statements, so a
// gap between them says the cost is the per-statement work and a gap between
// both and the first says the cost is the scanning loop itself.
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

// copyDumpBytes is roughly bytes of a pg_dump COPY block — the shape whose
// rows never touch the statement loop at all.
func copyDumpBytes(size int) string {
	var b strings.Builder
	b.WriteString("COPY public.guests (id, full_name, city) FROM stdin;\n")
	for i := 0; b.Len() < size; i++ {
		fmt.Fprintf(&b, "%d\tIonescu Vasile %d\tChisinau\n", i, i)
	}
	b.WriteString("\\.\n")
	return b.String()
}

// insertDumpBytes is roughly bytes of the same data written as INSERTs, with
// rowsPerStatement rows in each — one row each is what pg_dump --inserts and
// every MySQL or SQLite conversion produces.
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
