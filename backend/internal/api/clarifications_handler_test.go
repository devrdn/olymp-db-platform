package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/clarifications"
	"github.com/devrdn/db-contest/backend/internal/clarifications/clarificationstest"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/fanout"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// clarifyAccess is the participation gate as these routes meet it: each
// account resolves to its own registration, and err, when set, refuses
// everyone the way the gate would.
type clarifyAccess struct {
	mu       sync.Mutex
	contest  contests.Contest
	byUser   map[uuid.UUID]contests.Participant
	err      error
	readErr  error
	reads    int
	accesses int
	// started records StartOnRead calls; startErr is its answer.
	started  []uuid.UUID
	startErr error
}

func (a *clarifyAccess) StartOnRead(_ context.Context, _ contests.Contest, participant contests.Participant, _ netip.Addr) (contests.Participant, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.startErr != nil {
		return contests.Participant{}, a.startErr
	}
	a.started = append(a.started, participant.ID)
	now := time.Now()
	participant.StartedAt = &now
	return participant, nil
}

func (a *clarifyAccess) starts() []uuid.UUID {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]uuid.UUID(nil), a.started...)
}

func (a *clarifyAccess) AdmitRead(uuid.UUID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reads++
	return a.readErr
}

func (a *clarifyAccess) Access(_ context.Context, contestID, userID uuid.UUID, _ netip.Addr) (contests.Participant, contests.Contest, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.accesses++
	if a.err != nil {
		return contests.Participant{}, contests.Contest{}, a.err
	}
	p, ok := a.byUser[userID]
	if !ok || contestID != a.contest.ID {
		return contests.Participant{}, contests.Contest{}, contests.ErrNotAParticipant
	}
	return p, a.contest, nil
}

func (a *clarifyAccess) setErr(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

func (a *clarifyAccess) counts() (reads, accesses int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reads, a.accesses
}

// staffRoles gives each listed account its role in each listed contest.
type staffRoles map[[2]uuid.UUID]rbac.ContestRole

func (r staffRoles) ContestRole(_ context.Context, userID, contestID uuid.UUID) (rbac.ContestRole, error) {
	return r[[2]uuid.UUID{userID, contestID}], nil
}

type clarifyFixture struct {
	router  http.Handler
	access  *clarifyAccess
	domain  *clarificationstest.Fixture
	hub     *fanout.Hub
	contest uuid.UUID
	// other is a second running contest the organiser also runs.
	other                         uuid.UUID
	ana, ben, organizer, outsider users.User
	anaReg, benReg                uuid.UUID
	cookies                       map[uuid.UUID]*http.Cookie
	sessions                      *auth.SessionStore
	mw                            *auth.Middleware
}

func newClarifyFixture(t *testing.T) *clarifyFixture {
	t.Helper()
	userRepo := userstest.New()
	userRepo.GrantRole("student")
	// An organizer holds contest.clarify globally, which unlocks no contest
	// they do not run.
	userRepo.GrantRole("organizer", rbac.PermissionContestClarify, rbac.PermissionContestView)
	f := &clarifyFixture{cookies: map[uuid.UUID]*http.Cookie{}, hub: fanout.NewHub()}
	f.ana = userRepo.Add(users.User{Login: "ana", FullName: "Ana Ionescu", Status: users.StatusActive, Roles: []string{"student"}})
	f.ben = userRepo.Add(users.User{Login: "ben", FullName: "Ben Popa", Status: users.StatusActive, Roles: []string{"student"}})
	f.organizer = userRepo.Add(users.User{Login: "olga", FullName: "Olga", Status: users.StatusActive, Roles: []string{"organizer"}})
	f.outsider = userRepo.Add(users.User{Login: "oscar", FullName: "Oscar", Status: users.StatusActive, Roles: []string{"organizer"}})

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	log := logging.New("error", io.Discard)
	f.sessions = auth.NewSessionStore(c, time.Hour)
	for _, u := range []users.User{f.ana, f.ben, f.organizer, f.outsider} {
		token, err := f.sessions.Create(t.Context(), auth.Principal{UserID: u.ID, Login: u.Login})
		if err != nil {
			t.Fatal(err)
		}
		f.cookies[u.ID] = &http.Cookie{Name: auth.SessionCookieName, Value: token}
	}

	f.domain = clarificationstest.NewFixture()
	f.domain.Service = clarifications.NewService(clarifications.Config{
		Repository: f.domain.Repo, Contests: f.domain.Contests, Limiter: f.domain.Limiter, Notifier: f.hub,
		Audit: audit.New(f.domain.Audit), UnitOfWork: f.domain.UoW, Now: func() time.Time { return f.domain.Now },
	})
	f.contest, f.other = f.domain.RunningContest(), f.domain.RunningContest()
	f.domain.Repo.AddAccount(f.organizer.ID, f.organizer.Login)
	f.anaReg, f.benReg = uuid.New(), uuid.New()
	f.domain.Repo.Register(f.contest, f.anaReg, f.ana.Login, f.ana.FullName)
	f.domain.Repo.Register(f.contest, f.benReg, f.ben.Login, f.ben.FullName)
	f.access = &clarifyAccess{
		contest: contests.Contest{ID: f.contest, Status: contests.StatusRunning},
		byUser: map[uuid.UUID]contests.Participant{
			f.ana.ID: {ID: f.anaReg, ContestID: f.contest, UserID: f.ana.ID},
			f.ben.ID: {ID: f.benReg, ContestID: f.contest, UserID: f.ben.ID},
		},
	}

	f.mw = auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: f.sessions, Users: userRepo,
		Authorizer: rbac.New(staffRoles{
			{f.organizer.ID, f.contest}: rbac.RoleOwner,
			{f.organizer.ID, f.other}:   rbac.RoleManager,
		}),
		Cookies: auth.NewCookieWriter(false), Logger: log,
	})
	router := chi.NewRouter()
	api.NewClarificationsHandler(f.domain.Service, f.access, f.mw, log).Mount(router)
	f.router = router
	return f
}

