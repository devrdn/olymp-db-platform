package provisioning

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// Why a definition could not be saved. Named so a handler's fail switch can
// map each to its own status rather than serving "internal error" for what
// is, in every case, a mistake in what an organiser typed (CLAUDE.md rule 1).
var (
	// ErrDefinitionEmpty is a definition with no tables at all.
	ErrDefinitionEmpty = errors.New("the game definition has no tables")
	// ErrDefinitionTooLarge is a definition past MaxDefinitionTables, a table
	// past MaxDefinitionTableColumns, or the whole document past
	// MaxDefinitionBytes once encoded.
	ErrDefinitionTooLarge = errors.New("the game definition is larger than this platform allows")
	// ErrDefinitionInvalidName is a table or column name that is not a plain
	// PostgreSQL identifier (sqlpolicy.PlainIdentifier) — checked here,
	// before anything is stored, because the name is destined to be
	// interpolated into a CREATE TABLE statement by the SQL-generation task
	// that follows this one, and SQL has no parameter binding for an
	// identifier.
	ErrDefinitionInvalidName = errors.New("a table or column name is not a plain identifier")
	// ErrDefinitionDuplicateName is a table named twice, or a column named
	// twice within one table — folded the way PostgreSQL folds an unquoted
	// identifier, so `Guests` and `guests` collide exactly as they would in
	// the database this definition is meant to build.
	ErrDefinitionDuplicateName = errors.New("the game definition names the same table or column twice")
	// ErrDefinitionTableEmpty is a table with no columns. `CREATE TABLE t ()`
	// is not valid PostgreSQL, so a table with nothing in it is refused here
	// rather than becoming a build failure the organiser cannot see coming.
	ErrDefinitionTableEmpty = errors.New("a table has no columns")
	// ErrDefinitionInvalidType is a column whose type is not one of the
	// closed set ColumnType declares.
	ErrDefinitionInvalidType = errors.New("a column's type is not one this platform supports")
	// ErrDefinitionInvalidPrimaryKey is a primary key that names a column
	// its own table does not have, or names the same column twice.
	ErrDefinitionInvalidPrimaryKey = errors.New("the primary key names a column the table does not have")
)

// ColumnType is the closed set of column types the table builder may
// describe. A value from this set, never a string interpolated into SQL —
// the point of it being a distinct type rather than plain text, and the
// reason Validate refuses anything outside it before a definition is ever
// stored.
//
// Deliberately small for a first version: what a detective game needs to
// hold a suspect's name, the time a witness saw them, and whether the alibi
// checks out. Widened later, the same way sqlpolicy.Mode's own closed set
// could be — a value this platform never emits, in a column nothing yet
// reads, adds nothing a wider set would not have covered from the start.
type ColumnType string

const (
	// ColumnInteger is a whole number — a suspect's age, an evidence tag's
	// sequence number.
	ColumnInteger ColumnType = "integer"
	// ColumnText is free text of unbounded length — a name, a statement, an
	// address.
	ColumnText ColumnType = "text"
	// ColumnDate is a calendar date with no time of day — a birth date, the
	// date a case was opened.
	ColumnDate ColumnType = "date"
	// ColumnTimestamp is a date and time together — when a witness says they
	// saw something, down to the minute.
	ColumnTimestamp ColumnType = "timestamp"
	// ColumnNumeric is an exact decimal number — an amount of money, a
	// measurement — where floating-point rounding would be the wrong
	// arithmetic for a participant's query to hit.
	ColumnNumeric ColumnType = "numeric"
	// ColumnBoolean is a two-valued flag — whether an alibi was confirmed.
	ColumnBoolean ColumnType = "boolean"
)

// ColumnTypes lists every value of the closed set above, in the order they
// are declared. This is what internal/api's builder-limits response walks
// to tell a browser which types exist (CLAUDE.md rule 11): the alternative
// is a client keeping its own literal list of "the six types this platform
// accepts", which is exactly the second copy of a fact rule 11 exists to
// rule out — this is the one place that set is written down as data.
var ColumnTypes = []ColumnType{
	ColumnInteger, ColumnText, ColumnDate, ColumnTimestamp, ColumnNumeric, ColumnBoolean,
}

// valid reports whether t is one of the closed set above. Unexported: the
// set itself is the public contract (Validate is where a caller learns a
// type was rejected), not a membership test callers should be branching on.
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
	// Nullable says whether the generated column may hold NULL. False is the
	// stricter default: a detective game's own tables are usually facts that
	// are either known or the row would not exist, and an organiser has to
	// opt into NULL rather than get it by omission.
	Nullable bool `json:"nullable,omitempty"`
}

