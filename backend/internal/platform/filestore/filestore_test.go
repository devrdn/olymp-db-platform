package filestore_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/filestore"
)

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

// Get and Delete refuse the same keys Put does: a read is as much a way out of
// the directory as a write.
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

func TestDeletingWhatIsNotThereSucceeds(t *testing.T) {
	store := newStore(t, t.TempDir())

	if err := store.Delete(t.Context(), "never-existed.jpg"); err != nil {
		t.Errorf("Delete() = %v, want nil", err)
	}
}

// Two organisers uploading the same picture at once write the same name twice.
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

func TestPutRefusesABodyBeyondTheCeiling(t *testing.T) {
	store := newStore(t, t.TempDir())

	body := bytes.Repeat([]byte("x"), filestore.MaxFileBytes+1)
	if err := store.Put(t.Context(), "huge.jpg", "image/jpeg", body); !errors.Is(err, filestore.ErrTooLarge) {
		t.Errorf("Put() = %v, want ErrTooLarge", err)
	}
}

// An operator can write to the volume by hand, so Get checks the size too.
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

func TestNewRefusesAnEmptyDirectory(t *testing.T) {
	if _, err := filestore.New(""); err == nil {
		t.Error("New(\"\") = nil, want an error")
	}
}

func TestListReturnsEveryFileTheStoreHolds(t *testing.T) {
	store := newStore(t, t.TempDir())

	for _, key := range []string{"one.jpg", "two.png", "three.webp"} {
		if err := store.Put(t.Context(), key, "image/jpeg", []byte(key)); err != nil {
			t.Fatalf("Put(%q) = %v", key, err)
		}
	}

	files, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List() = %v", err)
	}

	sizes := map[string]int64{}
	for _, file := range files {
		sizes[file.Name()] = file.Size()
	}
	for _, key := range []string{"one.jpg", "two.png", "three.webp"} {
		size, listed := sizes[key]
		if !listed {
			t.Fatalf("List() does not name %q: %v", key, sizes)
		}
		if want := int64(len(key)); size != want {
			t.Errorf("List() reports %q as %d bytes, want %d", key, size, want)
		}
	}
}

func TestListSkipsWhatIsNotAKeyOfThisStore(t *testing.T) {
	dir := t.TempDir()
	store := newStore(t, dir)

	if err := store.Put(t.Context(), "kept.jpg", "image/jpeg", []byte("kept")); err != nil {
		t.Fatalf("Put() = %v", err)
	}
	for _, name := range []string{".tmp-inflight", ".probe-12345", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	files, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(files) != 1 || files[0].Name() != "kept.jpg" {
		var names []string
		for _, file := range files {
			names = append(names, file.Name())
		}
		t.Fatalf("List() = %v, want only kept.jpg", names)
	}
}

func TestListReportsTheModificationTime(t *testing.T) {
	dir := t.TempDir()
	store := newStore(t, dir)

	if err := store.Put(t.Context(), "aged.jpg", "image/jpeg", []byte("aged")); err != nil {
		t.Fatalf("Put() = %v", err)
	}
	long := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(filepath.Join(dir, "aged.jpg"), long, long); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	files, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("List() returned %d files, want 1", len(files))
	}
	if got := files[0].ModTime().UTC(); !got.Equal(long.UTC()) {
		t.Errorf("ModTime() = %s, want %s", got, long.UTC())
	}
}

func TestListOfAnEmptyDirectoryIsEmpty(t *testing.T) {
	store := newStore(t, t.TempDir())

	files, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(files) != 0 {
		t.Errorf("List() = %v, want nothing", files)
	}
}