func (f *clarifyFixture) do(who users.User, method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookies[who.ID])
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *clarifyFixture) play() string {
	return "/contests/" + f.contest.String() + "/play/clarifications"
}
func (f *clarifyFixture) staff() string { return "/contests/" + f.contest.String() + "/clarifications" }

// ask posts a question as who and returns its id; the rate is reset so a test
// asks as often as it needs.
func (f *clarifyFixture) ask(t *testing.T, who users.User, body string) string {
	t.Helper()
	f.domain.Tick()
	payload, _ := json.Marshal(map[string]any{"body": body})
	rec := f.do(who, http.MethodPost, f.play(), string(payload))
	if rec.Code != http.StatusCreated {
		t.Fatalf("ask as %s = %d %s", who.Login, rec.Code, rec.Body)
	}
	f.domain.Limiter.Reset()
	var item struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil || item.ID == "" {
		t.Fatalf("ask answered %s", rec.Body)
	}
	return item.ID
}

func (f *clarifyFixture) answer(t *testing.T, id, visibility string) {
	t.Helper()
	f.domain.Tick()
	rec := f.do(f.organizer, http.MethodPost, f.staff()+"/"+id+"/answer",
		`{"body":"The answer.","visibility":"`+visibility+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("answer %s = %d %s", id, rec.Code, rec.Body)
	}
}

type feedJSON struct {
	Items []map[string]any `json:"items"`
	Next  *string          `json:"next"`
	Open  int              `json:"open"`
}

func (f *clarifyFixture) feed(t *testing.T, who users.User, query string) feedJSON {
	t.Helper()
	rec := f.do(who, http.MethodGet, f.play()+query, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("feed as %s = %d %s", who.Login, rec.Code, rec.Body)
	}
	var page feedJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode the feed: %v", err)
	}
	return page
}

func ids(page feedJSON) []string {
	var out []string
	for _, item := range page.Items {
		out = append(out, item["id"].(string))
	}
	return out
}

