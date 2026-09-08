package gamefile

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// The arrangement the deployment actually runs: the transport's ceiling and
// this Store's MaxChunkBytes are the same number (app.go's WithMaxChunkBody
// call says why), so an over-sized chunk is stopped by the socket at exactly
// the byte the write cap stops at. The reader is the real
// http.MaxBytesReader for that reason — a hand-written stand-in would be
// free to answer the probe read the convenient way, and answering it the
// inconvenient way (0, error) is the whole case: read as "the caller had
// nothing more", it made a refused 9 MiB chunk a 200 OK that quietly kept 8.
//
// A nil ResponseWriter is what net/http itself allows here: the writer is
// only used for the server's own "request too large" bookkeeping, behind an
// interface assertion that a nil interface simply fails.
func TestAppendRefusesAChunkWhoseReaderFailsAtExactlyTheCap(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 1 << 20, MaxChunkBytes: 8}
	s := newTestStore(t, limits)
	const id = "aaaaaaaa-3333-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
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
	// The transport's own error travels out intact, so the HTTP layer that
	// created the MaxBytesReader can name it (CLAUDE.md rule 1: the handler's
	// switch has to be able to tell what happened), and the domain sentinel
	// says what happened to the bytes.
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

// failingReader gives up part-way through, the way a request body does when
// the connection drops or a read deadline expires mid-chunk.
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

