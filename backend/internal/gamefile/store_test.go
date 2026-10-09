package gamefile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// declaredForTest is the size an upload announces where the announcement
// is not under test. Tiny, because some tests run with a MaxDirBytes of a
// few bytes and Begin checks the declared size against it.
const declaredForTest = 5

func permissiveLimits() Limits {
	return Limits{
		MaxFileBytes:  1 << 20, // 1 MiB
		MaxDirBytes:   1 << 24, // 16 MiB
		MaxChunkBytes: 1 << 18, // 256 KiB
	}
}

func newTestStore(t testing.TB, limits Limits) *Store {
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

	if err := s.Begin(id, declaredForTest); err != nil {
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

	if err := s.Begin(id, declaredForTest); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("hello")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// A resumed upload's Begin must not wipe what was received.
	if err := s.Begin(id, declaredForTest); err != nil {
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
	if err := s.Begin("../escape", declaredForTest); !errors.Is(err, ErrBadUploadID) {
		t.Fatalf("Begin(\"../escape\") = %v, want ErrBadUploadID", err)
	}
}

func TestBeginRefusesWhenStoreFull(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 5, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)

	const id1 = "11111111-0000-0000-0000-000000000000"
	if err := s.Begin(id1, declaredForTest); err != nil {
		t.Fatalf("Begin(id1): %v", err)
	}
	if _, err := s.Append(id1, 0, strings.NewReader("hello")); err != nil { // 5 bytes == MaxDirBytes
		t.Fatalf("Append(id1): %v", err)
	}

	const id2 = "22222222-0000-0000-0000-000000000000"
	if err := s.Begin(id2, declaredForTest); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("Begin(id2) = %v, want ErrStoreFull", err)
	}
}

// Begin refuses an upload that cannot fit before any chunk is sent, not
// only when the directory is already full.
func TestBeginRefusesAnUploadLargerThanWhatIsLeftBeforeAnythingIsSent(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 100, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)

	const occupant = "c0000001-0000-0000-0000-000000000000"
	if err := s.Begin(occupant, 90); err != nil {
		t.Fatalf("Begin(occupant): %v", err)
	}
	if _, err := s.Append(occupant, 0, bytes.NewReader(bytes.Repeat([]byte("x"), 90))); err != nil {
		t.Fatalf("Append(occupant): %v", err)
	}

	// Ten bytes are left: some space, but not enough for this upload.
	const tooBig = "c0000002-0000-0000-0000-000000000000"
	if err := s.Begin(tooBig, 50); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("Begin(50 bytes with 10 left) = %v, want ErrStoreFull", err)
	}
	if _, err := s.Received(tooBig); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the refused upload left a file behind: Received = %v", err)
	}

	const fits = "c0000003-0000-0000-0000-000000000000"
	if err := s.Begin(fits, 10); err != nil {
		t.Fatalf("Begin(10 bytes with 10 left) = %v, want nil", err)
	}
}

func TestBeginRefusesADeclaredSizePastTheFileCeiling(t *testing.T) {
	s := newTestStore(t, Limits{MaxFileBytes: 10, MaxDirBytes: 1 << 20, MaxChunkBytes: 1 << 20})
	const id = "c0000004-0000-0000-0000-000000000000"
	if err := s.Begin(id, 11); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("Begin(11 bytes, MaxFileBytes 10) = %v, want ErrFileTooLarge", err)
	}
}

// Two uploads announced onto a volume that can hold one: the reservation
// refuses the second even though the first has written nothing yet.
func TestBeginCountsWhatAnotherUploadHasPromisedButNotYetSent(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 100, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)

	const first = "c0000005-0000-0000-0000-000000000000"
	if err := s.Begin(first, 80); err != nil {
		t.Fatalf("Begin(first): %v", err)
	}

	const second = "c0000006-0000-0000-0000-000000000000"
	if err := s.Begin(second, 80); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("Begin(second) = %v, want ErrStoreFull — the first upload's 80 bytes are spoken for", err)
	}

	// Aborting the first releases its reservation.
	if err := s.Abort(first); err != nil {
		t.Fatalf("Abort(first): %v", err)
	}
	if err := s.Begin(second, 80); err != nil {
		t.Fatalf("Begin(second) after the first was aborted = %v, want nil", err)
	}
}

func TestBeginForExistingUploadIgnoresStoreFull(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 5, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)

	const id = "33333333-0000-0000-0000-000000000000"
	if err := s.Begin(id, declaredForTest); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("hello")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// The directory is full; a resume reserves nothing new.
	if err := s.Begin(id, declaredForTest); err != nil {
		t.Fatalf("Begin (resume) = %v, want nil", err)
	}
}