func TestAParticipantNeverReadsAnotherParticipantsPrivateQuestion(t *testing.T) {
	f := newClarifyFixture(t)
	private := f.ask(t, f.ben, "ben's secret")
	f.answer(t, private, "private")
	waiting := f.ask(t, f.ben, "ben waits")

	anaFeed := f.do(f.ana, http.MethodGet, f.play(), "")
	if anaFeed.Code != http.StatusOK {
		t.Fatalf("ana's feed = %d", anaFeed.Code)
	}
	if body := anaFeed.Body.String(); strings.Contains(body, private) || strings.Contains(body, waiting) || strings.Contains(body, "ben") {
		t.Fatalf("ana's feed shows ben's private questions: %s", body)
	}
	if got := ids(f.feed(t, f.ben, "")); len(got) != 2 {
		t.Fatalf("ben's feed = %v, want both his questions", got)
	}

	// The routes that take an id are staff routes; a participant is refused
	// before any id is looked at.
	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, f.staff(), ""},
		{http.MethodPost, f.staff() + "/" + private + "/answer", `{"body":"x","visibility":"public"}`},
		{http.MethodPut, f.staff() + "/" + private + "/answer", `{"body":"x","visibility":"public"}`},
	} {
		rec := f.do(f.ana, req.method, req.path, req.body)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "forbidden" {
			t.Errorf("%s %s as a participant = %d %s, want 403 forbidden", req.method, req.path, rec.Code, rec.Body)
		}
	}
}

func TestStaffWithoutTheContestsPermissionAreRefused(t *testing.T) {
	f := newClarifyFixture(t)
	id := f.ask(t, f.ana, "Why?")
	announcements := "/contests/" + f.contest.String() + "/announcements"
	for _, req := range []struct{ method, path, body string }{
		{http.MethodGet, f.staff(), ""},
		{http.MethodPost, f.staff() + "/" + id + "/answer", `{"body":"x","visibility":"public"}`},
		{http.MethodPut, f.staff() + "/" + id + "/answer", `{"body":"x","visibility":"public"}`},
		{http.MethodGet, announcements, ""},
		{http.MethodPost, announcements, `{"body":"x"}`},
	} {
		rec := f.do(f.outsider, req.method, req.path, req.body)
		if rec.Code != http.StatusForbidden || errorCode(t, rec) != "forbidden" {
			t.Errorf("%s %s by staff of another contest = %d %s, want 403 forbidden", req.method, req.path, rec.Code, rec.Body)
		}
	}
	if len(f.domain.Audit.Entries) != 0 {
		t.Fatalf("a refused staff write left %d audit entries", len(f.domain.Audit.Entries))
	}
}

func TestAClarificationOfAnotherContestIsNotFoundToItsOwnStaff(t *testing.T) {
	f := newClarifyFixture(t)
	id := f.ask(t, f.ana, "Why?")
	rec := f.do(f.organizer, http.MethodPost, "/contests/"+f.other.String()+"/clarifications/"+id+"/answer",
		`{"body":"x","visibility":"private"}`)
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "clarification_not_found" {
		t.Fatalf("answering through another contest = %d %s, want 404 clarification_not_found", rec.Code, rec.Body)
	}
	rec = f.do(f.organizer, http.MethodPost, f.staff()+"/not-an-id/answer", `{"body":"x","visibility":"private"}`)
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "clarification_not_found" {
		t.Fatalf("an id that cannot be one = %d %s, want the same 404", rec.Code, rec.Body)
	}
}

