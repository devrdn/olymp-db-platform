// Package sqlpolicy says what a participant's SQL is allowed to do, and
// decides whether a given query stays inside that.
//
// It answers two questions the architecture deliberately keeps together
// (section 4.1): what an olympiad permits, and whether this statement is
// within it. They live in one package because the defence is layered — the
// same description drives both the check performed here and the GRANTs the
// database template is built with, and two layers meant to agree must not be
// able to drift apart.
//
// What it deliberately does not do: talk to a database, execute anything, or
// decide how much load a query may impose. Execution, timeouts and admission
// control belong to the Query Runner; rendering GRANTs belongs to the
// provisioner. This package is pure, which is what lets its security tests be
// exhaustive.
package sqlpolicy

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidPolicy marks a policy that does not describe a coherent olympiad.
var ErrInvalidPolicy = errors.New("invalid sql policy")

// Mode is the coarse setting an organizer picks; everything else refines it.
type Mode string

const (
	// ModeReadOnly is the default and covers the basic contest: the story is
	// read out of the database, nothing is written back.
	ModeReadOnly Mode = "read_only"
	// ModeReadWrite is for an olympiad that asks a participant to record
	// something — notes, marks on evidence, a view built along the way.
	ModeReadWrite Mode = "read_write"
)

// maxIdentifier is PostgreSQL's own limit on an unquoted name (NAMEDATALEN-1).
const maxIdentifier = 63

// MaxQueryBytes bounds one submitted query, before it is parsed or stored.
//
// Declared here rather than inside the checker, even though the checker is
// what enforces it against the parser: the façade in front of the Query
// Runner refuses an oversized query before writing it anywhere, and it must
// not link the checker to know the same number — the checker carries
// PostgreSQL's own parser through cgo, and the façade is compiled into the
// Core API, which must not (see internal/sqlpolicy/checker's own doc
// comment on maxQueryBytes, and CLAUDE.md's Go layout rule 7). One constant
// in the package both already import is what keeps a query that passes the
// façade's check from being refused for length a second time downstream.
const MaxQueryBytes = 64 << 10

// DefaultDiskQuotaRatio is how many times its template a participant's
// database may grow to when nobody chose a number. Five is the figure section
// 4.1 names.
const DefaultDiskQuotaRatio = 5

// Policy is one olympiad's answer to "what may a participant's SQL do".
//
// The zero value is deliberately invalid rather than read-only. A struct
// nobody filled in should fail loudly at the boundary, not silently pick the
// safe-looking option: the same value is about to be turned into GRANTs, and
// "whatever the caller forgot to say" is not a permission set anyone reviewed.
type Policy struct {
	Mode Mode
	// WritableTables are the game tables INSERT/UPDATE/DELETE may touch.
	// Anything outside the list is refused here and ungranted in the template.
	WritableTables []string
	// AllowCreateView lets a participant build their own VIEW, in schema
	// `work` — never over the game schema, which they have no CREATE on.
	AllowCreateView bool
	// AllowOwnTables lets them keep their own tables for notes and workings.
	AllowOwnTables bool
	// AllowTempTables lets them use temporary tables within one query.
	AllowTempTables bool
	// AllowCatalog covers the *structural* catalogs — pg_class, pg_attribute,
	// information_schema — which are on by default because reading the shape
	// of a table is part of the exercise. The sensitive catalogs are not
	// governed by this flag and are never readable.
	AllowCatalog bool
	// DiskQuotaRatio is how many times the template's size a participant's
	// database may grow to. Only meaningful where writing is permitted, since
	// nothing else can make one grow. Zero means the caller did not say, and
	// the service supplies the default rather than treating it as unlimited.
	DiskQuotaRatio int
}

// ReadOnly returns the default policy: read the game database, change nothing.
//
// A constructor rather than a zero value, because the safe default is not the
// zero of every field — structural catalogs are readable, and `false` there
// would be a stricter contest than anybody asked for.
func ReadOnly() Policy {
	return Policy{Mode: ModeReadOnly, AllowCatalog: true, DiskQuotaRatio: DefaultDiskQuotaRatio}
}

// ReadWrite returns a policy that permits writing to exactly these tables.
func ReadWrite(tables ...string) Policy {
	p := ReadOnly()
	p.Mode = ModeReadWrite
	p.WritableTables = tables
	return p
}

