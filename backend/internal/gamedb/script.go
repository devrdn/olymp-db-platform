package gamedb

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// maxStatementBytes bounds one statement's text while the reader looks for
// its terminating semicolon.
//
// A three-gigabyte dump must never be held in memory (that is this whole
// file's reason to exist), but a *single statement* inside one still is —
// COPY's own rows stream through copyDataReader below and never touch this
// buffer at all, so what this bounds is everything else: CREATE TABLE,
// CREATE FUNCTION bodies, and a hand-written bulk INSERT nobody rewrote as
// COPY. 16 MiB is generous for all of those — an olympiad's schema and even
// a large PL/pgSQL function are kilobytes, not megabytes — while still
// refusing outright, with a line number, before a script whose "statement"
// never finds its closing quote is read in its entirety.
const maxStatementBytes = 16 << 20

// maxCopyDataLineBytes bounds one line of a COPY block's data — one row, in
// COPY's text format — while copyDataReader looks for the newline that ends
// it.
//
// This is the one place a multi-gigabyte dump's own bulk passes through, so
// it is the one place CLAUDE.md rule 12 is really about: the build runs as a
// background task inside the API process that is serving hundreds of
// participants, and a "line" the file never terminates would otherwise be
// allocated whole before anything looked at it. Not hypothetical in the
// benign direction either — a dump with one wide text or bytea column has
// genuinely long rows — so the bound is generous rather than tight: 16 MiB
// is the same figure maxStatementBytes uses, and for the same reason, a row
// past it is a fact about the file rather than a size anybody meets by
// accident.
const maxCopyDataLineBytes = 16 << 20

// scanBufferSize is the bufio.Reader's own lookahead window. Small multiples
// of it are all this file ever peeks (a dollar-quote tag, at most
// maxDollarTagLookahead bytes), so this is sized for I/O efficiency and
// nothing about correctness depends on it.
const scanBufferSize = 64 * 1024

// maxPsqlCommandWordBytes bounds the command name readPsqlCommandWord will
// accumulate after a backslash at the start of a line.
//
// The third reader in this file that touches untrusted-sized input, and the
// one that had no ceiling at all: the word ends at a space, a tab or a
// newline, and a file whose line begins `\` and reaches none of them put the
// rest of itself — up to the whole of GAME_UPLOAD_MAX_FILE_BYTES — into one
// []byte, inside the API process serving hundreds of participants, and then
// copied it a second time into the refusal's own message. CLAUDE.md rule 12:
// bounded where the bytes arrive. The longest name this reader actually
// compares against is "unrestrict", so 64 is two orders of magnitude of
// headroom over every psql meta-command there is, and a "word" past it is a
// fact about the file rather than a name anybody typed.
const maxPsqlCommandWordBytes = 64

// maxCopyHeaderBytes bounds the parallel code-only buffer copyProbe keeps for
// the one question it answers: is this statement a `COPY ... FROM STDIN`?
//
// The probe stops accumulating the moment the statement's first token turns
// out not to be COPY, so in a dump this holds a COPY command's own header and
// nothing else. This is the ceiling for the case where it *is* one: PostgreSQL
// allows 1600 columns of at most 63 characters each, so a genuine header
// cannot reach 128 KiB, and a mebibyte is headroom over that rather than a
// figure any real script meets. Past it the probe gives up rather than grow
// alongside a 16 MiB statement buffer that already has its own bound.
const maxCopyHeaderBytes = 1 << 20

// maxDollarTagLookahead bounds how far tryDollarTag looks for a closing '$'
// before giving up and treating the '$' it saw as an ordinary character.
// PostgreSQL does not bound a dollar-quote tag's length, but a real one is a
// handful of characters ("$$", "$body$"); this is generous headroom against
// a script that never closes the tag, not a limit anyone should ever meet.
const maxDollarTagLookahead = 128

// ScriptSyntaxError is the reader's own verdict on the script text — a
// refusal PostgreSQL's parser never even saw, because this stopped before
// handing it anything. It satisfies provisioning.ScriptFailure exactly as
// ScriptError does (see BuildTemplate's own doc): a psql meta-command pg_dump
// wrote into the file, or a statement too large to buffer, is a fact about
// the author's own script and belongs on their screen — "syntax error at or
// near \" from PostgreSQL itself would explain nothing.
type ScriptSyntaxError struct {
	// Line is 1-based, and counts the line in the *source file* — what a
	// build failure's position is reported against, and where the console's
	// viewer can jump to.
	Line int
	// Message is the reader's own explanation, without PostgreSQL's SQLSTATE
	// machinery: there is no statement for the server to have refused.
	Message string
}

func (e *ScriptSyntaxError) Error() string {
	return scriptErrorLinePrefix(e.Line) + e.Message
}

// scriptErrorLinePrefix is how both script failures say where in the file
// they are — ScriptSyntaxError above and ScriptError (provisioner.go), which
// used to write the same literal each.
//
// One function because this string is a wire format, not prose: the upload
// console parses it back out to offer "jump to line" over the file it just
// sent (frontend/app/(admin)/contests/[contestId]/game/game-upload.tsx, the
// `/^line (\d+):/i` in its errorLine). A format written in two places drifts
// in one of them, and the console answers by quietly not offering the jump —
// no error anywhere, on either side.
// TestBothScriptFailuresNameTheLineInTheShapeTheConsoleParses holds the two
// together and holds them to that regular expression.
//
// Empty for a line of zero, which is an error PostgreSQL did not locate:
// there is nothing to jump to, and "line 0" would send the console to a line
// that does not exist.
func scriptErrorLinePrefix(line int) string {
	if line <= 0 {
		return ""
	}
	return fmt.Sprintf("line %d: ", line)
}

// ScriptRejection satisfies provisioning.ScriptFailure — see that
// interface's own doc for why this is a method a producer opts into rather
// than a type switch.
func (e *ScriptSyntaxError) ScriptRejection() string { return e.Error() }

