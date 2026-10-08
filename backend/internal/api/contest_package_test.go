package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// seedPackagedContest stores a contest with the two things that make the
// package sensitive and the one thing that makes it whole: reference answers,
// a hidden question, and a game script.
func seedPackagedContest(t *testing.T, f *contestFixture) contests.Contest {
	t.Helper()

	c := f.stores.SeedContest(contests.StatusFinished)
	if _, err := f.stores.Stories.Save(t.Context(), c.ID, map[string]string{
		"en": "A body in the stacks.",
	}); err != nil {
		t.Fatalf("seed story: %v", err)
	}
	f.stores.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 1, Kind: contests.KindChoice, Points: 5, IsVisible: true,
		ChoiceIDs: []string{"a", "b"},
		Texts: map[string]contests.QuestionText{
			"en": {BodyMD: "Who came in?", Choices: map[string]string{"a": "The butler", "b": "The gardener"}},
		},
		Answers: []contests.Answer{{MatchKind: contests.MatchExact, Value: "a"}},
	})
	f.stores.Questions.Put(contests.Question{
		ContestID: c.ID, Ord: 2, Kind: contests.KindFinal, Points: 10, IsVisible: false,
		Texts:   map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "Reginald the butler"}},
	})
	f.stores.Games.Put(c.ID, "CREATE TABLE suspects (id int);")
	return c
}

func TestTheContestPackageCarriesEverythingNeededToAuthorItAgain(t *testing.T) {
	f := newContestFixture(t, rbac.PermissionContestAdminAll)
	c := seedPackagedContest(t, f)

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q", got)
	}
	// Offered as a file, not rendered in the browser: it is a download, and
	// `nosniff` plus an attachment disposition is how every other download
	// this service serves is handed over.
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", got)
	}

	body := decode(t, rec)
	if body["format"] != "dbcontest.contest-package" {
		t.Errorf("format = %v", body["format"])
	}
	if body["version"] != float64(1) {
		t.Errorf("version = %v, want 1", body["version"])
	}

	contest, ok := body["contest"].(map[string]any)
	if !ok {
		t.Fatalf("the package carries no contest: %s", rec.Body.String())
	}
	if contest["default_language"] != "en" {
		t.Errorf("default_language = %v", contest["default_language"])
	}
	if langs, _ := contest["languages"].([]any); len(langs) != 1 || langs[0] != "en" {
		t.Errorf("languages = %v", contest["languages"])
	}
	translations, _ := contest["translations"].(map[string]any)
	english, _ := translations["en"].(map[string]any)
	if english["title"] != "The Library Murder" {
		t.Errorf("english title = %v", english["title"])
	}
	if contest["question_mode"] != contests.QuestionModeMulti || contest["timing"] != contests.TimingFixed {
		t.Errorf("contest configuration = %v", contest)
	}

	story, _ := body["story"].(map[string]any)
	if story["en"] != "A body in the stacks." {
		t.Errorf("story = %v", body["story"])
	}

	questions, _ := body["questions"].([]any)
	if len(questions) != 2 {
		t.Fatalf("the package carries %d questions, want 2", len(questions))
	}
	first, _ := questions[0].(map[string]any)
	if first["kind"] != contests.KindChoice {
		t.Errorf("the first question is %v, want the choice one first", first["kind"])
	}
	texts, _ := first["texts"].(map[string]any)
	englishText, _ := texts["en"].(map[string]any)
	choices, _ := englishText["choices"].(map[string]any)
	if choices["a"] != "The butler" {
		t.Errorf("the choice labels came back as %v", choices)
	}
	answers, _ := first["answers"].([]any)
	if len(answers) != 1 {
		t.Fatalf("the first question carries %d reference answers, want 1", len(answers))
	}
	answer, _ := answers[0].(map[string]any)
	if answer["match_kind"] != contests.MatchExact || answer["value"] != "a" {
		t.Errorf("the reference answer came back as %v", answer)
	}
	// The hidden question is part of the contest, and this file is for the
	// people who wrote it.
	second, _ := questions[1].(map[string]any)
	if second["is_visible"] != false {
		t.Errorf("the hidden question exported as visible: %v", second)
	}

	policy, _ := body["sql_policy"].(map[string]any)
	if policy["mode"] != contests.ModeReadOnly || policy["disk_quota_ratio"] != float64(5) {
		t.Errorf("sql policy = %v", policy)
	}

	game, _ := body["game"].(map[string]any)
	if game["script"] != "CREATE TABLE suspects (id int);" {
		t.Errorf("game = %v", body["game"])
	}
}

