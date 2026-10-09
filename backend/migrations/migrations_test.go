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

// lockConventionFrom is the first version held to the lock convention below.
// Earlier ones run on empty tables on a fresh database.
const lockConventionFrom = 34

// statements counts the statements in a migration, ignoring comments and the
// dollar-quoted bodies of functions, where a semicolon is ordinary text.
func statements(sql string) int {
	var count int
	var inLine, inDollar bool
	for i := 0; i < len(sql); i++ {
		switch {
		case inLine:
			if sql[i] == '\n' {
				inLine = false
			}
		case inDollar:
			if strings.HasPrefix(sql[i:], "$$") {
				inDollar = false
				i++
			}
		case strings.HasPrefix(sql[i:], "--"):
			inLine = true
		case strings.HasPrefix(sql[i:], "$$"):
			inDollar = true
			i++
		case sql[i] == ';':
			count++
		}
	}
	return count
}

// cmd/migrate sends a whole file as one string, so a multi-statement file runs
// as one implicit transaction and holds every lock until it commits; such a
// file must set lock_timeout. CREATE INDEX CONCURRENTLY cannot run in a
// transaction block, so its file holds one statement and no lock timeout: the
// build waits for every older transaction, and a timeout would abort it and
// leave an invalid index.
func TestAMigrationSaysHowLongItWaitsForALock(t *testing.T) {
	for _, name := range migrationNames(t) {
		version, _, direction := parseName(t, name)
		if direction != "up" || version < lockConventionFrom {
			continue
		}
		body, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sql := string(body)
		concurrent := strings.Contains(sql, "CONCURRENTLY")
		timeout := strings.Contains(sql, "SET lock_timeout")

		switch {
		case concurrent && statements(sql) != 1:
			t.Errorf("%s builds an index concurrently and holds %d statements: it would run inside a transaction block and fail",
				name, statements(sql))
		case concurrent && timeout:
			t.Errorf("%s sets a lock timeout on a concurrent build: it waits for older transactions through the lock manager, and the timeout aborts it",
				name)
		case !concurrent && !timeout:
			t.Errorf("%s takes its locks with no lock_timeout of its own: whatever it blocks, it blocks for as long as it takes", name)
		}
	}
}
