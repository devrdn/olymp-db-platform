package provisioning_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
)

// aColumn is the smallest valid ColumnDefinition, named for readability at
// the call site rather than repeated inline in every test below.
func aColumn(name string, typ provisioning.ColumnType) provisioning.ColumnDefinition {
	return provisioning.ColumnDefinition{Name: name, Type: typ}
}

// A small, valid game — the shape a detective olympiad actually needs: who
// the suspects are, and a primary key to tell them apart.
func TestAValidDefinitionPasses(t *testing.T) {
	t.Parallel()
	d := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{
			Name: "suspects",
			Columns: []provisioning.ColumnDefinition{
				aColumn("id", provisioning.ColumnInteger),
				aColumn("name", provisioning.ColumnText),
				aColumn("seen_at", provisioning.ColumnTimestamp),
				aColumn("alibi_confirmed", provisioning.ColumnBoolean),
			},
			PrimaryKey: []string{"id"},
		},
	}}
	if err := d.Validate(); err != nil {
		t.Fatalf("a valid definition was refused: %v", err)
	}
}

func TestEveryDeclaredColumnTypePasses(t *testing.T) {
	t.Parallel()
	for _, typ := range []provisioning.ColumnType{
		provisioning.ColumnInteger, provisioning.ColumnText, provisioning.ColumnDate,
		provisioning.ColumnTimestamp, provisioning.ColumnNumeric, provisioning.ColumnBoolean,
	} {
		t.Run(string(typ), func(t *testing.T) {
			t.Parallel()
			d := provisioning.Definition{Tables: []provisioning.TableDefinition{
				{Name: "t", Columns: []provisioning.ColumnDefinition{aColumn("c", typ)}},
			}}
			if err := d.Validate(); err != nil {
				t.Fatalf("declared type %q was refused: %v", typ, err)
			}
		})
	}
}

func TestADefinitionWithNoTablesIsRefused(t *testing.T) {
	t.Parallel()
	if err := (provisioning.Definition{}).Validate(); !errors.Is(err, provisioning.ErrDefinitionEmpty) {
		t.Fatalf("answered %v, want ErrDefinitionEmpty", err)
	}
}

// tablesOf builds n minimal one-column tables, named t0..t(n-1). Minimal on
// purpose: this is what lets the table-count boundary be tested without also
// brushing against MaxDefinitionBytes, which a name-padded or column-heavy
// definition would (TestADefinitionPastTheByteBoundIsRefused tests that bound
// on its own, deliberately).
func tablesOf(n int) []provisioning.TableDefinition {
	tables := make([]provisioning.TableDefinition, n)
	for i := range tables {
		tables[i] = provisioning.TableDefinition{
			Name:    "t" + itoa(i),
			Columns: []provisioning.ColumnDefinition{aColumn("c", provisioning.ColumnInteger)},
		}
	}
	return tables
}

// itoa avoids importing strconv for one call site.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for i > 0 {
		digits = string(rune('0'+i%10)) + digits
		i /= 10
	}
	return digits
}

// Exactly MaxDefinitionTables must pass and MaxDefinitionTables+1 must be
// refused — the boundary test CLAUDE.md rule 2 asks for beside every bound
// this package declares.
func TestTooManyTablesIsRefusedExactlyAtTheBoundary(t *testing.T) {
	t.Parallel()

	atLimit := provisioning.Definition{Tables: tablesOf(provisioning.MaxDefinitionTables)}
	if err := atLimit.Validate(); err != nil {
		t.Fatalf("exactly MaxDefinitionTables tables was refused: %v", err)
	}

	overLimit := provisioning.Definition{Tables: tablesOf(provisioning.MaxDefinitionTables + 1)}
	if err := overLimit.Validate(); !errors.Is(err, provisioning.ErrDefinitionTooLarge) {
		t.Fatalf("MaxDefinitionTables+1 tables answered %v, want ErrDefinitionTooLarge", err)
	}
}