// Validate reports whether the policy describes one coherent set of rules.
//
// Two kinds of incoherence are caught. The first is a permission the mode does
// not support: every option below needs the writer role and a GRANT, so
// granting one under `read_only` produces a policy that means one thing to the
// checker and another to the template builder. The second is a table name that
// is not a plain identifier — those names are interpolated into GRANT
// statements when the template is built, because SQL has no parameter binding
// for an identifier, and refusing them once here is what keeps every place
// that renders them safe.
func (p Policy) Validate() error {
	switch p.Mode {
	case ModeReadOnly:
		for _, granted := range []struct {
			what string
			on   bool
		}{
			{"writable tables", len(p.WritableTables) > 0},
			{"creating views", p.AllowCreateView},
			{"own tables", p.AllowOwnTables},
			{"temporary tables", p.AllowTempTables},
		} {
			if granted.on {
				return fmt.Errorf("%w: %s needs mode %q", ErrInvalidPolicy, granted.what, ModeReadWrite)
			}
		}
	case ModeReadWrite:
	default:
		return fmt.Errorf("%w: unknown mode %q", ErrInvalidPolicy, p.Mode)
	}

	seen := make(map[string]struct{}, len(p.WritableTables))
	for _, table := range p.WritableTables {
		if !PlainTableName(table) {
			return fmt.Errorf("%w: %q is not a plain table name", ErrInvalidPolicy, table)
		}
		folded := strings.ToLower(table)
		if _, already := seen[folded]; already {
			return fmt.Errorf("%w: table %q is listed twice", ErrInvalidPolicy, table)
		}
		seen[folded] = struct{}{}
	}
	return nil
}

// MayWriteTo reports whether the policy names this table.
//
// Either side may carry a schema, and an absent one means `public` on both:
// a contest naming `evidence` and a participant writing `public.evidence` mean
// the same table, and so do the other way round. Folded, because PostgreSQL
// lowercases an unquoted identifier and the parse tree hands over the folded
// form.
func (p Policy) MayWriteTo(schema, table string) bool {
	if p.Mode != ModeReadWrite {
		return false
	}
	if schema == "" {
		schema = "public"
	}
	for _, named := range p.WritableTables {
		namedSchema, namedTable, qualified := strings.Cut(named, ".")
		if !qualified {
			namedSchema, namedTable = "public", named
		}
		if strings.EqualFold(namedSchema, schema) && strings.EqualFold(namedTable, table) {
			return true
		}
	}
	return false
}

// PlainTableName reports whether name is a table a GRANT can safely name:
// one identifier, or a schema and an identifier.
//
// Qualification is allowed because a contest's game schema need not be
// `public` — the rest of the platform already stores names like
// `public.evidence` — and refused beyond one dot, because two would be a
// database reference and this cluster has no business with those.
func PlainTableName(name string) bool {
	schema, table, qualified := strings.Cut(name, ".")
	if !qualified {
		return PlainIdentifier(name)
	}
	return PlainIdentifier(schema) && PlainIdentifier(table)
}

// PlainIdentifier reports whether name is one unquoted PostgreSQL identifier.
//
// Exported because the rule is needed wherever a name must be interpolated
// into DDL, which SQL gives no way to bind: a table name here, a database name
// in internal/gamedb. One rule rather than two that agree today.
//
// An allow-list of characters, like everything else here: letters, digits and
// underscore, not starting with a digit. Anything needing quotes — a space, a
// dot, a semicolon, a quote of its own — is refused rather than escaped,
// because an escaping bug is silent and a refusal is not.
func PlainIdentifier(name string) bool {
	if name == "" || len(name) > maxIdentifier {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// QuoteIdentifier spells a name the way PostgreSQL does in its own dumps —
// double-quoted, with an embedded quote doubled.
//
// Lives beside PlainIdentifier rather than in internal/gamedb, which carried
// the original: the table builder's generated SQL (internal/provisioning)
// needs the identical quoting and cannot import gamedb to get it — gamedb
// already imports provisioning, to satisfy SchemaSource with provisioning's
// own Schema type (internal/gamedb/schema.go), and Go refuses the cycle the
// other direction would make. This package has no such dependency either
// way, which is what makes it the shared home rather than either domain
// package reaching into the other's.
//
// There is one copy and no forwarder: gamedb's own version was removed when
// this one landed, and every caller — gamedb's grants, COPY and CREATE
// DATABASE statements included — names this one directly.
func QuoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// WorkSchema is where a participant's own objects live. The template grants
// CREATE on it and on nothing else, so this and internal/gamedb are naming the
// same schema — the validator refuses what the privileges would refuse anyway,
// with a sentence instead of "permission denied".
const WorkSchema = "work"