func TestAPublicAnswerShowsTheQuestionButNeverWhoAskedIt(t *testing.T) {
	f := newClarifyFixture(t)
	id := f.ask(t, f.ben, "Is the year inclusive?")
	f.answer(t, id, "public")

	rec := f.do(f.ana, http.MethodGet, f.play(), "")
	body := rec.Body.String()
	for _, secret := range []string{f.ben.Login, f.ben.FullName, f.ben.ID.String(), f.benReg.String(), f.organizer.ID.String(), f.organizer.Login} {
		if strings.Contains(body, secret) {
			t.Fatalf("ana's feed names %q: %s", secret, body)
		}
	}
	page := f.feed(t, f.ana, "")
	if len(page.Items) != 1 {
		t.Fatalf("ana's feed = %+v, want ben's public answer", page)
	}
	item := page.Items[0]
	answer, _ := item["answer"].(map[string]any)
	if item["kind"] != "clarification" || item["mine"] != false || item["body"] != "Is the year inclusive?" ||
		answer == nil || answer["body"] != "The answer." || answer["visibility"] != "public" {
		t.Fatalf("the public item = %v", item)
	}
	for key := range item {
		switch key {
		case "kind", "id", "posted_at", "mine", "question_id", "body", "asked_at", "answer":
		default:
			t.Errorf("a participant's item carries %q, which no participant needs", key)
		}
	}
	for key := range answer {
		switch key {
		case "body", "declined", "visibility", "answered_at", "edited_at":
		default:
			t.Errorf("a participant's answer carries %q", key)
		}
	}
}

func TestAskingOutsideTheRunningContestIsRefusedAndStoresNothing(t *testing.T) {
	f := newClarifyFixture(t)
	for _, refusal := range []struct {
		err  error
		code string
	}{
		{contests.ErrContestEnded, "contest_ended"},
		{contests.ErrDeadlinePassed, "deadline_passed"},
		{contests.ErrParticipantFinished, "contest_finished"},
		{contests.ErrNotAParticipant, "not_a_participant"},
	} {
		f.access.setErr(refusal.err)
		f.domain.Limiter.Reset()
		rec := f.do(f.ana, http.MethodPost, f.play(), `{"body":"Too late?"}`)
		if errorCode(t, rec) != refusal.code {
			t.Errorf("asking when the gate says %v = %d %s, want %s", refusal.err, rec.Code, rec.Body, refusal.code)
		}
	}
	f.access.setErr(nil)
	if page := f.feed(t, f.ana, ""); len(page.Items) != 0 || page.Open != 0 {
		t.Fatalf("a refused question was stored: %+v", page)
	}
}

func TestAskingIsRateLimitedBeforeTheGateAndEveryAttemptCounts(t *testing.T) {
	f := newClarifyFixture(t)
	// A question too long is refused and still spends one of three.
	long := `{"body":"` + strings.Repeat("x", clarifications.MaxQuestionRunes+1) + `"}`
	if rec := f.do(f.ana, http.MethodPost, f.play(), long); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "clarification_too_long" {
		t.Fatalf("a question too long = %d %s, want 400 clarification_too_long", rec.Code, rec.Body)
	}
	for range clarifications.AsksPerMinute - 1 {
		if rec := f.do(f.ana, http.MethodPost, f.play(), `{"body":"Why?"}`); rec.Code != http.StatusCreated {
			t.Fatalf("ask = %d %s", rec.Code, rec.Body)
		}
	}
	rec := f.do(f.ana, http.MethodPost, f.play(), `{"body":"Why?"}`)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "clarification_too_often" || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("ask past the rate = %d %s (Retry-After %q), want 429 clarification_too_often after 60",
			rec.Code, rec.Body, rec.Header().Get("Retry-After"))
	}

	// The account is charged before the gate is asked: someone on no roster
	// pays for every attempt and soon reaches no lookup at all.
	f.domain.Limiter.Reset()
	f.access.setErr(contests.ErrNotAParticipant)
	_, before := f.access.counts()
	for range clarifications.AsksPerMinutePerAccount {
		f.do(f.ana, http.MethodPost, f.play(), `{"body":"Why?"}`)
	}
	rec = f.do(f.ana, http.MethodPost, f.play(), `{"body":"Why?"}`)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "clarification_too_often" {
		t.Fatalf("attempt %d of a refused account = %d %s, want 429", clarifications.AsksPerMinutePerAccount+1, rec.Code, rec.Body)
	}
	if _, after := f.access.counts(); after-before != clarifications.AsksPerMinutePerAccount {
		t.Fatalf("the gate was asked %d times, want %d: the refused attempt must not reach it",
			after-before, clarifications.AsksPerMinutePerAccount)
	}
}

