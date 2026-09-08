package gamedb

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"regexp"
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

// scanBufferSize is the bufio.Reader's own lookahead window. Small multiples
// of it are all this file ever peeks (a dollar-quote tag, at most
// maxDollarTagLookahead bytes), so this is sized for I/O efficiency and
// nothing about correctness depends on it.
const scanBufferSize = 64 * 1024

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
	return fmt.Sprintf("line %d: %s", e.Line, e.Message)
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
// one data line copyDataReader is currently passing through.
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
}

// NewScriptReader wraps r. Nothing is read until the first call to Next.
func NewScriptReader(r io.Reader) *ScriptReader {
	return &ScriptReader{r: bufio.NewReaderSize(r, scanBufferSize), line: 1, atLineStart: true}
}

// Next returns the next statement, or io.EOF once the script is exhausted.
//
// A non-EOF error means the reader itself refused the script — a psql
// meta-command, a statement past maxStatementBytes, or a quote/comment/
// dollar-quote still open when the file ran out — and always as
// *ScriptSyntaxError, so a caller that wants to show it to the script's
// author can (see that type's own doc). The reader must not be used again
// after any error, non-EOF or otherwise.
func (s *ScriptReader) Next() (Statement, error) {
	if s.copyOpen {
		return Statement{}, fmt.Errorf("gamedb: the previous COPY block's data was not read before Next")
	}

	var (
		buf         []byte
		state       = scanTop
		blockDepth  int
		tag         string
		escapeNext  bool // singleExtended only: the previous byte was '\', so this one is data
		startLine   int
		haveContent bool
	)

	for {
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
			return s.finish(buf, startLine), nil
		}

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

		switch state {
		case scanTop:
			switch {
			case atLineStart && b == '\\':
				word, rerr := s.readPsqlCommandWord()
				if rerr != nil {
					return Statement{}, fmt.Errorf("gamedb: read script: %w", rerr)
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
				}
			case b == '/':
				if next, _ := s.r.Peek(1); len(next) == 1 && next[0] == '*' {
					s.consumeInto(&buf, 1)
					blockDepth = 1
					state = scanBlockComment
				}
			case b == ';':
				stmt := s.finish(buf, startLine)
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
					s.skipToLineEnd()
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
	}
}

// finish decides whether the statement just closed is a `COPY ... FROM
// STDIN` and marks the reader accordingly.
func (s *ScriptReader) finish(buf []byte, startLine int) Statement {
	// Only the run of whitespace between the previous statement's ';' and
	// this one's own first byte is dropped — a leading comment stays, since
	// it is what makes copyFromStdinHeader recognise a COPY that pg_dump
	// preceded with one, and dropping it here would just make that pattern
	// unrecognisable again.
	text := string(bytes.TrimLeft(buf, " \t\r\n"))
	if header, ok := copyFromStdinHeader(text); ok {
		s.copyOpen = true
		return Statement{Text: text, Line: startLine, CopyHeader: header}
	}
	return Statement{Text: text, Line: startLine}
}

// CopyData returns the data for the COPY block Next just returned — call it
// only immediately after a Statement whose CopyHeader is non-empty, and read
// it to completion (as pgconn.PgConn.CopyFrom does) before calling Next
// again.
func (s *ScriptReader) CopyData() io.Reader {
	return &copyDataReader{parent: s}
}

// skipToLineEnd discards bytes up to and including the next '\n', or to EOF
// if there is none — the COPY command's own trailing newline, consumed as
// part of recognising the statement rather than left for CopyData to
// mistake for an empty data row (see the case b == ';' branch above).
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
func (s *ScriptReader) readPsqlCommandWord() (string, error) {
	var word []byte
	for {
		next, _ := s.r.Peek(1)
		if len(next) == 0 {
			return string(word), nil
		}
		c := next[0]
		if c == '\n' || c == ' ' || c == '\t' || c == '\r' {
			return string(word), nil
		}
		if _, err := s.r.Discard(1); err != nil {
			return "", err
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

// copyFromStdinPattern recognises a `COPY ... FROM STDIN` command inside a
// statement's full text, allowing the whitespace and comments pg_dump always
// writes just before one (a "-- Data for Name: ..." block).
//
// Anchored at the very start (after only that whitespace/comments) so a
// statement whose *content* happens to mention "FROM STDIN" — inside a
// string literal, say — can never match: the pattern only ever looks at
// what the statement's first real token is, never at what appears later
// inside it while that first token was something else.
var copyFromStdinPattern = regexp.MustCompile(
	`(?is)^(?:\s+|--[^\n]*|/\*.*?\*/)*copy\b.*?\bfrom\s+stdin\b`)

// copyFromStdinHeader reports whether stmt is a `COPY ... FROM STDIN`
// command and, if so, returns it trimmed and without its trailing ';' — the
// sql pgconn.PgConn.CopyFrom wants. The whole original text is returned
// rather than only the matched portion, so a WITH (...) clause after STDIN
// (a format PostgreSQL accepts and a hand-written script might use) reaches
// the server exactly as written instead of being silently dropped, which
// would make the server assume the wrong wire format for the data that
// follows.
func copyFromStdinHeader(stmt string) (string, bool) {
	if !copyFromStdinPattern.MatchString(stmt) {
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
// that nothing else has to hold in memory.
type copyDataReader struct {
	parent  *ScriptReader
	pending []byte
	done    bool
	err     error
}

func (c *copyDataReader) Read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		if c.done {
			return 0, io.EOF
		}
		if c.err != nil {
			return 0, c.err
		}

		line, readErr := c.parent.r.ReadBytes('\n')
		switch {
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
			// is caught on the next call once this drains and ReadBytes finds
			// nothing left to give.
			c.pending = line

		default:
			c.parent.line++
			if string(bytes.TrimRight(line, "\r\n")) == `\.` {
				c.done, c.parent.copyOpen, c.parent.atLineStart = true, false, true
				continue
			}
			c.pending = line
		}
	}

	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}
