package gamefile

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
)

func completeUpload(t *testing.T, s *Store, id, content string) Summary {
	t.Helper()
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader(content)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	sum, err := s.Complete(id, int64(len(content)))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return sum
}

func TestCompleteReportsChecksumAndLines(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "10000000-0000-0000-0000-000000000000"
	const content = "one\ntwo\nthree\n"

	sum := completeUpload(t, s, id, content)

	want := sha256.Sum256([]byte(content))
	if sum.SHA256 != hex.EncodeToString(want[:]) {
		t.Errorf("SHA256 = %s, want %s", sum.SHA256, hex.EncodeToString(want[:]))
	}
	if sum.Bytes != int64(len(content)) {
		t.Errorf("Bytes = %d, want %d", sum.Bytes, len(content))
	}
	if sum.Lines != 3 {
		t.Errorf("Lines = %d, want 3", sum.Lines)
	}
}

func TestCompleteEmptyFile(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "20000000-0000-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	sum, err := s.Complete(id, 0)
	if err != nil {
		t.Fatalf("Complete(empty file): %v", err)
	}
	if sum.Bytes != 0 || sum.Lines != 0 {
		t.Errorf("Complete(empty file) = %+v, want Bytes:0 Lines:0", sum)
	}
	wantSum := sha256.Sum256(nil)
	if sum.SHA256 != hex.EncodeToString(wantSum[:]) {
		t.Errorf("SHA256 of empty file = %s, want %s", sum.SHA256, hex.EncodeToString(wantSum[:]))
	}
}

func TestCompleteFileWithoutTrailingNewline(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "30000000-0000-0000-0000-000000000000"
	const content = "one\ntwo\nthree" // no trailing \n

	sum := completeUpload(t, s, id, content)
	if sum.Lines != 3 {
		t.Errorf("Lines = %d, want 3 (trailing content without a newline still counts as a line)", sum.Lines)
	}
}

func TestCompleteLengthMismatch(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "40000000-0000-0000-0000-000000000000"
	const content = "abcdefghij" // 10 bytes
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader(content)); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Declared length one byte short of what actually landed on disk.
	if _, err := s.Complete(id, 9); !errors.Is(err, ErrLengthMismatch) {
		t.Errorf("Complete(declared 9, actual 10) = %v, want ErrLengthMismatch", err)
	}
	// Declared length one byte over what actually landed on disk.
	if _, err := s.Complete(id, 11); !errors.Is(err, ErrLengthMismatch) {
		t.Errorf("Complete(declared 11, actual 10) = %v, want ErrLengthMismatch", err)
	}
}

func TestCompleteUnknownID(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	if _, err := s.Complete("50000000-0000-0000-0000-000000000000", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Complete on unknown id = %v, want ErrNotFound", err)
	}
}

func TestCompletePersistsAnIndexUsableAcrossOpens(t *testing.T) {
	// Complete's index is read back by a fresh os.Open in a later call
	// (e.g. after a process restart), not carried in memory — this checks
	// that round trip explicitly rather than only through Window.
	s := newTestStore(t, permissiveLimits())
	const id = "60000000-0000-0000-0000-000000000000"
	var lines []string
	for i := 1; i <= 2500; i++ {
		lines = append(lines, fmt.Sprintf("line-%d", i))
	}
	content := strings.Join(lines, "\n") + "\n"
	completeUpload(t, s, id, content)

	f, err := os.Open(s.indexPath(id))
	if err != nil {
		t.Fatalf("open index file: %v", err)
	}
	defer f.Close()

	hdr, err := readIndexHeader(f)
	if err != nil {
		t.Fatalf("readIndexHeader: %v", err)
	}
	if hdr.totalLines != 2500 {
		t.Errorf("hdr.totalLines = %d, want 2500", hdr.totalLines)
	}
	if hdr.totalBytes != int64(len(content)) {
		t.Errorf("hdr.totalBytes = %d, want %d", hdr.totalBytes, len(content))
	}
	// Marks for line-starts 1, 1001, 2001 => 3 marks for 2500 lines.
	if hdr.markCount != 3 {
		t.Errorf("hdr.markCount = %d, want 3", hdr.markCount)
	}

	off, err := markOffset(f, hdr, 0)
	if err != nil {
		t.Fatalf("markOffset(0): %v", err)
	}
	if off != 0 {
		t.Errorf("markOffset(0) = %d, want 0", off)
	}
}