func TestTheContestPackageLeavesLastYearsRunBehind(t *testing.T) {
	// The package is for authoring a contest again, not for restoring one.
	// Identifiers a new installation would have to mint itself, the status it
	// happens to be in and the window it ran in are all facts about last
	// year — carrying them makes the file look like a backup, which is a
	// different promise and one this does not keep (no roster, no
	// submissions, no query log).
	f := newContestFixture(t, rbac.PermissionContestAdminAll)
	c := seedPackagedContest(t, f)

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	body := decode(t, rec)
	contest, _ := body["contest"].(map[string]any)
	for _, absent := range []string{"id", "status", "starts_at", "ends_at", "created_at", "updated_at"} {
		if _, present := contest[absent]; present {
			t.Errorf("the package carries %q, which belongs to the run it came from", absent)
		}
	}
	for _, absent := range []string{"participants", "registrations", "submissions", "query_log", "managers"} {
		if _, present := body[absent]; present {
			t.Errorf("the package carries %q, which is not part of authoring a contest", absent)
		}
	}
	// The questions carry no identifiers either, for the same reason: an
	// importer mints its own, and a stale uuid in a file is an invitation to
	// write one that does not.
	questions, _ := body["questions"].([]any)
	first, _ := questions[0].(map[string]any)
	if _, present := first["id"]; present {
		t.Error("a packaged question carries the identifier it had last year")
	}
}

func TestTheContestPackageIsBehindContestEditRatherThanContestView(t *testing.T) {
	// The package is a full answer key, so it sits behind the permission that
	// means "may author the answers" rather than the one that means "may look
	// at this contest" (docs/ARCHITECTURE.md §15, item 12).
	//
	// Asserted against what the route actually asked the authorizer, because
	// no outcome could tell the two apart: rbac's managerPermissions grants a
	// manager contest.view and contest.edit together, so every caller who
	// passes one passes the other, and a 403 or a 200 would look identical
	// under either wiring.
	spy := &spyAuthorizer{allow: true}
	f := newContestFixtureWith(t, spy)
	c := seedPackagedContest(t, f)

	if rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/export", ""); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(spy.asked) != 1 || spy.asked[0] != rbac.PermissionContestEdit {
		t.Fatalf("the route asked for %v, want exactly [%s]", spy.asked, rbac.PermissionContestEdit)
	}
	if spy.scope != c.ID {
		t.Fatalf("the permission was checked against %s, want the contest in the URL", spy.scope)
	}
}

func TestAParticipantCannotReachTheAnswerKey(t *testing.T) {
	// An account with no permission at all and no standing in the contest —
	// which is what taking part in one is (a registration is not a contest
	// role). The refusal must carry nothing of the package with it.
	f := newContestFixture(t)
	c := seedPackagedContest(t, f)

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/export", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "butler") {
		t.Fatalf("the refusal carried a reference answer: %s", rec.Body.String())
	}
	if f.stores.Audit.Recorded("contest.package_export") {
		t.Error("a refused export was recorded as one")
	}
}

