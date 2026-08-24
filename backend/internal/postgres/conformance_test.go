package postgres

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// These assertions are the seam's guard rail. The domain packages declare what
// they need; if an implementation drifts from an interface, the build fails
// here rather than at wiring time in main.
var (
	_ users.Repository       = (*Users)(nil)
	_ audit.Sink             = (*AuditSink)(nil)
	_ rbac.ContestRoleLoader = (*ContestRoles)(nil)
)

func TestImplementationsSatisfyTheDomainInterfaces(t *testing.T) {
	// The assertions above do the work at compile time; this test exists so
	// the intent is visible in the test output and cannot be deleted as an
	// "unused" declaration.
	t.Log("postgres implementations satisfy users.Repository, audit.Sink and rbac.ContestRoleLoader")
}