// Statement is one unit of work ScriptReader hands back: either plain SQL to
// run with Exec, or the header of a COPY ... FROM STDIN whose data must be
// streamed separately through CopyData before Next is called again.
type Statement struct {
	// Text is the SQL exactly as it appeared in the source, from the first
	// non-whitespace byte after the previous statement's terminator to the
	// ';' that ends this one (or to EOF, for a script with no trailing
	// semicolon). Never rewritten or re-serialised (CLAUDE.md rule 14): what
	// runs is what the author wrote, byte for byte.
	Text string
	// Line is the 1-based source line this statement starts on.
	Line int
	// CopyHeader is non-empty exactly when this statement is a
	// `COPY ... FROM STDIN`: the same text as Text, trimmed, for passing to
	// pgconn.PgConn.CopyFrom's sql argument. When it is set, Text must not
	// also be executed with Exec — the two are the same statement described
	// two ways for two different callers.
	CopyHeader string
}

// scanState is where the byte just read sits relative to the statement's own
// punctuation — the quoting and commenting rules a semicolon has to be
// outside of before it means anything (see the package-level doc this file
// contributes to).
type scanState int

const (
	scanTop scanState = iota
	scanSingle
	scanSingleExtended // E'...' or e'...': backslash escapes apply
	scanDouble
	scanDollar
	scanLineComment
	scanBlockComment
)

// ScriptReader turns an io.Reader of PostgreSQL SQL — an organiser's own
// script, or a pg_dump text-format dump — into one Statement at a time,
// never holding more of the source in memory than the statement currently
// being read (bounded by maxStatementBytes) plus, inside a COPY block, the
// one data line copyDataReader is currently passing through (bounded, in
// turn, by maxCopyDataLineBytes).
//
// It is not a SQL parser: it does not know what a valid statement is, only
// where one ends. That is exactly the amount of PostgreSQL's own lexical
// grammar semicolon-splitting needs — string and identifier quoting, the two
// comment forms, and dollar-quoting — and the reason it has to be this much
// rather than a naive strings.Split(";") is a single embedded semicolon
// inside a string literal or a function body, which pg_dump and any real
// script both contain routinely.
type ScriptReader struct {
	r *bufio.Reader
	// line is the 1-based line the reader's cursor is currently on — the
	// next byte read belongs to this line.
	line int
	// atLineStart is true exactly when the byte about to be read is the
	// first one on its line: the file's first byte, or the byte right after
	// a '\n'. It is what lets a psql meta-command be recognised (backslash
	// at the very start of a line) without also flagging a stray backslash
	// that is merely mid-statement — inside E'...\n', say.
	atLineStart bool
	// copyOpen is true from the moment Next returns a Statement with a
	// CopyHeader until that block's data has been read to completion through
	// CopyData. Next refuses to run again while it is true: the reader has
	// no way to skip a COPY block it was never asked to stream, because
	// skipping it would mean scanning for "\." itself — exactly the job
	// CopyData already does, and duplicating it would be the two disagreeing
	// about where the block ends.
	copyOpen bool
	// buf is the statement buffer, kept across calls to Next rather than
	// allocated fresh for each one.
	//
	// A dump exported with --inserts holds one statement per row, so "fresh
	// buffer per statement" is one allocation cycle per row of the file —
	// measured at 5.4 bytes allocated for every byte read. The buffer is
	// handed to nobody: finish copies what it needs into a string, and the
	// probe keeps its own bytes, so reusing this one aliases nothing.
	// maxRetainedStatementBytes is what keeps one enormous statement from
	// making that reuse a permanent reservation.
	buf []byte
}

// maxRetainedStatementBytes is how much of the statement buffer is kept for
// the next statement. A build holds one reader, so this is a per-build
// reservation and a mebibyte of it is nothing beside the file being read —
// while a script with one 16 MiB statement in it (maxStatementBytes) must not
// leave that much held for the rest of a build that never needs it again.
const maxRetainedStatementBytes = 1 << 20

// NewScriptReader wraps r. Nothing is read until the first call to Next.
func NewScriptReader(r io.Reader) *ScriptReader {
	return &ScriptReader{r: bufio.NewReaderSize(r, scanBufferSize), line: 1, atLineStart: true}
}

// The bytes that mean something to the lexer, per state — everything else in
// that state is buffered as-is and changes nothing about where the statement
// ends. skipDull consumes a run of "everything else" in one step; see its own
// doc for why the loop below cannot simply be a faster loop.
//
// '\n' is in every one of them, which is what keeps the line counter and
// atLineStart correct without a run ever having to be re-scanned for newlines.
// In dullTop it also covers the backslash rule twice over: '\\' is a stop byte
// in its own right, so a psql meta-command at the start of a line is never
// swallowed as ordinary text.
const (
	dullTop            = "'\"$-/;\n\\"
	dullSingle         = "'\n"
	dullSingleExtended = "'\\\n"
	dullDouble         = "\"\n"
	dullDollar         = "$\n"
	dullLineComment    = "\n"
	dullBlockComment   = "*/\n"
)

// skipDull consumes, in one step, the run of bytes already in the reader's own
// window that cannot change the lexer's state — and returns them so the caller
// can append them to the statement buffer with a single append.
//
// This is what the byte-at-a-time loop in Next was costing. Measured on the
// same bytes: a COPY-format dump, which streams through copyDataReader and
// never touches that loop, parsed at 1826 MB/s; the same content as INSERT
// statements at 119 MB/s, and — the measurement that says where the time
// actually went — the same bytes as statements of a thousand rows each at
// 144 MB/s. Twelve times slower for the same work, whatever the number of
// statements, because the cost was the loop itself: one ReadByte call, one
// bounds-checked append and one probe callback for every byte of a
// three-gigabyte file, which is twenty-one seconds of a fully occupied core
// inside the API process that is serving the olympiad.
//
// bytes.IndexAny over the buffered window is the same instrument buildIndex
// already uses for the same reason (its own doc measures IndexByte at 6.51
// GiB/s against 1.26 for a byte loop): it is assembly over the machine's vector
// registers, and the run it finds is appended once instead of a byte at a time.
//
// It reads nothing that Next would not have read anyway, and it never crosses
// a byte the lexer has to look at: the returned run stops at the first byte in
// stop, which the caller then reads and handles exactly as before. A run can
// contain no '\n' (every stop set has one), so neither the line counter nor
// atLineStart can drift — the caller sets atLineStart to false for a non-empty
// run and that is the whole of the bookkeeping.
//
// The returned slice aliases the reader's own buffer and stays valid only
// until the next read from it, which is why the caller appends it immediately.
func (s *ScriptReader) skipDull(stop string) []byte {
	window, _ := s.r.Peek(s.r.Buffered())
	if len(window) == 0 {
		// Nothing buffered: fill, exactly as the ReadByte after this would
		// have. A read error is not this function's to report — the caller's
		// own ReadByte meets it a moment later and has the branch for it.
		if _, err := s.r.Peek(1); err != nil {
			return nil
		}
		window, _ = s.r.Peek(s.r.Buffered())
	}

	end := bytes.IndexAny(window, stop)
	if end < 0 {
		end = len(window)
	}
	if end == 0 {
		return nil
	}
	// Cannot fail: end is inside what Peek just reported as buffered.
	_, _ = s.r.Discard(end)
	return window[:end]
}

