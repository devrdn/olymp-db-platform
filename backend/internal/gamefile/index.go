package gamefile

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// indexInterval is how often the line index records a byte offset: every
// 1000th line, so that reading "lines 1,200,000-1,200,200" means seeking to
// the mark at line 1,200,001 and reading forward two hundred lines, never
// reading the gigabyte that precedes them.
const indexInterval = 1000

// scanBufferSize bounds how much of the data file buildIndex holds at once
// while it scans for line boundaries and feeds the hash. It is fixed
// regardless of how large the file — or any single line inside it — is,
// which is what makes one sequential pass over a multi-gigabyte dump safe to
// run in this process at all (CLAUDE.md rule 12).
const scanBufferSize = 1 << 20 // 1 MiB

// indexMagic tags an index file so a stray file of the wrong shape fails
// loudly instead of being read as garbage marks.
var indexMagic = [4]byte{'G', 'F', 'I', '1'}

// indexHeaderSize is magic + totalBytes + totalLines + markCount, each an
// 8-byte field except the 4-byte magic.
const indexHeaderSize = 4 + 8 + 8 + 8

// fileIndex is what one sequential pass over a data file learns: its total
// size and line count, and the byte offset of every indexInterval-th line.
// marks[i] is the offset of line i*indexInterval + 1 (1-based).
type fileIndex struct {
	marks      []int64
	totalBytes int64
	totalLines int64
}

// buildIndex reads f from the start to the end exactly once, computing the
// SHA-256 of its bytes and the line index together. f's position is left at
// EOF; callers that need it from the start again must Seek.
//
// The scan never buffers a whole line: it looks at chunk bytes one at a
// time for '\n', and what crosses a chunk boundary is just an integer state
// (are we at the start of a line right now) rather than any accumulated
// bytes. A single line of several gigabytes costs this function nothing
// beyond scanBufferSize.
func buildIndex(f *os.File) (fileIndex, [sha256.Size]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fileIndex{}, [sha256.Size]byte{}, err
	}

	h := sha256.New()
	buf := make([]byte, scanBufferSize)

	var (
		idx         fileIndex
		offset      int64
		lineNo      int64
		atLineStart = true
		sawContent  bool
	)

	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			for i, b := range chunk {
				if atLineStart {
					if lineNo%indexInterval == 0 {
						idx.marks = append(idx.marks, offset+int64(i))
					}
					atLineStart = false
				}
				if b == '\n' {
					lineNo++
					atLineStart = true
					sawContent = false
				} else {
					sawContent = true
				}
			}
			// hash.Hash.Write never returns an error (documented on the
			// interface); there is nothing here to check.
			_, _ = h.Write(chunk)
			offset += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fileIndex{}, [sha256.Size]byte{}, rerr
		}
	}

	idx.totalLines = lineNo
	if sawContent {
		// The file has trailing bytes after the last '\n' (or none at all):
		// a final line that never got terminated still counts.
		idx.totalLines++
	}
	idx.totalBytes = offset

	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return idx, sum, nil
}

// writeIndex persists idx next to the data file it describes. It writes to
// a temporary file in the same directory and renames it into place, so a
// process killed mid-write leaves either the previous index or none, never
// a truncated one that Window would misread.
//
// The marks slice is proportional to totalLines/indexInterval, not to
// totalBytes — for a several-gigabyte dump with short lines that is still
// only tens of thousands of int64s, comfortably a bounded, small structure
// rather than a second copy of the file.
func writeIndex(path string, idx fileIndex) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName) // no-op once the rename below succeeds
	}()

	w := bufio.NewWriter(tmp)
	var header [indexHeaderSize]byte
	copy(header[0:4], indexMagic[:])
	binary.BigEndian.PutUint64(header[4:12], uint64(idx.totalBytes))
	binary.BigEndian.PutUint64(header[12:20], uint64(idx.totalLines))
	binary.BigEndian.PutUint64(header[20:28], uint64(len(idx.marks)))
	if _, err := w.Write(header[:]); err != nil {
		_ = tmp.Close()
		return err
	}

	var markBuf [8]byte
	for _, m := range idx.marks {
		binary.BigEndian.PutUint64(markBuf[:], uint64(m))
		if _, err := w.Write(markBuf[:]); err != nil {
			_ = tmp.Close()
			return err
		}
	}

	if err := w.Flush(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// indexHeader is the fixed-size prefix of an index file, read without
// touching the marks that follow it.
type indexHeader struct {
	totalBytes int64
	totalLines int64
	markCount  int64
}

// readIndexHeader reads and validates the header of an already-open index
// file, leaving its position just past the header, ready for markOffset to
// seek relative to.
func readIndexHeader(f *os.File) (indexHeader, error) {
	var raw [indexHeaderSize]byte
	if _, err := io.ReadFull(f, raw[:]); err != nil {
		return indexHeader{}, fmt.Errorf("read index header: %w", err)
	}
	if [4]byte(raw[0:4]) != indexMagic {
		return indexHeader{}, fmt.Errorf("index file has an unrecognised header")
	}
	return indexHeader{
		totalBytes: int64(binary.BigEndian.Uint64(raw[4:12])),
		totalLines: int64(binary.BigEndian.Uint64(raw[12:20])),
		markCount:  int64(binary.BigEndian.Uint64(raw[20:28])),
	}, nil
}

// markOffset reads the byte offset recorded for mark markIdx (0-based),
// which is the start of line markIdx*indexInterval + 1. It seeks directly to
// that mark's slot rather than reading every mark before it, so looking up
// one mark in a file with tens of thousands of them costs one seek and one
// 8-byte read.
func markOffset(f *os.File, hdr indexHeader, markIdx int64) (int64, error) {
	if markIdx < 0 || markIdx >= hdr.markCount {
		return 0, fmt.Errorf("mark %d is out of range (have %d)", markIdx, hdr.markCount)
	}
	at := int64(indexHeaderSize) + markIdx*8
	var raw [8]byte
	if _, err := f.ReadAt(raw[:], at); err != nil {
		return 0, fmt.Errorf("read mark %d: %w", markIdx, err)
	}
	return int64(binary.BigEndian.Uint64(raw[:])), nil
}
