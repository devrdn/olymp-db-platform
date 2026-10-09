package provisioning

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// Why a definition could not be saved: always the organiser's mistake, so
// each maps to its own status (CLAUDE.md rule 1).
var (
	// ErrDefinitionEmpty is a definition with no tables at all.
	ErrDefinitionEmpty = errors.New("the game definition has no tables")
	// ErrDefinitionTooLarge is a definition past MaxDefinitionTables, a table
	// past MaxDefinitionTableColumns, or the whole document past
	// MaxDefinitionBytes once encoded.
	ErrDefinitionTooLarge = errors.New("the game definition is larger than this platform allows")
	// ErrDefinitionInvalidName is a table or column name that is not a plain
	// PostgreSQL identifier (sqlpolicy.PlainIdentifier). Names end up in
	// CREATE TABLE, and SQL cannot bind an identifier as a parameter.
	ErrDefinitionInvalidName = errors.New("a table or column name is not a plain identifier")
	// ErrDefinitionDuplicateName is a table named twice, or a column named
	// twice within one table, compared case-folded as PostgreSQL folds an
	// unquoted identifier.
	ErrDefinitionDuplicateName = errors.New("the game definition names the same table or column twice")
	// ErrDefinitionTableEmpty is a table with no columns, which PostgreSQL
	// would refuse at build time.
	ErrDefinitionTableEmpty = errors.New("a table has no columns")
	// ErrDefinitionInvalidType is a column whose type is not one of the
	// closed set ColumnType declares.
	ErrDefinitionInvalidType = errors.New("a column's type is not one this platform supports")
	// ErrDefinitionInvalidPrimaryKey is a primary key that names a column
	// its own table does not have, or names the same column twice.
	ErrDefinitionInvalidPrimaryKey = errors.New("the primary key names a column the table does not have")
	// ErrDefinitionTableLocked is a definition that would change the name,
	// columns or primary key of a table that already holds data.
	//
	// The table's CSV header names its columns by name and position, never by
	// type, so a type change or an added column could load existing rows
	// silently against the wrong structure. The whole table freezes, matching
	// what the builder screen already disables. A table with zero rows is not
	// locked: the build skips the header, so nothing it reads can disagree.
	ErrDefinitionTableLocked = errors.New("a table that already holds data may not have its structure changed")
)

// ColumnType is the closed set of column types the table builder may
// describe. It maps to a fixed SQL keyword and is never interpolated as text.
type ColumnType string

const (
	// ColumnInteger is a whole number.
	ColumnInteger ColumnType = "integer"
	// ColumnText is free text of unbounded length.
	ColumnText ColumnType = "text"
	// ColumnDate is a calendar date with no time of day.
	ColumnDate ColumnType = "date"
	// ColumnTimestamp is a date and time without a time zone.
	ColumnTimestamp ColumnType = "timestamp"
	// ColumnNumeric is an exact decimal number, for values such as money
	// where floating-point rounding would be wrong.
	ColumnNumeric ColumnType = "numeric"
	// ColumnBoolean is a two-valued flag.
	ColumnBoolean ColumnType = "boolean"
)

// ColumnTypes lists every ColumnType in declaration order. The API serves it
// so a client never keeps its own copy of the set (CLAUDE.md rule 11).
var ColumnTypes = []ColumnType{
	ColumnInteger, ColumnText, ColumnDate, ColumnTimestamp, ColumnNumeric, ColumnBoolean,
}

func (t ColumnType) valid() bool {
	switch t {
	case ColumnInteger, ColumnText, ColumnDate, ColumnTimestamp, ColumnNumeric, ColumnBoolean:
		return true
	}
	return false
}

// ColumnDefinition is one column of a TableDefinition.
type ColumnDefinition struct {
	Name string     `json:"name"`
	Type ColumnType `json:"type"`
	// Nullable says whether the generated column may hold NULL. False by
	// default: an organiser opts into NULL rather than getting it by omission.
	Nullable bool `json:"nullable,omitempty"`
}