// A chunk body that stops arriving is a named refusal, not an internal
// error: the rollback is right, but the error it comes back with has to be
// one the HTTP layer can map to "your upload was interrupted, send that
// chunk again" (CLAUDE.md rule 1). It reaches the client on a route whose
// body is megabytes, so it is a normal event, not a bug of ours.
func TestAppendNamesAChunkThatStoppedArrivingMidBody(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "aaaaaaaa-4444-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
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

// TestAppendConcurrentChunksForSameUploadDoNotCorrupt is defect 1: without a
// per-id lock, Append's Stat-then-Seek-then-Write is not atomic, so two
// goroutines racing Append for the same upload can both read the same
// "received so far" length, seek to the same offset, and write over each
// other. Run with -race — that verifies the fix's own synchronisation (the
// idLocks map in lock.go) is race-free; the corruption this test looks for
// is a race on file content, which the Go race detector has no visibility
// into on its own (nothing here is shared Go memory), which is why the
// assertions below inspect the actual bytes on disk.
//
// Two goroutines both call Append(id, 0, ...) — the same offset — each with
// its own several-megabyte chunk (well above copyBufferSize, so a single
// Append call makes many separate Read/Write round trips: the window a
// concurrent, unsynchronised Append for the same id can land writes inside
// of). Both chunks are the same size but a different repeated byte, so any
// interleaving between the two calls' writes is visible as a file that is
// not uniformly one byte value throughout.
//
// This models the real trigger honestly rather than assuming which chunk
// "should" win: two goroutines legitimately reach Append for the same id at
// the same offset when a browser retries a chunk that is still in flight,
// and Store cannot tell that case apart from two different chunks that
// simply arrived out of order. Either way, the outcome must match some
// valid sequential execution — one call's bytes fully on disk — never a mix
// of both.
func TestAppendConcurrentChunksForSameUploadDoNotCorrupt(t *testing.T) {
	limits := Limits{MaxFileBytes: 8 << 20, MaxDirBytes: 8 << 20, MaxChunkBytes: 8 << 20}
	s := newTestStore(t, limits)
	const id = "a0000000-0000-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
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

	// Both calls describe the same offset, so a correct Store treats this
	// exactly like a retried chunk racing the attempt still in flight:
	// whichever it serialises first writes, and the other sees offset 0
	// already received and returns the current length as a no-op — neither
	// is an error.
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

// TestAppendRefusesWhenSecondUploadWouldExceedDirBudget is defect 2:
// ErrStoreFull was checked only in Begin, so two uploads that both began on
// an empty directory could each grow all the way to MaxFileBytes
// independently — together well past MaxDirBytes. The budget has to be
// checked in Append, where the bytes actually arrive.
func TestAppendRefusesWhenSecondUploadWouldExceedDirBudget(t *testing.T) {
	limits := Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 20, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)

	const id1 = "b0000001-0000-0000-0000-000000000000"
	const id2 = "b0000002-0000-0000-0000-000000000000"
	// Both begin on an empty directory: MaxFileBytes alone would let either
	// one grow all the way to MaxDirBytes on its own.
	if err := s.Begin(id1); err != nil {
		t.Fatalf("Begin(id1): %v", err)
	}
	if err := s.Begin(id2); err != nil {
		t.Fatalf("Begin(id2): %v", err)
	}

	if _, err := s.Append(id1, 0, bytes.NewReader(bytes.Repeat([]byte("a"), 15))); err != nil {
		t.Fatalf("Append(id1): %v", err)
	}

	// The directory now holds 15 of its 20-byte MaxDirBytes. id2 growing by
	// 10 more bytes would push the directory to 25 — over budget — even
	// though id2's own MaxFileBytes has plenty of headroom left.
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

// pausingReader hands out data a few bytes at a time and, once it has handed
// out pauseAt bytes across previous calls, blocks on a channel before
// producing any more — letting a test inspect the data file's on-disk size
// from another goroutine while an Append call that is reading far more than
// MaxFileBytes is still in progress. It is only ever driven by the single
// goroutine running Append; paused/resume are the only fields the test
// goroutine touches, and channels are what make that safe.
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

// TestAppendNeverWritesPastMaxFileBytesBeforeEnforcingIt is defect 3: Append
// used to write a chunk in full and only afterwards compare the new length
// against MaxFileBytes, Truncating back if it was over. The excess bytes
// reached disk before the limit was applied — the write rule 12 says must
// not happen. This test drives a chunk that is much larger than
// MaxFileBytes through Append a few dozen bytes at a time and, if the file
// is ever observed to have grown past MaxFileBytes while Append is still
// running, fails with that observation. The fixed Append bounds the reader
// itself, so it never asks pausingReader for more than the remaining file
// budget and the pause point is never reached at all.
func TestAppendNeverWritesPastMaxFileBytesBeforeEnforcingIt(t *testing.T) {
	limits := Limits{MaxFileBytes: 100, MaxDirBytes: 1 << 20, MaxChunkBytes: 1 << 20}
	s := newTestStore(t, limits)
	const id = "d0000000-0000-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
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
		// The reader was never asked for more than the remaining file
		// budget, so it never reached the pause point at all.
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

// TestAppendAfterCompleteIsRefused is defect 4: Begin treats an id that
// already has data on disk — including a completed one — as a resumed
// upload, so Append would happily write into an upload Complete had already
// sealed. The checksum and line index Complete already returned would then
// describe bytes that no longer match the file, with nothing recording
// that. Append must refuse by a named sentinel once Complete has run, and
// Received/Window must keep answering for what Complete computed.
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

	w, err := s.Window(id, 1, 10, 1<<20)
	if err != nil {
		t.Fatalf("Window: %v", err)
	}
	if len(w.Lines) != 3 {
		t.Fatalf("Window after refused Append returned %d lines, want 3: %v", len(w.Lines), w.Lines)
	}
}

// idSet turns a slice into a set for order-independent comparison — UploadIDs
// promises no particular order, only which ids are present and how many
// times.
func idSet(ids []string) map[string]int {
	set := make(map[string]int, len(ids))
	for _, id := range ids {
		set[id]++
	}
	return set
}

// TestUploadIDsEmptyStore is the janitor's ordinary case: a fresh volume, or
// one that currently has nothing in flight, must report no ids at all
// rather than erroring on an empty directory.
func TestUploadIDsEmptyStore(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	ids, err := s.UploadIDs()
	if err != nil {
		t.Fatalf("UploadIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("UploadIDs on an empty store = %v, want none", ids)
	}
}

// TestUploadIDsIncludesAnInProgressUpload is what makes the orphan sweep
// work at all: an upload that has only been Begin'd (or partially Append'd,
// never Complete'd) is exactly the shape a crash between Store.Begin
// succeeding and the caller's own database row leaves behind, and it has to
// show up here for the janitor to ever find it.
func TestUploadIDsIncludesAnInProgressUpload(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "f0000001-0000-0000-0000-000000000000"
	if err := s.Begin(id); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := s.Append(id, 0, strings.NewReader("partial")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	ids, err := s.UploadIDs()
	if err != nil {
		t.Fatalf("UploadIDs: %v", err)
	}
	if got := idSet(ids); len(got) != 1 || got[id] != 1 {
		t.Fatalf("UploadIDs = %v, want exactly one entry for %s", ids, id)
	}
}

// TestUploadIDsCountsACompletedUploadOnce is the guarantee the task's own
// brief singles out: Complete leaves two files behind — the data file and
// its side index — and both belong to the same upload. A caller reconciling
// the volume against its own bookkeeping must see one id, not two, or a
// sweep built on this would double-count (or, worse, treat the index as a
// second orphan upload with no data of its own).
func TestUploadIDsCountsACompletedUploadOnce(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const id = "f0000002-0000-0000-0000-000000000000"
	completeUpload(t, s, id, "one\ntwo\n")

	// Both files really are on disk — this test is pointless otherwise.
	if _, err := os.Stat(s.dataPath(id)); err != nil {
		t.Fatalf("data file missing after Complete: %v", err)
	}
	if _, err := os.Stat(s.indexPath(id)); err != nil {
		t.Fatalf("index file missing after Complete: %v", err)
	}

	ids, err := s.UploadIDs()
	if err != nil {
		t.Fatalf("UploadIDs: %v", err)
	}
	if got := idSet(ids); len(got) != 1 || got[id] != 1 {
		t.Fatalf("UploadIDs = %v, want exactly one entry for %s (data and index are one upload)", ids, id)
	}
}

// TestUploadIDsForgetsAnAbortedUpload is Abort's own promise (it "removes an
// upload's data and any index it had, and forgets it") checked from
// UploadIDs' side: nothing on disk should still answer to the id once Abort
// has run, whether it was aborted mid-upload or after Complete sealed it.
func TestUploadIDsForgetsAnAbortedUpload(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const midUpload = "f0000003-0000-0000-0000-000000000000"
	const sealed = "f0000004-0000-0000-0000-000000000000"

	if err := s.Begin(midUpload); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	completeUpload(t, s, sealed, "a\nb\n")

	if err := s.Abort(midUpload); err != nil {
		t.Fatalf("Abort(midUpload): %v", err)
	}
	if err := s.Abort(sealed); err != nil {
		t.Fatalf("Abort(sealed): %v", err)
	}

	ids, err := s.UploadIDs()
	if err != nil {
		t.Fatalf("UploadIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("UploadIDs after aborting everything = %v, want none", ids)
	}
}

// TestUploadIDsIgnoresSideFilesAndAnythingElseOnTheVolume plants exactly the
// kind of file the orphan sweep must not misread as an upload: a lone index
// file with no data behind it (the tail of a crash between the two Complete
// writes, or simply a stray leftover), and a file whose name has nothing to
// do with this package's own naming convention at all. Neither must be
// reported as an upload id — an id that is not [0-9a-fA-F-] cannot even
// have been produced by validateUploadID, and a bare index file is a side
// file, not the thing UploadIDs promises to list one-per-upload.
func TestUploadIDsIgnoresSideFilesAndAnythingElseOnTheVolume(t *testing.T) {
	s := newTestStore(t, permissiveLimits())
	const real = "f0000005-0000-0000-0000-000000000000"
	completeUpload(t, s, real, "x\n")

	// A lone index file: remove the data half by hand, leaving only the
	// side file behind, exactly what "an id nothing about is confused with
	// data" is testing for.
	const orphanIndexOnly = "f0000006-0000-0000-0000-000000000000"
	completeUpload(t, s, orphanIndexOnly, "y\n")
	if err := os.Remove(s.dataPath(orphanIndexOnly)); err != nil {
		t.Fatalf("remove data file to leave a lone index: %v", err)
	}

	// Something that is not this package's naming convention at all —
	// unrelated to any upload id.
	if err := os.WriteFile(filepath.Join(s.dir, "README.txt"), []byte("not an upload"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	ids, err := s.UploadIDs()
	if err != nil {
		t.Fatalf("UploadIDs: %v", err)
	}
	if got := idSet(ids); len(got) != 1 || got[real] != 1 {
		t.Fatalf("UploadIDs = %v, want exactly the one real upload %s (no side file, no stray file)", ids, real)
	}
}