// TableDefinition is one table an organiser described structurally.
type TableDefinition struct {
	Name    string             `json:"name"`
	Columns []ColumnDefinition `json:"columns"`
	// PrimaryKey names the columns that together identify a row, in the
	// order they will appear in the generated PRIMARY KEY clause. Every name
	// in it must also appear in Columns — Validate is what checks that a
	// definition may not otherwise say without contradicting itself.
	PrimaryKey []string `json:"primary_key,omitempty"`
}

// Definition is a contest's game, described as tables rather than as SQL —
// the third way a game may be built (migration 26 / TemplateSource.
// SourceBuilder), alongside the editor (SourceEditor) and an uploaded dump
// (SourceFile). What SQL it turns into is a later task's own work: this type
// only says what may be saved, and Validate says what may not.
//
// Deliberately not Schema (schema.go), even though the two shapes look
// alike. Schema is read back from a real database after a build — it
// carries a foreign key's own target because the catalogue already knows
// it, and it is bounded for what a jsonb cache and a participant's browser
// can hold. Definition is written before any database exists, describes
// intent rather than fact, and is bounded for a staff member's own screen.
// Confusing the two would have the console showing an organiser's typo as
// if PostgreSQL had already accepted it.
//
// Foreign keys between the game's own tables are deliberately not part of
// this version. A foreign key naming another table in the same definition
// needs two things checked before it means anything — that the target table
// and column exist, and that the two columns' types are compatible — and
// both questions can only be answered against the *whole* definition, not
// one table read in isolation the way everything else here validates. That
// is buildable, but it is not this task's own scope, and half-validating a
// foreign key (accepting the field, checking neither property) would be
// worse than refusing it: the SQL-generation task that follows this one
// would then have to decide whether to trust a field this layer never
// actually checked. Left out entirely, so the next task plans its
// generation around a definition that has no field meaning something this
// layer did not verify.
type Definition struct {
	Tables []TableDefinition `json:"tables"`
}

// MaxDefinitionTables and MaxDefinitionTableColumns bound the definition
// (CLAUDE.md rule 2). The definition is written once by a staff member
// setting up an olympiad, not by a participant, but rule 2 is not about an
// adversary here either: a jsonb column has no ceiling of its own, and
// nothing else stops a definition from growing without limit if a person
// mis-clicks "add table" in a loop.
//
// Fifty of each is far above the design's own example game (seven tables) and
// far above anything a detective story needs — a contest with fifty tables of
// fifty columns is not a puzzle a participant can hold in their head during a
// timed round — while staying generous enough that no genuine game plan is
// ever the one refused.
const (
	MaxDefinitionTables       = 50
	MaxDefinitionTableColumns = 50
)

// MaxDefinitionBytes bounds the whole document once encoded (CLAUDE.md rule
// 2), the same way MaxScriptBytes bounds an editor's own script and
// MaxUploadFilenameBytes bounds a filename: the column behind it is an
// unbounded jsonb, and the request body limit bounds the request that
// carries a definition in, not this field once it is decoded.
//
// Sixty-four kibibytes is "the tens of kilobytes" this feature was scoped
// for: MaxDefinitionTables × MaxDefinitionTableColumns is 2,500 columns at
// the absolute ceiling, and even a verbose column name and type leave that
// document at a small fraction of this bound — so the byte bound is not
// expected to be the one an organiser ever actually hits; it exists as the
// backstop for whichever of the two counts above turns out to have been set
// too generously.
const MaxDefinitionBytes = 64 << 10

// Validate reports whether the definition describes a game the table
// builder can act on: every table and column name a plain identifier, no
// name repeated, every column a type from the closed set, every primary key
// column actually declared on its own table, and the whole document within
// bound.
//
// Checked here, the moment an organiser saves it, rather than when a
// background build eventually turns it into SQL (a later task's own work) —
// the same reason SetScript refuses an empty or oversized script before
// anything is asked, rather than inside Build. A mistake belongs to whoever
// made it at the moment they made it, not ten minutes later out of a queue
// they are no longer watching.
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

		seenColumns := make(map[string]struct{}, len(table.Columns))
		for _, column := range table.Columns {
			if !sqlpolicy.PlainIdentifier(column.Name) {
				return fmt.Errorf("%w: column %q of table %q", ErrDefinitionInvalidName, column.Name, table.Name)
			}
			foldedColumn := strings.ToLower(column.Name)
			if _, dup := seenColumns[foldedColumn]; dup {
				return fmt.Errorf("%w: column %q of table %q", ErrDefinitionDuplicateName, column.Name, table.Name)
			}
			seenColumns[foldedColumn] = struct{}{}

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
			if _, exists := seenColumns[foldedKey]; !exists {
				return fmt.Errorf("%w: table %q, column %q", ErrDefinitionInvalidPrimaryKey, table.Name, key)
			}
		}
	}

	// The byte bound is checked last and against the whole document, because
	// it is the backstop for the two counts above rather than a
	// per-table rule — see MaxDefinitionBytes's own doc.
	document, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encode the game definition: %w", err)
	}
	if len(document) > MaxDefinitionBytes {
		return fmt.Errorf("%w: %d bytes, the limit is %d", ErrDefinitionTooLarge, len(document), MaxDefinitionBytes)
	}
	return nil
}