// TableDefinition is one table an organiser described structurally.
type TableDefinition struct {
	Name    string             `json:"name"`
	Columns []ColumnDefinition `json:"columns"`
	// PrimaryKey names the key columns in PRIMARY KEY order; each must also
	// appear in Columns.
	PrimaryKey []string `json:"primary_key,omitempty"`
}

// Definition is a contest's game described as tables rather than as SQL, the
// builder source beside the editor script and an uploaded dump.
//
// It is not Schema: Schema is read back from a built database, while a
// Definition is the organiser's intent before any database exists. Foreign
// keys are not supported, since checking one needs the whole definition and
// a half-checked field would be worse than none.
type Definition struct {
	Tables []TableDefinition `json:"tables"`
}

// MaxDefinitionTables and MaxDefinitionTableColumns bound the definition
// (CLAUDE.md rule 2), far above what a real game needs.
const (
	MaxDefinitionTables       = 50
	MaxDefinitionTableColumns = 50
)

// MaxDefinitionBytes bounds the whole document once encoded (CLAUDE.md rule
// 2). The two counts above should be hit first; this is the backstop.
const MaxDefinitionBytes = 64 << 10

// Validate reports whether the definition describes a game the table builder
// can act on: plain, unique identifiers, known column types, primary key
// columns declared on their table, and the document within bounds. It runs on
// save, so the organiser sees a mistake at once rather than from a later build.
func (d Definition) Validate() error {
	if len(d.Tables) == 0 {
		return ErrDefinitionEmpty
	}
	if len(d.Tables) > MaxDefinitionTables {
		return fmt.Errorf("%w: %d tables, the limit is %d", ErrDefinitionTooLarge, len(d.Tables), MaxDefinitionTables)
	}

	seenTables := make(map[string]struct{}, len(d.Tables))
	for _, table := range d.Tables {
		if !sqlpolicy.PlainIdentifier(table.Name) {
			return fmt.Errorf("%w: table %q", ErrDefinitionInvalidName, table.Name)
		}
		foldedTable := strings.ToLower(table.Name)
		if _, dup := seenTables[foldedTable]; dup {
			return fmt.Errorf("%w: table %q", ErrDefinitionDuplicateName, table.Name)
		}
		seenTables[foldedTable] = struct{}{}

		if len(table.Columns) == 0 {
			return fmt.Errorf("%w: %q", ErrDefinitionTableEmpty, table.Name)
		}
		if len(table.Columns) > MaxDefinitionTableColumns {
			return fmt.Errorf("%w: table %q has %d columns, the limit is %d",
				ErrDefinitionTooLarge, table.Name, len(table.Columns), MaxDefinitionTableColumns)
		}

		// Uniqueness is case-folded, as PostgreSQL folds unquoted names.
		// Primary key membership is exact, because createTableStatement quotes
		// every name: a key "ID" would not match a column "id".
		seenColumns := make(map[string]struct{}, len(table.Columns))
		columnNames := make(map[string]struct{}, len(table.Columns))
		for _, column := range table.Columns {
			if !sqlpolicy.PlainIdentifier(column.Name) {
				return fmt.Errorf("%w: column %q of table %q", ErrDefinitionInvalidName, column.Name, table.Name)
			}
			foldedColumn := strings.ToLower(column.Name)
			if _, dup := seenColumns[foldedColumn]; dup {
				return fmt.Errorf("%w: column %q of table %q", ErrDefinitionDuplicateName, column.Name, table.Name)
			}
			seenColumns[foldedColumn] = struct{}{}
			columnNames[column.Name] = struct{}{}

			if !column.Type.valid() {
				return fmt.Errorf("%w: column %q of table %q has type %q",
					ErrDefinitionInvalidType, column.Name, table.Name, column.Type)
			}
		}

		seenKey := make(map[string]struct{}, len(table.PrimaryKey))
		for _, key := range table.PrimaryKey {
			foldedKey := strings.ToLower(key)
			if _, dup := seenKey[foldedKey]; dup {
				return fmt.Errorf("%w: table %q lists %q twice", ErrDefinitionInvalidPrimaryKey, table.Name, key)
			}
			seenKey[foldedKey] = struct{}{}
			if _, exists := columnNames[key]; !exists {
				return fmt.Errorf("%w: table %q, column %q", ErrDefinitionInvalidPrimaryKey, table.Name, key)
			}
		}
	}

	document, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encode the game definition: %w", err)
	}
	if len(document) > MaxDefinitionBytes {
		return fmt.Errorf("%w: %d bytes, the limit is %d", ErrDefinitionTooLarge, len(document), MaxDefinitionBytes)
	}
	return nil
}

