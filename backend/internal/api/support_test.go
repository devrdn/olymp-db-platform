package api_test

import (
	"bytes"
	"context"
	"strings"
	"sync"

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

func (apiSink) AppendMany(context.Context, []audit.Entry) error { return nil }

// noRoles answers every contest-role lookup with "not staff".
type noRoles struct{}

func (noRoles) ContestRole(context.Context, uuid.UUID, uuid.UUID) (rbac.ContestRole, error) {
	return rbac.RoleNone, nil
}

// logBuffer collects what a handler logged, safely for a handler writing from
// its own goroutine, so a test can assert that an operator was told.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// loggedError reports whether an Error-level line carrying message was written.
func (b *logBuffer) loggedError(message string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, line := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(line, `"level":"ERROR"`) && strings.Contains(line, message) {
			return true
		}
	}
	return false
}
