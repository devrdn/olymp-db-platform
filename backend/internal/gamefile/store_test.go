package gamefile

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// permissiveLimits are large enough that no test relying on them is
// exercising a bound — tests that care about a specific bound set it
// themselves.
func permissiveLimits() Limits {
	return Limits{
		MaxFileBytes:  1 << 20, // 1 MiB
		MaxDirBytes:   1 << 24, // 16 MiB
		MaxChunkBytes: 1 << 18, // 256 KiB
	}
}

func newTestStore(t *testing.T, limits Limits) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir(), limits)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestValidateUploadID(t *testing.T) {
	valid := []string{
		"a1b2c3d4-0000-1111-2222-333344445555",
		"deadbeef",
		"0",
		strings.Repeat("a", maxUploadIDLen),
	}
	for _, id := range valid {
		if err := validateUploadID(id); err != nil {
			t.Errorf("validateUploadID(%q) = %v, want nil", id, err)
		}
	}

	invalid := []string{
		"",
		"..",
		"../../etc/passwd",
		"a/b",
		"a\\b",
		"has space",
		"has.dot",
		"UPPER_SNAKE",
		strings.Repeat("a", maxUploadIDLen+1),
	}
	for _, id := range invalid {
		if err := validateUploadID(id); !errors.Is(err, ErrBadUploadID) {
			t.Errorf("validateUploadID(%q) = %v, want ErrBadUploadID", id, err)
		}
	}
}

func TestNewStoreRejectsBadLimits(t *testing.T) {
	dir := t.TempDir()
	cases := []Limits{
		{MaxFileBytes: 0, MaxDirBytes: 10, MaxChunkBytes: 10},
		{MaxFileBytes: 10, MaxDirBytes: 0, MaxChunkBytes: 10},
		{MaxFileBytes: 10, MaxDirBytes: 10, MaxChunkBytes: 0},
		{MaxFileBytes: -1, MaxDirBytes: 10, MaxChunkBytes: 10},
	}
	for _, limits := range cases {
		if _, err := NewStore(dir, limits); err == nil {
			t.Errorf("NewStore(%+v) = nil error, want one", limits)
		}
	}
}

func TestNewStoreCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "uploads")
	if _, err := NewStore(dir, permissiveLimits()); err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("NewStore did not create %s", dir)
	}
}

func TestBeginThenReceivedIsZero(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "aaaaaaaa-0000-0000-0000-000000000000"

	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	n, err := s.Received(id)
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if n != 0 {
		t.Fatalf("Received = %d, want 0", n)
	}
}

func TestBeginIsIdempotentAndDoesNotTruncate(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "bbbbbbbb-0000-0000-0000-000000000000"

	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("hello")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Calling Begin again — as a client re-announcing a resumed upload —
	// must not wipe out what was already received.
	if err := s.Begin(id); err != nil {
		t.Fatalf("second Begin: %v", err)
	}
	n, err := s.Received(id)
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if n != 5 {
		t.Fatalf("Received = %d, want 5 (Begin must not truncate)", n)
	}
}

func TestBeginRejectsBadID(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	if err := s.Begin("../escape"); !errors.Is(err, ErrBadUploadID) {
		t.Fatalf("Begin(\"../escape\") = %v, want ErrBadUploadID", err)
	}
}

func TestBeginRefusesWhenStoreFull(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 5, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)

	const id1 = "11111111-0000-0000-0000-000000000000"
	if err := s.Begin(id1); err != nil {
		t.Fatalf("Begin(id1): %v", err)
	}
	if _, err := s.Append(id1, 0, strings.NewReader("hello")); err != nil { // 5 bytes == MaxDirBytes
		t.Fatalf("Append(id1): %v", err)
	}

	const id2 = "22222222-0000-0000-0000-000000000000"
	if err := s.Begin(id2); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("Begin(id2) = %v, want ErrStoreFull", err)
	}
}

func TestBeginForExistingUploadIgnoresStoreFull(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 5, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)

	const id = "33333333-0000-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("hello")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// The directory is now at MaxDirBytes; resuming the same id must still
	// work because it reserves nothing new.
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin (resume) = %v, want nil", err)
	}
}

func TestAppendWritesAtOffset(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "44444444-0000-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	n, err := s.Append(id, 0, strings.NewReader("hello "))
	if err != nil {
		t.Fatalf("Append #1: %v", err)
	}
	if n != 6 {
		t.Fatalf("Append #1 length = %d, want 6", n)
	}

	n, err = s.Append(id, 6, strings.NewReader("world"))
	if err != nil {
		t.Fatalf("Append #2: %v", err)
	}
	if n != 11 {
		t.Fatalf("Append #2 length = %d, want 11", n)
	}

	f, err := s.Open(id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("content = %q, want %q", got, "hello world")
	}
}

