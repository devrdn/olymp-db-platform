package filestore_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/filestore"
)

// newStore opens a store on dir, failing the test rather than returning an
// error: every test here is about what a working store does, and a store that
// could not be opened has nothing to say about any of it.
func newStore(t *testing.T, dir string) *filestore.Store {
	t.Helper()

	store, err := filestore.New(dir)
	if err != nil {
		t.Fatalf("New(%q) = %v", dir, err)
	}
	return store
}

func TestPutThenGetReturnsTheSameBytes(t *testing.T) {
	store := newStore(t, t.TempDir())

	if err := store.Put(t.Context(), "cover-1600.jpg", "image/jpeg", []byte("not really a jpeg")); err != nil {
		t.Fatalf("Put() = %v", err)
	}

	body, contentType, err := store.Get(t.Context(), "cover-1600.jpg")
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	if string(body) != "not really a jpeg" || contentType != "image/jpeg" {
		t.Errorf("Get() = %q, %q", body, contentType)
	}
}

func TestGettingWhatIsNotThereIsNotFound(t *testing.T) {
	store := newStore(t, t.TempDir())
	if _, _, err := store.Get(t.Context(), "absent.jpg"); !errors.Is(err, filestore.ErrNotFound) {
		t.Errorf("Get() = %v, want ErrNotFound", err)
	}
}

// A key is a name, never a path: the caller's hash is data, and data that can
// walk out of the directory it is written into is how an upload becomes a
// write to /etc.
func TestAKeyCannotClimbOutOfTheDirectory(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)

	for _, key := range []string{"../escape.jpg", "a/../../escape.jpg", "/absolute.jpg", "nested/deep.jpg"} {
		if err := store.Put(t.Context(), key, "image/jpeg", []byte("x")); err == nil {
			t.Errorf("Put(%q) was accepted", key)
		}
	}
	if entries, _ := os.ReadDir(filepath.Dir(root)); len(entries) == 0 {
		t.Fatal("the test's own parent directory vanished, which is its own kind of news")
	}
}

// A half-written file is worse than no file: a reader would serve a truncated
// picture forever, because the name is a content hash and nothing ever
// rewrites it.
func TestPutIsAllOrNothing(t *testing.T) {
	root := t.TempDir()
	store := newStore(t, root)

	if err := store.Put(t.Context(), "whole.jpg", "image/jpeg", bytes.Repeat([]byte("x"), 1<<20)); err != nil {
		t.Fatalf("Put() = %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") || strings.HasPrefix(e.Name(), ".") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

// The refusal a key that is not a name earns is a declared sentinel, so a
// handler's fail switch can name it instead of reporting the caller's own
// mistake as an internal error (CLAUDE.md, security rule 1). Get and Delete
// refuse the same keys Put does: a read is as much a way out of the directory
// as a write.
func TestARefusedKeyIsErrBadKeyOnEveryOperation(t *testing.T) {
	store := newStore(t, t.TempDir())

	for _, key := range []string{"", ".", "..", "../escape.jpg", "a/../../escape.jpg", "/absolute.jpg", "nested/deep.jpg", "a\x00b.jpg", strings.Repeat("a", 300) + ".jpg"} {
		if err := store.Put(t.Context(), key, "image/jpeg", []byte("x")); !errors.Is(err, filestore.ErrBadKey) {
			t.Errorf("Put(%q) = %v, want ErrBadKey", key, err)
		}
		if _, _, err := store.Get(t.Context(), key); !errors.Is(err, filestore.ErrBadKey) {
			t.Errorf("Get(%q) = %v, want ErrBadKey", key, err)
		}
		if err := store.Delete(t.Context(), key); !errors.Is(err, filestore.ErrBadKey) {
			t.Errorf("Delete(%q) = %v, want ErrBadKey", key, err)
		}
	}
}

// The content type comes from the extension rather than from a second file
// kept beside the picture: one file on disk is one thing to write, one thing
// to back up, and one thing that cannot disagree with itself.
func TestTheContentTypeComesFromTheExtension(t *testing.T) {
	store := newStore(t, t.TempDir())

	for key, want := range map[string]string{
		"a.jpg":  "image/jpeg",
		"b.jpeg": "image/jpeg",
		"c.png":  "image/png",
		"d.webp": "image/webp",
	} {
		if err := store.Put(t.Context(), key, "image/jpeg", []byte("x")); err != nil {
			t.Fatalf("Put(%q) = %v", key, err)
		}
		if _, got, err := store.Get(t.Context(), key); err != nil || got != want {
			t.Errorf("Get(%q) = %q, %v, want %q", key, got, err, want)
		}
	}
}

// An extension the store does not serve is refused on the way in. Whatever a
// later reader would guess for it — text/html above all — is served from the
// same origin as the application, so the set of types this directory can ever
// hand back is decided here, once, rather than by mime.TypeByExtension on a
// machine whose /etc/mime.types nobody has read.
func TestAnUnservableExtensionIsRefused(t *testing.T) {
	store := newStore(t, t.TempDir())

	for _, key := range []string{"cover", "cover.html", "cover.svg", "cover.jpg.html"} {
		if err := store.Put(t.Context(), key, "image/jpeg", []byte("x")); !errors.Is(err, filestore.ErrBadKey) {
			t.Errorf("Put(%q) was accepted, want ErrBadKey", key)
		}
	}
}

func TestDeleteRemovesTheFile(t *testing.T) {
	store := newStore(t, t.TempDir())

	if err := store.Put(t.Context(), "gone.jpg", "image/jpeg", []byte("x")); err != nil {
		t.Fatalf("Put() = %v", err)
	}
	if err := store.Delete(t.Context(), "gone.jpg"); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	if _, _, err := store.Get(t.Context(), "gone.jpg"); !errors.Is(err, filestore.ErrNotFound) {
		t.Errorf("Get() after Delete() = %v, want ErrNotFound", err)
	}
}

// Deleting what is already gone is not an error: the caller — a sweep for
// files no contest refers to any more — would otherwise have to distinguish
// "nothing to do" from "the disk refused", and it has no use for the
// difference.
func TestDeletingWhatIsNotThereSucceeds(t *testing.T) {
	store := newStore(t, t.TempDir())

	if err := store.Delete(t.Context(), "never-existed.jpg"); err != nil {
		t.Errorf("Delete() = %v, want nil", err)
	}
}

// Overwriting is not something the covers themselves ever do — the name is a
// content hash — but two organisers uploading the same picture at the same
// moment do write the same name twice, and neither may see a torn file.
func TestPutOverAnExistingKeyReplacesItWhole(t *testing.T) {
	store := newStore(t, t.TempDir())

	if err := store.Put(t.Context(), "same.jpg", "image/jpeg", []byte("first")); err != nil {
		t.Fatalf("Put() = %v", err)
	}
	if err := store.Put(t.Context(), "same.jpg", "image/jpeg", []byte("second")); err != nil {
		t.Fatalf("Put() = %v", err)
	}

	body, _, err := store.Get(t.Context(), "same.jpg")
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	if string(body) != "second" {
		t.Errorf("Get() = %q, want %q", body, "second")
	}
}

// The directory is created at start-up, so an operator who mounted a volume
// but never made the directory inside it gets a working service rather than a
// failure on the first upload.
func TestNewCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "covers", "nested")

	store := newStore(t, dir)

	if err := store.Ping(t.Context()); err != nil {
		t.Fatalf("Ping() = %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Errorf("Stat(%q) = %v, %v", dir, info, err)
	}
}

// A directory nobody can write to must refuse at start-up, not on the day of
// the olympiad: Ping writes and removes a probe file rather than merely
// stat-ing the directory, because a read-only mount stats perfectly well.
func TestPingFailsOnADirectoryItCannotWriteTo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a 0500 directory regardless of its mode")
	}
	dir := t.TempDir()
	store := newStore(t, dir)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod() = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := store.Ping(t.Context()); err == nil {
		t.Error("Ping() = nil on a directory that cannot be written to")
	}
}