// postgresType names the PostgreSQL type ColumnType generates.
//
// A closed switch, never the value round-tripped as a string: CLAUDE.md rule
// 14 is about cutting SQL where the parser says a statement ends, and the
// same reasoning applies one level up — an organiser's JSON says "text", and
// what reaches CREATE TABLE is the literal keyword this switch chose for it,
// not their string spliced in. ErrDefinitionInvalidType guards a value
// outside the six Validate already accepts; SQL's own caller
// (Games.finishDefinitionBuild) only ever hands this a definition that
// passed Validate, so reaching the default case would mean a row was stored
// before this switch knew about a type Validate had already let through —
// worth refusing loudly rather than emitting a column of some type nobody
// declared.
func (t ColumnType) postgresType() (string, error) {
	switch t {
	case ColumnInteger:
		return "integer", nil
	case ColumnText:
		return "text", nil
	case ColumnDate:
		return "date", nil
	case ColumnTimestamp:
		// Spelled out, the way pg_dump spells it: an organiser comparing a
		// generated schema against a dumped one should not have to know that
		// the bare word means the same thing.
		return "timestamp without time zone", nil
	case ColumnNumeric:
		return "numeric", nil
	case ColumnBoolean:
		return "boolean", nil
	}
	return "", fmt.Errorf("%w: %q", ErrDefinitionInvalidType, t)
}

// SQL turns the definition into the CREATE TABLE statements that build it —
// the table builder's own answer to the SQL an editor's game already has, or
// an uploaded dump's own bytes: gamedb.Provisioner.BuildTemplate cannot tell
// the three apart, and does not need to (Games.finishDefinitionBuild feeds
// this straight into the same BuildTemplate an editor-sourced script runs
// through, wrapped in strings.NewReader exactly as claimed.Script already
// is).
//
// Deterministic, byte for byte, for the same Definition. There is nothing
// here for two calls to disagree about: Tables and TableDefinition.Columns
// are ordered slices an organiser arranged, never a map, and this walks them
// in that order and nothing else varies the output — no timestamp, no
// generated id, no map iteration.
//
// Schema-qualified as `public.<name>`, the schema an uploaded dump's own
// COPY blocks already name (see the dump examples in
// game_integration_test.go) — so a table built this way sits exactly where
// one written by hand or restored from a file would.
//
// Every name — table or column — goes through sqlpolicy.QuoteIdentifier
// unconditionally (CLAUDE.md rule 14: SQL is not assembled by splicing
// user-chosen text into it as-is). Validate already refused anything that is
// not a plain identifier before a definition could be saved, but quoting
// does not become conditional on that: an organiser's "Suspects" or
// "order" — both valid identifiers, both needing quotes to keep their case
// or to be usable as a table name at all — is exactly the case this
// protects, and quoting an identifier that never needed it costs nothing.
//
// ErrDefinitionEmpty guards a definition with no tables reaching here at
// all. Validate refuses that before a save takes hold (its own doc explains
// why), so this is a build-time backstop for a row that should not exist
// rather than a path an organiser can hit through the ordinary form — but
// finishDefinitionBuild must still refuse with this sentinel rather than
// silently building an empty database that answers every question "no such
// table" (CLAUDE.md rule 1): the mistake is the definition's, not this
// installation's, so it belongs on the organiser's own screen, worded
// plainly, and not behind BuildFailedInternally.
//
// What this does not do: load a single row. Every table it creates is
// empty. A table builder's own data lands as CSV files on a volume
// (internal/gamefile, tabledata.go) and is loaded separately at the seam
// named in finishDefinitionBuild's own doc, at the one point after these
// statements have run where the tables exist and nothing has been marked
// ready yet — a table nobody uploaded data for simply stays empty.
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

// createTableStatement is one table's own CREATE TABLE — the helper SQL
// above calls once per table, kept on TableDefinition rather than inlined
// into that loop because a single table's statement is what a future
// foreign-key or data-loading change would need to touch (Definition's own
// doc explains why neither is this task's scope), and a method with a name
// of its own is where that change would look first.
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
