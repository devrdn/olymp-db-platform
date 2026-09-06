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
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// fakeAccess answers Access with whatever a test staged, so the handler's own
// mapping from a refusal to a status and a code can be exercised without a
// real queryproxy.Service behind it — that admission is proven where
// queryproxy owns it (internal/queryproxy).
type fakeAccess struct {
	participant contests.Participant
	contest     contests.Contest
	// err is guarded by mu rather than left a bare field: the events handler
	// tests (events_handler_test.go) change it while a connection's own
	// goroutine is concurrently calling Access on every resync tick — a
	// scenario nothing in this file needed until that one had a channel
	// left open across several ticks.
	mu  sync.Mutex
	err error
	// gotContestID records what Access was asked about, so a test can prove
	// the identifier came from the URL.
	gotContestID uuid.UUID
	// admitReadErr is what AdmitRead answers; nil means every caller is
	// admitted. accessCalled records whether Access was reached, so a test
	// can prove a refusal here stops the request before Access's own lookups.
	admitReadErr error
	accessCalled bool
	// delay, when set, is how long AccessForEvents waits before answering —
	// events_handler_test.go's own way of standing for a resync tick's two
	// lookups running slow (finding 2), without a real, adjustable-latency
	// store behind this fake.
	delay time.Duration
}

func (a *fakeAccess) AdmitRead(uuid.UUID) error {
	return a.admitReadErr
}

func (a *fakeAccess) Access(_ context.Context, contestID, _ uuid.UUID, _ netip.Addr) (contests.Participant, contests.Contest, error) {
	a.accessCalled = true
	a.gotContestID = contestID
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.participant, a.contest, a.err
}

// AccessForEvents answers with whatever a test staged, exactly like Access —
// the events handler tests care about what the handler does with the
// participant and contest a test hands it, not about re-deriving
// queryproxy.Service's own admission rule (proven in its own package,
// internal/queryproxy/queryproxy_test.go). This fake never distinguishes the
// one status AccessForEvents admits that Access would not
// (contests.StatusPublished) — a test controls that simply by staging
// f.access.contest.Status itself.
func (a *fakeAccess) AccessForEvents(ctx context.Context, contestID, userID uuid.UUID, addr netip.Addr) (contests.Participant, contests.Contest, error) {
	a.mu.Lock()
	delay := a.delay
	a.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return contests.Participant{}, contests.Contest{}, ctx.Err()
		}
	}
	return a.Access(ctx, contestID, userID, addr)
}

// setDelay stages how long the next AccessForEvents calls take to answer
// (finding 2), safely against a connection's own goroutine reading it
// concurrently on its next resync tick.
func (a *fakeAccess) setDelay(d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.delay = d
}

// setErr changes what Access answers with, safely against a connection's own
// goroutine reading it concurrently on its next resync tick.
func (a *fakeAccess) setErr(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

// setContest changes what Access answers a contest with, safely against a
// connection's own goroutine reading it concurrently on its next resync tick
// — a test's way of staging the published → running transition mid-connection
// (finding 4) without a second implementation of "who is this and are they
// still in".
func (a *fakeAccess) setContest(c contests.Contest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.contest = c
}

// fakeSubmitter answers Submit with whatever a test staged, so the answer
// endpoint's own request wiring and error mapping can be exercised without a
// real contests.Service behind it — that behaviour is proven where
// contests.Service owns it (internal/contests/submission_test.go).
type fakeSubmitter struct {
	outcome contests.SubmitOutcome
	err     error
	// gotCmd records what Submit was asked, so a test can prove the
	// question identifier and the value came from the request rather than
	// being invented here.
	gotCmd contests.SubmitCommand
	called bool
}

func (s *fakeSubmitter) Submit(_ context.Context, cmd contests.SubmitCommand) (contests.SubmitOutcome, error) {
	s.called = true
	s.gotCmd = cmd
	return s.outcome, s.err
}

// participantFixture mounts the participant endpoints behind a session, with
// a fake Access and a real Reader over in-memory stores — the same
// conteststest fakes internal/contests's own Reader tests use, so what is
// under test here is the handler's wiring and error mapping, not a second
// implementation of the reading rules.
type participantFixture struct {
	router    http.Handler
	access    *fakeAccess
	submitter *fakeSubmitter
	stories   *conteststest.Stories
	questions *conteststest.Questions
	attempts  *conteststest.Attempts
	cookie    *http.Cookie
}

func newParticipantFixture(t *testing.T) *participantFixture {
	t.Helper()

	userRepo := userstest.New()
	userRepo.GrantRole("student")
	actor := userRepo.Add(users.User{Login: "student", FullName: "Student", Status: users.StatusActive, Roles: []string{"student"}})

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: actor.ID, Login: actor.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: userRepo,
		Authorizer: rbac.New(noRoles{}),
		Cookies:    auth.NewCookieWriter(false), Logger: log,
	})

	stories := conteststest.NewStories()
	questions := conteststest.NewQuestions()
	attempts := conteststest.NewAttempts()
	reader := contests.NewReader(stories, questions, attempts, nil)
	access := &fakeAccess{}
	submitter := &fakeSubmitter{}

	router := chi.NewRouter()
	api.NewParticipantHandler(access, reader, submitter, mw, log, "en").Mount(router)

	return &participantFixture{
		router: router, access: access, submitter: submitter,
		stories: stories, questions: questions, attempts: attempts,
		cookie: &http.Cookie{Name: auth.SessionCookieName, Value: token},
	}
}

