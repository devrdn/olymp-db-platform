package api_test

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/api"
	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/clarifications"
	"github.com/devrdn/olymp-db-platform/backend/internal/clarifications/clarificationstest"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests/conteststest"
	"github.com/devrdn/olymp-db-platform/backend/internal/monitor"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation/participationtest"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/cache"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/logging"
	"github.com/devrdn/olymp-db-platform/backend/internal/queryproxy"
	"github.com/devrdn/olymp-db-platform/backend/internal/rbac"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/devrdn/olymp-db-platform/backend/internal/users/userstest"
	"github.com/devrdn/olymp-db-platform/backend/internal/workspace"
	"github.com/devrdn/olymp-db-platform/backend/internal/workspace/workspacetest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The route's budget is charged before the contest identifier is even
// parsed (CLAUDE.md rule 13): over budget, a malformed identifier is refused
// for the rate; within it, as malformed, and it is looked up nowhere.
func TestAMalformedContestIdentifierSpendsTheRoutesBudgetFirst(t *testing.T) {
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/contests/not-a-uuid/play/story", ""},
		{http.MethodGet, "/contests/not-a-uuid/play/log.csv", ""},
		{http.MethodPost, "/contests/not-a-uuid/questions/" + "not-a-uuid" + "/answer", `{"value":"x"}`},
	} {
		t.Run(route.path, func(t *testing.T) {
			f := newParticipantFixture(t)

			expectStatus(t, f.send(route.method, route.path, route.body), http.StatusBadRequest, "invalid_contest_id")
			if f.reads.charged() != 1 {
				t.Fatalf("the read budget was charged %d times for a malformed identifier, want 1", f.reads.charged())
			}

			f.reads.refuseWith(participation.ErrTooManyReads)
			expectStatus(t, f.send(route.method, route.path, route.body), http.StatusTooManyRequests, "play_read_too_often")
			if f.roster.Lookups() != 0 {
				t.Fatalf("a malformed identifier was looked up %d times", f.roster.Lookups())
			}
		})
	}
}

// The workspace's and the signals' own budgets are charged before the parse
// too: past their rate a malformed identifier hears the rate.
func TestAMalformedContestIdentifierSpendsAWritesOwnBudgetFirst(t *testing.T) {
	f := newParticipantFixture(t)
	for range monitor.BatchesPerMinute {
		f.send(http.MethodPost, "/contests/not-a-uuid/play/signals", signalsBody(goodAbsence))
	}
	expectStatus(t, f.send(http.MethodPost, "/contests/not-a-uuid/play/signals", signalsBody(goodAbsence)),
		http.StatusTooManyRequests, "signals_too_often")
}

// staffRoutesOnParticipantMounts are the routes the participant mounts
// register that serve staff through a permission, not admission. A route
// missing from both this list and admission fails
// TestEveryParticipantRouteIsAdmittedFirst.
var staffRoutesOnParticipantMounts = map[string]bool{
	"GET /contests/{contestID}/clarifications":                           true,
	"POST /contests/{contestID}/clarifications/{clarificationID}/answer": true,
	"PUT /contests/{contestID}/clarifications/{clarificationID}/answer":  true,
	"GET /contests/{contestID}/announcements":                            true,
	"POST /contests/{contestID}/announcements":                           true,
}

// Admission is a call each participant handler makes first, not something
// the mounts apply, so this walks every route the handlers newParticipantMounts
// builds register: a caller on no roster hears 403 not_a_participant, and once
// the route's budget of one is spent the next request hears 429 before
// anything is looked up. A route added without admission answers neither. A
// new participant handler or With… mount wired in internal/app must be added
// to newParticipantMounts to be walked.
func TestEveryParticipantRouteIsAdmittedFirst(t *testing.T) {
	var routes []string
	if err := chi.Walk(newParticipantMounts(t).router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, method+" "+route)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	walked := map[string]bool{}
	for _, route := range routes {
		walked[route] = true
	}
	for route := range staffRoutesOnParticipantMounts {
		if !walked[route] {
			t.Errorf("%s is listed as a staff route but no mount registers it", route)
		}
	}

	for _, route := range routes {
		if staffRoutesOnParticipantMounts[route] {
			continue
		}
		t.Run(route, func(t *testing.T) {
			m := newParticipantMounts(t)
			method, pattern, _ := strings.Cut(route, " ")
			path := routeParam.ReplaceAllStringFunc(pattern, func(string) string { return uuid.NewString() })

			expectStatus(t, m.send(method, path), http.StatusForbidden, "not_a_participant")
			lookups := m.lookups()
			if rec := m.send(method, path); rec.Code != http.StatusTooManyRequests {
				t.Fatalf("past its budget = %d, want 429 (body: %s)", rec.Code, rec.Body.String())
			}
			if m.lookups() != lookups {
				t.Error("a request refused for its budget was looked up")
			}
		})
	}
}

