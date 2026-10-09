package provisioning_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
)

func aColumn(name string, typ provisioning.ColumnType) provisioning.ColumnDefinition {
	return provisioning.ColumnDefinition{Name: name, Type: typ}
}

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

// tablesOf builds n minimal one-column tables, small enough that the count
// bound is reached long before MaxDefinitionBytes.
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

func columnsOf(n int) []provisioning.ColumnDefinition {
	columns := make([]provisioning.ColumnDefinition, n)
	for i := range columns {
		columns[i] = aColumn("c"+itoa(i), provisioning.ColumnInteger)
	}
	return columns
}

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

// Long, distinct, valid names push the document past 64 KiB while staying
// within both count bounds.
func TestADefinitionPastTheByteBoundIsRefused(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 63)
	tables := make([]provisioning.TableDefinition, provisioning.MaxDefinitionTables)
	for i := range tables {
		columns := make([]provisioning.ColumnDefinition, provisioning.MaxDefinitionTableColumns)
		for j := range columns {
			// Unique and under 63 characters; nullable adds bytes to the JSON.
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

// The key is quoted in PRIMARY KEY (...), so "ID" would not match column "id"
// at build time.
func TestAPrimaryKeySpelledInAnotherCaseThanItsColumnIsRefused(t *testing.T) {
	t.Parallel()
	d := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{
			Name:       "suspects",
			Columns:    []provisioning.ColumnDefinition{aColumn("id", provisioning.ColumnInteger)},
			PrimaryKey: []string{"ID"},
		},
	}}
	if err := d.Validate(); !errors.Is(err, provisioning.ErrDefinitionInvalidPrimaryKey) {
		t.Fatalf("answered %v, want ErrDefinitionInvalidPrimaryKey", err)
	}
}

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

func TestSQLOfAnEmptyDefinitionIsRefused(t *testing.T) {
	t.Parallel()
	_, err := (provisioning.Definition{}).SQL()
	if !errors.Is(err, provisioning.ErrDefinitionEmpty) {
		t.Fatalf("answered %v, want ErrDefinitionEmpty", err)
	}
}

// The whole statement is compared, because a substring check cannot tell
// "timestamp" from "timestamptz", which would reinterpret naive local times.
func TestSQLMapsEveryDeclaredTypeToItsPostgreSQLKeyword(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		typ  provisioning.ColumnType
		want string
	}{
		{provisioning.ColumnInteger, "integer"},
		{provisioning.ColumnText, "text"},
		{provisioning.ColumnDate, "date"},
		{provisioning.ColumnTimestamp, "timestamp without time zone"},
		{provisioning.ColumnNumeric, "numeric"},
		{provisioning.ColumnBoolean, "boolean"},
	} {
		t.Run(string(tc.typ), func(t *testing.T) {
			t.Parallel()
			d := provisioning.Definition{Tables: []provisioning.TableDefinition{
				{Name: "t", Columns: []provisioning.ColumnDefinition{aColumn("c", tc.typ)}},
			}}
			sql, err := d.SQL()
			if err != nil {
				t.Fatalf("generate SQL: %v", err)
			}
			want := "CREATE TABLE public.\"t\" (\n    \"c\" " + tc.want + " NOT NULL\n);\n"
			if sql != want {
				t.Fatalf("declared type %q produced:\n%s\nwant:\n%s", tc.typ, sql, want)
			}
		})
	}
	if len(provisioning.ColumnTypes) != 6 {
		t.Fatalf("ColumnTypes lists %d types; this table has to grow with it", len(provisioning.ColumnTypes))
	}
}

func TestSQLAddsNotNullExactlyWhereTheColumnIsNotNullable(t *testing.T) {
	t.Parallel()
	d := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{
			Name: "suspects",
			Columns: []provisioning.ColumnDefinition{
				{Name: "id", Type: provisioning.ColumnInteger, Nullable: false},
				{Name: "nickname", Type: provisioning.ColumnText, Nullable: true},
			},
		},
	}}
	sql, err := d.SQL()
	if err != nil {
		t.Fatalf("generate SQL: %v", err)
	}
	if !strings.Contains(sql, `"id" integer NOT NULL`) {
		t.Fatalf("a non-nullable column did not get NOT NULL:\n%s", sql)
	}
	if strings.Contains(sql, `"nickname" text NOT NULL`) {
		t.Fatalf("a nullable column got NOT NULL anyway:\n%s", sql)
	}
}

