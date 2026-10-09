package gamedb_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// codesScript is a template with one empty table, as a build leaves it before
// LoadTableData fills it.
const codesScript = `CREATE TABLE codes (id integer PRIMARY KEY, label text NOT NULL);`

// The end-to-end build is covered by internal/provisioning's
// game_integration_test.go.
func TestLoadTableDataCopiesRowsIntoTheTemplate(t *testing.T) {
	p := provisioner(t)
	template := named(t, "tpl")
	if err := buildTemplateString(p, t.Context(), template, codesScript, sqlpolicy.ReadOnly()); err != nil {
		t.Fatalf("building the template: %v", err)
	}

	if err := p.LoadTableData(t.Context(), template, "codes", []string{"id", "label"},
		strings.NewReader("1,alpha\n2,beta\n")); err != nil {
		t.Fatalf("loading table data: %v", err)
	}

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, sqlpolicy.ReadOnly()); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}
	reader := connectAs(t, roleReader, testReaderPassword(t), instance)
	var count int
	if err := reader.QueryRow(t.Context(), `SELECT count(*) FROM codes`).Scan(&count); err != nil {
		t.Fatalf("a participant cannot read the loaded table: %v", err)
	}
	if count != 2 {
		t.Fatalf("codes = %d rows, want 2", count)
	}
}

func TestLoadTableDataRefusalBecomesATableDataError(t *testing.T) {
	p := provisioner(t)
	template := named(t, "tpl")
	if err := buildTemplateString(p, t.Context(), template, codesScript, sqlpolicy.ReadOnly()); err != nil {
		t.Fatalf("building the template: %v", err)
	}

	// An empty unquoted field is COPY's NULL. provisioning.validateRow would
	// catch it first in production; here PostgreSQL does.
	err := p.LoadTableData(t.Context(), template, "codes", []string{"id", "label"}, strings.NewReader("1,\n"))
	if err == nil {
		t.Fatal("a NOT NULL violation was not refused")
	}

	var tde *gamedb.TableDataError
	if !errors.As(err, &tde) {
		t.Fatalf("error = %v (%T), want *gamedb.TableDataError", err, err)
	}
	if tde.Table != "codes" {
		t.Fatalf("TableDataError.Table = %q, want %q", tde.Table, "codes")
	}
	if !strings.Contains(tde.ScriptRejection(), "codes") {
		t.Fatalf("ScriptRejection() = %q, does not name the table", tde.ScriptRejection())
	}
}