func TestAppendWritesAtOffset(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "44444444-0000-0000-0000-000000000000"
	if err := s.Begin(id, declaredForTest); err != nil {
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
	if err := s.Begin(id, declaredForTest); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("abc")); err != nil {
		t.Fatalf("Append #1: %v", err)
	}

	// A retry of the same chunk after a lost response is a no-op.
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
	if err := s.Begin(id, declaredForTest); err != nil {
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
	if err := s.Begin(id, declaredForTest); err != nil {
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

// infiniteReader never returns io.EOF, so the test hangs if Append ever
// reads a whole chunk before deciding it is too large.
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
	if err := s.Begin(id, declaredForTest); err != nil {
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

// The deployed arrangement: the transport's body limit equals MaxChunkBytes,
// so an oversized chunk is stopped by http.MaxBytesReader at the same byte
// as the write cap and the probe read returns (0, error). Treating that as
// EOF once accepted a truncated chunk. net/http allows a nil ResponseWriter
// here.
func TestAppendRefusesAChunkWhoseReaderFailsAtExactlyTheCap(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 1 << 20, MaxChunkBytes: 8}
	s := newTestStore(t, limits)
	const id = "aaaaaaaa-3333-0000-0000-000000000000"
	if err := s.Begin(id, declaredForTest); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	body := http.MaxBytesReader(nil, io.NopCloser(strings.NewReader("123456789")), 8)
	n, err := s.Append(id, 0, body)
	if err == nil {
		t.Fatalf("Append(9 bytes past an 8-byte transport cap) = (%d, nil), want a refusal", n)
	}
	if n != 0 {
		t.Fatalf("Append returned length %d, want 0 (rolled back)", n)
	}
	// The transport's error stays wrapped so the HTTP layer can recognise it
	// (CLAUDE.md rule 1).
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("Append error = %v, does not carry *http.MaxBytesError", err)
	}
	if !errors.Is(err, ErrChunkIncomplete) {
		t.Fatalf("Append error = %v, want ErrChunkIncomplete", err)
	}

	got, err := s.Received(id)
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if got != 0 {
		t.Fatalf("Received after a refused chunk = %d, want 0 — bytes were kept from a chunk that was refused", got)
	}
}

// failingReader fails part-way, like a request body whose connection drops.
type failingReader struct {
	remaining int
	err       error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, r.err
	}
	n := min(len(p), r.remaining)
	for i := range p[:n] {
		p[i] = 'z'
	}
	r.remaining -= n
	return n, nil
}

// An interrupted chunk body is ErrChunkIncomplete, which the client can
// retry, not an internal error (CLAUDE.md rule 1).
func TestAppendNamesAChunkThatStoppedArrivingMidBody(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "aaaaaaaa-4444-0000-0000-000000000000"
	if err := s.Begin(id, declaredForTest); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("already here")); err != nil {
		t.Fatalf("first Append: %v", err)
	}

	broken := &failingReader{remaining: 4096, err: os.ErrDeadlineExceeded}
	n, err := s.Append(id, 12, broken)
	if !errors.Is(err, ErrChunkIncomplete) {
		t.Fatalf("Append(interrupted body) = %v, want ErrChunkIncomplete", err)
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Append error = %v, dropped the cause the deployment has to diagnose from", err)
	}
	if n != 12 {
		t.Fatalf("Append returned length %d, want 12 (rolled back to the offset it started at)", n)
	}
	got, err := s.Received(id)
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if got != 12 {
		t.Fatalf("Received after an interrupted chunk = %d, want 12", got)
	}
}

