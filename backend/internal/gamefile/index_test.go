package gamefile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
