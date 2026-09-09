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

// A definition with no tables cannot be saved (TestADefinitionWithNoTablesIsRefused,
// above) — but SQL is the generator finishDefinitionBuild calls on whatever
// a row actually holds, and a row that somehow got there empty must refuse
// there too, plainly, rather than hand back an empty script that would build
// a database with none of the organiser's tables in it silently.
func TestSQLOfAnEmptyDefinitionIsRefused(t *testing.T) {
	t.Parallel()
	_, err := (provisioning.Definition{}).SQL()
	if !errors.Is(err, provisioning.ErrDefinitionEmpty) {
		t.Fatalf("answered %v, want ErrDefinitionEmpty", err)
	}
}

// Every declared type maps to the literal PostgreSQL keyword this platform
// chose for it — never the organiser's own string interpolated back in
// (CLAUDE.md rule 14), which this proves by using a ColumnType whose Go
// constant and PostgreSQL keyword actually differ (ColumnNumeric ->
// "numeric" is the only one that doesn't just restate itself, so it is not
// the only case worth having, but it is the one a copy-the-string bug would
// not be caught by).
func TestSQLMapsEveryDeclaredTypeToItsPostgreSQLKeyword(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		typ  provisioning.ColumnType
		want string
	}{
		{provisioning.ColumnInteger, "integer"},
		{provisioning.ColumnText, "text"},
		{provisioning.ColumnDate, "date"},
		{provisioning.ColumnTimestamp, "timestamp"},
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
			if !strings.Contains(sql, `"c" `+tc.want) {
				t.Fatalf("declared type %q produced:\n%s\nwant a column typed %q", tc.typ, sql, tc.want)
			}
		})
	}
}

// A column not marked nullable gets NOT NULL; one that is marked nullable
// does not — Nullable's own doc says false is the stricter default, and this
// is where that default actually reaches the database.
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

// A composite primary key generates one PRIMARY KEY clause naming every
// column, in the order the definition gave them — the order a participant's
// own query plan and an organiser's own reading of the table both depend on.
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

// A table declaring no primary key at all gets no PRIMARY KEY clause — the
// field is optional (TableDefinition's own doc), and a clause naming nothing
// is not valid PostgreSQL.
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

// Twice from the same definition must be byte for byte identical — the whole
// point of walking ordered slices rather than anything keyed by a map
// (SQL's own doc). A rebuild that could disagree with an earlier build of
// the identical definition would make "why did the schema change" a
// question with no real answer.
func TestSQLIsDeterministicAcrossCalls(t *testing.T) {
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
			Name:    "witnesses",
			Columns: []provisioning.ColumnDefinition{aColumn("statement", provisioning.ColumnText)},
		},
	}}

	first, err := d.SQL()
	if err != nil {
		t.Fatalf("generate SQL (first): %v", err)
	}
	second, err := d.SQL()
	if err != nil {
		t.Fatalf("generate SQL (second): %v", err)
	}
	if first != second {
		t.Fatalf("two calls on the same definition disagreed:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// A name that is a valid plain identifier (sqlpolicy.PlainIdentifier allows
// both cases and does not know PostgreSQL's own reserved words) can still
// need quoting to mean what the organiser wrote — mixed case, which an
// unquoted identifier would fold to lowercase, and a bare reserved word,
// which an unquoted CREATE TABLE would fail to parse as a table name at
// all. Both are exactly what CLAUDE.md rule 14 and QuoteIdentifier's own doc
// are for: quoted unconditionally, so neither case is special-cased here.
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