func TestAppendFileExactlyAtLimitSucceeds(t *testing.T) {
	limits := Limits{MaxFileBytes: 10, MaxDirBytes: 1 << 20, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)
	const id = "aaaaaaaa-1111-0000-0000-000000000000"
	if err := s.Begin(id, declaredForTest); err != nil {
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
	if err := s.Begin(id, declaredForTest); err != nil {
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
	if err := s.Begin(id, declaredForTest); err != nil {
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
	if err := s.Begin(id, declaredForTest); err != nil {
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

// Abort is the last code path that names an id, so it must also remove a
// temporary index a killed Complete left behind.
func TestAbortRemovesATemporaryIndexAnInterruptedCompleteLeftBehind(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "dddddddd-0000-0000-0000-000000000001"
	if err := s.Begin(id, declaredForTest); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("a\nb\n")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// What os.CreateTemp leaves when the process dies before Rename.
	if err := os.WriteFile(s.indexPath(id)+".tmp-1174093", []byte("half an index"), 0o600); err != nil {
		t.Fatalf("plant the leftover: %v", err)
	}

	if err := s.Abort(id); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	if entries, err := os.ReadDir(s.dir); err != nil {
		t.Fatalf("ReadDir: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("directory not empty after Abort — the leftover is unreachable for good now: %v", entries)
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

// Two Appends at the same offset (a retried chunk racing the original)
// must leave one call's bytes on disk, never a mix. Chunks are several
// times copyBufferSize and use different bytes, so an interleaving shows in
// the file content, which the race detector cannot see.
func TestAppendConcurrentChunksForSameUploadDoNotCorrupt(t *testing.T) {
	limits := Limits{MaxFileBytes: 8 << 20, MaxDirBytes: 8 << 20, MaxChunkBytes: 8 << 20}
	s := newTestStore(t, limits)
	const id = "a0000000-0000-0000-0000-000000000000"
	if err := s.Begin(id, declaredForTest); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	const chunkSize = 2 << 20 // several multiples of copyBufferSize (64 KiB)
	chunkA := bytes.Repeat([]byte{'A'}, chunkSize)
	chunkB := bytes.Repeat([]byte{'B'}, chunkSize)

	start := make(chan struct{})
	var wg sync.WaitGroup
	var nA, nB int64
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		nA, errA = s.Append(id, 0, bytes.NewReader(chunkA))
	}()
	go func() {
		defer wg.Done()
		<-start
		nB, errB = s.Append(id, 0, bytes.NewReader(chunkB))
	}()
	close(start) // release both at once to maximise the chance they overlap
	wg.Wait()

	// One call writes; the other sees offset 0 already received and is a
	// no-op. Neither is an error.
	if errA != nil {
		t.Fatalf("Append A: %v", errA)
	}
	if errB != nil {
		t.Fatalf("Append B: %v", errB)
	}
	if nA != nB {
		t.Fatalf("the two calls disagree about the resulting length: A=%d B=%d", nA, nB)
	}

	got, err := s.Received(id)
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if got != int64(chunkSize) {
		t.Fatalf("Received = %d, want %d (exactly one chunk landed, not a mix or a partial overwrite)", got, chunkSize)
	}
	if nA != got {
		t.Fatalf("Append reported length %d but the file actually holds %d bytes", nA, got)
	}

	f, err := s.Open(id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(content) != chunkSize {
		t.Fatalf("file size = %d, want %d", len(content), chunkSize)
	}
	first := content[0]
	if first != 'A' && first != 'B' {
		t.Fatalf("file's first byte is %q, want 'A' or 'B'", first)
	}
	for i, b := range content {
		if b != first {
			t.Fatalf("byte %d is %q, want %q — the two goroutines' writes were interleaved instead of one cleanly winning", i, b, first)
		}
	}
}

// The directory budget is checked in Append, not only in Begin: two
// uploads begun on an empty directory must not together exceed it.
func TestAppendRefusesWhenSecondUploadWouldExceedDirBudget(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 20, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)

	const id1 = "b0000001-0000-0000-0000-000000000000"
	const id2 = "b0000002-0000-0000-0000-000000000000"
	if err := s.Begin(id1, declaredForTest); err != nil {
		t.Fatalf("Begin(id1): %v", err)
	}
	if err := s.Begin(id2, declaredForTest); err != nil {
		t.Fatalf("Begin(id2): %v", err)
	}

	if _, err := s.Append(id1, 0, bytes.NewReader(bytes.Repeat([]byte("a"), 15))); err != nil {
		t.Fatalf("Append(id1): %v", err)
	}

	// The directory holds 15 of 20 bytes; 10 more for id2 would exceed it
	// although id2's own file budget allows them.
	n, err := s.Append(id2, 0, bytes.NewReader(bytes.Repeat([]byte("b"), 10)))
	if !errors.Is(err, ErrStoreFull) {
		t.Fatalf("Append(id2) = %v, want ErrStoreFull", err)
	}
	if n != 0 {
		t.Fatalf("Append(id2) returned length %d, want 0 (rolled back, nothing written)", n)
	}

	got, err := s.Received(id2)
	if err != nil {
		t.Fatalf("Received(id2): %v", err)
	}
	if got != 0 {
		t.Fatalf("Received(id2) = %d, want 0 — the over-budget chunk must not have grown the file", got)
	}
}

// pausingReader hands out a few bytes at a time and blocks after pauseAt
// bytes, so the test can check the file size while Append is still
// running. Only paused and resume are shared with the test goroutine.
type pausingReader struct {
	data       []byte
	step       int
	pauseAt    int
	handedOut  int
	firedPause bool
	paused     chan struct{}
	resume     chan struct{}
}

func (r *pausingReader) Read(p []byte) (int, error) {
	if r.handedOut >= r.pauseAt && !r.firedPause {
		r.firedPause = true
		close(r.paused)
		<-r.resume
	}
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := r.step
	if n > len(r.data) {
		n = len(r.data)
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	r.handedOut += n
	return n, nil
}

// Append bounds the reader before writing, so an oversized chunk never
// puts bytes past MaxFileBytes on disk, even briefly (CLAUDE.md rule 12).
// The test fails if it ever sees the file grow past the limit mid-call.
func TestAppendNeverWritesPastMaxFileBytesBeforeEnforcingIt(t *testing.T) {
	limits := Limits{MaxFileBytes: 100, MaxDirBytes: 1 << 20, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)
	const id = "d0000000-0000-0000-0000-000000000000"
	if err := s.Begin(id, declaredForTest); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	reader := &pausingReader{
		data:    bytes.Repeat([]byte("x"), 600),
		step:    32,
		pauseAt: 200, // well past MaxFileBytes(100); only reachable if excess bytes were already written
		paused:  make(chan struct{}),
		resume:  make(chan struct{}),
	}

	appendDone := make(chan struct{})
	var gotN int64
	var gotErr error
	go func() {
		gotN, gotErr = s.Append(id, 0, reader)
		close(appendDone)
	}()

	select {
	case <-reader.paused:
		info, statErr := os.Stat(s.dataPath(id))
		if statErr != nil {
			t.Fatalf("stat mid-append: %v", statErr)
		}
		grewTo := info.Size()
		close(reader.resume)
		<-appendDone
		t.Fatalf("data file grew to %d bytes while Append was still reading a chunk — MaxFileBytes is %d, so those bytes reached disk before the limit was enforced (rule 12: bound the reader, do not write then Truncate)", grewTo, limits.MaxFileBytes)
	case <-appendDone:
	}

	if !errors.Is(gotErr, ErrFileTooLarge) {
		t.Fatalf("Append = %v, want ErrFileTooLarge", gotErr)
	}
	if gotN != 0 {
		t.Fatalf("Append length = %d, want 0", gotN)
	}
	got, err := s.Received(id)
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if got != 0 {
		t.Fatalf("Received = %d, want 0", got)
	}
}

// An Append after Complete is refused with ErrUploadSealed, and Received
// and Window keep answering for what Complete computed.
func TestAppendAfterCompleteIsRefused(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "e0000000-0000-0000-0000-000000000000"
	const content = "one\ntwo\nthree\n"
	sum := completeUpload(t, s, id, content)

	n, err := s.Append(id, sum.Bytes, strings.NewReader("more"))
	if !errors.Is(err, ErrUploadSealed) {
		t.Fatalf("Append after Complete = %v, want ErrUploadSealed", err)
	}
	if n != sum.Bytes {
		t.Fatalf("Append after Complete returned length %d, want %d (unchanged)", n, sum.Bytes)
	}

	got, err := s.Received(id)
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if got != sum.Bytes {
		t.Fatalf("Received after refused Append = %d, want %d", got, sum.Bytes)
	}

	w, err := s.Window(t.Context(), id, 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	if len(w.Lines) != 3 {
		t.Fatalf("Window after refused Append returned %d lines, want 3: %v", len(w.Lines), w.Lines)
	}
}

// anyAge is a cut-off later than any file a test has written.
func anyAge() time.Time { return time.Now().Add(time.Hour) }

func idSet(ids []string) map[string]int {
	set := make(map[string]int, len(ids))
	for _, id := range ids {
		set[id]++
	}
	return set
}

func TestUploadIDsEmptyStore(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	ids, err := s.UploadIDs(anyAge())
	if err != nil {
		t.Fatalf("UploadIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("UploadIDs on an empty store = %v, want none", ids)
	}
}

// An upload begun but never completed is what a crash before the caller's
// row leaves, so the orphan sweep must see it.
func TestUploadIDsIncludesAnInProgressUpload(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "f0000001-0000-0000-0000-000000000000"
	if err := s.Begin(id, declaredForTest); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("partial")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	ids, err := s.UploadIDs(anyAge())
	if err != nil {
		t.Fatalf("UploadIDs: %v", err)
	}
	if got := idSet(ids); len(got) != 1 || got[id] != 1 {
		t.Fatalf("UploadIDs = %v, want exactly one entry for %s", ids, id)
	}
}

// A completed upload has a data file and an index, and must be listed once.
func TestUploadIDsCountsACompletedUploadOnce(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "f0000002-0000-0000-0000-000000000000"
	completeUpload(t, s, id, "one\ntwo\n")

	if _, err := os.Stat(s.dataPath(id)); err != nil {
		t.Fatalf("data file missing after Complete: %v", err)
	}
	if _, err := os.Stat(s.indexPath(id)); err != nil {
		t.Fatalf("index file missing after Complete: %v", err)
	}

	ids, err := s.UploadIDs(anyAge())
	if err != nil {
		t.Fatalf("UploadIDs: %v", err)
	}
	if got := idSet(ids); len(got) != 1 || got[id] != 1 {
		t.Fatalf("UploadIDs = %v, want exactly one entry for %s (data and index are one upload)", ids, id)
	}
}

func TestUploadIDsForgetsAnAbortedUpload(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const midUpload = "f0000003-0000-0000-0000-000000000000"
	const sealed = "f0000004-0000-0000-0000-000000000000"

	if err := s.Begin(midUpload, declaredForTest); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	completeUpload(t, s, sealed, "a\nb\n")

	if err := s.Abort(midUpload); err != nil {
		t.Fatalf("Abort(midUpload): %v", err)
	}
	if err := s.Abort(sealed); err != nil {
		t.Fatalf("Abort(sealed): %v", err)
	}

	ids, err := s.UploadIDs(anyAge())
	if err != nil {
		t.Fatalf("UploadIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("UploadIDs after aborting everything = %v, want none", ids)
	}
}

// Neither a lone index file nor a file outside the naming convention is an
// upload id.
func TestUploadIDsIgnoresSideFilesAndAnythingElseOnTheVolume(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const real = "f0000005-0000-0000-0000-000000000000"
	completeUpload(t, s, real, "x\n")

	// A lone index file: remove the data half by hand.
	const orphanIndexOnly = "f0000006-0000-0000-0000-000000000000"
	completeUpload(t, s, orphanIndexOnly, "y\n")
	if err := os.Remove(s.dataPath(orphanIndexOnly)); err != nil {
		t.Fatalf("remove data file to leave a lone index: %v", err)
	}

	if err := os.WriteFile(filepath.Join(s.dir, "README.txt"), []byte("not an upload"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	ids, err := s.UploadIDs(anyAge())
	if err != nil {
		t.Fatalf("UploadIDs: %v", err)
	}
	if got := idSet(ids); len(got) != 1 || got[real] != 1 {
		t.Fatalf("UploadIDs = %v, want exactly the one real upload %s (no side file, no stray file)", ids, real)
	}
}

// A file created at the cut-off is not listed: the caller writes the file
// before its row, and the sweep must not delete an upload whose row is still
// being inserted (provisioning.orphanFileGrace).
func TestUploadIDsLeavesOutAFileYoungerThanTheCutOff(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	fresh, old := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "11111111-2222-3333-4444-555555555555"
	for _, id := range []string{fresh, old} {
		if err := s.Begin(id, declaredForTest); err != nil {
			t.Fatalf("Begin(%s): %v", id, err)
		}
	}

	// Only one of the two is older than the cut-off.
	cutOff := time.Now().Add(-time.Minute)
	when := cutOff.Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(s.dir, old+dataSuffix), when, when); err != nil {
		t.Fatalf("age the older upload: %v", err)
	}

	ids, err := s.UploadIDs(cutOff)
	if err != nil {
		t.Fatalf("UploadIDs: %v", err)
	}
	if got := idSet(ids); len(got) != 1 || got[old] != 1 {
		t.Fatalf("UploadIDs = %v, want only %s — the file written moments ago is not the caller's to act on yet", ids, old)
	}
}

// dirWalks reports when the directory was last walked, so a test can assert
// that a chunk did not walk it.
func (s *Store) lastDirWalk() time.Time {
	s.dirMu.Lock()
	defer s.dirMu.Unlock()
	return s.dirMeasuredAt
}

// Appending a chunk reuses the cached directory size instead of walking
// the directory (dirBytesMaxAge).
func TestAppendDoesNotWalkTheDirectoryForEveryChunk(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 8 << 20, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)

	const id = "c0000001-0000-0000-0000-000000000000"
	if err := s.Begin(id, 300); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	walkedAt := s.lastDirWalk()
	if walkedAt.IsZero() {
		t.Fatal("Begin did not measure the directory at all")
	}

	var offset int64
	for chunk := range 5 {
		n, err := s.Append(id, offset, bytes.NewReader(bytes.Repeat([]byte("x"), 60)))
		if err != nil {
			t.Fatalf("Append %d: %v", chunk, err)
		}
		offset = n
	}
	if s.lastDirWalk() != walkedAt {
		t.Fatal("a chunk walked the directory again although the budget was nowhere near binding")
	}
	if offset != 300 {
		t.Fatalf("wrote %d bytes in all, want 300", offset)
	}
}

// A refusal never rests on the cached size: when the budget is close,
// Append measures again. Here bytes vanish behind the Store's back and a
// chunk the stale figure would refuse is accepted.
func TestAppendMeasuresForRealOnceTheDirectoryBudgetCouldBind(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 200, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)

	const filler = "c0000002-0000-0000-0000-000000000000"
	const id = "c0000003-0000-0000-0000-000000000000"
	if err := s.Begin(filler, 100); err != nil {
		t.Fatalf("Begin(filler): %v", err)
	}
	if _, err := s.Append(filler, 0, bytes.NewReader(bytes.Repeat([]byte("f"), 100))); err != nil {
		t.Fatalf("Append(filler): %v", err)
	}
	if err := s.Begin(id, 100); err != nil {
		t.Fatalf("Begin(id): %v", err)
	}

	// The filler disappears without the Store knowing; the cached figure
	// now overstates the directory by 100 bytes.
	if err := os.Remove(s.dataPath(filler)); err != nil {
		t.Fatalf("removing the filler: %v", err)
	}

	// 150 bytes fits the real free space (200) but not the cached one (100).
	if _, err := s.Append(id, 0, bytes.NewReader(bytes.Repeat([]byte("x"), 150))); err != nil {
		t.Fatalf("Append: %v — the refusal was decided on a remembered figure", err)
	}
	got, err := s.Received(id)
	if err != nil {
		t.Fatalf("Received: %v", err)
	}
	if got != 150 {
		t.Fatalf("Received = %d, want 150", got)
	}
}

// What a chunk costs on an empty and a busy directory. Run with
// `go test -bench AppendChunk -benchmem ./internal/gamefile/`.
func BenchmarkAppendChunkEmptyDirectory(b *testing.B)   { benchmarkAppendChunk(b, 0) }
func BenchmarkAppendChunkBusyDirectory(b *testing.B)    { benchmarkAppendChunk(b, 500) }
func BenchmarkAppendChunkCrowdedDirectory(b *testing.B) { benchmarkAppendChunk(b, 5000) }

func benchmarkAppendChunk(b *testing.B, neighbours int) {
	const chunk = 8 << 20
	limits := Limits{MaxFileBytes: 1 << 30, MaxDirBytes: 1 << 45, MaxChunkBytes: chunk}
	s := newTestStore(b, limits)

	for i := range neighbours {
		name := filepath.Join(s.dir, fmt.Sprintf("%08x-0000-0000-0000-000000000000%s", i, dataSuffix))
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			b.Fatalf("writing a neighbour: %v", err)
		}
	}

	const id = "d0000001-0000-0000-0000-000000000000"
	if err := s.Begin(id, 1<<30); err != nil {
		b.Fatalf("Begin: %v", err)
	}
	payload := bytes.Repeat([]byte("x"), chunk)

	b.SetBytes(chunk)
	b.ReportAllocs()
	for b.Loop() {
		// Each iteration rewrites the first chunk; the truncation outside the
		// timer is invisible to the cache, which is fine with a terabyte budget.
		b.StopTimer()
		if err := os.Truncate(s.dataPath(id), 0); err != nil {
			b.Fatalf("truncate: %v", err)
		}
		b.StartTimer()

		if _, err := s.Append(id, 0, bytes.NewReader(payload)); err != nil {
			b.Fatalf("Append: %v", err)
		}
	}
}

// Store.Complete.

func completeUpload(t *testing.T, s *Store, id, content string) Summary {
	t.Helper()
	if err := s.Begin(id, declaredForTest); err != nil {
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
	if err := s.Begin(id, declaredForTest); err != nil {
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
	if err := s.Begin(id, declaredForTest); err != nil {
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
	// The index is read back from disk by a later call, not kept in memory.
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