func TestAParticipantHasAtMostTwentyQuestionsWaitingOverHTTP(t *testing.T) {
	f := newClarifyFixture(t)
	for range clarifications.MaxOpenPerRegistration {
		f.ask(t, f.ana, "Why?")
	}
	rec := f.do(f.ana, http.MethodPost, f.play(), `{"body":"One more"}`)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "clarification_too_many_open" {
		t.Fatalf("question 21 = %d %s, want 409 clarification_too_many_open", rec.Code, rec.Body)
	}
	if page := f.feed(t, f.ana, ""); page.Open != clarifications.MaxOpenPerRegistration {
		t.Fatalf("open = %d, want %d", page.Open, clarifications.MaxOpenPerRegistration)
	}
}

func TestAQuestionNamesOnlyAQuestionOfItsContest(t *testing.T) {
	f := newClarifyFixture(t)
	if rec := f.do(f.ana, http.MethodPost, f.play(), `{"body":"Why?","question_id":"nope"}`); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_question_id" {
		t.Fatalf("a malformed question id = %d %s, want 400 invalid_question_id", rec.Code, rec.Body)
	}
	foreign := f.domain.Question(f.other)
	if rec := f.do(f.ana, http.MethodPost, f.play(), `{"body":"Why?","question_id":"`+foreign.String()+`"}`); rec.Code != http.StatusNotFound || errorCode(t, rec) != "question_not_found" {
		t.Fatalf("another contest's question = %d %s, want 404 question_not_found", rec.Code, rec.Body)
	}
	own := f.domain.Question(f.contest)
	f.domain.Limiter.Reset()
	rec := f.do(f.ana, http.MethodPost, f.play(), `{"body":"Why?","question_id":"`+own.String()+`"}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), own.String()) {
		t.Fatalf("a question about the contest's own = %d %s", rec.Code, rec.Body)
	}
}

func TestTheFeedSpendsTheReadBudgetBeforeTheGate(t *testing.T) {
	f := newClarifyFixture(t)
	f.feed(t, f.ana, "")
	if reads, _ := f.access.counts(); reads != 1 {
		t.Fatalf("a feed read spent %d reads, want 1", reads)
	}
	f.access.readErr = queryproxy.ErrTooManyReads
	_, before := f.access.counts()
	rec := f.do(f.ana, http.MethodGet, f.play(), "")
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "play_read_too_often" {
		t.Fatalf("a feed past the read budget = %d %s, want 429 play_read_too_often", rec.Code, rec.Body)
	}
	if _, after := f.access.counts(); after != before {
		t.Fatal("a feed refused for its rate still asked the gate")
	}
	// Asking is a write and spends its own budget, never the read budget.
	f.access.readErr = nil
	reads, _ := f.access.counts()
	f.ask(t, f.ana, "Why?")
	if after, _ := f.access.counts(); after != reads {
		t.Fatal("asking spent the read budget")
	}
}

func TestTheFeedPagesWithItsCursorAndRefusesAForgedOne(t *testing.T) {
	f := newClarifyFixture(t)
	first := f.ask(t, f.ana, "one")
	second := f.ask(t, f.ana, "two")
	third := f.ask(t, f.ana, "three")
	page := f.feed(t, f.ana, "?limit=2")
	if got := ids(page); len(got) != 2 || got[0] != third || got[1] != second || page.Next == nil {
		t.Fatalf("first page = %v (next %v), want the two newest and a cursor", got, page.Next)
	}
	rest := f.feed(t, f.ana, "?limit=2&before="+*page.Next)
	if got := ids(rest); len(got) != 1 || got[0] != first || rest.Next != nil {
		t.Fatalf("second page = %v, want the oldest and no cursor", got)
	}
	rec := f.do(f.ana, http.MethodGet, f.play()+"?before=forged", "")
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "clarification_invalid_cursor" {
		t.Fatalf("a forged cursor = %d %s, want 400 clarification_invalid_cursor", rec.Code, rec.Body)
	}
}

func TestStaffAnswerOnceThenEditAndEveryWriteIsAudited(t *testing.T) {
	f := newClarifyFixture(t)
	id := f.ask(t, f.ana, "Why?")
	answer := f.staff() + "/" + id + "/answer"

	if rec := f.do(f.organizer, http.MethodPut, answer, `{"body":"x","visibility":"private"}`); rec.Code != http.StatusConflict || errorCode(t, rec) != "clarification_not_answered" {
		t.Fatalf("editing before answering = %d %s, want 409 clarification_not_answered", rec.Code, rec.Body)
	}
	if rec := f.do(f.organizer, http.MethodPost, answer, `{"body":"x","visibility":"everyone"}`); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_request" {
		t.Fatalf("an unknown visibility = %d %s, want 400 invalid_request", rec.Code, rec.Body)
	}
	rec := f.do(f.organizer, http.MethodPost, answer, `{"body":"Because.","visibility":"private"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body)
	}
	var item map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	asker, _ := item["asker"].(map[string]any)
	if asker["login"] != "ana" || asker["full_name"] != "Ana Ionescu" {
		t.Fatalf("staff see the asker as %v, want ana", asker)
	}
	if rec := f.do(f.organizer, http.MethodPost, answer, `{"body":"Again.","visibility":"private"}`); rec.Code != http.StatusConflict || errorCode(t, rec) != "clarification_already_answered" {
		t.Fatalf("a second answer = %d %s, want 409 clarification_already_answered", rec.Code, rec.Body)
	}
	if rec := f.do(f.organizer, http.MethodPut, answer, `{"body":"Because, really.","visibility":"public"}`); rec.Code != http.StatusOK {
		t.Fatalf("edit = %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(f.organizer, http.MethodPost, "/contests/"+f.contest.String()+"/announcements", `{"body":"Ten minutes left."}`); rec.Code != http.StatusCreated {
		t.Fatalf("announce = %d %s", rec.Code, rec.Body)
	}

	var actions []string
	for _, e := range f.domain.Audit.Entries {
		actions = append(actions, e.Action)
		if e.ActorID == nil || *e.ActorID != f.organizer.ID {
			t.Errorf("%s was recorded for %v, want the organiser", e.Action, e.ActorID)
		}
	}
	want := []string{audit.ActionClarificationAnswer, audit.ActionClarificationPublish, audit.ActionAnnouncementCreate}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("audited %v, want %v", actions, want)
	}
}