// dullStop is the stop set for the state the lexer is in, or "" for a position
// where nothing may be skipped.
//
// Two states allow no skipping at all. In scanTop it is skipped only once the
// statement has some SQL in it (haveSQL): before that, every byte still has to
// be looked at individually to decide whether it is the whitespace in front of
// a statement, the first character of one — which is what sets the line the
// statement is reported against — or the start of a comment. In
// scanSingleExtended a byte the previous '\' escaped is data whatever it is, so
// a skip could run straight past a quote that closes nothing.
func dullStop(state scanState, haveSQL, escapeNext bool) string {
	switch state {
	case scanTop:
		if !haveSQL {
			return ""
		}
		return dullTop
	case scanSingle:
		return dullSingle
	case scanSingleExtended:
		if escapeNext {
			return ""
		}
		return dullSingleExtended
	case scanDouble:
		return dullDouble
	case scanDollar:
		return dullDollar
	case scanLineComment:
		return dullLineComment
	case scanBlockComment:
		return dullBlockComment
	default:
		return ""
	}
}

// Next returns the next statement, or io.EOF once the script is exhausted.
//
// A non-EOF error means the reader itself refused the script — a psql
// meta-command, a statement past maxStatementBytes, or a quote/comment/
// dollar-quote still open when the file ran out — and always as
// *ScriptSyntaxError, so a caller that wants to show it to the script's
// author can (see that type's own doc). The reader must not be used again
// after any error, non-EOF or otherwise.
//
// The one meta-command pair Next does not refuse is \restrict and
// \unrestrict — pg_dump's own unconditional wrapper on recent versions (see
// the atLineStart case in the loop below for why letting just this pair
// through is not a weaker check than refusing everything).
func (s *ScriptReader) Next() (Statement, error) {
	if s.copyOpen {
		return Statement{}, fmt.Errorf("gamedb: the previous COPY block's data was not read before Next")
	}

	// Reused across calls rather than allocated per statement — see
	// ScriptReader.buf. The deferred store is what hands it back however this
	// returns, and dropping an oversized one is what keeps the reuse from
	// becoming a reservation.
	buf := s.buf[:0]
	defer func() {
		if cap(buf) > maxRetainedStatementBytes {
			buf = nil
		}
		s.buf = buf
	}()

	var (
		probe copyProbe
		state = scanTop
		// blockDepth, tag and escapeNext are the lexer's own working state
		// for the three constructs a semicolon can hide inside.
		blockDepth  int
		tag         string
		escapeNext  bool // singleExtended only: the previous byte was '\', so this one is data
		startLine   int
		haveContent bool
		// haveSQL is haveContent minus the comments: true once a byte of the
		// statement *itself* has been buffered, false while the buffer holds
		// only whitespace and the comment blocks pg_dump writes between
		// statements. Only the \restrict case below needs the distinction,
		// and it needs it precisely: that case drops the buffer, which is
		// harmless for a comment and is losing a statement for anything else.
		haveSQL bool
	)

	for {
		// Everything between here and the next byte the lexer actually has to
		// look at, taken in one step (skipDull). The byte it stops on is read
		// and handled by exactly the code below, unchanged.
		if stop := dullStop(state, haveSQL, escapeNext); stop != "" {
			if run := s.skipDull(stop); len(run) > 0 {
				buf = append(buf, run...)
				if len(buf) > maxStatementBytes {
					return Statement{}, &ScriptSyntaxError{
						Line:    startLine,
						Message: fmt.Sprintf("a statement exceeds %d bytes", maxStatementBytes),
					}
				}
				if state == scanTop {
					// prev == next == scanTop for every byte of the run, which
					// is copyProbe.observe's own "this byte is code" case.
					probe.pushRun(run)
				}
				// A run holds no '\n' (every stop set has one), so the line
				// number is untouched and the next byte is certainly not the
				// first on its line.
				s.atLineStart = false
			}
		}

		b, err := s.r.ReadByte()
		if err != nil {
			if err != io.EOF {
				return Statement{}, fmt.Errorf("gamedb: read script: %w", err)
			}
			if state != scanTop && state != scanLineComment {
				return Statement{}, &ScriptSyntaxError{Line: startLine, Message: unterminatedMessage(state)}
			}
			if !haveContent || len(bytes.TrimSpace(buf)) == 0 {
				return Statement{}, io.EOF
			}
			return s.finish(buf, &probe, startLine)
		}

		prevState := state
		atLineStart := s.atLineStart
		if b == '\n' {
			s.line++
			s.atLineStart = true
		} else {
			s.atLineStart = false
		}

		if !haveContent && b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			startLine = s.line
			haveContent = true
		}

		buf = append(buf, b)
		if len(buf) > maxStatementBytes {
			return Statement{}, &ScriptSyntaxError{
				Line: startLine, Message: fmt.Sprintf("a statement exceeds %d bytes", maxStatementBytes),
			}
		}

		// sqlBefore is haveSQL as it stood before this byte — what the two
		// comment-opening cases below restore it to once the byte turns out
		// to have opened a comment rather than a statement, and what the
		// \restrict case asks about the buffer it is on the point of
		// dropping. Every byte a comment's own body contributes is handled
		// under scanLineComment/scanBlockComment, so it never reaches here.
		sqlBefore := haveSQL
		if state == scanTop && b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			haveSQL = true
		}

		switch state {
		case scanTop:
			switch {
			case atLineStart && b == '\\':
				word, whole, rerr := s.readPsqlCommandWord()
				if rerr != nil {
					return Statement{}, fmt.Errorf("gamedb: read script: %w", rerr)
				}
				if !whole {
					// Not a command name anybody wrote, so it is not named
					// back: what follows the backslash is unread bytes of a
					// file this reader will not go on buffering.
					return Statement{}, &ScriptSyntaxError{
						Line: s.line,
						Message: fmt.Sprintf(
							"a line begins with a backslash and no psql command name follows it "+
								"within %d bytes — psql commands are not SQL, so export the dump without them",
							maxPsqlCommandWordBytes),
					}
				}
				if word == "restrict" || word == "unrestrict" {
					// pg_dump 16.10/17.6/18 and newer wrap every plain-text
					// dump in \restrict <token> right after the header and
					// \unrestrict <token> at the very end, unconditionally
					// and with no flag to turn it off — it is pg_dump's own
					// fix for CVE-2025-1094-adjacent risk, guarding against
					// *psql* running meta-commands it finds inside a dump
					// it did not write. That risk does not exist here: this
					// reader is not psql, has no meta-command interpreter,
					// and executes nothing it did not itself parse as SQL.
					// So this is not a hole opened in the refusal below —
					// every *other* backslash command a dump could contain
					// (\connect, \i, \copy, anything) is still refused by
					// name, unread, exactly as before. This pair alone is a
					// no-op wrapper pg_dump now always writes and that
					// carries no SQL of its own, so it is dropped with its
					// line rather than rejected as if it were content.
					//
					// Dropped only where pg_dump puts it, though: between two
					// statements. Dropping the buffer is what makes this a
					// skip, and the buffer holds everything read since the
					// last ';' — so in a file assembled by hand or joined
					// from two dumps, where the directive can land in the
					// middle of a statement, the same line would silently
					// take that statement with it and leave a lone ';' to
					// execute. A game that "built successfully" without part
					// of its data is worse than one that refused, so anything
					// but whitespace and comments in front of it (haveSQL,
					// declared above) is a refusal naming the line.
					if sqlBefore {
						return Statement{}, &ScriptSyntaxError{
							Line: s.line,
							Message: fmt.Sprintf(
								`\%s appears in the middle of a statement, where it cannot be skipped — `+
									`end the statement before it, or remove the line`, word),
						}
					}
					s.skipToLineEnd()
					buf, probe = buf[:0], copyProbe{}
					haveContent, haveSQL = false, false
					continue
				}
				return Statement{}, &ScriptSyntaxError{
					Line: s.line,
					Message: fmt.Sprintf(
						`\%s is a psql command, not SQL — export the dump without it`, word),
				}
			case b == '\'':
				if isExtendedStringPrefix(buf) {
					state, escapeNext = scanSingleExtended, false
				} else {
					state = scanSingle
				}
			case b == '"':
				state = scanDouble
			case b == '$':
				if t, ok := s.tryDollarTag(&buf); ok {
					tag, state = t, scanDollar
				}
			case b == '-':
				if next, _ := s.r.Peek(1); len(next) == 1 && next[0] == '-' {
					s.consumeInto(&buf, 1)
					state = scanLineComment
					haveSQL = sqlBefore // this '-' opened a comment, not a statement
				}
			case b == '/':
				if next, _ := s.r.Peek(1); len(next) == 1 && next[0] == '*' {
					s.consumeInto(&buf, 1)
					blockDepth = 1
					state = scanBlockComment
					haveSQL = sqlBefore
				}
			case b == ';':
				stmt, err := s.finish(buf, &probe, startLine)
				if err != nil {
					return Statement{}, err
				}
				if stmt.CopyHeader != "" {
					// pg_dump's own layout: the data rows start on the line
					// right after the COPY command's, never on the same one.
					// Next's own scanning stops the instant it sees this ';',
					// so the '\n' that ends *this* line is still unread — and
					// if it stayed unread, copyDataReader's first ReadBytes
					// would return it as an empty data row that was never in
					// the dump. Consumed here, once, as part of recognising a
					// COPY statement rather than as part of any statement's
					// own text.
					if err := s.endCopyHeaderLine(); err != nil {
						return Statement{}, err
					}
				}
				return stmt, nil
			}

		case scanSingle:
			if b == '\'' {
				if next, _ := s.r.Peek(1); len(next) == 1 && next[0] == '\'' {
					s.consumeInto(&buf, 1) // doubled quote: a literal '\'' in the value
				} else {
					state = scanTop
				}
			}

		case scanSingleExtended:
			switch {
			case escapeNext:
				escapeNext = false // this byte was escaped by the previous '\'; already appended above
			case b == '\\':
				escapeNext = true
			case b == '\'':
				if next, _ := s.r.Peek(1); len(next) == 1 && next[0] == '\'' {
					s.consumeInto(&buf, 1) // '' also escapes a quote inside E'...'
				} else {
					state = scanTop
				}
			}

		case scanDouble:
			if b == '"' {
				if next, _ := s.r.Peek(1); len(next) == 1 && next[0] == '"' {
					s.consumeInto(&buf, 1)
				} else {
					state = scanTop
				}
			}

		case scanDollar:
			if b == '$' && s.matchDollarTag(&buf, tag) {
				state = scanTop
			}

		case scanLineComment:
			if b == '\n' {
				state = scanTop
			}

		case scanBlockComment:
			switch {
			case b == '/':
				if next, _ := s.r.Peek(1); len(next) == 1 && next[0] == '*' {
					s.consumeInto(&buf, 1)
					blockDepth++
				}
			case b == '*':
				if next, _ := s.r.Peek(1); len(next) == 1 && next[0] == '/' {
					s.consumeInto(&buf, 1)
					blockDepth--
					if blockDepth == 0 {
						state = scanTop
					}
				}
			}
		}

		// Last, with the byte's own role finally settled: the lexer is the
		// only thing that knows whether it was code, the body of a comment or
		// the inside of a literal, and copyFromStdinHeader needs exactly that
		// distinction (see copyProbe).
		probe.observe(prevState, state, b)
	}
}

