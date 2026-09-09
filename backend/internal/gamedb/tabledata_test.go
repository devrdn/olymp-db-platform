package gamedb_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// codesScript is a template with one empty table — the state
// provisioning.Games.finishDefinitionBuild leaves every builder-sourced
// table in right before this file's own LoadTableData is asked to fill one.
const codesScript = `CREATE TABLE codes (id integer PRIMARY KEY, label text NOT NULL);`

// TestLoadTableDataCopiesRowsIntoTheTemplate is the one proof at this
// package's own level that LoadTableData's COPY really lands rows in a
// database, over the provisioning role's own connection rather than
// game_author's (that role's grants are already gone by the time this
// method may be called — its own doc explains why). The end-to-end proof
// that this is wired into a real build lives in internal/provisioning's own
// integration test (game_integration_test.go), which is what
// `make test-game-build` runs; this is the narrower proof of this one method.
func TestLoadTableDataCopiesRowsIntoTheTemplate(t *testing.T) {
	p := provisioner(t)
	template := named(t, "tpl")
	if err := p.BuildTemplateString(t.Context(), template, codesScript, sqlpolicy.ReadOnly()); err != nil {
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

// TestLoadTableDataRefusalBecomesATableDataError proves the classification
// tableDataFailure makes: a constraint COPY itself catches — a NULL for a
// NOT NULL column, in this case, past whatever this package's own
// pre-validation already checked — comes back as *gamedb.TableDataError,
// naming the table, rather than as a bare driver error nobody can show an
// organiser (CLAUDE.md rule 1, the same distinction ScriptError draws for a
// script's own statements).
func TestLoadTableDataRefusalBecomesATableDataError(t *testing.T) {
	p := provisioner(t)
	template := named(t, "tpl")
	if err := p.BuildTemplateString(t.Context(), template, codesScript, sqlpolicy.ReadOnly()); err != nil {
		t.Fatalf("building the template: %v", err)
	}

	// An empty, unquoted field is COPY's own NULL — refused by the column's
	// own NOT NULL, exactly the constraint this package's own pre-validation
	// (provisioning.validateRow) would have already caught for an organiser;
	// this test is about what happens when PostgreSQL is the one that catches
	// it, not about reaching this path past that check.
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