// columnsOf builds n minimal integer columns, named c0..c(n-1).
func columnsOf(n int) []provisioning.ColumnDefinition {
	columns := make([]provisioning.ColumnDefinition, n)
	for i := range columns {
		columns[i] = aColumn("c"+itoa(i), provisioning.ColumnInteger)
	}
	return columns
}

// The same boundary, for MaxDefinitionTableColumns.
func TestTooManyColumnsIsRefusedExactlyAtTheBoundary(t *testing.T) {
	t.Parallel()

	atLimit := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{Name: "t", Columns: columnsOf(provisioning.MaxDefinitionTableColumns)},
	}}
	if err := atLimit.Validate(); err != nil {
		t.Fatalf("exactly MaxDefinitionTableColumns columns was refused: %v", err)
	}

	overLimit := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{Name: "t", Columns: columnsOf(provisioning.MaxDefinitionTableColumns + 1)},
	}}
	if err := overLimit.Validate(); !errors.Is(err, provisioning.ErrDefinitionTooLarge) {
		t.Fatalf("MaxDefinitionTableColumns+1 columns answered %v, want ErrDefinitionTooLarge", err)
	}
}

// A definition within the count bounds can still be too large once encoded
// — the backstop MaxDefinitionBytes's own doc describes. Long, distinct
// names (each itself a valid plain identifier, up to PostgreSQL's own
// 63-character limit) are what gets a definition well past 64 KiB without
// ever touching MaxDefinitionTables or MaxDefinitionTableColumns, which is
// the point: this is a genuinely separate bound, not a restatement of the
// other two.
func TestADefinitionPastTheByteBoundIsRefused(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 63)
	tables := make([]provisioning.TableDefinition, provisioning.MaxDefinitionTables)
	for i := range tables {
		columns := make([]provisioning.ColumnDefinition, provisioning.MaxDefinitionTableColumns)
		for j := range columns {
			// Each column name padded and suffixed to stay unique and under
			// 63 characters, and every one nullable so the "nullable" key is
			// on the wire too — the point is bytes, not columns.
			columns[j] = provisioning.ColumnDefinition{
				Name: long[:60] + itoa(j%10) + itoa(j/10%10) + itoa(j/100%10), Type: provisioning.ColumnNumeric, Nullable: true,
			}
		}
		tables[i] = provisioning.TableDefinition{Name: long[:60] + itoa(i%10) + itoa(i/10%10) + itoa(i/100%10), Columns: columns}
	}
	d := provisioning.Definition{Tables: tables}

	if err := d.Validate(); !errors.Is(err, provisioning.ErrDefinitionTooLarge) {
		t.Fatalf("a definition at the table/column count bound but grossly over MaxDefinitionBytes answered %v, want ErrDefinitionTooLarge", err)
	}
}

func TestATableNameThatIsNotAPlainIdentifierIsRefused(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "my table", "my-table", "1table", "table;drop", strings.Repeat("a", 64)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := provisioning.Definition{Tables: []provisioning.TableDefinition{
				{Name: name, Columns: []provisioning.ColumnDefinition{aColumn("c", provisioning.ColumnInteger)}},
			}}
			if err := d.Validate(); !errors.Is(err, provisioning.ErrDefinitionInvalidName) {
				t.Fatalf("table name %q answered %v, want ErrDefinitionInvalidName", name, err)
			}
		})
	}
}

func TestAColumnNameThatIsNotAPlainIdentifierIsRefused(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "my column", "1st", "a.b"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := provisioning.Definition{Tables: []provisioning.TableDefinition{
				{Name: "t", Columns: []provisioning.ColumnDefinition{aColumn(name, provisioning.ColumnInteger)}},
			}}
			if err := d.Validate(); !errors.Is(err, provisioning.ErrDefinitionInvalidName) {
				t.Fatalf("column name %q answered %v, want ErrDefinitionInvalidName", name, err)
			}
		})
	}
}

