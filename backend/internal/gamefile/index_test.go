package gamefile

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"testing"
)

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

// The first four bytes of an index are the only part of it a stray file of
// the wrong shape is guaranteed to fail on, and they were the one header
// check that answered with a bare fmt.Errorf while its three neighbours
// wrapped ErrCorruptIndex. provisioning catches that sentinel to tell an
// organiser "the index is damaged, upload the file again" and turns anything
// else into "internal error" — so damaging the first four bytes produced
// exactly the 500 the sentinel was introduced to stop (CLAUDE.md rule 1).
// These tests damage the header's fourth field and its length, the two the
// suite never touched.
func TestReadIndexHeaderRejectsAnUnrecognisedMagic(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "80000000-0000-0000-0000-000000000005"
	completeUpload(t, s, id, "one\ntwo\nthree\n")
	path := s.indexPath(id)

	// One flipped byte in the magic — a truncated write, a damaged disk, or
	// a file of another shape left under the id's name.
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open index for corruption: %v", err)
	}
	if _, err := f.WriteAt([]byte("X"), 0); err != nil {
		t.Fatalf("corrupt the magic: %v", err)
	}
	_ = f.Close()

	idx, err := os.Open(path)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idx.Close()

	if _, err := readIndexHeader(idx); !errors.Is(err, ErrCorruptIndex) {
		t.Fatalf("readIndexHeader with an unrecognised magic = %v, want ErrCorruptIndex", err)
	}
}

func TestReadIndexHeaderRejectsAFileTooShortToHoldAHeader(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "80000000-0000-0000-0000-000000000006"
	completeUpload(t, s, id, "one\ntwo\nthree\n")
	path := s.indexPath(id)

	// A write that stopped halfway: the file exists and is shorter than one
	// header. io.ReadFull answers io.ErrUnexpectedEOF, which is a damaged
	// index and not a fault of the caller's.
	if err := os.Truncate(path, indexHeaderSize-3); err != nil {
		t.Fatalf("truncate the index: %v", err)
	}

	idx, err := os.Open(path)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idx.Close()

	if _, err := readIndexHeader(idx); !errors.Is(err, ErrCorruptIndex) {
		t.Fatalf("readIndexHeader on a half-written file = %v, want ErrCorruptIndex", err)
	}
}

// The same class one level down: a header whose line count and mark count
// disagree, so Window computes a mark index the file does not have. Reached
// through Window, because that is the only caller and the only place the
// refusal has to arrive as a sentinel.
func TestWindowSurfacesAnIndexWhoseMarksDoNotCoverTheLinesItClaims(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "80000000-0000-0000-0000-000000000007"
	completeUpload(t, s, id, buildNumberedLines(2500)) // 3 marks

	// The file keeps its three marks; the header now claims a hundred
	// thousand lines. Asking for line 50,000 asks for mark 49.
	putUint64At(t, s.indexPath(id), headerTotalLinesOff, 100000)

	if _, err := s.Window(t.Context(), id, 50000, 10, 1<<20); !errors.Is(err, ErrCorruptIndex) {
		t.Fatalf("Window against an index with too few marks = %v, want ErrCorruptIndex", err)
	}
}

// writeIndex writes to a temporary file next to the index and renames it into
// place, so a process killed mid-write leaves either the old index or none.
// What it also leaves is the temporary file — and nothing ever looked for
// those: not Abort, not the orphan sweep (which asks Store.UploadIDs, and
// that only ever names data files). They sit in the directory for good, and
// every byte of them counts against MaxDirBytes, because usage() sums every
// regular file it finds. On a volume sized for multi-gigabyte dumps that is a
// budget draining with no way to get it back short of somebody logging in.
//
// The next write of the same index is the natural place to notice: it holds
// the same per-id lock, and it is about to create a temporary file for the
// very same path.
func TestWriteIndexClearsATemporaryFileAnInterruptedWriteLeftBehind(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "80000000-0000-0000-0000-000000000008"

	// What a process killed between CreateTemp and Rename leaves on disk.
	stray := s.indexPath(id) + ".tmp-2038411"
	if err := os.WriteFile(stray, []byte("half an index"), 0o600); err != nil {
		t.Fatalf("plant the leftover: %v", err)
	}

	completeUpload(t, s, id, "one\ntwo\nthree\n")

	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("the leftover temporary index is still on the volume (stat: %v)", err)
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

	if _, err := s.Window(t.Context(), id, 1, 10, 1<<20); !errors.Is(err, ErrCorruptIndex) {
		t.Fatalf("Window against a corrupt index = %v, want ErrCorruptIndex", err)
	}
}