// finish decides whether the statement just closed is a `COPY ... FROM
// STDIN` and marks the reader accordingly — or refuses it outright, for the
// one statement that would change the dialect the lexer above reads in
// (dialectRefusal).
func (s *ScriptReader) finish(buf []byte, probe *copyProbe, startLine int) (Statement, error) {
	// Only the run of whitespace between the previous statement's ';' and
	// this one's own first byte is dropped. A leading comment stays: what
	// runs is what the author wrote (Statement.Text's own doc), and
	// PostgreSQL reads a comment in front of a statement exactly as if it
	// were not there — including in the sql pgconn.PgConn.CopyFrom is given.
	text := string(bytes.TrimLeft(buf, " \t\r\n"))
	if refusal := dialectRefusal(probe, text, startLine); refusal != nil {
		return Statement{}, refusal
	}
	if header, ok := copyFromStdinHeader(text, probe); ok {
		s.copyOpen = true
		return Statement{Text: text, Line: startLine, CopyHeader: header}, nil
	}
	return Statement{Text: text, Line: startLine}, nil
}

// copyProbe is the statement's own SQL with every comment body and every
// literal body taken out — the only text copyFromStdinHeader may ask its
// question of.
//
// The question is "is this statement a `COPY ... FROM STDIN`?", and the
// version of it that ran against the statement's *raw* text got it wrong in
// the one direction that hurts. A pattern skipping leading comments with an
// alternative like `--[^\n]*` can be backtracked into: RE2 is free to stop
// halfway through a comment, find `copy` inside the comment itself, and then
// reach across the newline into the following statement for `from stdin`.
// The result is not a COPY block missed but an ordinary `CREATE TABLE` handed
// to pgconn.PgConn.CopyFrom because the organiser wrote an English sentence
// above it — and the error that comes back is not a *pgconn.PgError, so
// scriptFailure cannot make it a ScriptError and the author is told
// "internal error, ask an administrator" about SQL that works (CLAUDE.md
// rule 1). No pattern fixes that, because the pattern is not what knows where
// a comment ends. The lexer above is; this is how its knowledge reaches the
// decision.
//
// Every comment and every quoted body contributes exactly one space, so
// tokens on either side of one stay separate without any of their bytes
// taking part in the match. What is left is code, and only code.
type copyProbe struct {
	// code is the statement's code bytes, with leading whitespace dropped so
	// that code[0] is the statement's first real character.
	code []byte
	// ruledOut is set once the answer can no longer change: the first token is
	// not COPY, or the header has outgrown maxCopyHeaderBytes. Nothing is
	// accumulated afterwards, which is what keeps this from being a second
	// full-size copy of every statement in a multi-gigabyte dump.
	ruledOut bool
}

