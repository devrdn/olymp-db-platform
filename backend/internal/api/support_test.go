package api_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/api"
	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation/participationtest"
	"github.com/devrdn/olymp-db-platform/backend/internal/rbac"
	"github.com/google/uuid"
)

const testPassword = "correct horse battery staple"

// sessionName is the session cookie's name under the writer every test router
// is given.
var sessionName = auth.NewCookieWriter(false).SessionName()

func assertNoSessionCookie(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionName && c.Value != "" {
			t.Errorf("a session cookie was set: %+v", c)
		}
	}
}

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

func (b *logBuffer) loggedError(message string) bool { return b.loggedAt("ERROR", message) }

func (b *logBuffer) loggedWarning(message string) bool { return b.loggedAt("WARN", message) }

func (b *logBuffer) loggedAt(level, message string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, line := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(line, `"level":"`+level+`"`) && strings.Contains(line, message) {
			return true
		}
	}
	return false
}

// assertRetryAfter fails t unless a refusal says to wait want, in seconds as
// the header carries it.
func assertRetryAfter(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if got := rec.Header().Get("Retry-After"); got != want {
		t.Errorf("Retry-After = %q, want %q", got, want)
	}
}

// assertRetryAfterWithin fails t unless a refusal says to wait more than over
// and at most within: the rest of the window that refused it, which has
// already begun to run.
func assertRetryAfterWithin(t *testing.T, rec *httptest.ResponseRecorder, over, within time.Duration) {
	t.Helper()
	seconds, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || time.Duration(seconds)*time.Second <= over || time.Duration(seconds)*time.Second > within {
		t.Errorf("Retry-After = %q, want more than %s and at most %s", rec.Header().Get("Retry-After"), over, within)
	}
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

// Admission is the real participation module over a roster a test stages,
// so the gate decides every refusal; the protocol itself is tested in
// internal/participation.

// openUntil is a contest end no test here mistakes for closed.
var openUntil = time.Now().Add(24 * time.Hour)

// readBudget is the read budget a fixture admits with: it counts what was
// spent, and refuses with refuse once that is set.
type readBudget struct {
	mu     sync.Mutex
	spent  int
	refuse error
}

func (b *readBudget) Charge(context.Context, uuid.UUID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent++
	return b.refuse
}

func (b *readBudget) charged() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

func (b *readBudget) refuseWith(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refuse = err
}

// clock is the instant a fixture's admitter asks the gate at; zero is the
// wall clock, so a fixed-date contest does not expire as the calendar moves.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() {
		return time.Now().UTC()
	}
	return c.at
}

func (c *clock) set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = at
}

// stageRefusal stages on roster the state admission refuses with err: a
// refusal of the gate as that state, anything else as the lookup failing.
// nil finds the staged state again.
func stageRefusal(roster *participationtest.Roster, err error) {
	for _, refusal := range []error{
		contests.ErrNotAParticipant, contests.ErrParticipantFinished, contests.ErrContestEnded,
		contests.ErrContestNotRunning, contests.ErrDeadlinePassed, contests.ErrAddressNotAllowed,
	} {
		if errors.Is(err, refusal) {
			roster.Refuse(err)
			return
		}
	}
	roster.Fail(err)
}

// admitting is admission over roster, at now (nil is the wall clock), with a
// read budget that refuses nothing.
func admitting(roster *participationtest.Roster, now func() time.Time) api.Participation {
	return api.Participation{Admitter: participationtest.NewAdmitter(roster, nil, now), Reads: &readBudget{}}
}

// admittedAs is admission that finds participant in contest for every account.
func admittedAs(contest contests.Contest, participant contests.Participant, now func() time.Time) api.Participation {
	roster := participationtest.NewRoster()
	roster.Stage(contest, participant)
	return admitting(roster, now)
}
