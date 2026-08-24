package migrations

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// parseName splits "000002_contests.up.sql" into version, title and direction.
func parseName(t *testing.T, name string) (int, string, string) {
	t.Helper()
	base, direction, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), ".")
	if !ok {
		t.Fatalf("migration %q has no direction suffix", name)
	}
	versionPart, title, ok := strings.Cut(base, "_")
	if !ok {
		t.Fatalf("migration %q has no title", name)
	}
	version, err := strconv.Atoi(versionPart)
	if err != nil {
		t.Fatalf("migration %q has a non-numeric version: %v", name, err)
	}
	return version, title, direction
}

func migrationNames(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestEmbeddedSetIsNotEmpty(t *testing.T) {
	// A broken embed directive yields an empty FS and a silently unmigrated
	// database.
	if len(migrationNames(t)) == 0 {
		t.Fatal("no migrations were embedded")
	}
}

func TestEveryMigrationHasBothDirections(t *testing.T) {
	seen := map[string]map[string]bool{}
	for _, name := range migrationNames(t) {
		version, title, direction := parseName(t, name)
		key := strconv.Itoa(version) + "_" + title
		if seen[key] == nil {
			seen[key] = map[string]bool{}
		}
		seen[key][direction] = true
	}

	for key, directions := range seen {
		if !directions["up"] {
			t.Errorf("migration %s has no up file", key)
		}
		if !directions["down"] {
			t.Errorf("migration %s has no down file, so it cannot be rolled back", key)
		}
	}
}

func TestVersionsAreUniqueAndSequential(t *testing.T) {
	titles := map[int]string{}
	for _, name := range migrationNames(t) {
		version, title, _ := parseName(t, name)
		if existing, ok := titles[version]; ok && existing != title {
			t.Fatalf("version %d is used by both %q and %q; migrate would apply only one", version, existing, title)
		}
		titles[version] = title
	}

	// Gaps are legal for golang-migrate but signal a lost file in review.
	for v := 1; v <= len(titles); v++ {
		if _, ok := titles[v]; !ok {
			t.Errorf("version %d is missing from the sequence", v)
		}
	}
}

func TestUpMigrationsAreNotEmpty(t *testing.T) {
	for _, name := range migrationNames(t) {
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		content, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(strings.TrimSpace(string(content))) == 0 {
			t.Errorf("migration %s is empty", name)
		}
	}
}
