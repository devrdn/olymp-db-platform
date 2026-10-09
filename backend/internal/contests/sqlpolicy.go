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

// tableName is an optionally schema-qualified lowercase identifier. Stricter
// than PostgreSQL because these names are spliced into GRANT statements, where
// parameters cannot be used.
var tableName = regexp.MustCompile(`^[a-z_][a-z0-9_]*(\.[a-z_][a-z0-9_]*)?$`)

// maxWritableTables bounds the list, since every entry becomes a grant on
// every participant's database.
const maxWritableTables = 100

// SQLPolicy is how much SQL power a contest hands its participants. Both the
// AST validator and the database grants derive from it, so they cannot drift.
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
	// AllowCatalog covers the structural catalogs (pg_class,
	// information_schema). Catalogs exposing other databases and activity are
	// closed in every mode.
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

	// Write permissions on a read-only contest are a contradiction one half
	// would silently win when grants are generated.
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

// PolicyStore stores the SQL access policy of a contest, apart from
// Repository because the game loop reads it and needs nothing else.
type PolicyStore interface {
	// ByContest returns the contest's policy, or DefaultSQLPolicy for a
	// contest never configured.
	ByContest(ctx context.Context, contestID uuid.UUID) (SQLPolicy, error)
	Save(ctx context.Context, p SQLPolicy) error
}

// auditFields is the whole policy, for the audit trail.
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
