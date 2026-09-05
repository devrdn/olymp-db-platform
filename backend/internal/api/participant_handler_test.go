package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
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
	err         error
	// gotContestID records what Access was asked about, so a test can prove
	// the identifier came from the URL.
	gotContestID uuid.UUID
}

func (a *fakeAccess) Access(_ context.Context, contestID, _ uuid.UUID, _ netip.Addr) (contests.Participant, contests.Contest, error) {
	a.gotContestID = contestID
	return a.participant, a.contest, a.err
}

// participantFixture mounts the participant endpoints behind a session, with
// a fake Access and a real Reader over in-memory stores — the same
// conteststest fakes internal/contests's own Reader tests use, so what is
// under test here is the handler's wiring and error mapping, not a second
// implementation of the reading rules.
type participantFixture struct {
	router    http.Handler
	access    *fakeAccess
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
	reader := contests.NewReader(stories, questions, attempts)
	access := &fakeAccess{}

	router := chi.NewRouter()
	api.NewParticipantHandler(access, reader, mw, log, "en").Mount(router)

	return &participantFixture{
		router: router, access: access,
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
	reader := contests.NewReader(stores.Stories, stores.Questions, conteststest.NewAttempts())
	access := &fakeAccess{err: queryproxy.ErrNotAParticipant}
	api.NewParticipantHandler(access, reader, mw, log, "en").Mount(router)

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
