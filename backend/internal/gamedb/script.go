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
// its terminating semicolon. COPY rows never enter this buffer; 16 MiB is far
// above any real schema or function body, and refuses a never-closed quote
// before the whole file is read.
const maxStatementBytes = 16 << 20

// maxCopyDataLineBytes bounds one row of a COPY block while copyDataReader
// looks for its newline (CLAUDE.md rule 12). Generous, since a wide text or
// bytea column makes long rows; past it the file is refused.
const maxCopyDataLineBytes = 16 << 20

// scanBufferSize is the bufio.Reader's window, sized for I/O efficiency only.
const scanBufferSize = 64 * 1024

// maxPsqlCommandWordBytes bounds the command name readPsqlCommandWord reads
// after a backslash at the start of a line (CLAUDE.md rule 12). Without it, a
// line starting with `\` and no whitespace would buffer the rest of the file.
// The longest name compared against is "unrestrict".
const maxPsqlCommandWordBytes = 64

// maxCopyHeaderBytes bounds copyProbe's code-only buffer. The probe stops at
// the first token unless it is one of probeWords, so this only holds a COPY,
// SET or RESET statement. A real COPY header (1600 columns of 63 characters)
// stays under 128 KiB.
const maxCopyHeaderBytes = 1 << 20

// maxDollarTagLookahead bounds how far tryDollarTag looks for a closing '$'
// before treating the '$' as an ordinary character. Real tags are a few
// characters long.
const maxDollarTagLookahead = 128

// ScriptSyntaxError is the reader's own refusal of the script text, raised
// before PostgreSQL saw anything (a psql meta-command, an oversized
// statement). Like ScriptError it satisfies provisioning.ScriptFailure, so it
// is shown to the script's author.
type ScriptSyntaxError struct {
	// Line is the 1-based line in the source file.
	Line    int
	Message string
}

func (e *ScriptSyntaxError) Error() string {
	return scriptErrorLinePrefix(e.Line) + e.Message
}

// scriptErrorLinePrefix is how both script failures say where in the file
// they are. It is a wire format: the upload console parses `/^line (\d+):/i`
// back out (game-upload.tsx, errorLine) to offer "jump to line". Empty for an
// unknown line.
func scriptErrorLinePrefix(line int) string {
	if line <= 0 {
		return ""
	}
	return fmt.Sprintf("line %d: ", line)
}

// ScriptRejection satisfies provisioning.ScriptFailure.
func (e *ScriptSyntaxError) ScriptRejection() string { return e.Error() }

// Statement is one unit of work ScriptReader hands back: either plain SQL to
// run with Exec, or the header of a COPY ... FROM STDIN whose data must be
// streamed separately through CopyData before Next is called again.
type Statement struct {
	// Text is the SQL as it appeared in the source, from its first
	// non-whitespace byte to its ';' (or EOF). Never rewritten (CLAUDE.md
	// rule 14).
	Text string
	// Line is the 1-based source line this statement starts on.
	Line int
	// CopyHeader is set only for a `COPY ... FROM STDIN`: Text trimmed, for
	// pgconn.PgConn.CopyFrom. When set, Text must not also be run with Exec.
	CopyHeader string
}

// scanState is the lexer's quoting or comment context; a semicolon ends a
// statement only in scanTop.
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

// ScriptReader turns PostgreSQL SQL (an organiser's script or a pg_dump
// text-format dump) into one Statement at a time, holding at most one
// statement (maxStatementBytes) or one COPY data line (maxCopyDataLineBytes)
// in memory.
//
// It is not a SQL parser: it only finds where a statement ends, which needs
// PostgreSQL's quoting, comment and dollar-quote rules because a semicolon
// inside a literal or function body is routine.
type ScriptReader struct {
	r *bufio.Reader
	// line is the 1-based line the next byte read belongs to.
	line int
	// atLineStart is true when the next byte is the first on its line, so a
	// psql meta-command (a backslash at line start) is told apart from a
	// backslash inside a statement.
	atLineStart bool
	// copyOpen is true from a COPY header until CopyData has read its block
	// to the end. Next refuses to run meanwhile: only CopyData knows where
	// the block ends.
	copyOpen bool
	// buf is the statement buffer, reused across calls to Next: an --inserts
	// dump has one statement per row, and a fresh buffer each time measured
	// 5.4 bytes allocated per byte read. Nothing retains it, so reuse aliases
	// nothing.
	buf []byte
}