func (f *participantFixture) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *participantFixture) post(path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// TestAHiddenQuestionIsAbsentFromTheParticipantsList is the requirement
// section 6.1 is explicit about: a hidden question exists fully and is
// simply never shown.
func TestAHiddenQuestionIsAbsentFromTheParticipantsList(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}

	visible := f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	hidden := f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 2, Kind: contests.KindText, Points: 5, IsVisible: false,
		Texts:   map[string]contests.QuestionText{"en": {BodyMD: "What weapon?"}},
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "candlestick"}},
	})

	rec := f.get("/contests/" + contestID.String() + "/play/questions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var payload struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].ID != visible.ID.String() {
		t.Fatalf("items = %+v, want exactly the visible question", payload.Items)
	}
	if strings.Contains(rec.Body.String(), hidden.ID.String()) {
		t.Fatalf("the hidden question's own identifier appeared in the response: %s", rec.Body.String())
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "weapon") {
		t.Fatalf("the hidden question's wording appeared in the response: %s", rec.Body.String())
	}
}

// Finding 1: the response must carry no display position at all. A dense
// ordinal — 1, 3, 4, 7 — would tell the caller exactly how many questions are
// hidden and precisely where each one sits, which is the one fact §6.1 says a
// participant must work out rather than read off a field. The items array
// already arrives in display order, so there is nothing an ordinal would add
// except that leak.
func TestTheQuestionsResponseCarriesNoOrdinal(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}

	// Three visible questions with two hidden ones between them, so a dense
	// staff ordinal would visibly skip (1, then 4, 5 — never 2 or 3).
	f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 1, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Q1"}},
	})
	f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 2, Kind: contests.KindText, Points: 1, IsVisible: false,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Hidden A"}},
	})
	f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 3, Kind: contests.KindText, Points: 1, IsVisible: false,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Hidden B"}},
	})
	f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 4, Kind: contests.KindText, Points: 1, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Q2"}},
	})

	rec := f.get("/contests/" + contestID.String() + "/play/questions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"ord"`) {
		t.Fatalf("the response carries a display position, which counts and places the hidden questions: %s", rec.Body.String())
	}

	var payload struct {
		Items []struct {
			BodyMD string `json:"body_md"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 2 || payload.Items[0].BodyMD != "Q1" || payload.Items[1].BodyMD != "Q2" {
		t.Fatalf("items = %+v, want Q1 then Q2 in display order with nothing to say how far apart they are", payload.Items)
	}
}

// The reference answer must never appear anywhere in the response, for a
// visible question either.
func TestAReferenceAnswerNeverAppearsInTheQuestionsResponse(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}

	f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		Texts:   map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})

	rec := f.get("/contests/" + contestID.String() + "/play/questions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := strings.ToLower(rec.Body.String())
	for _, leak := range []string{"the butler", "match_kind", `"answers"`} {
		if strings.Contains(body, leak) {
			t.Fatalf("the reference answer or its shape leaked into the response (%q): %s", leak, rec.Body.String())
		}
	}
}

// CLAUDE.md rule 1: every refusal queryproxy.Service.Access can answer with
// needs a declared sentinel, a mapping in fail(), and a test asserting the
// 4xx. This table is that test for every one of them, driven through both
// endpoints.
func TestAccessRefusalsBecomeTheDocumentedStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"not a participant", queryproxy.ErrNotAParticipant, http.StatusForbidden, "not_a_participant"},
		{"contest not running", queryproxy.ErrContestNotRunning, http.StatusConflict, "contest_not_running"},
		{"participant finished", queryproxy.ErrFinished, http.StatusConflict, "contest_finished"},
		{"address not allowed", queryproxy.ErrAddressNotAllowed, http.StatusForbidden, "address_not_allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newParticipantFixture(t)
			f.access.err = tc.err
			contestID := uuid.New()

			for _, path := range []string{
				"/contests/" + contestID.String() + "/play/story",
				"/contests/" + contestID.String() + "/play/questions",
			} {
				rec := f.get(path)
				if rec.Code != tc.wantStatus {
					t.Fatalf("%s: status = %d, want %d (body: %s)", path, rec.Code, tc.wantStatus, rec.Body.String())
				}
				if code := errorCode(t, rec); code != tc.wantCode {
					t.Fatalf("%s: code = %q, want %q", path, code, tc.wantCode)
				}
			}
		})
	}
}

// A participant of another contest is refused the same way a stranger to
// every contest is — not_a_participant, never a 404 that would confirm the
// contest exists, and never a body that says anything about it.
func TestAParticipantOfAnotherContestLearnsNothingAboutThisOne(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.err = queryproxy.ErrNotAParticipant

	rec := f.get("/contests/" + uuid.New().String() + "/play/story")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "not_a_participant" {
		t.Fatalf("code = %q, want not_a_participant", code)
	}
}

// Finding 3: a caller over their own rate budget is refused before Access
// ever runs its lookups — the same order Run itself uses, and the same
// sentinel the console maps to 429 (queryrunner.ErrTooManyQueries,
// codeQueryTooOften).
func TestARateLimitRefusalIsA429AndNeverReachesAccess(t *testing.T) {
	for _, path := range []string{"story", "questions"} {
		t.Run(path, func(t *testing.T) {
			f := newParticipantFixture(t)
			f.access.admitReadErr = queryrunner.ErrTooManyQueries

			rec := f.get("/contests/" + uuid.New().String() + "/play/" + path)
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429 (body: %s)", rec.Code, rec.Body.String())
			}
			if code := errorCode(t, rec); code != "query_too_often" {
				t.Fatalf("code = %q, want query_too_often", code)
			}
			if f.access.accessCalled {
				t.Fatal("Access was called after AdmitRead refused — the rate check must run first, before Access's own lookups")
			}
		})
	}
}

// A caller within their own rate budget is unaffected: AdmitRead admits them
// and Access runs exactly as it always has.
func TestACallerWithinTheRateBudgetStillReachesAccess(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.contest = contests.Contest{
		ID: uuid.New(), Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}
	if _, err := f.stories.Save(context.Background(), f.access.contest.ID, map[string]string{"en": "A body."}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	rec := f.get("/contests/" + f.access.contest.ID.String() + "/play/story")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !f.access.accessCalled {
		t.Fatal("Access was never called for a caller within their rate budget")
	}
}

// Finding 4: contests.ErrStoryNotFound must map to 404 — a sentinel with a
// mapping in fail() and, until now, no test asserting it.
func TestAMissingStoryIsA404(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}
	// No story saved: f.stories has nothing for contestID, so the reader
	// answers contests.ErrStoryNotFound.

	rec := f.get("/contests/" + contestID.String() + "/play/story")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "story_not_found" {
		t.Fatalf("code = %q, want story_not_found", code)
	}
}

// The story falls back per §6.2 when the requested language has no
// translation: the contest's own default, here English, answers instead of
// an error, and the response says which language it actually served.
func TestStoryFallsBackToTheContestsDefaultLanguage(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ru"}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}
	if _, err := f.stories.Save(context.Background(), contestID, map[string]string{
		"en": "A body in the stacks.",
		"ru": "Тело в архиве.",
	}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	// A language the contest does not offer at all (French) falls back to the
	// contest's own default (English), not to an error and not to whatever
	// the installation's own default happens to be.
	rec := f.get("/contests/" + contestID.String() + "/play/story?lang=fr")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Lang   string `json:"lang"`
		BodyMD string `json:"body_md"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Lang != "en" || payload.BodyMD != "A body in the stacks." {
		t.Fatalf("payload = %+v, want the contest's own default language", payload)
	}

	// A language it does offer, Russian, is honoured.
	rec = f.get("/contests/" + contestID.String() + "/play/story?lang=ru")
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Lang != "ru" || payload.BodyMD != "Тело в архиве." {
		t.Fatalf("payload = %+v, want the Russian story", payload)
	}
}

// The questions list also carries attempts remaining and closed, resolved
// from this participant's own attempts.
func TestQuestionsCarryAttemptsRemainingAndClosed(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	registrationID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: registrationID}

	maxAttempts := 3
	q := f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		MaxAttempts: &maxAttempts,
		Texts:       map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	f.attempts.Put(registrationID, q.ID, contests.AttemptStats{Attempts: 2})

	rec := f.get("/contests/" + contestID.String() + "/play/questions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			ID                string `json:"id"`
			AttemptsRemaining *int   `json:"attempts_remaining"`
			Closed            bool   `json:"closed"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items = %+v, want 1", payload.Items)
	}
	got := payload.Items[0]
	if got.AttemptsRemaining == nil || *got.AttemptsRemaining != 1 {
		t.Fatalf("attempts_remaining = %v, want 1", got.AttemptsRemaining)
	}
	if got.Closed {
		t.Fatalf("closed = true, want false (one attempt remains)")
	}
}

// A contest identifier that is not a UUID is a 400, not a 500 and not a call
// to Access with garbage.
func TestAnInvalidContestIDInTheURLIsA400(t *testing.T) {
	f := newParticipantFixture(t)

	rec := f.get("/contests/not-a-uuid/play/story")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if f.access.gotContestID != uuid.Nil {
		t.Fatalf("Access was called with %s for a URL that never named a contest", f.access.gotContestID)
	}
}

// TestParticipantRoutesDoNotShadowTheStaffContentEndpoints is a regression
// test for a defect this task's own review found rather than reasoned about:
// mounting this handler's story and questions routes at the same paths
// ContestsHandler already answers (/contests/{id}/story,
// /contests/{id}/questions) does not fail to build — chi's router silently
// lets the later Mount win — and with both mounted the way app.go actually
// mounts them, the staff-only endpoint stopped answering as itself at all.
// This assembles both handlers over one router exactly as app.go does and
// proves each still answers its own path: the organizer's endpoint still
// requires contest.view and returns the staff shape, and the participant's
// own view lives at its own /play prefix rather than contesting that URL.
func TestParticipantRoutesDoNotShadowTheStaffContentEndpoints(t *testing.T) {
	stores := conteststest.NewFixture()
	stores.Users.GrantRole("staff", "contest.create")
	owner := stores.Users.Add(users.User{Login: "organizer", FullName: "Organizer", Status: users.StatusActive, Roles: []string{"staff"}})
	contestID := stores.SeedPublishableContest().ID
	if err := stores.Managers.Grant(t.Context(), contests.Manager{
		ContestID: contestID, UserID: owner.ID, Role: rbac.RoleOwner, GrantedBy: owner.ID,
	}); err != nil {
		t.Fatalf("Grant() = %v", err)
	}

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: owner.ID, Login: owner.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users,
		Authorizer: rbac.New(contestRoles{stores}),
		Cookies:    auth.NewCookieWriter(false), Logger: log,
	})
	cookie := &http.Cookie{Name: auth.SessionCookieName, Value: token}

	// Both handlers mounted over one router, exactly as app.go mounts them.
	router := chi.NewRouter()
	api.NewContestsHandler(stores.Service, mw, log, "en").Mount(router)
	reader := contests.NewReader(stores.Stories, stores.Questions, conteststest.NewAttempts(), stores.Sequence)
	access := &fakeAccess{err: queryproxy.ErrNotAParticipant}
	api.NewParticipantHandler(access, reader, stores.Service, mw, log, "en").Mount(router)

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// The organizer's own endpoint, unaffected: still answering with the
	// staff shape (a contest UUID and every translation) rather than the
	// participant's {lang, body_md}.
	staff := do("/contests/" + contestID.String() + "/story")
	if staff.Code != http.StatusOK {
		t.Fatalf("the staff story endpoint = %d, want 200 (body: %s)", staff.Code, staff.Body.String())
	}
	var staffBody struct {
		ID           string            `json:"id"`
		Translations map[string]string `json:"translations"`
	}
	if err := json.NewDecoder(staff.Body).Decode(&staffBody); err != nil {
		t.Fatalf("decode staff body: %v", err)
	}
	if staffBody.ID == "" || staffBody.Translations["en"] == "" {
		t.Fatalf("staff body = %+v, want the full staff projection", staffBody)
	}

	// The participant's own path lives at /play and is unaffected by the
	// staff route sharing a prefix — here refused, since the fake Access is
	// staged to refuse everyone, which is enough to prove the request
	// reached this handler's own admission check rather than the staff
	// route's RBAC gate (that would answer a bare 403 forbidden, not this
	// handler's own not_a_participant).
	participant := do("/contests/" + contestID.String() + "/play/story")
	if participant.Code != http.StatusForbidden {
		t.Fatalf("the participant story endpoint = %d, want 403 (body: %s)", participant.Code, participant.Body.String())
	}
	if code := errorCode(t, participant); code != "not_a_participant" {
		t.Fatalf("code = %q, want not_a_participant (proving this handler, not the staff route's RBAC, answered)", code)
	}
}

// The answer endpoint sends Submit exactly what the URL and the body carry —
// never a value invented by the handler, and never a participant or contest
// other than what Access just resolved.
func TestAnswerSubmitsTheURLsQuestionAndTheBodysValue(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	questionID := uuid.New()
	participantID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: participantID}

	rec := f.post("/contests/"+contestID.String()+"/questions/"+questionID.String()+"/answer",
		`{"value":"the butler"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !f.submitter.called {
		t.Fatal("Submit was never called")
	}
	got := f.submitter.gotCmd
	if got.QuestionID != questionID {
		t.Fatalf("QuestionID = %s, want %s (from the URL)", got.QuestionID, questionID)
	}
	if got.Value != "the butler" {
		t.Fatalf("Value = %q, want %q (from the body)", got.Value, "the butler")
	}
	if got.Participant.ID != participantID || got.Contest.ID != contestID {
		t.Fatalf("command = %+v, want the participant and contest Access resolved", got)
	}
}

// The response carries exactly what SubmitOutcome says — never more, never
// a reference answer.
func TestAnswerReturnsTheOutcome(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	remaining := 2
	f.submitter.outcome = contests.SubmitOutcome{
		Correct: true, PointsAwarded: 10, AttemptsRemaining: &remaining, Closed: true,
	}

	rec := f.post("/contests/"+contestID.String()+"/questions/"+uuid.New().String()+"/answer", `{"value":"yes"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Correct           bool `json:"correct"`
		PointsAwarded     int  `json:"points_awarded"`
		AttemptsRemaining *int `json:"attempts_remaining"`
		Closed            bool `json:"closed"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !payload.Correct || payload.PointsAwarded != 10 || payload.AttemptsRemaining == nil ||
		*payload.AttemptsRemaining != 2 || !payload.Closed {
		t.Fatalf("payload = %+v, want the staged outcome", payload)
	}
}

// CLAUDE.md rule 1: every refusal contests.Service.Submit can answer with
// needs a declared sentinel, a mapping in fail(), and a test asserting the
// 4xx.
func TestAnswerRefusalsBecomeTheDocumentedStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"question not found", contests.ErrQuestionNotFound, http.StatusNotFound, "question_not_found"},
		{"answer too long", contests.ErrAnswerTooLong, http.StatusBadRequest, "answer_too_long"},
		{"question closed", contests.ErrQuestionClosed, http.StatusConflict, "question_closed"},
		// §6.1.1: a sequential contest refuses an answer to a question a
		// registration has not opened yet, whatever the interface shows.
		{"question not open", contests.ErrQuestionNotOpen, http.StatusConflict, "question_not_open"},
		{"deadline passed", contests.ErrDeadlinePassed, http.StatusConflict, "deadline_passed"},
		// Finding 1: seven concurrent answers to the very same question can
		// run contests.Service.Submit out of retries; before this fix the
		// handler had no case for it and a real outage and this ordinary
		// contention answered the same way — internal_error, 500.
		{"too many concurrent attempts", contests.ErrTooManyAttemptConflicts, http.StatusConflict, "attempt_conflict"},
		{"not a participant", queryproxy.ErrNotAParticipant, http.StatusForbidden, "not_a_participant"},
		{"contest not running", queryproxy.ErrContestNotRunning, http.StatusConflict, "contest_not_running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newParticipantFixture(t)
			contestID := uuid.New()
			f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
			f.access.participant = contests.Participant{ID: uuid.New()}
			f.submitter.err = tc.err

			rec := f.post("/contests/"+contestID.String()+"/questions/"+uuid.New().String()+"/answer", `{"value":"x"}`)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if code := errorCode(t, rec); code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

// A question identifier that is not a UUID is a 400, and Submit is never
// called with garbage.
func TestAnswerWithAnInvalidQuestionIDIsA400(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}

	rec := f.post("/contests/"+contestID.String()+"/questions/not-a-uuid/answer", `{"value":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if f.submitter.called {
		t.Fatal("Submit was called with an invalid question identifier")
	}
}

// A malformed body is a 400, and Submit is never called.
func TestAnswerWithAnInvalidBodyIsA400(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}

	rec := f.post("/contests/"+contestID.String()+"/questions/"+uuid.New().String()+"/answer", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if f.submitter.called {
		t.Fatal("Submit was called with a body that could not be decoded")
	}
}

// Finding 1, end to end: the review's own scenario is seven simultaneous
// answers to the very same question outrunning contests.Service.Submit's own
// retry bound — every retry loses the attempt-number race, and Submit used
// to hand the caller an error the handler had no case for (a 500,
// indistinguishable from a real outage) rather than the refusal a
// participant can act on. This drives that scenario through the real
// contests.Service (not fakeSubmitter's canned error) and the real handler,
// so what is under test is Submit's own retry loop and fail()'s own mapping
// together, not either one asserted in isolation.
//
// conteststest.Submissions.ConflictsRemaining stands in for the seven
// simultaneous racers — the same technique
// TestSubmitGivesUpAfterTooManyConflicts uses at the service level (its own
// doc explains why: a Go map has no analogue of the table's own UNIQUE
// constraint racing two real transactions, so the genuine race is proven
// against PostgreSQL instead, in
// internal/postgres/submissions_test.go and
// TestInsertConcurrentlyNeverExceedsMaxAttemptsOrDuplicatesAnAttemptNumber).
// What this test adds on top is the part that repository-level proof cannot
// reach on its own: that running out of retries surfaces as a 4xx, not a
// 5xx.
func TestSevenConcurrentAnswersEndUpAsARefusalNotAnInternalError(t *testing.T) {
	stores := conteststest.NewFixture()
	starts := conteststest.FixtureNow.Add(-time.Hour)
	ends := conteststest.FixtureNow.Add(time.Hour)
	c := stores.Contests.Put(contests.Contest{
		Status: contests.StatusRunning, Timing: contests.TimingFixed,
		StartsAt: &starts, EndsAt: &ends,
	})
	user := stores.AddUser("racing-student")
	p := stores.Registrations.Put(contests.Participant{
		ContestID: c.ID, UserID: user.ID, Status: contests.RegistrationActive,
	})
	q := stores.Questions.Put(contests.Question{ContestID: c.ID, Kind: contests.KindText, IsVisible: true})

	// However many of the seven actually lost every round, this is what it
	// looks like from Submit's side: its own retry loop never once sees
	// anything but a conflict.
	stores.Submissions.ConflictsRemaining = 1000

	c2 := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c2.Close() })
	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c2, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: user.ID, Login: user.Login})
	if err != nil {
		t.Fatalf("session Create() = %v", err)
	}
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users,
		Authorizer: rbac.New(noRoles{}),
		Cookies:    auth.NewCookieWriter(false), Logger: log,
	})

	access := &fakeAccess{participant: p, contest: c}
	reader := contests.NewReader(stores.Stories, stores.Questions, conteststest.NewAttempts(), stores.Sequence)
	router := chi.NewRouter()
	// stores.Service, not a fakeSubmitter: what answers here is the real
	// retry loop.
	api.NewParticipantHandler(access, reader, stores.Service, mw, log, "en").Mount(router)

	req := httptest.NewRequest(http.MethodPost,
		"/contests/"+c.ID.String()+"/questions/"+q.ID.String()+"/answer",
		strings.NewReader(`{"value":"anything"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "attempt_conflict" {
		t.Fatalf("code = %q, want attempt_conflict (body: %s)", code, rec.Body.String())
	}
}

// Finding 3 (queryproxy's own, reused here): a caller over their own rate
// budget is refused before Access — and before Submit — ever run.
func TestAnswerRateLimitRefusalIsA429AndNeverReachesSubmit(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.admitReadErr = queryrunner.ErrTooManyQueries

	rec := f.post("/contests/"+uuid.New().String()+"/questions/"+uuid.New().String()+"/answer", `{"value":"x"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "query_too_often" {
		t.Fatalf("code = %q, want query_too_often", code)
	}
	if f.access.accessCalled || f.submitter.called {
		t.Fatal("Access or Submit was reached after AdmitRead refused")
	}
}
