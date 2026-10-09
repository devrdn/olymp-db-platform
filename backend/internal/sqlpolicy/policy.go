// Package sqlpolicy says what a participant's SQL is allowed to do, and
// decides whether a given query stays inside that.
//
// One description drives both this check and the GRANTs the database template
// is built with, so the two layers cannot drift apart.
//
// It does not talk to a database, execute anything or bound load: execution,
// timeouts and admission control belong to the Query Runner, rendering GRANTs
// to the provisioner. Being pure lets its security tests be exhaustive.
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
	// ModeReadOnly is the default: nothing is written back.
	ModeReadOnly Mode = "read_only"
	// ModeReadWrite lets a participant record notes, marks or views.
	ModeReadWrite Mode = "read_write"
)

// maxIdentifier is PostgreSQL's own limit on an unquoted name (NAMEDATALEN-1).
const maxIdentifier = 63

// MaxQueryBytes bounds one submitted query, before it is parsed or stored.
// It lives here, not in the checker, so the façade in the Core API can refuse
// an oversized query without linking the cgo parser (see
// queryrunner.Validator).
const MaxQueryBytes = 64 << 10

// DefaultDiskQuotaRatio is how many times its template a participant's
// database may grow to when nobody chose a number.
const DefaultDiskQuotaRatio = 5

// Policy is one olympiad's answer to "what may a participant's SQL do".
// The zero value is invalid rather than read-only: it becomes GRANTs, so a
// struct nobody filled in must fail loudly instead of picking a default.
type Policy struct {
	Mode Mode
	// WritableTables are the game tables INSERT/UPDATE/DELETE may touch.
	WritableTables []string
	// AllowCreateView lets a participant create views in schema `work` only.
	AllowCreateView bool
	// AllowOwnTables lets a participant keep their own tables, in schema
	// `work`, for notes and workings.
	AllowOwnTables bool
	// AllowTempTables permits temporary tables, which last one query.
	AllowTempTables bool
	// AllowCatalog covers the structural catalogs (pg_class, pg_attribute,
	// information_schema). The sensitive catalogs are never readable.
	AllowCatalog bool
	// DiskQuotaRatio is how many times the template's size a participant's
	// database may grow to; only writes can grow it. Zero means unset and the
	// service supplies the default, never "unlimited".
	DiskQuotaRatio int
}

// ReadOnly returns the default policy: read the game database, change nothing.
// The structural catalogs are readable, which the zero value would not give.
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

// Validate reports whether the policy describes one coherent set of rules: no
// write permission under read_only (it needs the writer role and a GRANT),
// and only plain table names, since they are interpolated into GRANTs.
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

// MayWriteTo reports whether the policy names this table. A missing schema
// means `public` on either side, and names compare case-folded, as PostgreSQL
// folds unquoted identifiers.
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
// one identifier, or a schema and an identifier. A second dot would be a
// database reference, which is refused.
func PlainTableName(name string) bool {
	schema, table, qualified := strings.Cut(name, ".")
	if !qualified {
		return PlainIdentifier(name)
	}
	return PlainIdentifier(schema) && PlainIdentifier(table)
}

// PlainIdentifier reports whether name is one unquoted PostgreSQL identifier:
// letters, digits and underscore, not starting with a digit. It guards every
// name interpolated into DDL. Anything needing quotes is refused rather than
// escaped, because an escaping bug is silent and a refusal is not.
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

// QuoteIdentifier double-quotes a name with any embedded quote doubled, as
// PostgreSQL does. It lives here because both internal/gamedb and
// internal/provisioning need it and gamedb already imports provisioning.
func QuoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// WorkSchema is where a participant's own objects live, the only schema the
// template grants CREATE on. The checker refuses the rest with a clear reason
// rather than leaving it to "permission denied".
const WorkSchema = "work"