// probeWords are the first tokens that make a statement worth keeping the
// code-only copy of: `copy`, for the COPY ... FROM STDIN question this probe
// was built for, and `set`/`reset`, for the one setting this reader's own
// parse depends on (see dialectRefusal). Everything else is ruled out at the
// first byte that cannot begin one of them, which is what keeps this from
// being a second full-size copy of every statement in a multi-gigabyte dump.
var probeWords = [][]byte{[]byte("copy"), []byte("set"), []byte("reset")}

// stillInteresting reports whether code — the statement's first bytes, with
// comments and literals already removed — can still be the start of one of
// probeWords followed by a token boundary. "copyright" is not "copy", which
// is why the byte past the word is checked and not only the word itself.
func stillInteresting(code []byte) bool {
	for _, word := range probeWords {
		switch {
		case len(code) < len(word):
			if bytes.EqualFold(code, word[:len(code)]) {
				return true
			}
		case bytes.EqualFold(code[:len(word)], word):
			if len(code) == len(word) || !isIdentByte(code[len(word)]) {
				return true
			}
		}
	}
	return false
}

// observe is called once per byte the lexer read, with the state it was in
// before that byte and the state it is in after — which together say whether
// the byte was code, opened or closed a comment or literal, or was part of
// one's body.
func (p *copyProbe) observe(prev, next scanState, b byte) {
	switch {
	case p.ruledOut:
	case prev == scanTop && next == scanTop:
		p.push(b)
	case prev != next:
		// A comment or a quoted body just opened or closed. It separates the
		// tokens around it, so it is worth exactly one space and none of its
		// own bytes.
		p.push(' ')
	}
}

// pushRun is observe's code-byte case for a whole run at once — what
// Next.skipDull hands it when the lexer skipped past a stretch of ordinary
// SQL. It stops the moment the probe rules itself out, which for every
// statement but a COPY header is within the first token.
func (p *copyProbe) pushRun(run []byte) {
	for _, b := range run {
		if p.ruledOut {
			return
		}
		p.push(b)
	}
}

func (p *copyProbe) push(b byte) {
	if len(p.code) == 0 && (b == ' ' || b == '\t' || b == '\r' || b == '\n') {
		// Dropped rather than buffered, so that code[0] is the statement's
		// first real character and the first-token check below is a look at
		// four fixed bytes rather than a scan past however many blank lines
		// and comments pg_dump wrote in front of this statement.
		return
	}
	p.code = append(p.code, b)
	if len(p.code) > maxCopyHeaderBytes {
		p.ruledOut, p.code = true, nil
		return
	}
	// The first token decides everything, so as soon as it is known not to
	// begin one of probeWords there is nothing left to accumulate.
	if !stillInteresting(p.code) {
		p.ruledOut, p.code = true, nil
	}
}

// CopyData returns the data for the COPY block Next just returned — call it
// only immediately after a Statement whose CopyHeader is non-empty, and read
// it to completion (as pgconn.PgConn.CopyFrom does) before calling Next
// again.
func (s *ScriptReader) CopyData() io.Reader {
	return &copyDataReader{parent: s}
}

