package main

import (
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
)

func TestTargetRefusesADatabaseThatIsNotATestDatabase(t *testing.T) {
	for name, dsn := range map[string]string{
		"the product's database":     "postgres://dbcontest:secret@localhost:5432/dbcontest_core?sslmode=disable",
		"keyword/value form":         "host=localhost user=dbcontest password=secret dbname=dbcontest_core",
		"test in the middle":         "postgres://dbcontest:secret@localhost:5432/dbcontest_test_core",
		"the suffix alone":           "postgres://dbcontest:secret@localhost:5432/_test",
		"a different case of suffix": "postgres://dbcontest:secret@localhost:5432/dbcontest_core_TEST",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := target(dsn); !errors.Is(err, storagetest.ErrNotATestDatabase) {
				t.Fatalf("target(%q) = %v, want ErrNotATestDatabase", name, err)
			}
		})
	}
}

func TestTargetRefusesADSNThatNamesNoDatabase(t *testing.T) {
	// Emptied, or the driver would fill the missing name in from it.
	t.Setenv("PGDATABASE", "")
	if _, _, err := target("postgres://dbcontest:secret@localhost:5432/"); !errors.Is(err, storagetest.ErrNotATestDatabase) {
		t.Fatalf("target() = %v, want ErrNotATestDatabase", err)
	}
}

func TestTargetConnectsToTheMaintenanceDatabaseOfTheSameServer(t *testing.T) {
	cfg, name, err := target("postgres://dbcontest:secret@db.example:6543/dbcontest_core_test?sslmode=disable")
	if err != nil {
		t.Fatalf("target: %v", err)
	}
	if name != "dbcontest_core_test" {
		t.Errorf("name = %q, want dbcontest_core_test", name)
	}
	if cfg.Database != maintenanceDatabase || cfg.Host != "db.example" || cfg.Port != 6543 || cfg.User != "dbcontest" {
		t.Errorf("connects to %s@%s:%d/%s, want dbcontest@db.example:6543/%s",
			cfg.User, cfg.Host, cfg.Port, cfg.Database, maintenanceDatabase)
	}
}