// Ping leaves nothing behind: it runs on every readiness probe, and a probe
// file per probe would fill the volume it is checking.
func TestPingLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	store := newStore(t, dir)

	for range 3 {
		if err := store.Ping(t.Context()); err != nil {
			t.Fatalf("Ping() = %v", err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Ping() left %d entries behind: %v", len(entries), entries)
	}
}

// Every body that reaches storage carries an explicit bound (CLAUDE.md,
// security rule 2). The covers this directory holds are re-encoded by the
// service itself and run to a few hundred kilobytes; anything near the
// ceiling is a caller that has lost track of what it is writing.
func TestPutRefusesABodyBeyondTheCeiling(t *testing.T) {
	store := newStore(t, t.TempDir())

	body := bytes.Repeat([]byte("x"), filestore.MaxFileBytes+1)
	if err := store.Put(t.Context(), "huge.jpg", "image/jpeg", body); !errors.Is(err, filestore.ErrTooLarge) {
		t.Errorf("Put() = %v, want ErrTooLarge", err)
	}
}

// The same ceiling on the way out, and for a reason Put's own check does not
// cover: the directory is a volume an operator can write to by hand, and Get
// reads a whole file into the memory of the process serving the olympiad.
// The size is read from the file's metadata, before the bytes are allocated.
func TestGetRefusesAFileBeyondTheCeiling(t *testing.T) {
	dir := t.TempDir()
	store := newStore(t, dir)

	body := bytes.Repeat([]byte("x"), filestore.MaxFileBytes+1)
	if err := os.WriteFile(filepath.Join(dir, "planted.jpg"), body, 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}

	if _, _, err := store.Get(t.Context(), "planted.jpg"); !errors.Is(err, filestore.ErrTooLarge) {
		t.Errorf("Get() = %v, want ErrTooLarge", err)
	}
}

// An empty directory is a configuration mistake, and one this package refuses
// rather than resolves: a store rooted at the process's working directory
// would write covers wherever the container happened to start.
func TestNewRefusesAnEmptyDirectory(t *testing.T) {
	if _, err := filestore.New(""); err == nil {
		t.Error("New(\"\") = nil, want an error")
	}
}