func TestTheStaffQueueListsOneStateWithBothCounts(t *testing.T) {
	f := newClarifyFixture(t)
	older := f.ask(t, f.ben, "older")
	f.ask(t, f.ana, "newer")
	done := f.ask(t, f.ana, "done")
	f.answer(t, done, "private")

	var queue struct {
		Items  []map[string]any `json:"items"`
		Counts struct{ Open, Answered int }
	}
	rec := f.do(f.organizer, http.MethodGet, f.staff()+"?state=open&limit=1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("queue = %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &queue); err != nil {
		t.Fatal(err)
	}
	if len(queue.Items) != 1 || queue.Items[0]["id"] != older || queue.Counts.Open != 2 || queue.Counts.Answered != 1 {
		t.Fatalf("open queue = %+v, want the oldest of two open and one answered", queue)
	}
	rec = f.do(f.organizer, http.MethodGet, f.staff()+"?state=answered", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &queue); err != nil {
		t.Fatal(err)
	}
	answer, _ := queue.Items[0]["answer"].(map[string]any)
	if len(queue.Items) != 1 || queue.Items[0]["id"] != done || answer == nil || answer["answered_by"] != f.organizer.Login {
		t.Fatalf("answered queue = %+v", queue)
	}
}

func TestAnAnnouncementReachesEveryParticipantsFeed(t *testing.T) {
	f := newClarifyFixture(t)
	announcements := "/contests/" + f.contest.String() + "/announcements"
	if rec := f.do(f.organizer, http.MethodPost, announcements, `{"body":"  "}`); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "clarification_empty" {
		t.Fatalf("an empty announcement = %d %s, want 400 clarification_empty", rec.Code, rec.Body)
	}
	if rec := f.do(f.organizer, http.MethodPost, announcements, `{"body":"`+strings.Repeat("x", clarifications.MaxAnnouncementRunes+1)+`"}`); errorCode(t, rec) != "announcement_too_long" {
		t.Fatalf("an announcement too long = %d %s, want announcement_too_long", rec.Code, rec.Body)
	}
	if rec := f.do(f.organizer, http.MethodPost, announcements, `{"body":"Ten minutes left."}`); rec.Code != http.StatusCreated {
		t.Fatalf("announce = %d %s", rec.Code, rec.Body)
	}
	for _, who := range []users.User{f.ana, f.ben} {
		page := f.feed(t, who, "")
		if len(page.Items) != 1 || page.Items[0]["kind"] != "announcement" || page.Items[0]["body"] != "Ten minutes left." {
			t.Fatalf("%s's feed = %+v, want the announcement", who.Login, page.Items)
		}
	}
	var list struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	rec := f.do(f.organizer, http.MethodGet, announcements, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("staff announcements = %d %s", rec.Code, rec.Body)
	}
}