var routeParam = regexp.MustCompile(`\{[^}]+\}`)

// participantMounts is the participant, console, clarifications and events
// handlers with every optional With… route mounted, over a roster nobody is on
// and budgets that each admit one request per key: the read budget, the
// console's rate, and every budget kept on the cache.
type participantMounts struct {
	router  chi.Router
	roster  *participationtest.Roster
	console *countingConsoleLookup
	cookie  *http.Cookie
}

func newParticipantMounts(t *testing.T) *participantMounts {
	t.Helper()
	userRepo := userstest.New()
	userRepo.GrantRole("student")
	actor := userRepo.Add(users.User{Login: "student", FullName: "Student", Status: users.StatusActive, Roles: []string{"student"}})

	sessionCache := cache.NewMemory(1000)
	t.Cleanup(func() { _ = sessionCache.Close() })
	budgets := oneEach{cache.NewMemory(1000)}
	t.Cleanup(func() { _ = budgets.Close() })
	limiter := auth.NewLimiter(budgets)

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(sessionCache, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: actor.ID, Login: actor.Login})
	if err != nil {
		t.Fatal(err)
	}
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: userRepo, Authorizer: rbac.New(noRoles{}),
		Cookies: auth.NewCookieWriter(false), Logger: log,
	})

	roster := participationtest.NewRoster()
	admitter := participationtest.NewAdmitter(roster, nil, nil)
	admission := api.Participation{Admitter: admitter, Reads: participation.NewReads(1)}
	consoleLookup := &countingConsoleLookup{}
	console := queryproxy.New(admitter, consoleLookup, nil, nil).WithPerMinuteDefault(1)

	questions, submissions := conteststest.NewQuestions(), conteststest.NewSubmissions()
	reader := contests.NewReader(conteststest.NewStories(), conteststest.NewSchemaNotes(), questions,
		conteststest.NewAttempts(submissions), conteststest.NewSequentialProgress(questions, submissions))
	clarify := clarificationstest.NewFixture()
	asks := clarifications.NewService(clarifications.Config{
		Repository: clarify.Repo, Contests: clarify.Contests, Limiter: limiter, Notifier: clarify.Notifier,
		Audit: audit.New(clarify.Audit), UnitOfWork: clarify.UoW,
	})

	router := chi.NewRouter()
	api.NewParticipantHandler(admission, reader, &fakeHistory{}, &fakeSubmitter{}, answerRate(budgets, 1), mw, log, "en").
		WithWorkspace(workspace.NewService(workspacetest.NewRepository(), limiter)).
		WithSignals(monitor.NewSignals(limiter, &signalStore{})).
		WithDatabaseReset(console).
		WithSchemas(&fakeSchemas{}).
		Mount(router)
	api.NewConsoleHandler(console, mw, log).Mount(router)
	api.NewClarificationsHandler(asks, admission, mw, log).Mount(router)
	api.NewEventsHandler(admission, mw, log, make(chan struct{})).Mount(router)

	return &participantMounts{router: router, roster: roster, console: consoleLookup,
		cookie: &http.Cookie{Name: sessionName, Value: token}}
}

// send makes one request with a body every route here can decode.
func (m *participantMounts) send(method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(m.cookie)
	rec := httptest.NewRecorder()
	m.router.ServeHTTP(rec, req)
	return rec
}

func (m *participantMounts) lookups() int { return m.roster.Lookups() + m.console.calls() }

// oneEach is a cache whose every counter admits its first increment and
// refuses the rest, so each budget kept on it allows one request per key.
type oneEach struct{ *cache.Memory }

func (c oneEach) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	n, err := c.Memory.Incr(ctx, key, ttl)
	if err != nil || n == 1 {
		return n, err
	}
	return math.MaxInt64, nil
}

// countingConsoleLookup is the console's lookup for a caller on no roster,
// counting how often it was asked.
type countingConsoleLookup struct {
	mu    sync.Mutex
	asked int
}

func (l *countingConsoleLookup) ForRun(context.Context, uuid.UUID, uuid.UUID) (queryproxy.LookupResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.asked++
	return queryproxy.LookupResult{}, contests.ErrParticipantNotFound
}

func (l *countingConsoleLookup) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.asked
}
