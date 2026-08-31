package contests

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"
)

// SQL access modes, see docs/ARCHITECTURE.md §4.1.
const (
	// ModeReadOnly is the default: participants only query.
	ModeReadOnly = "read_only"
	// ModeReadWrite lets a contest hand out writes, scoped by the policy.
	ModeReadWrite = "read_write"
)

// ErrInvalidPolicy reports a policy that cannot be enforced as written.
var ErrInvalidPolicy = errors.New("sql policy is not valid")

// tableName is the shape a writable table may take: an optionally
// schema-qualified lowercase identifier.
//
// Stricter than PostgreSQL allows, on purpose. These names are turned into
// GRANT statements when the game template is built, where they cannot be
// passed as parameters; the narrow form is what keeps that construction safe
// whatever an organizer types into the form.
var tableName = regexp.MustCompile(`^[a-z_][a-z0-9_]*(\.[a-z_][a-z0-9_]*)?$`)

// maxWritableTables bounds the list, since every entry becomes a grant on
// every participant's database.
const maxWritableTables = 100

// SQLPolicy is how much SQL power a contest hands its participants.
//
// One row per contest, and the single description both the AST validator and
// the database grants are derived from — they cannot drift apart because there
// is nothing for them to drift from.
type SQLPolicy struct {
	ContestID uuid.UUID
	Mode      string
	// WritableTables are the game tables participants may write to.
	WritableTables []string
	// AllowCreateView lets a participant build views over the evidence.
	AllowCreateView bool
	// AllowOwnTables lets a participant keep notes in tables of their own.
	AllowOwnTables  bool
	AllowTempTables bool
	// AllowCatalog covers the structural catalogs (pg_class, information_schema)
	// that make browsing the schema possible. The sensitive ones — other
	// people's databases and activity — are closed in every mode and are not
	// represented here at all.
	AllowCatalog bool
	// DiskQuotaRatio caps an instance at N times the template size.
	DiskQuotaRatio int
	UpdatedBy      *uuid.UUID
	UpdatedAt      time.Time
}

// DefaultSQLPolicy is what a new contest gets: read-only, catalogs open.
func DefaultSQLPolicy(contestID uuid.UUID) SQLPolicy {
	return SQLPolicy{
		ContestID:      contestID,
		Mode:           ModeReadOnly,
		WritableTables: []string{},
		AllowCatalog:   true,
		DiskQuotaRatio: 5,
	}
}

// Validate checks that the policy is coherent and enforceable.
func (p SQLPolicy) Validate() error {
	if !slices.Contains([]string{ModeReadOnly, ModeReadWrite}, p.Mode) {
		return fmt.Errorf("%w: unknown mode %q", ErrInvalidPolicy, p.Mode)
	}
	if p.DiskQuotaRatio <= 0 {
		return fmt.Errorf("%w: the disk quota ratio must be positive", ErrInvalidPolicy)
	}

	// A read-only contest carrying write permissions is not a policy but a
	// contradiction: one of the two halves would have to win silently when the
	// template grants are generated.
	if p.Mode == ModeReadOnly {
		switch {
		case len(p.WritableTables) > 0:
			return fmt.Errorf("%w: a read-only contest has no writable tables", ErrInvalidPolicy)
		case p.AllowCreateView, p.AllowOwnTables, p.AllowTempTables:
			return fmt.Errorf("%w: creating objects requires read_write mode", ErrInvalidPolicy)
		}
	}

	if len(p.WritableTables) > maxWritableTables {
		return fmt.Errorf("%w: at most %d writable tables", ErrInvalidPolicy, maxWritableTables)
	}
	seen := make(map[string]struct{}, len(p.WritableTables))
	for _, table := range p.WritableTables {
		if !tableName.MatchString(table) {
			return fmt.Errorf("%w: %q is not a valid table name", ErrInvalidPolicy, table)
		}
		if _, duplicate := seen[table]; duplicate {
			return fmt.Errorf("%w: table %q listed twice", ErrInvalidPolicy, table)
		}
		seen[table] = struct{}{}
	}
	return nil
}

// PolicyStore stores the SQL access policy of a contest.
//
// Separate from Repository because it has a different consumer: the game loop
// reads the policy to build grants and configure the validator, and has no
// business with titles or schedules.
type PolicyStore interface {
	// ByContest returns the contest's policy. A contest that has never been
	// configured reports DefaultSQLPolicy rather than an error: read-only is
	// what an unconfigured contest means.
	ByContest(ctx context.Context, contestID uuid.UUID) (SQLPolicy, error)
	// Save stores the policy.
	Save(ctx context.Context, p SQLPolicy) error
}

// auditFields is the part of a policy that may be written to the audit trail —
// which is all of it, since a policy is nothing but configuration.
func (p SQLPolicy) auditFields() map[string]any {
	return map[string]any{
		"mode":              p.Mode,
		"writable_tables":   p.WritableTables,
		"allow_create_view": p.AllowCreateView,
		"allow_own_tables":  p.AllowOwnTables,
		"allow_temp_tables": p.AllowTempTables,
		"allow_catalog":     p.AllowCatalog,
		"disk_quota_ratio":  p.DiskQuotaRatio,
	}
}
