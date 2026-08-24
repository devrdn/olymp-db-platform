package api_test

import (
	"context"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

// testPassword is the password every fixture account is created with.
const testPassword = "correct horse battery staple"

// maxLoginAttempts mirrors the service's per-account throttle, so the handler
// tests can drive it without reaching into the package.
const maxLoginAttempts = 10

// apiSink swallows audit entries; the handler tests assert on responses, and
// the trail itself is covered where it is written.
type apiSink struct{}

func (apiSink) Append(context.Context, audit.Entry) error { return nil }

// noRoles answers every contest-role lookup with "not staff".
type noRoles struct{}

func (noRoles) ContestRole(context.Context, uuid.UUID, uuid.UUID) (rbac.ContestRole, error) {
	return rbac.RoleNone, nil
}