func TestAppendRepeatOfLastChunkIsANoOp(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "55555555-0000-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("abc")); err != nil {
		t.Fatalf("Append #1: %v", err)
	}

	// The connection "dropped" after this chunk landed but before the
	// client saw the response, so it retries the same chunk at the same
	// offset. This must be silently accepted, not double-written.
	n, err := s.Append(id, 0, strings.NewReader("abc"))
	if err != nil {
		t.Fatalf("Append (retry) = %v, want nil error", err)
	}
	if n != 3 {
		t.Fatalf("Append (retry) length = %d, want 3", n)
	}

	f, err := s.Open(id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "abc" {
		t.Fatalf("content = %q, want %q (no duplication)", got, "abc")
	}
}

func TestAppendGapInOffsetIsRejected(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "66666666-0000-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("abc")); err != nil {
		t.Fatalf("Append #1: %v", err)
	}

	n, err := s.Append(id, 10, strings.NewReader("xyz"))
	if !errors.Is(err, ErrChunkOutOfOrder) {
		t.Fatalf("Append with a gap = %v, want ErrChunkOutOfOrder", err)
	}
	if n != 3 {
		t.Fatalf("Append with a gap returned length %d, want unchanged 3", n)
	}
}

func TestAppendUnknownIDIsNotFound(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	if _, err := s.Append("77777777-0000-0000-0000-000000000000", 0, strings.NewReader("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Append on unknown id = %v, want ErrNotFound", err)
	}
}

func TestAppendChunkTooLarge(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 1 << 20, MaxChunkBytes: 8}
	s := newTestStore(t, limits)
	const id = "88888888-0000-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	n, err := s.Append(id, 0, bytes.NewReader(bytes.Repeat([]byte("x"), 9)))
	if !errors.Is(err, ErrChunkTooLarge) {
		t.Fatalf("Append(9 bytes, limit 8) = %v, want ErrChunkTooLarge", err)
	}
	if n != 0 {
		t.Fatalf("Append too-large chunk returned length %d, want 0 (rolled back)", n)
	}
	got, err := s.Received(id)
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if got != 0 {
		t.Fatalf("Received after a rejected chunk = %d, want 0", got)
	}
}

// infiniteReader never returns io.EOF. Append must not attempt to drain it —
// if Append ever tried to read "everything" before deciding a chunk is too
// large, this test would hang instead of returning ErrChunkTooLarge.
type infiniteReader struct{}

func (infiniteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestAppendChunkTooLargeStopsAtTheLimit(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 30, MaxDirBytes: 1 << 30, MaxChunkBytes: 1024}
	s := newTestStore(t, limits)
	const id = "99999999-0000-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.Append(id, 0, infiniteReader{})
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrChunkTooLarge) {
			t.Fatalf("Append(infinite reader) = %v, want ErrChunkTooLarge", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Append did not return promptly against an infinite reader — it read past the chunk limit")
	}
}

func TestAppendFileExactlyAtLimitSucceeds(t *testing.T) {
	limits := Limits{MaxFileBytes: 10, MaxDirBytes: 1 << 20, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)
	const id = "aaaaaaaa-1111-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	n, err := s.Append(id, 0, bytes.NewReader(bytes.Repeat([]byte("y"), 10)))
	if err != nil {
		t.Fatalf("Append exactly at MaxFileBytes: %v", err)
	}
	if n != 10 {
		t.Fatalf("Append length = %d, want 10", n)
	}
}

func TestAppendFileOneByteOverLimitFails(t *testing.T) {
	limits := Limits{MaxFileBytes: 10, MaxDirBytes: 1 << 20, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)
	const id = "aaaaaaaa-2222-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	n, err := s.Append(id, 0, bytes.NewReader(bytes.Repeat([]byte("y"), 11)))
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("Append(11 bytes, MaxFileBytes 10) = %v, want ErrFileTooLarge", err)
	}
	if n != 0 {
		t.Fatalf("Append over MaxFileBytes returned length %d, want 0 (rolled back)", n)
	}
	got, err := s.Received(id)
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if got != 0 {
		t.Fatalf("Received after a rejected chunk = %d, want 0", got)
	}
}

func TestReceivedUnknownID(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	if _, err := s.Received("bbbbbbbb-1111-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Received on unknown id = %v, want ErrNotFound", err)
	}
}

func TestAbortMidUpload(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "cccccccc-0000-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("partial")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if err := s.Abort(id); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	if _, err := s.Received(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Received after Abort = %v, want ErrNotFound", err)
	}
	if entries, err := os.ReadDir(s.dir); err != nil {
		t.Fatalf("ReadDir: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("directory not empty after Abort: %v", entries)
	}
}

func TestAbortRemovesIndexToo(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "dddddddd-0000-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("a\nb\n")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := s.Complete(id, 4); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := s.Abort(id); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if entries, err := os.ReadDir(s.dir); err != nil {
		t.Fatalf("ReadDir: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("directory not empty after Abort of a completed upload: %v", entries)
	}
}

func TestAbortUnknownID(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	if err := s.Abort("eeeeeeee-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Abort on unknown id = %v, want ErrNotFound", err)
	}
}

func TestOpenUnknownID(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	if _, err := s.Open("ffffffff-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open on unknown id = %v, want ErrNotFound", err)
	}
}