// endCopyHeaderLine consumes what is left of the line a COPY header's ';'
// ended on — the newline itself, and the trailing spaces pg_dump or an editor
// may have left in front of it — so that copyDataReader's first read starts
// on the first data row instead of returning that newline as an empty row
// that was never in the dump.
//
// Anything else on that line is refused rather than discarded, which is the
// whole difference from the plain skip this replaced. COPY's data begins on
// the next line by the format's own definition, so a second statement written
// after the header — `COPY t FROM stdin; SELECT setval('t_id_seq', 100);` —
// cannot be run: there is nowhere to put it. It used to be thrown away in
// silence, and the reasoning against that is already written a few dozen
// lines above, for \restrict: a game that "built successfully" without part
// of its data is worse than one that refused.
func (s *ScriptReader) endCopyHeaderLine() error {
	for {
		b, err := s.r.ReadByte()
		if err != nil {
			return nil // EOF right after the header: an empty, unterminated block
		}
		if b == '\n' {
			s.line++
			s.atLineStart = true
			return nil
		}
		if b != ' ' && b != '\t' && b != '\r' {
			return &ScriptSyntaxError{
				Line: s.line,
				Message: "a COPY ... FROM stdin is followed by more text on the same line, where " +
					"the data block begins — put whatever follows it on its own line after the " +
					`block's "\." terminator`,
			}
		}
	}
}

// skipToLineEnd discards bytes up to and including the next '\n', or to EOF
// if there is none — the rest of a \restrict line, dropped with the directive
// itself (see the atLineStart case above).
func (s *ScriptReader) skipToLineEnd() {
	for {
		b, err := s.r.ReadByte()
		if err != nil {
			return
		}
		if b == '\n' {
			s.line++
			s.atLineStart = true
			return
		}
	}
}

// consumeInto reads exactly n more bytes (already known, via Peek, to be
// available) and appends them to *buf, advancing the reader and the line
// counter. Used for every multi-byte token this file recognises (--, /*,
// */, and a doubled quote inside a string or identifier literal) once the
// second byte has been confirmed by a Peek — so the byte is consumed
// exactly once, here, rather than by a second ReadByte call duplicated at
// each call site.
func (s *ScriptReader) consumeInto(buf *[]byte, n int) {
	for range n {
		b, err := s.r.ReadByte()
		if err != nil {
			// Cannot happen: the caller only calls this after Peek(n)
			// reported n bytes available, and nothing else reads from s.r
			// between that Peek and this call.
			return
		}
		if b == '\n' {
			s.line++
			s.atLineStart = true
		} else {
			s.atLineStart = false
		}
		*buf = append(*buf, b)
	}
}

// isExtendedStringPrefix reports whether the quote just appended to buf
// (its last byte) opens an E'...' string rather than a plain '...' one: the
// standard_conforming_strings=on rule that only E'...' processes backslash
// escapes (see this package's own doc on the two forms).
//
// True exactly when the byte before the quote is 'E' or 'e' and that letter
// is not itself the tail of a longer identifier — "TYPE'" is not a valid
// E-string prefix (nor valid SQL at all: PostgreSQL requires whitespace
// between an identifier and a following literal, except for this prefix and
// its siblings X'/B'/U&'), so treating a mid-identifier 'e' as one would only
// misparse text that was never going to run either way.
func isExtendedStringPrefix(buf []byte) bool {
	n := len(buf)
	if n < 2 {
		return false
	}
	prev := buf[n-2]
	if prev != 'E' && prev != 'e' {
		return false
	}
	if n < 3 {
		return true
	}
	return !isIdentByte(buf[n-3])
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b >= 0x80
}

// tryDollarTag is called with buf's last byte the '$' that opens a possible
// dollar-quote. It looks ahead, without consuming anything until the whole
// tag is confirmed, for PostgreSQL's own tag grammar: an identifier (first
// character not a digit) or nothing at all, followed by a second '$'.
//
// On success it consumes and appends the tag and the closing '$' to *buf and
// returns the tag (without either dollar sign) and true. On failure nothing
// more is consumed and it returns false — critically, this is what keeps a
// PL/pgSQL parameter placeholder like $1 from being mistaken for the start
// of a dollar-quote: '1' is a digit, so the very first character check fails
// and the lone '$' already in buf is left as an ordinary character.
func (s *ScriptReader) tryDollarTag(buf *[]byte) (string, bool) {
	peeked, _ := s.r.Peek(maxDollarTagLookahead)

	i := 0
	for i < len(peeked) {
		c := peeked[i]
		if c == '$' {
			break
		}
		if !isIdentByte(c) || (i == 0 && c >= '0' && c <= '9') {
			return "", false
		}
		i++
	}
	if i >= len(peeked) || peeked[i] != '$' {
		return "", false
	}

	consumed := peeked[:i+1]
	if _, err := s.r.Discard(len(consumed)); err != nil {
		return "", false
	}
	*buf = append(*buf, consumed...)
	// A tag cannot contain '\n' (rejected above as not isIdentByte), so the
	// closing '$' just consumed is on the same line the opening one was.
	return string(consumed[:i]), true
}

// matchDollarTag is called with buf's last byte the '$' that might close the
// dollar-quote opened with tag. It peeks the bytes that would have to follow
// for this to be the closer (tag, then another '$') and, only if they match,
// consumes and appends them and returns true — otherwise the '$' already in
// buf stands as an ordinary character inside the quoted body, and scanning
// continues from right after it.
func (s *ScriptReader) matchDollarTag(buf *[]byte, tag string) bool {
	need := len(tag) + 1
	peeked, _ := s.r.Peek(need)
	if len(peeked) < need {
		return false
	}
	if len(tag) > 0 && string(peeked[:len(tag)]) != tag {
		return false
	}
	if peeked[len(tag)] != '$' {
		return false
	}
	if _, err := s.r.Discard(need); err != nil {
		return false
	}
	*buf = append(*buf, peeked...)
	return true
}