func TestStaffWritesAfterTheFinishAreRefused(t *testing.T) {
	f := newClarifyFixture(t)
	id := f.ask(t, f.ana, "Why?")
	f.domain.Contests.SetStatus(f.contest, contests.StatusFinished)
	rec := f.do(f.organizer, http.MethodPost, f.staff()+"/"+id+"/answer", `{"body":"x","visibility":"private"}`)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "clarifications_closed" {
		t.Fatalf("answering after the finish = %d %s, want 409 clarifications_closed", rec.Code, rec.Body)
	}
	// The queue stays readable.
	if rec := f.do(f.organizer, http.MethodGet, f.staff(), ""); rec.Code != http.StatusOK {
		t.Fatalf("the queue after the finish = %d", rec.Code)
	}
}

// The whole path a hint takes: an answer through these routes reaches the open
// event channels of exactly its audience.
func TestAnAnswerReachesTheEventChannelsOfItsAudienceOnly(t *testing.T) {
	f := newClarifyFixture(t)
	anaStream, closeAna, anaDone := runningStream(t, f.hub, f.contest, f.anaReg)
	benStream, closeBen, benDone := runningStream(t, f.hub, f.contest, f.benReg)
	defer func() {
		closeAna()
		closeBen()
		waitDone(t, anaDone)
		waitDone(t, benDone)
	}()

	// A question tells nobody: staff poll.
	private := f.ask(t, f.ana, "private")
	public := f.ask(t, f.ana, "public")
	f.answer(t, private, "private")
	waitForCount(t, anaStream, "event: clarification", 1)
	f.answer(t, public, "public")
	waitForCount(t, anaStream, "event: clarification", 2)
	waitForCount(t, benStream, "event: clarification", 1)
	time.Sleep(30 * time.Millisecond)
	if got := strings.Count(benStream.String(), "event: clarification"); got != 1 {
		t.Fatalf("ben's channel carried %d hints, want only the public answer's", got)
	}
}

// Times always carry six fractional digits, so they compare as text in the
// order they happened.
func TestClarificationTimesCompareAsText(t *testing.T) {
	f := newClarifyFixture(t)
	f.domain.Now = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	f.ask(t, f.ana, "Why?")
	page := f.feed(t, f.ana, "")
	if got := page.Items[0]["posted_at"]; got != "2026-11-02T09:00:01.000000Z" {
		t.Fatalf("posted_at = %v, want six fractional digits on a whole second", got)
	}
}

