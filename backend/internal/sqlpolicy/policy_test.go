package sqlpolicy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

func TestReadOnlyIsTheDefaultAndAllowsNoWriting(t *testing.T) {
	p := sqlpolicy.ReadOnly()

	if p.Mode != sqlpolicy.ModeReadOnly {
		t.Fatalf("mode = %q, want read_only", p.Mode)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("the default policy does not validate: %v", err)
	}
	if !p.AllowCatalog {
		t.Fatal("structural catalogs should be readable by default")
	}
}

func TestTheZeroPolicyIsRefused(t *testing.T) {
	if err := (sqlpolicy.Policy{}).Validate(); err == nil {
		t.Fatal("the zero policy validated")
	}
}

func TestReadOnlyCannotCarryPermissionToWrite(t *testing.T) {
	cases := map[string]func(*sqlpolicy.Policy){
		"a writable table": func(p *sqlpolicy.Policy) { p.WritableTables = []string{"evidence"} },
		"creating views":   func(p *sqlpolicy.Policy) { p.AllowCreateView = true },
		"own tables":       func(p *sqlpolicy.Policy) { p.AllowOwnTables = true },
		"temp tables":      func(p *sqlpolicy.Policy) { p.AllowTempTables = true },
	}

	for name, grant := range cases {
		t.Run(name, func(t *testing.T) {
			p := sqlpolicy.ReadOnly()
			grant(&p)
			if err := p.Validate(); err == nil {
				t.Fatalf("read_only with %s validated", name)
			}
		})
	}
}

func TestWritableTablesMustBePlainIdentifiers(t *testing.T) {
	// These names are interpolated into GRANTs; identifiers cannot be bound.
	for _, name := range []string{
		"evidence; DROP DATABASE core",
		`evidence" TO PUBLIC; --`,
		"",
		"has space",
		strings.Repeat("x", 64), // past PostgreSQL's identifier length
	} {
		t.Run(name, func(t *testing.T) {
			p := sqlpolicy.ReadWrite(name)
			if err := p.Validate(); err == nil {
				t.Fatalf("writable table %q validated", name)
			}
		})
	}
}

func TestReadWriteAcceptsOrdinaryTableNames(t *testing.T) {
	p := sqlpolicy.ReadWrite("evidence", "case_notes", "Suspects2")
	if err := p.Validate(); err != nil {
		t.Fatalf("ordinary table names were refused: %v", err)
	}
	if !p.MayWriteTo("", "evidence") {
		t.Fatal("a table the policy names is not writable")
	}
	if !p.MayWriteTo("", "suspects2") {
		t.Fatal("identifier folding is not applied")
	}
	if p.MayWriteTo("", "users") {
		t.Fatal("a table the policy does not name is writable")
	}
	if !p.MayWriteTo("public", "evidence") {
		t.Fatal("a qualified spelling of the same table is not writable")
	}
	if p.MayWriteTo("other", "evidence") {
		t.Fatal("the same name in another schema is writable")
	}
}

func TestADuplicateTableIsRefused(t *testing.T) {
	if err := sqlpolicy.ReadWrite("evidence", "evidence").Validate(); err == nil {
		t.Fatal("a duplicated table validated")
	}
}

func TestTheReasonIsNamed(t *testing.T) {
	err := sqlpolicy.ReadWrite("evidence; DROP DATABASE core").Validate()
	if !errors.Is(err, sqlpolicy.ErrInvalidPolicy) {
		t.Fatalf("error %v is not an ErrInvalidPolicy", err)
	}
	if !strings.Contains(err.Error(), "evidence; DROP DATABASE core") {
		t.Fatalf("the message does not name the offending table: %v", err)
	}
}