// readPsqlCommandWord reads the command name following a backslash already
// consumed by the caller — up to the first whitespace or end of line — so
// the refusal can name it: "\connect" and "\connect mydb" read the same to
// whoever has to remove it from the dump.
//
// It stops at maxPsqlCommandWordBytes and says so, rather than reading on:
// see that constant for why a reader with no ceiling here is a way to spend
// the API process's memory twice over on a file nobody validated.
func (s *ScriptReader) readPsqlCommandWord() (string, bool, error) {
	var word []byte
	for {
		next, _ := s.r.Peek(1)
		if len(next) == 0 {
			return string(word), true, nil
		}
		c := next[0]
		if c == '\n' || c == ' ' || c == '\t' || c == '\r' {
			return string(word), true, nil
		}
		if len(word) == maxPsqlCommandWordBytes {
			return string(word), false, nil
		}
		if _, err := s.r.Discard(1); err != nil {
			return "", false, err
		}
		word = append(word, c)
	}
}

// unterminatedMessage names what never closed, for the EOF branch of Next.
func unterminatedMessage(state scanState) string {
	switch state {
	case scanSingle, scanSingleExtended:
		return "an unterminated string literal reaches the end of the file"
	case scanDouble:
		return "an unterminated quoted identifier reaches the end of the file"
	case scanDollar:
		return "an unterminated dollar-quoted string reaches the end of the file"
	case scanBlockComment:
		return "an unterminated block comment reaches the end of the file"
	default:
		return "the script ends unexpectedly"
	}
}

// copyFromStdinPattern recognises a `COPY ... FROM STDIN` command.
//
// Matched against copyProbe.code and never against a statement's raw text:
// the probe has already removed every comment and every literal body, so this
// pattern sees only SQL. It therefore needs no alternative for skipping
// comments — that alternative is precisely what could be backtracked into,
// and what made a comment above a `CREATE TABLE` turn it into a COPY block
// (copyProbe's own doc). The anchor is the whole guard on the left: what the
// statement's first token is, decided by the lexer rather than by the
// regexp engine.
var copyFromStdinPattern = regexp.MustCompile(`(?is)^copy\b.*\bfrom\s+stdin\b`)

// dialectPattern recognises a statement that changes standard_conforming_strings.
//
// Matched against copyProbe.code for the same reason copyFromStdinPattern is:
// the probe's bytes are code and only code, so a table called
// standard_conforming_strings or a comment mentioning it can never be mistaken
// for the setting being changed, and the first token is the lexer's answer
// rather than the regexp engine's.
var dialectPattern = regexp.MustCompile(
	`(?is)^(?:set|reset)\s+(?:session\s+|local\s+)?standard_conforming_strings\b`)

// dialectValuePattern reads the value out of such a statement.
//
// This one runs against the statement's raw text, because the probe blanks
// every literal body — `TO 'on'` and `TO 'off'` are the same two spaces to it,
// and the difference is the whole question. Matching raw text is safe here
// and nowhere else: it runs only after dialectPattern has already decided,
// from code alone, that this statement is a SET or RESET of this one setting,
// so there is no comment or literal left for it to be misled by.
var dialectValuePattern = regexp.MustCompile(`(?is)standard_conforming_strings\s*(?:=|\bto\b)\s*([^\s;]+)`)

// dialectOnValues are the values that leave the session in the dialect this
// reader parses in. PostgreSQL accepts several spellings of a boolean GUC;
// these are the ones that mean on.
var dialectOnValues = map[string]bool{"on": true, "'on'": true, `"on"`: true, "true": true, "'true'": true, "1": true, "'1'": true}

// dialectRefusal is the reader's verdict on a statement that would move the
// server out of the dialect the lexer above reads in, or nil for every other
// statement.
//
// The lexer parses literals under standard_conforming_strings=on: a backslash
// inside '...' is an ordinary character, and only E'...' processes escapes
// (isExtendedStringPrefix's own doc). That is an assumption about the server,
// and a dump exported from a database that had the setting off carries
// `SET standard_conforming_strings = off;` at its top — after which the two
// disagree about where a literal *ends*. `INSERT INTO t VALUES ('a\'); SELECT
// 1;` is two statements here and, to that server, one unterminated literal
// that swallows the next: the good case is a build failure pointing at the
// wrong line, and the bad one is text running as SQL that neither the author
// nor this reader meant.
//
// Pinning the setting on the connection (runScript does, so a cluster whose
// own configuration says otherwise cannot surprise us either) does not cover
// this: the script's own SET runs afterwards and wins. The only sound answer
// is to refuse, which is also the honest one — a dump written for a dialect
// this service does not execute is a fact about the file, exactly like the
// psql meta-command case above, and re-exporting it is something the person
// holding it can do.
//
// Setting it *on* is left alone: that is what every modern pg_dump writes and
// it is the dialect already assumed. RESET is refused with the rest, because
// it hands the setting back to a cluster-level default this process does not
// decide.
func dialectRefusal(probe *copyProbe, text string, line int) *ScriptSyntaxError {
	if probe.ruledOut || !dialectPattern.Match(probe.code) {
		return nil
	}
	// No value at all is a RESET, or a `SET ... TO DEFAULT`: both hand the
	// setting back to a cluster-level default this process does not decide.
	if value := dialectValuePattern.FindStringSubmatch(text); value != nil && dialectOnValues[strings.ToLower(value[1])] {
		return nil
	}
	return &ScriptSyntaxError{
		Line: line,
		Message: "this script sets standard_conforming_strings to something other than on, and " +
			"the reader that splits it into statements can only read the on dialect — export the " +
			"dump from a server with standard_conforming_strings on, or remove the statement",
	}
}

// copyFromStdinHeader reports whether the statement the probe watched is
// a `COPY ... FROM STDIN` command and, if so, returns stmt trimmed and
// without its trailing ';' — the sql pgconn.PgConn.CopyFrom wants. The whole
// original text is returned rather than only the matched portion, so a
// WITH (...) clause after STDIN (a format PostgreSQL accepts and a
// hand-written script might use) reaches the server exactly as written
// instead of being silently dropped, which would make the server assume the
// wrong wire format for the data that follows.
func copyFromStdinHeader(stmt string, probe *copyProbe) (string, bool) {
	if probe.ruledOut || !copyFromStdinPattern.Match(probe.code) {
		return "", false
	}
	trimmed := bytes.TrimSpace([]byte(stmt))
	trimmed = bytes.TrimRight(trimmed, ";")
	return string(bytes.TrimSpace(trimmed)), true
}