// putUint64At overwrites 8 bytes of an already-on-disk index file at byte
// offset at, in place. Every corrupt-index test below builds a genuine index
// with Complete and then damages one real file on t.TempDir() this way,
// rather than constructing a fileIndex or indexHeader value by hand: the
// thing under test is what readIndexHeader and markOffset do with bytes that
// actually came from a stat+read of a file on disk, which a mock struct
// cannot stand in for.
func putUint64At(t *testing.T, path string, at int64, v uint64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open index for corruption: %v", err)
	}
	defer f.Close()

	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	if _, err := f.WriteAt(buf[:], at); err != nil {
		t.Fatalf("corrupt index at %d: %v", at, err)
	}
}

// Byte offsets of the header's three fields, mirroring writeIndex/
// readIndexHeader's own layout (magic, then three 8-byte fields).
const (
	headerTotalBytesOff = 4
	headerTotalLinesOff = 12
	headerMarkCountOff  = 20
)

func TestReadIndexHeaderRejectsFieldThatOverflowsInt64(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "80000000-0000-0000-0000-000000000000"
	completeUpload(t, s, id, "one\ntwo\nthree\n")
	path := s.indexPath(id)

	// A genuine writeIndex never puts a value at or above 2^63 in the
	// header (it only ever converts non-negative int64s); this is the
	// shape a corrupted or substituted file takes instead.
	putUint64At(t, path, headerTotalBytesOff, uint64(math.MaxInt64)+1)

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer f.Close()

	if _, err := readIndexHeader(f); !errors.Is(err, ErrCorruptIndex) {
		t.Fatalf("readIndexHeader with an out-of-range totalBytes = %v, want ErrCorruptIndex", err)
	}
}

func TestReadIndexHeaderRejectsMarkCountThatDoesNotMatchFileSize(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "80000000-0000-0000-0000-000000000001"
	completeUpload(t, s, id, "one\ntwo\nthree\n")
	path := s.indexPath(id)

	// The file on disk still has exactly enough bytes for the marks a
	// genuine header would declare; claiming one more than that is what a
	// truncated write or a substituted file looks like.
	putUint64At(t, path, headerMarkCountOff, 2)

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer f.Close()

	if _, err := readIndexHeader(f); !errors.Is(err, ErrCorruptIndex) {
		t.Fatalf("readIndexHeader with a mismatched markCount = %v, want ErrCorruptIndex", err)
	}
}

func TestMarkOffsetRejectsMarkThatOverflowsInt64(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "80000000-0000-0000-0000-000000000002"
	completeUpload(t, s, id, buildNumberedLines(2500)) // 3 marks, see above
	path := s.indexPath(id)

	// Mark 0's slot sits immediately after the header.
	putUint64At(t, path, indexHeaderSize, uint64(math.MaxInt64)+1)

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer f.Close()
	hdr, err := readIndexHeader(f)
	if err != nil {
		t.Fatalf("readIndexHeader: %v", err)
	}

	if _, err := markOffset(f, hdr, 0); !errors.Is(err, ErrCorruptIndex) {
		t.Fatalf("markOffset with a mark that overflows int64 = %v, want ErrCorruptIndex", err)
	}
}

func TestMarkOffsetRejectsOffsetPastDataLength(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "80000000-0000-0000-0000-000000000003"
	const content = "one\ntwo\nthree\n"
	completeUpload(t, s, id, content)
	path := s.indexPath(id)

	// A value that fits a signed 64-bit offset perfectly well, but is
	// larger than the data file this very index says it describes — not an
	// integer-overflow bug, a self-inconsistent index.
	putUint64At(t, path, indexHeaderSize, uint64(len(content))+1000)

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer f.Close()
	hdr, err := readIndexHeader(f)
	if err != nil {
		t.Fatalf("readIndexHeader: %v", err)
	}

	if _, err := markOffset(f, hdr, 0); !errors.Is(err, ErrCorruptIndex) {
		t.Fatalf("markOffset with an offset past the data length = %v, want ErrCorruptIndex", err)
	}
}

// TestWindowSurfacesCorruptIndex exercises the same corruption through the
// package's public entry point rather than the internal helpers above: a
// caller of Store.Window never calls readIndexHeader or markOffset directly,
// so this is what actually proves a damaged index file on disk becomes a
// declared refusal instead of a bad Seek somewhere underneath Window.
func TestWindowSurfacesCorruptIndex(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "80000000-0000-0000-0000-000000000004"
	completeUpload(t, s, id, buildNumberedLines(2500))

	putUint64At(t, s.indexPath(id), indexHeaderSize, uint64(math.MaxInt64)+1)

	if _, err := s.Window(id, 1, 10, 1<<20); !errors.Is(err, ErrCorruptIndex) {
		t.Fatalf("Window against a corrupt index = %v, want ErrCorruptIndex", err)
	}
}
