package api_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

const testPassword = "correct horse battery staple"

// maxLoginAttempts mirrors the service's per-account throttle.
const maxLoginAttempts = 10

// apiSink swallows audit entries; the trail is tested where it is written.
type apiSink struct{}

func (apiSink) Append(context.Context, audit.Entry) error { return nil }

func (apiSink) AppendMany(context.Context, []audit.Entry) error { return nil }

// noRoles answers every contest-role lookup with "not staff".
type noRoles struct{}

func (noRoles) ContestRole(context.Context, uuid.UUID, uuid.UUID) (rbac.ContestRole, error) {
	return rbac.RoleNone, nil
}

// logBuffer collects what a handler logged, safe for a handler writing from its
// own goroutine.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

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

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	body := decode(t, rec)
	detail, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("response carries no error object: %s", rec.Body.String())
	}
	code, _ := detail["code"].(string)
	return code
}

func errorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	body := decode(t, rec)
	detail, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("response carries no error object: %s", rec.Body.String())
	}
	message, _ := detail["message"].(string)
	return message
}