// copyDataReader streams one COPY ... FROM STDIN block's data to its caller
// (pgconn.PgConn.CopyFrom, in normal use) exactly as pg_dump wrote it — \N
// for NULL, every backslash-escape, untouched — because that is the COPY
// wire format's own rule, not SQL, and rewriting a byte here would silently
// corrupt a participant's data. It stops the instant it reads the line
// consisting of exactly "\.", the terminator pg_dump writes at the end of
// every block, and returns io.EOF there without ever handing that line to
// the caller — matching real protocol-level COPY, which has no such line at
// all.
//
// One line of lookahead at a time, never the whole block: a table's COPY
// data is exactly the part of a multi-gigabyte dump this reader exists so
// that nothing else has to hold in memory. "One line" is itself bounded by
// maxCopyDataLineBytes — a file with no newline where one belongs is
// untrusted input, not a promise (see readDataLine).
type copyDataReader struct {
	parent  *ScriptReader
	pending []byte
	done    bool
	err     error
}

// Read fills p with as many whole data lines as fit, and only then returns.
//
// The line-at-a-time version this replaced returned the first row it read,
// however small — and the caller is pgconn.PgConn.CopyFrom, which offers a
// 65531-byte buffer and turns *every* Read into one CopyData protocol message
// written straight to the socket (pgproto3's SendUnbufferedEncodedCopyData
// flushes and writes with no buffering of its own). One write(2) and one
// protocol message per row of the dump: a three-gigabyte dump of hundred-byte
// rows is thirty million of each, about a minute and a half of a fully
// occupied core spent in syscalls alone, inside the API process that is at
// the same time serving the olympiad — plus 150 MB of message headers the
// server has to parse. Filling the buffer instead makes that one message per
// ~650 rows.
//
// What is *not* changed is where the bytes arrive: lines are still read one
// at a time under maxCopyDataLineBytes (readDataLine), and this holds no more
// than the caller's own buffer plus at most one line beyond it. Filling p is
// packing what has already been bounded, not relaxing the bound (CLAUDE.md
// rule 12).
func (c *copyDataReader) Read(p []byte) (int, error) {
	var n int
	for n < len(p) {
		if len(c.pending) > 0 {
			copied := copy(p[n:], c.pending)
			// A line longer than the room left in p is split here: what fits
			// goes now, the rest stays in pending and is the first thing the
			// next call hands over. pending may alias the bufio window
			// (readDataLine's own doc), and nothing reads from the underlying
			// reader while it is non-empty — the loop below only advances once
			// pending has drained — so the slice stays valid for exactly as
			// long as it is held.
			c.pending = c.pending[copied:]
			n += copied
			continue
		}
		if c.done || c.err != nil {
			// Reported on the next call rather than alongside these bytes:
			// the terminator and any refusal belong to what comes after what
			// is already in p.
			break
		}
		c.advance()
	}

	if n > 0 {
		return n, nil
	}
	if c.done {
		return 0, io.EOF
	}
	return 0, c.err
}

// advance reads one more data line and settles it into exactly one of
// pending, done or err — so Read's loop always makes progress.
func (c *copyDataReader) advance() {
	line, readErr := c.readDataLine()
	var tooLong *ScriptSyntaxError
	switch {
	case errors.As(readErr, &tooLong):
		// Already the reader's own verdict on the script, with its own
		// line number (ScriptSyntaxError's doc): handed on as it is
		// rather than wrapped in "read COPY data", which would read as
		// an I/O failure of ours instead of a fact about their file.
		c.err = readErr

	case readErr != nil && readErr != io.EOF:
		c.err = fmt.Errorf("gamedb: read COPY data: %w", readErr)

	case readErr == io.EOF && len(line) == 0:
		c.err = &ScriptSyntaxError{
			Line: c.parent.line, Message: `COPY data ends with no terminating "\." line`,
		}

	case readErr == io.EOF:
		// A final line with no trailing newline — a truncated file, since
		// pg_dump always terminates every line including the last one.
		// Handed out as data rather than dropped; the missing terminator
		// is caught on the next call once this drains and ReadSlice finds
		// nothing left to give.
		c.pending = line

	default:
		c.parent.line++
		if string(bytes.TrimRight(line, "\r\n")) == `\.` {
			c.done, c.parent.copyOpen, c.parent.atLineStart = true, false, true
			return
		}
		c.pending = line
	}
}

// readDataLine reads one line of COPY data — up to and including its '\n',
// or to EOF for a truncated final line — and never allocates more than
// maxCopyDataLineBytes doing it.
//
// bufio.Reader.ReadBytes, which this replaced, grows a single buffer until it
// finds the delimiter, with no ceiling at all: a header followed by three
// gigabytes containing no '\n' would be allocated whole, inside the API
// process, on the very first Read pgconn.PgConn.CopyFrom makes. ReadSlice
// hands back at most the reader's own window and says so with
// bufio.ErrBufferFull, which is what turns "grow until it fits" into "grow
// while there is budget, then refuse" (CLAUDE.md rule 12: the bound belongs
// where the bytes arrive, not on what is done with them afterwards).
//
// A line that fits in the window is returned as a slice of that window, not
// a copy — the common case for a dump's rows, and the reason streaming a
// gigabyte of them costs no allocation per row. The slice stays valid until
// the next read from c.parent.r, which is exactly as long as it is held:
// Read only refills c.pending once it has drained, and nothing else reads
// from the underlying reader while a COPY block is open (Next refuses to run
// at all until it closes).
func (c *copyDataReader) readDataLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := c.parent.r.ReadSlice('\n')
		if len(line)+len(chunk) > maxCopyDataLineBytes {
			return nil, &ScriptSyntaxError{
				Line: c.parent.line,
				Message: fmt.Sprintf(
					"a line of COPY data exceeds %d bytes", maxCopyDataLineBytes),
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			line = append(line, chunk...)
			continue
		}
		if len(line) == 0 {
			return chunk, err // the whole line fit in the window: no copy
		}
		return append(line, chunk...), err
	}
}