// postgresType names the PostgreSQL type ColumnType generates. It is a closed
// switch, so CREATE TABLE only ever receives a keyword chosen here, never the
// organiser's string.
func (t ColumnType) postgresType() (string, error) {
	switch t {
	case ColumnInteger:
		return "integer", nil
	case ColumnText:
		return "text", nil
	case ColumnDate:
		return "date", nil
	case ColumnTimestamp:
		// Spelled as pg_dump spells it, so a generated schema reads like a
		// dumped one.
		return "timestamp without time zone", nil
	case ColumnNumeric:
		return "numeric", nil
	case ColumnBoolean:
		return "boolean", nil
	}
	return "", fmt.Errorf("%w: %q", ErrDefinitionInvalidType, t)
}

// SQL turns the definition into the CREATE TABLE statements that build it, in
// the public schema, deterministic for the same Definition. Every name is
// quoted, since a valid identifier such as "Suspects" or "order" still needs
// quotes to keep its case or to be usable at all. A definition with no tables
// returns ErrDefinitionEmpty rather than an empty database. The tables are
// created empty; their rows are loaded separately from CSV (tabledata.go).
func (d Definition) SQL() (string, error) {
	if len(d.Tables) == 0 {
		return "", ErrDefinitionEmpty
	}

	statements := make([]string, len(d.Tables))
	for i, table := range d.Tables {
		statement, err := table.createTableStatement()
		if err != nil {
			return "", err
		}
		statements[i] = statement
	}
	return strings.Join(statements, "\n"), nil
}

// sameStructure reports whether t and other describe the same table for
// ErrDefinitionTableLocked: identical columns in identical order, and the same
// set of primary key columns in any order.
func (t TableDefinition) sameStructure(other TableDefinition) bool {
	if len(t.Columns) != len(other.Columns) {
		return false
	}
	for i, c := range t.Columns {
		if c != other.Columns[i] {
			return false
		}
	}
	return sameNameSet(t.PrimaryKey, other.PrimaryKey)
}

// sameNameSet reports whether a and b name the same identifiers regardless of
// order. Spelling is compared exactly, not folded, because the names are
// quoted in PRIMARY KEY (...), where "ID" and "id" are different columns.
func sameNameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, k := range a {
		set[k] = struct{}{}
	}
	for _, k := range b {
		if _, ok := set[k]; !ok {
			return false
		}
	}
	return true
}

// createTableStatement is one table's CREATE TABLE statement.
func (t TableDefinition) createTableStatement() (string, error) {
	lines := make([]string, 0, len(t.Columns)+1)
	for _, column := range t.Columns {
		pgType, err := column.Type.postgresType()
		if err != nil {
			return "", fmt.Errorf("table %q: %w", t.Name, err)
		}
		line := "    " + sqlpolicy.QuoteIdentifier(column.Name) + " " + pgType
		if !column.Nullable {
			line += " NOT NULL"
		}
		lines = append(lines, line)
	}

	if len(t.PrimaryKey) > 0 {
		keys := make([]string, len(t.PrimaryKey))
		for i, key := range t.PrimaryKey {
			keys[i] = sqlpolicy.QuoteIdentifier(key)
		}
		lines = append(lines, "    PRIMARY KEY ("+strings.Join(keys, ", ")+")")
	}

	var b strings.Builder
	b.WriteString("CREATE TABLE public.")
	b.WriteString(sqlpolicy.QuoteIdentifier(t.Name))
	b.WriteString(" (\n")
	b.WriteString(strings.Join(lines, ",\n"))
	b.WriteString("\n);\n")
	return b.String(), nil
}