// Folded the way PostgreSQL folds an unquoted identifier: "Suspects" and
// "suspects" collide in the database this definition is meant to build, so
// they must collide here too.
func TestDuplicateTableNamesAreRefusedCaseInsensitively(t *testing.T) {
	t.Parallel()
	d := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{Name: "Suspects", Columns: []provisioning.ColumnDefinition{aColumn("id", provisioning.ColumnInteger)}},
		{Name: "suspects", Columns: []provisioning.ColumnDefinition{aColumn("id", provisioning.ColumnInteger)}},
	}}
	if err := d.Validate(); !errors.Is(err, provisioning.ErrDefinitionDuplicateName) {
		t.Fatalf("answered %v, want ErrDefinitionDuplicateName", err)
	}
}

func TestDuplicateColumnNamesWithinATableAreRefusedCaseInsensitively(t *testing.T) {
	t.Parallel()
	d := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{Name: "t", Columns: []provisioning.ColumnDefinition{
			aColumn("Name", provisioning.ColumnText),
			aColumn("name", provisioning.ColumnText),
		}},
	}}
	if err := d.Validate(); !errors.Is(err, provisioning.ErrDefinitionDuplicateName) {
		t.Fatalf("answered %v, want ErrDefinitionDuplicateName", err)
	}
}

// `CREATE TABLE t ()` is not valid PostgreSQL, so a table with no columns is
// refused here rather than becoming a build failure the organiser cannot
// see coming.
func TestATableWithNoColumnsIsRefused(t *testing.T) {
	t.Parallel()
	d := provisioning.Definition{Tables: []provisioning.TableDefinition{{Name: "t"}}}
	if err := d.Validate(); !errors.Is(err, provisioning.ErrDefinitionTableEmpty) {
		t.Fatalf("answered %v, want ErrDefinitionTableEmpty", err)
	}
}

func TestAColumnTypeOutsideTheClosedSetIsRefused(t *testing.T) {
	t.Parallel()
	d := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{Name: "t", Columns: []provisioning.ColumnDefinition{aColumn("c", provisioning.ColumnType("varchar"))}},
	}}
	if err := d.Validate(); !errors.Is(err, provisioning.ErrDefinitionInvalidType) {
		t.Fatalf("answered %v, want ErrDefinitionInvalidType", err)
	}
}

func TestAPrimaryKeyNamingAColumnTheTableDoesNotHaveIsRefused(t *testing.T) {
	t.Parallel()
	d := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{
			Name:       "t",
			Columns:    []provisioning.ColumnDefinition{aColumn("id", provisioning.ColumnInteger)},
			PrimaryKey: []string{"nope"},
		},
	}}
	if err := d.Validate(); !errors.Is(err, provisioning.ErrDefinitionInvalidPrimaryKey) {
		t.Fatalf("answered %v, want ErrDefinitionInvalidPrimaryKey", err)
	}
}

func TestAPrimaryKeyListingTheSameColumnTwiceIsRefused(t *testing.T) {
	t.Parallel()
	d := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{
			Name:       "t",
			Columns:    []provisioning.ColumnDefinition{aColumn("id", provisioning.ColumnInteger)},
			PrimaryKey: []string{"id", "ID"},
		},
	}}
	if err := d.Validate(); !errors.Is(err, provisioning.ErrDefinitionInvalidPrimaryKey) {
		t.Fatalf("answered %v, want ErrDefinitionInvalidPrimaryKey", err)
	}
}

// A composite primary key — more than one column identifying a row
// together — is an ordinary shape (an evidence log keyed by case and item
// number, say) and must not be refused just for having more than one name.
func TestACompositePrimaryKeyIsAccepted(t *testing.T) {
	t.Parallel()
	d := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{
			Name: "evidence",
			Columns: []provisioning.ColumnDefinition{
				aColumn("case_id", provisioning.ColumnInteger),
				aColumn("item_no", provisioning.ColumnInteger),
			},
			PrimaryKey: []string{"case_id", "item_no"},
		},
	}}
	if err := d.Validate(); err != nil {
		t.Fatalf("a composite primary key was refused: %v", err)
	}
}
