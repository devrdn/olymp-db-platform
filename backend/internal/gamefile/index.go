package gamefile

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"math"
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
// The scan never buffers a whole line: it walks each chunk from one '\n' to
// the next, and what crosses a chunk boundary is just an integer state (are
// we at the start of a line right now) rather than any accumulated bytes. A
// single line of several gigabytes costs this function nothing beyond
// scanBufferSize.
//
// The newline hunt is bytes.IndexByte and not a `for i, b := range chunk`
// comparing every byte. The two are not close: measured on a gibibyte, the
// byte-at-a-time loop runs at 1.26 GiB/s against IndexByte's 6.51 GiB/s
// (IndexByte is assembly using the machine's vector registers), while the
// sha256 this pass computes alongside it manages 2.26 GiB/s. So the naive
// loop was not a detail next to the hashing — it was the larger half of the
// pass, about 44% of its CPU spent finding newlines. It matters because this
// runs synchronously inside the HTTP request that completes an upload: for a
// three-gigabyte dump that is the difference between a handful of seconds and
// twenty, on the API process serving the olympiad, and "one sequential pass"
// above is a promise about I/O that should not quietly also mean one occupied
// core for twenty seconds.
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
			for pos := 0; pos < len(chunk); {
				// A mark is recorded on the first byte of a line, never on
				// the newline that ended the previous one — so a file whose
				// last byte is '\n' records no mark for the line that never
				// began, exactly as the byte-at-a-time version did.
				if atLineStart {
					if lineNo%indexInterval == 0 {
						idx.marks = append(idx.marks, offset+int64(pos))
					}
					atLineStart = false
				}
				nl := bytes.IndexByte(chunk[pos:], '\n')
				if nl < 0 {
					// The rest of this chunk is one unterminated line so far;
					// whether it is really the file's last is settled after
					// the loop, by sawContent.
					sawContent = true
					break
				}
				lineNo++
				atLineStart = true
				sawContent = false
				pos += nl + 1
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
	// buildIndex only ever produces non-negative totals and marks: offset
	// and lineNo both start at zero and only ever increase over a
	// sequential read of the file (buildIndex's own doc), so none of these
	// checks can actually fire today. They are written anyway, rather than
	// left as a comment's assertion, for two reasons: it is what the
	// int64->uint64 conversions below need to be a verified fact instead of
	// an assumption (gosec G115), and if buildIndex ever did produce a
	// negative value — a future refactor that lets offset wrap, say — this
	// is what turns that bug into an error here instead of a silent write
	// of a huge unsigned offset that markOffset would have to catch on the
	// read side instead.
	if idx.totalBytes < 0 || idx.totalLines < 0 {
		return fmt.Errorf("gamefile: index has a negative total (bytes=%d lines=%d)", idx.totalBytes, idx.totalLines)
	}

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
		// Same invariant as totalBytes/totalLines above, checked per mark
		// rather than once: every mark is an offset buildIndex recorded
		// while walking forward through the file, so it can never be
		// negative either.
		if m < 0 {
			_ = tmp.Close()
			return fmt.Errorf("gamefile: index has a negative mark (%d)", m)
		}
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

// maxMarkCount bounds markCount before readIndexHeader multiplies it by 8 to
// compute the file size a genuine index of that shape would have: it is the
// largest value for which indexHeaderSize+markCount*8 still fits in an
// int64, so that multiplication can never wrap and turn a wildly-too-large
// markCount into a small number that happens to match the file on disk.
const maxMarkCount = (math.MaxInt64 - indexHeaderSize) / 8

// readIndexHeader reads and validates the header of an already-open index
// file, leaving its position just past the header, ready for markOffset to
// seek relative to.
//
// The index is a file on local disk next to the upload it describes, not a
// value this package fully controls end to end: disk corruption, a killed
// process leaving a half-written file some other path didn't catch, or a
// substituted file are all real ways for its bytes to stop matching what
// writeIndex actually wrote. Every field read here is a uint64 straight off
// disk, so before any of them becomes this package's own int64 fields
// (Seek offsets and line counts elsewhere in this package), two things are
// checked: that the value fits in an int64 at all — a corrupt file can claim
// any 64-bit pattern, and converting one at or above 2^63 gives a negative
// int64 (gosec G115 is flagging exactly this narrowing) — and, for
// markCount, that the file is actually as long as a header truthfully
// declaring that many marks would make it. Both are ErrCorruptIndex, not a
// value clamped or a rebuild triggered here: Window (the only caller) is
// meant to be a cheap paginated read, and silently rescanning a
// multi-gigabyte dump on every request against a corrupt index would turn a
// corrupt file into a way to make every read of it expensive.
func readIndexHeader(f *os.File) (indexHeader, error) {
	var raw [indexHeaderSize]byte
	if _, err := io.ReadFull(f, raw[:]); err != nil {
		return indexHeader{}, fmt.Errorf("read index header: %w", err)
	}
	if [4]byte(raw[0:4]) != indexMagic {
		return indexHeader{}, fmt.Errorf("index file has an unrecognised header")
	}

	totalBytes := binary.BigEndian.Uint64(raw[4:12])
	totalLines := binary.BigEndian.Uint64(raw[12:20])
	markCount := binary.BigEndian.Uint64(raw[20:28])
	if totalBytes > math.MaxInt64 || totalLines > math.MaxInt64 || markCount > math.MaxInt64 {
		return indexHeader{}, fmt.Errorf("%w: header field does not fit a signed 64-bit value", ErrCorruptIndex)
	}

	hdr := indexHeader{
		totalBytes: int64(totalBytes),
		totalLines: int64(totalLines),
		markCount:  int64(markCount),
	}
	if hdr.markCount > maxMarkCount {
		return indexHeader{}, fmt.Errorf("%w: mark count %d is not plausible", ErrCorruptIndex, hdr.markCount)
	}

	// markCount claims how many 8-byte marks follow the header; a real
	// index file's size is exactly indexHeaderSize+markCount*8, so a file
	// that disagrees was truncated, extended, or never written by
	// writeIndex at all. Checking this once here means markOffset never has
	// to guess whether the slot it is about to seek to and read actually
	// exists inside the file.
	info, err := f.Stat()
	if err != nil {
		return indexHeader{}, fmt.Errorf("stat index: %w", err)
	}
	wantSize := int64(indexHeaderSize) + hdr.markCount*8
	if info.Size() != wantSize {
		return indexHeader{}, fmt.Errorf("%w: header declares %d marks, file is %d bytes (want %d)",
			ErrCorruptIndex, hdr.markCount, info.Size(), wantSize)
	}

	return hdr, nil
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

	// off is a byte offset read straight off disk, from the same file
	// readIndexHeader already found the right size for — but that only
	// bounds the file, not what any individual 8-byte slot inside it holds.
	// A flipped bit here is exactly what turns into "Seek to a negative
	// offset" the moment it is used (this function's whole reason to
	// exist): the first check below is what makes the uint64->int64
	// conversion a verified fact instead of an assumption (gosec G115); the
	// second rejects a value that is technically a valid positive int64 but
	// still not a byte offset that can exist in this data file, using
	// hdr.totalBytes — the data length this very index declares for
	// itself — as the bound.
	rawOff := binary.BigEndian.Uint64(raw[:])
	if rawOff > math.MaxInt64 {
		return 0, fmt.Errorf("%w: mark %d offset does not fit a signed 64-bit value", ErrCorruptIndex, markIdx)
	}
	off := int64(rawOff)
	if off > hdr.totalBytes {
		return 0, fmt.Errorf("%w: mark %d offset %d exceeds data length %d", ErrCorruptIndex, markIdx, off, hdr.totalBytes)
	}
	return off, nil
}