// Under individual timing a participant's clock starts on their first read of
// the contest's content or their first act. The feed carries organisers'
// corrections of the tasks, so it counts, and so does asking; a refused read
// or ask starts nothing.
func TestReadingTheFeedOrAskingStartsTheClockAndARefusalDoesNot(t *testing.T) {
	f := newClarifyFixture(t)

	if rec := f.do(f.ana, http.MethodGet, f.play()+"?before=forged", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("a forged cursor = %d", rec.Code)
	}
	if got := f.access.starts(); len(got) != 0 {
		t.Fatalf("a refused read started the clock for %v", got)
	}
	f.feed(t, f.ana, "")
	if got := f.access.starts(); len(got) != 1 || got[0] != f.anaReg {
		t.Fatalf("after a feed read the clock started for %v, want ana's registration", got)
	}

	long := `{"body":"` + strings.Repeat("x", clarifications.MaxQuestionRunes+1) + `"}`
	f.do(f.ben, http.MethodPost, f.play(), long)
	if got := f.access.starts(); len(got) != 1 {
		t.Fatalf("a refused ask started a clock: %v", got)
	}
	f.ask(t, f.ben, "Why?")
	if got := f.access.starts(); len(got) != 2 || got[1] != f.benReg {
		t.Fatalf("after ben's ask the clocks started are %v, want ben's last", got)
	}
}

// A start that cannot be recorded sends nothing: content is never shown to a
// participant whose start was not recorded.
func TestAFeedWhoseClockCannotStartIsRefused(t *testing.T) {
	f := newClarifyFixture(t)
	f.access.startErr = contests.ErrDeadlinePassed
	rec := f.do(f.ana, http.MethodGet, f.play(), "")
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "deadline_passed" {
		t.Fatalf("a feed whose start failed = %d %s, want 409 deadline_passed", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "items") {
		t.Fatalf("the feed was sent anyway: %s", rec.Body)
	}
}

func sequentialContest(f *clarifyFixture) {
	f.access.mu.Lock()
	defer f.access.mu.Unlock()
	f.access.contest.Progression = contests.ProgressionSequential
	f.access.contest.QuestionMode = contests.QuestionModeMulti
}

func TestAnAskAboutAHiddenOrUnreachedQuestionIsNotFound(t *testing.T) {
	f := newClarifyFixture(t)
	sequentialContest(f)
	first, second := f.domain.Question(f.contest), f.domain.Question(f.contest)
	hidden := f.domain.HiddenQuestion(f.contest)
	for _, q := range []uuid.UUID{second, hidden} {
		f.domain.Limiter.Reset()
		rec := f.do(f.ana, http.MethodPost, f.play(), `{"body":"Why?","question_id":"`+q.String()+`"}`)
		if rec.Code != http.StatusNotFound || errorCode(t, rec) != "question_not_found" {
			t.Fatalf("asking about %v = %d %s, want 404 question_not_found", q, rec.Code, rec.Body)
		}
	}
	if page := f.feed(t, f.ana, ""); len(page.Items) != 0 {
		t.Fatalf("a refused question was stored: %+v", page.Items)
	}
	f.domain.Limiter.Reset()
	if rec := f.do(f.ana, http.MethodPost, f.play(), `{"body":"Why?","question_id":"`+first.String()+`"}`); rec.Code != http.StatusCreated {
		t.Fatalf("asking about the question reached = %d %s", rec.Code, rec.Body)
	}
}

// A published answer names its question only to a reader who has reached it.
func TestAPublicAnswerNamesItsQuestionOnlyToWhoeverReachedIt(t *testing.T) {
	f := newClarifyFixture(t)
	sequentialContest(f)
	first, second := f.domain.Question(f.contest), f.domain.Question(f.contest)
	f.domain.Repo.Close(f.anaReg, first)
	f.domain.Tick()
	rec := f.do(f.ana, http.MethodPost, f.play(), `{"body":"About the second","question_id":"`+second.String()+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("ask = %d %s", rec.Code, rec.Body)
	}
	var asked struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &asked)
	f.answer(t, asked.ID, "public")

	page := f.feed(t, f.ben, "")
	if len(page.Items) != 1 || page.Items[0]["question_id"] != nil {
		t.Fatalf("ben, before the second question, reads %v", page.Items)
	}
	if strings.Contains(f.do(f.ben, http.MethodGet, f.play(), "").Body.String(), second.String()) {
		t.Fatal("ben's feed carries the id of a question he has not reached")
	}
}