func TestAContestWithMoreQuestionsThanThePackageHoldsIsRefusedWithItsOwnCode(t *testing.T) {
	f := newContestFixture(t, rbac.PermissionContestAdminAll)
	c := f.stores.SeedContest(contests.StatusDraft)
	for i := range contests.MaxPackageQuestions + 1 {
		f.stores.Questions.Put(contests.Question{
			ContestID: c.ID, Ord: i + 1, Kind: contests.KindText, Points: 1, IsVisible: true,
			Texts: map[string]contests.QuestionText{"en": {BodyMD: "?"}},
		})
	}

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/export", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec); got != "package_too_large" {
		t.Fatalf("code = %q, want package_too_large", got)
	}
}

// spyAuthorizer records what a route asked of it. It satisfies
// auth.Authorizer, which is the interface the middleware holds precisely so
// that the permission a route demands can be asserted rather than inferred
// from a status code that several permissions would produce alike.
type spyAuthorizer struct {
	asked []string
	scope uuid.UUID
	allow bool
}

func (s *spyAuthorizer) Authorize(_ context.Context, _ rbac.Identity, permission string, contestID uuid.UUID) error {
	s.asked = append(s.asked, permission)
	s.scope = contestID
	if s.allow {
		return nil
	}
	return rbac.ErrForbidden
}

// newContestFixtureWith mounts the contest endpoints against a supplied
// authorizer, for the tests that assert which permission a route demands
// rather than what the real rules make of it.
func newContestFixtureWith(t *testing.T, authorizer auth.Authorizer) *contestFixture {
	t.Helper()

	stores := conteststest.NewFixture()
	actor := stores.Users.Add(users.User{
		Login: "organizer", FullName: "Organizer", Status: users.StatusActive,
	})

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: actor.ID, Login: actor.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users, Authorizer: authorizer,
		Cookies: auth.NewCookieWriter(false), Logger: log,
	})

	router := chi.NewRouter()
	api.NewContestsHandler(stores.Service, mw, log, "en").Mount(router)

	return &contestFixture{
		router:   router,
		service:  stores.Service,
		stores:   stores,
		sessions: sessions,
		actor:    actor,
		cookie:   &http.Cookie{Name: auth.SessionCookieName, Value: token},
	}
}

// Both export routes hang off /contests/{id}, and they are registered by two
// different modules: the package inside ContestsHandler's own r.Route
// subtree, the CSV as a flat path ParticipantHandler adds beside it. chi does
// not fail to build when two modules claim overlapping paths — it silently
// lets one win, which is how GET /contests/{id}/story once stopped answering
// as the staff endpoint at all (ParticipantHandler.Mount's own doc). So the
// assembled router is probed rather than reasoned about.
func TestBothExportRoutesSurviveBeingMountedTogether(t *testing.T) {
	stores := conteststest.NewFixture()
	actor := stores.Users.Add(users.User{
		Login: "student", FullName: "Student", Status: users.StatusActive,
	})

	memory := cache.NewMemory(1000)
	t.Cleanup(func() { _ = memory.Close() })

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(memory, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: actor.ID, Login: actor.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users, Authorizer: rbac.New(noRoles{}),
		Cookies: auth.NewCookieWriter(false), Logger: log,
	})

	router := chi.NewRouter()
	api.NewContestsHandler(stores.Service, mw, log, "en").Mount(router)
	access := &fakeAccess{}
	access.err = contests.ErrNotAParticipant
	api.NewParticipantHandler(access, contests.NewReader(stores.Stories, stores.Questions, conteststest.NewAttempts(stores.Submissions), nil),
		&fakeHistory{}, &fakeSubmitter{}, answerRate(memory, fixtureAnswersPerMinute), mw, log, "en").Mount(router)

	contestID := uuid.New().String()
	for _, tc := range []struct{ name, path string }{
		{"the contest package", "/contests/" + contestID + "/export"},
		{"the participant's query log as CSV", "/contests/" + contestID + "/play/log.csv"},
		// The paged read beside it, which the .csv suffix must not have
		// shadowed.
		{"the participant's query log as a page", "/contests/" + contestID + "/play/log"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s answered 404: the route is not reachable once both modules are mounted", tc.name)
		}
	}
}
