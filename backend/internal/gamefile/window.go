package gamefile

import (
	"fmt"
	"io"
	"os"
)

// windowScanBufferSize bounds how much of the data file Window holds at once
// while it skips to the requested line and reads the lines themselves. Like
// scanBufferSize in index.go, it does not grow with the file or with any one
// line inside it.
const windowScanBufferSize = 64 * 1024

// Window returns up to maxLines lines of id's completed upload, starting at
// fromLine (1-based), and never reads more than maxBytes of the file doing
// it. maxBytes is the caller's own budget for this call — the same role
// MaxChunkBytes plays for Append, just decided by whoever is asking for a
// look at the file rather than fixed at Store construction, since a console
// page's reasonable window size has nothing to do with an upload's chunk
// size.
func (s *Store) Window(id string, fromLine, maxLines int, maxBytes int64) (Window, error) {
	if err := validateUploadID(id); err != nil {
		return Window{}, err
	}
	if fromLine < 1 {
		fromLine = 1
	}

	idxFile, err := os.Open(s.indexPath(id))
	if err != nil {
		if !os.IsNotExist(err) {
			return Window{}, fmt.Errorf("gamefile: open index: %w", err)
		}
		// No index yet: either this id was never begun, or it was begun
		// but never sealed with Complete. Those are different refusals for
		// a caller to act on, so tell them apart rather than collapsing
		// both into ErrNotFound.
		if _, statErr := os.Stat(s.dataPath(id)); statErr != nil {
			if os.IsNotExist(statErr) {
				return Window{}, ErrNotFound
			}
			return Window{}, fmt.Errorf("gamefile: stat upload: %w", statErr)
		}
		return Window{}, ErrIncomplete
	}
	defer idxFile.Close()

	hdr, err := readIndexHeader(idxFile)
	if err != nil {
		return Window{}, fmt.Errorf("gamefile: read index: %w", err)
	}

	if maxLines <= 0 || int64(fromLine) > hdr.totalLines {
		// Nothing was asked for, or the request starts past the end of the
		// file. Both are an empty window, not an error — the caller finding
		// out there is nothing more to show is a normal outcome of paging.
		return Window{FromLine: fromLine, TotalLines: hdr.totalLines}, nil
	}

	markIdx := int64(fromLine-1) / indexInterval
	markOff, err := markOffset(idxFile, hdr, markIdx)
	if err != nil {
		return Window{}, fmt.Errorf("gamefile: locate mark: %w", err)
	}
	lineAtMark := markIdx*indexInterval + 1

	dataFile, err := os.Open(s.dataPath(id))
	if err != nil {
		return Window{}, fmt.Errorf("gamefile: open upload: %w", err)
	}
	defer dataFile.Close()

	if _, err := dataFile.Seek(markOff, io.SeekStart); err != nil {
		return Window{}, fmt.Errorf("gamefile: seek upload: %w", err)
	}

	if toSkip := int64(fromLine) - lineAtMark; toSkip > 0 {
		if err := skipLines(dataFile, toSkip); err != nil {
			return Window{}, fmt.Errorf("gamefile: skip to line %d: %w", fromLine, err)
		}
	}

	lines, truncated, err := readWindowLines(dataFile, maxLines, maxBytes)
	if err != nil {
		return Window{}, fmt.Errorf("gamefile: read window: %w", err)
	}

	return Window{
		FromLine:   fromLine,
		Lines:      lines,
		TotalLines: hdr.totalLines,
		Truncated:  truncated,
	}, nil
}

// skipLines advances f past exactly n newlines, leaving its position at the
// first byte of the line that follows. It never accumulates the bytes it
// skips over — it only counts '\n' occurrences through a fixed buffer — so
// skipping across lines that are themselves megabytes long costs no more
// memory than skipping across short ones.
func skipLines(f *os.File, n int64) error {
	start, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}

	buf := make([]byte, windowScanBufferSize)
	pos := start
	var skipped int64
	for {
		nRead, rerr := f.Read(buf)
		for i := 0; i < nRead; i++ {
			if buf[i] != '\n' {
				continue
			}
			skipped++
			if skipped == n {
				// Seek to just past this newline rather than trusting the
				// buffered read position: the buffer likely holds bytes
				// beyond the target, and this call must leave f positioned
				// exactly at the start of the requested line.
				_, err := f.Seek(pos+int64(i)+1, io.SeekStart)
				return err
			}
		}
		pos += int64(nRead)

		if rerr == io.EOF {
			// The index guaranteed fromLine <= totalLines, so this means
			// the data file no longer matches the index it was completed
			// with — surfaced rather than silently read from the wrong
			// place.
			return fmt.Errorf("reached end of file after %d of %d lines", skipped, n)
		}
		if rerr != nil {
			return rerr
		}
	}
}

// readWindowLines reads forward from f's current position, collecting up to
// maxLines complete lines and stopping the instant maxBytes bytes have been
// read, whichever comes first. A line that is still open when the byte
// budget runs out is returned as the (honestly partial) last line, with
// truncated set to true.
//
// The memory this holds is bounded by maxBytes — the caller's own budget for
// this call, the same way MaxChunkBytes bounds one Append. It is never
// bounded by the file, because the file is never the thing being measured
// here.
func readWindowLines(f *os.File, maxLines int, maxBytes int64) ([]string, bool, error) {
	buf := make([]byte, windowScanBufferSize)
	var (
		lines     []string
		cur       []byte
		remaining = maxBytes
		truncated bool
	)

readLoop:
	for len(lines) < maxLines {
		n, rerr := f.Read(buf)
		for i := 0; i < n; i++ {
			if remaining <= 0 {
				truncated = true
				break readLoop
			}
			remaining--

			b := buf[i]
			if b == '\n' {
				lines = append(lines, string(cur))
				cur = nil
				if len(lines) >= maxLines {
					break readLoop
				}
				continue
			}
			cur = append(cur, b)
		}
		if rerr == io.EOF {
			break readLoop
		}
		if rerr != nil {
			return nil, false, rerr
		}
	}

	// Whatever line was still open when the loop stopped: on a byte-budget
	// cutoff it is genuinely partial (truncated=true says so); on reaching
	// EOF it is a file with no trailing newline, and it is whole.
	if len(cur) > 0 {
		lines = append(lines, string(cur))
	}

	return lines, truncated, nil
}
