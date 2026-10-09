package gamefile

import (
	"context"
	"fmt"
	"io"
	"os"
)

// windowScanBufferSize is all Window holds of the data file at once.
const windowScanBufferSize = 64 * 1024

// maxWindowSkipBytes bounds the walk from the nearest index mark to the
// requested line, which maxBytes does not cover. Lines have no length limit,
// so without it a request for one byte at line 1,999 could read most of a
// multi-gigabyte file. 8 MiB averages about 8 KiB per skipped line; past it
// Window returns ErrWindowUnreachable.
const maxWindowSkipBytes = 8 << 20

// Window returns up to maxLines lines of id's completed upload, starting at
// fromLine (1-based).
//
// maxBytes bounds the lines returned and maxWindowSkipBytes bounds reaching
// fromLine, so the call's cost never depends on the file's size. ctx stops
// the read between buffers, so a client that hung up does not keep a
// goroutine reading.
func (s *Store) Window(ctx context.Context, id string, fromLine, maxLines int, maxBytes int64) (Window, error) {
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
		// No index: never begun (ErrNotFound) or not yet completed
		// (ErrIncomplete).
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
		// Paging past the end is an empty window, not an error.
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
		if err := skipLines(ctx, dataFile, toSkip, maxWindowSkipBytes); err != nil {
			return Window{}, fmt.Errorf("gamefile: skip to line %d: %w", fromLine, err)
		}
	}

	lines, truncated, err := readWindowLines(ctx, dataFile, maxLines, maxBytes)
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

// skipLines advances f past n newlines, reading at most budget bytes, and
// leaves it at the first byte of the following line. ctx is checked once per
// buffer.
func skipLines(ctx context.Context, f *os.File, n, budget int64) error {
	start, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}

	buf := make([]byte, windowScanBufferSize)
	pos := start
	var skipped, read int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if read >= budget {
			return fmt.Errorf("%w: %d of %d line(s) walked in %d bytes",
				ErrWindowUnreachable, skipped, n, read)
		}

		nRead, rerr := f.Read(buf)
		read += int64(nRead)
		for i := 0; i < nRead; i++ {
			if buf[i] != '\n' {
				continue
			}
			skipped++
			if skipped == n {
				// The read went past the target; seek back to it.
				_, err := f.Seek(pos+int64(i)+1, io.SeekStart)
				return err
			}
		}
		pos += int64(nRead)

		if rerr == io.EOF {
			// The index promised fromLine <= totalLines, so the data file
			// no longer matches it.
			return fmt.Errorf("%w: the data file ended after %d of %d line(s)",
				ErrCorruptIndex, skipped, n)
		}
		if rerr != nil {
			return rerr
		}
	}
}

// readWindowLines reads up to maxLines lines from f's position, stopping once
// maxBytes have been read. A line cut by the budget is returned partial, with
// truncated set. ctx is checked once per buffer.
func readWindowLines(ctx context.Context, f *os.File, maxLines int, maxBytes int64) ([]string, bool, error) {
	buf := make([]byte, windowScanBufferSize)
	var (
		lines     []string
		cur       []byte
		remaining = maxBytes
		truncated bool
	)

readLoop:
	for len(lines) < maxLines {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
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

	// The open line is partial after a budget cutoff, or whole at EOF.
	if len(cur) > 0 {
		lines = append(lines, string(cur))
	}

	return lines, truncated, nil
}