func TestSQLGeneratesTheCompositePrimaryKeyInDeclaredOrder(t *testing.T) {
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
	sql, err := d.SQL()
	if err != nil {
		t.Fatalf("generate SQL: %v", err)
	}
	if !strings.Contains(sql, `PRIMARY KEY ("case_id", "item_no")`) {
		t.Fatalf("SQL did not name the composite key in declared order:\n%s", sql)
	}
}

func TestSQLOmitsThePrimaryKeyClauseWhenNoneWasDeclared(t *testing.T) {
	t.Parallel()
	d := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{Name: "notes", Columns: []provisioning.ColumnDefinition{aColumn("body", provisioning.ColumnText)}},
	}}
	sql, err := d.SQL()
	if err != nil {
		t.Fatalf("generate SQL: %v", err)
	}
	if strings.Contains(sql, "PRIMARY KEY") {
		t.Fatalf("a table with no declared key got a PRIMARY KEY clause:\n%s", sql)
	}
}

// The exact text is what catches a map in the generator: tables and columns
// are not in alphabetical order, so neither random iteration nor sorting by
// name can reproduce it.
func TestSQLIsTheSameTextEveryTimeForTheSameDefinition(t *testing.T) {
	t.Parallel()
	d := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{
			Name: "suspects",
			Columns: []provisioning.ColumnDefinition{
				aColumn("id", provisioning.ColumnInteger),
				aColumn("name", provisioning.ColumnText),
			},
			PrimaryKey: []string{"id"},
		},
		{
			// Many columns, unsorted, so a map iteration rarely matches.
			Name: "witnesses",
			Columns: []provisioning.ColumnDefinition{
				aColumn("statement", provisioning.ColumnText),
				aColumn("heard_at", provisioning.ColumnTimestamp),
				aColumn("seen_on", provisioning.ColumnDate),
				aColumn("distance_km", provisioning.ColumnNumeric),
				aColumn("credible", provisioning.ColumnBoolean),
			},
		},
		{
			Name: "alibis",
			Columns: []provisioning.ColumnDefinition{
				aColumn("suspect_id", provisioning.ColumnInteger),
				aColumn("confirmed", provisioning.ColumnBoolean),
			},
		},
	}}

	// Each statement ends in a newline and SQL joins them with one more,
	// hence the blank lines.
	const want = `CREATE TABLE public."suspects" (
    "id" integer NOT NULL,
    "name" text NOT NULL,
    PRIMARY KEY ("id")
);

CREATE TABLE public."witnesses" (
    "statement" text NOT NULL,
    "heard_at" timestamp without time zone NOT NULL,
    "seen_on" date NOT NULL,
    "distance_km" numeric NOT NULL,
    "credible" boolean NOT NULL
);

CREATE TABLE public."alibis" (
    "suspect_id" integer NOT NULL,
    "confirmed" boolean NOT NULL
);
`

	first, err := d.SQL()
	if err != nil {
		t.Fatalf("generate SQL (first): %v", err)
	}
	if first != want {
		t.Fatalf("SQL() produced:\n%s\nwant:\n%s", first, want)
	}
	second, err := d.SQL()
	if err != nil {
		t.Fatalf("generate SQL (second): %v", err)
	}
	if first != second {
		t.Fatalf("two calls on the same definition disagreed:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// Plain identifiers can still need quotes: mixed case would fold, and a
// reserved word would not parse.
func TestSQLQuotesANameThatWouldOtherwiseNeedIt(t *testing.T) {
	t.Parallel()
	d := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{
			Name: "Suspects",
			Columns: []provisioning.ColumnDefinition{
				aColumn("id", provisioning.ColumnInteger),
				aColumn("order", provisioning.ColumnText),
			},
		},
	}}
	sql, err := d.SQL()
	if err != nil {
		t.Fatalf("generate SQL: %v", err)
	}
	if !strings.Contains(sql, `CREATE TABLE public."Suspects"`) {
		t.Fatalf("a mixed-case table name was not kept quoted:\n%s", sql)
	}
	if !strings.Contains(sql, `"order" text`) {
		t.Fatalf("a column named after a reserved word was not kept quoted:\n%s", sql)
	}
}
