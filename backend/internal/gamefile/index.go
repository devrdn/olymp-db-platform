package gamefile

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

// indexInterval is how many lines apart the line index records a byte offset,
// so a window seeks near its first line instead of reading from the start.
const indexInterval = 1000

// scanBufferSize is all buildIndex holds of the data file at once, however
// large the file or any line in it (CLAUDE.md rule 12).
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

// buildIndex reads f once from the start, computing its SHA-256 and line
// index together, and leaves f at EOF. Only a line-start flag crosses chunk
// boundaries, so a line of any length costs nothing beyond scanBufferSize.
//
// Newlines are found with bytes.IndexByte, not a byte loop: measured at
// 6.5 GiB/s against 1.3 GiB/s, while SHA-256 runs at 2.3 GiB/s. This runs
// inside the request that completes an upload, so for a 3 GB dump the byte
// loop would cost about twenty seconds of a core instead of a few.
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
				// Marks go on a line's first byte, so a trailing '\n'
				// records no mark for a line that never began.
				if atLineStart {
					if lineNo%indexInterval == 0 {
						idx.marks = append(idx.marks, offset+int64(pos))
					}
					atLineStart = false
				}
				nl := bytes.IndexByte(chunk[pos:], '\n')
				if nl < 0 {
					sawContent = true
					break
				}
				lineNo++
				atLineStart = true
				sawContent = false
				pos += nl + 1
			}
			_, _ = h.Write(chunk) // hash.Hash.Write never returns an error
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
		idx.totalLines++ // an unterminated last line still counts
	}
	idx.totalBytes = offset

	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return idx, sum, nil
}

// indexTempPattern is both the CreateTemp pattern and the glob that finds
// leftovers of it.
const indexTempPattern = ".tmp-*"

// clearIndexTemps removes temporary index files left next to path by a
// writeIndex that was killed before its rename. Nothing else can name them
// (UploadIDs lists data files only), yet they count against MaxDirBytes.
// Callers hold the per-id lock.
func clearIndexTemps(path string) error {
	// path comes from a validated id, so it holds no glob metacharacters.
	leftovers, err := filepath.Glob(path + indexTempPattern)
	if err != nil {
		return fmt.Errorf("gamefile: list leftover index files: %w", err)
	}
	for _, leftover := range leftovers {
		if err := os.Remove(leftover); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("gamefile: remove leftover index file: %w", err)
		}
	}
	return nil
}

// writeIndex persists idx next to its data file via a temporary file and a
// rename, so a killed process leaves the previous index or none, never a torn
// one.
func writeIndex(path string, idx fileIndex) error {
	// buildIndex never produces negative values; the checks make the uint64
	// conversions below verified (gosec G115).
	if idx.totalBytes < 0 || idx.totalLines < 0 {
		return fmt.Errorf("gamefile: index has a negative total (bytes=%d lines=%d)", idx.totalBytes, idx.totalLines)
	}

	if err := clearIndexTemps(path); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+indexTempPattern)
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

// indexHeader is the fixed-size prefix of an index file.
type indexHeader struct {
	totalBytes int64
	totalLines int64
	markCount  int64
}

// maxMarkCount is the largest markCount for which indexHeaderSize+markCount*8
// fits in an int64, so the size check cannot overflow.
const maxMarkCount = (math.MaxInt64 - indexHeaderSize) / 8

// readIndexHeader reads and validates the header of an open index file.
//
// The file on disk may be corrupt or substituted, so each uint64 is checked to
// fit an int64 (gosec G115) and the file size must match markCount. Failures
// are ErrCorruptIndex rather than a rebuild: rescanning a multi-gigabyte dump
// on every Window would make a corrupt index a way to make every read
// expensive.
func readIndexHeader(f *os.File) (indexHeader, error) {
	var raw [indexHeaderSize]byte
	if _, err := io.ReadFull(f, raw[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return indexHeader{}, fmt.Errorf("%w: the index is shorter than one header", ErrCorruptIndex)
		}
		return indexHeader{}, fmt.Errorf("read index header: %w", err)
	}
	if [4]byte(raw[0:4]) != indexMagic {
		return indexHeader{}, fmt.Errorf("%w: the file does not begin with an index header", ErrCorruptIndex)
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

	// Checked once here so markOffset knows every slot exists.
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

// markOffset reads the byte offset of mark markIdx (0-based), the start of
// line markIdx*indexInterval + 1, with one 8-byte read.
func markOffset(f *os.File, hdr indexHeader, markIdx int64) (int64, error) {
	if markIdx < 0 || markIdx >= hdr.markCount {
		// Window checked the line against totalLines, so this is the index
		// contradicting itself.
		return 0, fmt.Errorf("%w: mark %d is out of range (have %d)", ErrCorruptIndex, markIdx, hdr.markCount)
	}
	at := int64(indexHeaderSize) + markIdx*8
	var raw [8]byte
	if _, err := f.ReadAt(raw[:], at); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			// The size was checked, so the file shrank underneath us.
			return 0, fmt.Errorf("%w: mark %d could not be read in full", ErrCorruptIndex, markIdx)
		}
		return 0, fmt.Errorf("read mark %d: %w", markIdx, err)
	}

	// A corrupt slot must not become a negative or out-of-file seek: check it
	// fits an int64 (gosec G115) and lies within the declared data length.
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
