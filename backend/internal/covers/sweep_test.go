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

// dirVolume is a real directory used directly, since this package does not
// import filestore (CLAUDE.md layout rule 7); the tests need files whose
// modification time they can set.
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

type hashes struct {
	referenced []string
	err        error
}

func (h hashes) ReferencedHashes(context.Context) ([]string, error) {
	return h.referenced, h.err
}

func hashOf(c string) string { return strings.Repeat(c, 64) }

// writeFile puts a cover rendition on the volume and backdates it by age.
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
	// Sorted by key, so 1600 comes before 800.
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

func TestAPictureTwoContestsShareIsNotAnOrphan(t *testing.T) {
	dir := t.TempDir()
	shared := hashOf("d")

	writeFile(t, dir, covers.Key(shared, 1600), 72*time.Hour)
	writeFile(t, dir, covers.Key(shared, 800), 72*time.Hour)

	sweeper := covers.NewOrphanSweeper(&dirVolume{dir: dir}, hashes{referenced: []string{shared}})

	found, err := sweeper.Find(t.Context())
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("Find() = %v, want nothing: the picture is still somebody's", keysOf(found))
	}
}

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