// maxRetainedStatementBytes is how much of the statement buffer is kept for
// the next statement, so one 16 MiB statement does not stay reserved for the
// rest of the build.
const maxRetainedStatementBytes = 1 << 20

// NewScriptReader wraps r. Nothing is read until the first call to Next.
func NewScriptReader(r io.Reader) *ScriptReader {
	return &ScriptReader{r: bufio.NewReaderSize(r, scanBufferSize), line: 1, atLineStart: true}
}

// The bytes that mean something to the lexer, per state; skipDull consumes
// everything else in one step. '\n' is in every set, so a skipped run never
// moves the line counter or atLineStart. dullTop stops on '\\' so a psql
// meta-command is never skipped.
const (
	dullTop            = "'\"$-/;\n\\"
	dullSingle         = "'\n"
	dullSingleExtended = "'\\\n"
	dullDouble         = "\"\n"
	dullDollar         = "$\n"
	dullLineComment    = "\n"
	dullBlockComment   = "*/\n"
)

// skipDull consumes the run of buffered bytes before the first byte in stop
// and returns it for a single append.
//
// The byte-at-a-time loop alone parsed INSERT dumps at about 120 MB/s against
// 1826 MB/s for COPY data; bytes.IndexAny over the window removes that cost.
// The run stops before any byte the lexer must see and holds no '\n'.
//
// The returned slice aliases the bufio buffer and is valid only until the
// next read, so the caller appends it immediately.
func (s *ScriptReader) skipDull(stop string) []byte {
	window, _ := s.r.Peek(s.r.Buffered())
	if len(window) == 0 {
		// A read error is left for the caller's next ReadByte to report.
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

// dullStop is the stop set for the lexer's state, or "" where nothing may be
// skipped: in scanTop before the statement's first SQL byte (which sets its
// line), and right after a '\' inside E'...', where the next byte is data.
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
// A refusal of the script text (a psql meta-command, an oversized statement,
// an unclosed quote or comment at EOF) is a *ScriptSyntaxError; an I/O error
// is wrapped. The reader must not be used after any error. pg_dump's
// \restrict and \unrestrict lines are skipped rather than refused.
func (s *ScriptReader) Next() (Statement, error) {
	if s.copyOpen {
		return Statement{}, fmt.Errorf("gamedb: the previous COPY block's data was not read before Next")
	}

	// Stored back however this returns; an oversized buffer is dropped.
	buf := s.buf[:0]
	defer func() {
		if cap(buf) > maxRetainedStatementBytes {
			buf = nil
		}
		s.buf = buf
	}()

	var (
		probe       copyProbe
		state       = scanTop
		blockDepth  int
		tag         string
		escapeNext  bool // singleExtended only: the previous byte was '\', so this one is data
		startLine   int
		haveContent bool
		// haveSQL is haveContent minus comments: true once a byte of the
		// statement itself is buffered. The \restrict case drops the buffer,
		// which is safe only while this is false.
		haveSQL bool
	)

	for {
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
					// Every byte of the run is code.
					probe.pushRun(run)
				}
				// A run holds no '\n'.
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

		// sqlBefore is haveSQL before this byte, restored when the byte turns
		// out to open a comment.
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
					// Not named back: the word was cut off unread.
					return Statement{}, &ScriptSyntaxError{
						Line: s.line,
						Message: fmt.Sprintf(
							"a line begins with a backslash and no psql command name follows it "+
								"within %d bytes — psql commands are not SQL, so export the dump without them",
							maxPsqlCommandWordBytes),
					}
				}
				if word == "restrict" || word == "unrestrict" {
					// pg_dump 16.10/17.6/18+ always wraps a plain-text dump
					// in \restrict/\unrestrict, guarding psql against
					// meta-commands. This reader runs no meta-commands, so the
					// pair is skipped; every other one is still refused.
					//
					// Only between statements: skipping drops the buffer, and
					// mid-statement that would silently lose the statement. A
					// build missing part of its data is worse than a refusal.
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
					// Data starts on the next line; the rest of this one must
					// not reach copyDataReader as an empty row.
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

		// Last, once the lexer has settled whether the byte was code.
		probe.observe(prevState, state, b)
	}
}

// finish decides whether the statement just closed is a `COPY ... FROM
// STDIN` and marks the reader accordingly, or refuses a statement that would
// change the lexer's dialect (dialectRefusal).
func (s *ScriptReader) finish(buf []byte, probe *copyProbe, startLine int) (Statement, error) {
	// Only leading whitespace is dropped; a leading comment stays, and
	// PostgreSQL ignores it.
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

// copyProbe is the statement's SQL with comment and literal bodies removed,
// the only text the COPY and dialect patterns are matched against.
//
// Matching raw text let a regexp find `copy` inside a comment and `from
// stdin` in the next statement, sending a plain CREATE TABLE to CopyFrom.
// Only the lexer knows where a comment ends. Each comment or quoted body
// becomes one space, so the tokens around it stay separate.
type copyProbe struct {
	// code is the statement's code bytes; code[0] is its first real character.
	code []byte
	// ruledOut is set once the first token is not a probe word or the header
	// outgrew maxCopyHeaderBytes; nothing is kept afterwards.
	ruledOut bool
}

// probeWords are the first tokens worth probing: `copy` for COPY ... FROM
// STDIN, `set`/`reset` for dialectRefusal. Any other statement is ruled out
// at its first byte, so the probe never copies a whole dump.
var probeWords = [][]byte{[]byte("copy"), []byte("set"), []byte("reset")}

// stillInteresting reports whether code can still begin with one of
// probeWords followed by a token boundary ("copyright" is not "copy").
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

// observe is called once per byte the lexer read, with the states before and
// after it.
func (p *copyProbe) observe(prev, next scanState, b byte) {
	switch {
	case p.ruledOut:
	case prev == scanTop && next == scanTop:
		p.push(b)
	case prev != next:
		// A comment or quoted body opened or closed: one space, none of its
		// bytes.
		p.push(' ')
	}
}

// pushRun is observe's code-byte case for a run skipDull skipped.
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
		return
	}
	p.code = append(p.code, b)
	if len(p.code) > maxCopyHeaderBytes {
		p.ruledOut, p.code = true, nil
		return
	}
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

// endCopyHeaderLine consumes the rest of a COPY header's line (trailing
// whitespace and the newline), so copyDataReader starts on the first data row.
//
// Any other text there is refused, not discarded: COPY data begins on the next
// line, so a statement after the header cannot run, and dropping it silently
// would build a game missing part of its script.
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

// skipToLineEnd discards bytes up to and including the next '\n', or to EOF.
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

// consumeInto reads n bytes that a Peek already confirmed are available and
// appends them to *buf, keeping the line counter in step.
func (s *ScriptReader) consumeInto(buf *[]byte, n int) {
	for range n {
		b, err := s.r.ReadByte()
		if err != nil {
			// Cannot happen after a successful Peek(n).
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

// isExtendedStringPrefix reports whether the quote just appended to buf opens
// an E'...' string, the only form that processes backslash escapes under
// standard_conforming_strings=on. The 'E' must not end a longer identifier;
// "TYPE'" is not valid SQL anyway.
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

// tryDollarTag is called with buf's last byte a '$' that may open a
// dollar-quote. It peeks for PostgreSQL's tag grammar (an identifier not
// starting with a digit, or empty, then '$'); on a match it consumes and
// appends the tag and closing '$' and returns the tag. Otherwise nothing is
// consumed, which keeps a parameter like $1 an ordinary character.
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
	// A tag holds no '\n', so the line counter needs no update.
	return string(consumed[:i]), true
}

// matchDollarTag is called with buf's last byte a '$' that may close the
// dollar-quote opened with tag. It consumes and appends the rest of the
// closer only if it matches; otherwise the '$' is part of the body.
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

// readPsqlCommandWord reads the command name after a backslash, up to
// whitespace or EOF, so the refusal can name it. whole is false when it
// stopped at maxPsqlCommandWordBytes.
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

// copyFromStdinPattern recognises a `COPY ... FROM STDIN` command. It is
// matched only against copyProbe.code, which holds no comments or literals.
var copyFromStdinPattern = regexp.MustCompile(`(?is)^copy\b.*\bfrom\s+stdin\b`)

// dialectPattern recognises a statement that changes
// standard_conforming_strings, matched against copyProbe.code.
var dialectPattern = regexp.MustCompile(
	`(?is)^(?:set|reset)\s+(?:session\s+|local\s+)?standard_conforming_strings\b`)

// dialectValuePattern reads the value out of such a statement. It runs on raw
// text, since the probe blanks literals and 'on' versus 'off' is the question;
// that is safe only after dialectPattern has matched on code.
var dialectValuePattern = regexp.MustCompile(`(?is)standard_conforming_strings\s*(?:=|\bto\b)\s*([^\s;]+)`)

// dialectOnValues are the spellings of a boolean GUC that mean on.
var dialectOnValues = map[string]bool{"on": true, "'on'": true, `"on"`: true, "true": true, "'true'": true, "1": true, "'1'": true}

// dialectRefusal refuses a statement that sets standard_conforming_strings to
// anything but on, or nil for every other statement.
//
// The lexer assumes the setting is on (a backslash in '...' is data). With it
// off, `('a\'); SELECT 1;` is one unterminated literal to the server and two
// statements here, so text could run as SQL nobody meant. runScript pins the
// setting, but the script's own SET would run later and win. RESET and
// SET ... TO DEFAULT are refused too: they defer to a cluster default.
func dialectRefusal(probe *copyProbe, text string, line int) *ScriptSyntaxError {
	if probe.ruledOut || !dialectPattern.Match(probe.code) {
		return nil
	}
	// No value at all is a RESET or SET ... TO DEFAULT.
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

// copyFromStdinHeader reports whether the probed statement is a `COPY ...
// FROM STDIN` and, if so, returns stmt trimmed and without its trailing ';'.
// The whole text is kept so a WITH (...) clause after STDIN, which sets the
// data format, reaches the server.
func copyFromStdinHeader(stmt string, probe *copyProbe) (string, bool) {
	if probe.ruledOut || !copyFromStdinPattern.Match(probe.code) {
		return "", false
	}
	trimmed := bytes.TrimSpace([]byte(stmt))
	trimmed = bytes.TrimRight(trimmed, ";")
	return string(bytes.TrimSpace(trimmed)), true
}

// copyDataReader streams one COPY ... FROM STDIN block's data unchanged, since
// rewriting a byte of COPY text format would corrupt data. It returns io.EOF
// at the "\." terminator line without passing that line on, and reads one
// line at a time, each bounded by maxCopyDataLineBytes.
type copyDataReader struct {
	parent  *ScriptReader
	pending []byte
	done    bool
	err     error
}

// Read fills p with as many data lines as fit before returning.
//
// pgconn.PgConn.CopyFrom sends every Read as one unbuffered CopyData message,
// so returning one row per Read cost one write(2) per row of the dump. Lines
// are still read one at a time under maxCopyDataLineBytes, so memory stays
// bounded by p plus one line (CLAUDE.md rule 12).
func (c *copyDataReader) Read(p []byte) (int, error) {
	var n int
	for n < len(p) {
		if len(c.pending) > 0 {
			copied := copy(p[n:], c.pending)
			// The rest of a long line stays in pending. It may alias the bufio
			// window, which is safe because nothing reads until it drains.
			c.pending = c.pending[copied:]
			n += copied
			continue
		}
		if c.done || c.err != nil {
			// Reported on the next call, after the bytes already in p.
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

// advance reads one more data line and sets exactly one of pending, done or
// err, so Read's loop always makes progress.
func (c *copyDataReader) advance() {
	line, readErr := c.readDataLine()
	var tooLong *ScriptSyntaxError
	switch {
	case errors.As(readErr, &tooLong):
		// Unwrapped, so it still reads as a fault in the author's file.
		c.err = readErr

	case readErr != nil && readErr != io.EOF:
		c.err = fmt.Errorf("gamedb: read COPY data: %w", readErr)

	case readErr == io.EOF && len(line) == 0:
		c.err = &ScriptSyntaxError{
			Line: c.parent.line, Message: `COPY data ends with no terminating "\." line`,
		}

	case readErr == io.EOF:
		// A truncated final line: handed out as data; the missing "\." is
		// reported on the next call.
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

// readDataLine reads one line of COPY data, including its '\n' (or to EOF),
// and never allocates more than maxCopyDataLineBytes doing it. ReadSlice is
// used instead of ReadBytes, which grows without a ceiling (CLAUDE.md
// rule 12).
//
// A line that fits the bufio window is returned as a slice of it, with no
// copy. It stays valid until the next read from c.parent.r, and nothing reads
// before Read drains it.
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
