package covers_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/covers"
)

// dirVolume is a directory acted on directly, rather than through
// internal/platform/filestore.
//
// The sweep is domain logic and this package does not import the
// infrastructure that implements its port (CLAUDE.md, Go layout rules 3 and
// 7); the store's own listing is tested in its own package. What this test
// needs from a volume is a real file whose modification time it can set,
// because the age of a file is half of what the sweep decides on.
type dirVolume struct {
	dir     string
	deleted []string
	failOn  string
}

func (v *dirVolume) List(context.Context) ([]fs.FileInfo, error) {
	entries, err := os.ReadDir(v.dir)
	if err != nil {
		return nil, err
	}
	files := make([]fs.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		files = append(files, info)
	}
	return files, nil
}

func (v *dirVolume) Delete(_ context.Context, key string) error {
	if key == v.failOn {
		return os.ErrPermission
	}
	v.deleted = append(v.deleted, key)
	return os.Remove(filepath.Join(v.dir, key))
}

// hashes is the core database's account of which pictures are still spoken
// for, with no database behind it.
type hashes struct {
	referenced []string
	err        error
}

func (h hashes) ReferencedHashes(context.Context) ([]string, error) {
	return h.referenced, h.err
}

// hashOf builds a name-shaped hash out of one character, so a test can say
// which picture it means without a line of hexadecimal.
func hashOf(c string) string { return strings.Repeat(c, 64) }

// writeFile puts a cover rendition on the volume and dates it, since a file's
// age is what tells an upload in flight apart from an orphan. The time is set
// explicitly rather than waited for: a test that sleeps for an hour is a test
// nobody runs.
func writeFile(t *testing.T, dir, key string, age time.Duration) {
	t.Helper()

	path := filepath.Join(dir, key)
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		t.Fatalf("write %s: %v", key, err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("date %s: %v", key, err)
	}
}

func keysOf(files []covers.OrphanFile) []string {
	keys := make([]string, 0, len(files))
	for _, file := range files {
		keys = append(keys, file.Key)
	}
	return keys
}

// The whole sweep in one pass, because the three cases are one decision made
// three ways and a test that separated them would let two of them drift:
// a file no row names goes, a file a row names stays, and a file too young to
// judge stays whatever else is true of it.
func TestTheSweepRemovesOnlyWhatNoRowNamesAndNothingRecent(t *testing.T) {
	dir := t.TempDir()
	kept, orphan, fresh := hashOf("a"), hashOf("b"), hashOf("c")

	writeFile(t, dir, covers.Key(kept, 1600), 72*time.Hour)
	writeFile(t, dir, covers.Key(kept, 800), 72*time.Hour)
	writeFile(t, dir, covers.Key(orphan, 1600), 72*time.Hour)
	writeFile(t, dir, covers.Key(orphan, 800), 72*time.Hour)
	writeFile(t, dir, covers.Key(fresh, 1600), time.Minute)

	volume := &dirVolume{dir: dir}
	sweeper := covers.NewOrphanSweeper(volume, hashes{referenced: []string{kept}})

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	// By key, which sorts the widths as text: 1600 before 800.
	want := []string{covers.Key(orphan, 1600), covers.Key(orphan, 800)}
	if got := keysOf(found); len(got) != len(want) {
		t.Fatalf("Find() = %v, want %v", got, want)
	}
	for i, key := range want {
		if found[i].Key != key {
			t.Fatalf("Find()[%d] = %q, want %q", i, found[i].Key, key)
		}
	}

	result, err := sweeper.Remove(t.Context(), found)
	if err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if result.Removed != 2 || result.Failed != 0 {
		t.Errorf("Remove() = %+v, want 2 removed and none failed", result)
	}

	for _, key := range []string{covers.Key(kept, 1600), covers.Key(kept, 800), covers.Key(fresh, 1600)} {
		if _, err := os.Stat(filepath.Join(dir, key)); err != nil {
			t.Errorf("%s is gone, and it is still referred to or still too young: %v", key, err)
		}
	}
	for _, key := range want {
		if _, err := os.Stat(filepath.Join(dir, key)); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep: %v", key, err)
		}
	}
}

// The file is named by the hash of its content, so two contests that uploaded
// the same picture share one file. A cover removed from one of them may not
// take the other's picture with it: the question is whether *any* row names
// the hash, never whether this contest does.
func TestAPictureTwoContestsShareIsNotAnOrphan(t *testing.T) {
	dir := t.TempDir()
	shared := hashOf("d")

	writeFile(t, dir, covers.Key(shared, 1600), 72*time.Hour)
	writeFile(t, dir, covers.Key(shared, 800), 72*time.Hour)

	// One contest dropped its cover; the other still wears the same picture,
	// so the hash is still referenced — once.
	sweeper := covers.NewOrphanSweeper(&dirVolume{dir: dir}, hashes{referenced: []string{shared}})

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("Find() = %v, want nothing: the picture is still somebody's", keysOf(found))
	}
}

// A name this service never wrote is not this sweep's business, however old
// it is. The same rule cmd/gameorphans keeps about a database the core
// database has never heard of: removing what we cannot account for is how an
// operator's own file, or a directory somebody mounted here by mistake,
// disappears.
func TestAFileThatIsNotACoverIsLeftAlone(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"holiday.jpg", "deadbeef-1600.jpg", hashOf("e") + ".jpg", hashOf("f") + "-1600.png"} {
		writeFile(t, dir, name, 72*time.Hour)
	}

	sweeper := covers.NewOrphanSweeper(&dirVolume{dir: dir}, hashes{})

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("Find() = %v, want nothing: none of those is a name this service writes", keysOf(found))
	}
}

// Find asks the database first. A sweep that listed the volume, failed to
// read the rows and carried on would be a sweep whose set of referenced
// hashes is empty — which is to say, one that proposes to delete everything.
func TestFindRefusesWhenTheRowsCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, covers.Key(hashOf("a"), 1600), 72*time.Hour)

	volume := &dirVolume{dir: dir}
	sweeper := covers.NewOrphanSweeper(volume, hashes{err: os.ErrClosed})

	if _, err := sweeper.Find(t.Context()); err == nil {
		t.Fatal("Find() succeeded with no account of which pictures are referenced")
	}
	if len(volume.deleted) != 0 {
		t.Fatalf("a failed Find() deleted %v", volume.deleted)
	}
}

// One file the volume refuses does not stop the rest, and it is counted:
// a sweep that reported success over a directory it could not write to would
// be run again next month and report the same thing.
func TestARefusedDeleteIsCountedAndDoesNotStopTheRest(t *testing.T) {
	dir := t.TempDir()
	stuck, gone := hashOf("a"), hashOf("b")

	writeFile(t, dir, covers.Key(stuck, 1600), 72*time.Hour)
	writeFile(t, dir, covers.Key(gone, 1600), 72*time.Hour)

	volume := &dirVolume{dir: dir, failOn: covers.Key(stuck, 1600)}
	sweeper := covers.NewOrphanSweeper(volume, hashes{})

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	result, err := sweeper.Remove(t.Context(), found)
	if err == nil {
		t.Fatal("Remove() hid a refusal")
	}
	if result.Removed != 1 || result.Failed != 1 {
		t.Errorf("Remove() = %+v, want one removed and one failed", result)
	}
	if _, err := os.Stat(filepath.Join(dir, covers.Key(gone, 1600))); !os.IsNotExist(err) {
		t.Errorf("the sweep stopped at the first refusal: %v", err)
	}
}
