package api_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/olymp-db-platform/backend/internal/api"
	"github.com/devrdn/olymp-db-platform/backend/internal/archive"
	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests/conteststest"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/cache"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/logging"
	"github.com/devrdn/olymp-db-platform/backend/internal/rbac"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
)

type archiveStore struct {
	contest archive.Contest
	err     error
	lists   atomic.Int64
	filter  archive.Filter
}

func (s *archiveStore) List(_ context.Context, f archive.Filter) (archive.Page, error) {
	s.lists.Add(1)
	s.filter = f
	return archive.Page{Items: []archive.Contest{s.contest}, Total: 1}, nil
}

func (s *archiveStore) ByID(context.Context, uuid.UUID) (archive.Contest, error) {
	if s.err != nil {
		return archive.Contest{}, s.err
	}
	return s.contest, nil
}

type archiveSwitch struct {
	on  atomic.Bool
	err error
}

func (s *archiveSwitch) ArchiveEnabled(context.Context) (bool, error) { return s.on.Load(), s.err }

type archiveTexts struct{ lang string }

func (a *archiveTexts) BodyIn(_ context.Context, _ uuid.UUID, lang string) (string, error) {
	return "story " + lang, nil
}

func (a *archiveTexts) FirstIn(_ context.Context, _ uuid.UUID, langs []string) (string, error) {
	return "notes " + langs[0], nil
}

func (a *archiveTexts) ForContest(_ context.Context, _ uuid.UUID, lang string) ([]contests.VisibleQuestion, error) {
	a.lang = lang
	if lang != "ro" && lang != "en" {
		return nil, nil
	}
	return []contests.VisibleQuestion{{
		ID: uuid.New(), Kind: contests.KindText, Points: 5, Title: "Q " + lang, BodyMD: "body", ChoiceIDs: []string{"a"}, Choices: map[string]string{"a": "A"},
	}}, nil
}

type archiveFixture struct {
	router  http.Handler
	store   *archiveStore
	on      *archiveSwitch
	texts   *archiveTexts
	cookie  *http.Cookie
	contest archive.Contest
}

func newArchiveFixture(t *testing.T) *archiveFixture {
	t.Helper()
	stores := conteststest.NewFixture()
	u := stores.Users.Add(users.User{Login: "reader", FullName: "Rita Reader", Status: users.StatusActive})
	c := cache.NewMemory(10000)
	t.Cleanup(func() { _ = c.Close() })
	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: u.ID, Login: u.Login})
	if err != nil {
		t.Fatal(err)
	}
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users, Authorizer: rbac.New(noRoles{}),
		Cookies: auth.NewCookieWriter(false), Logger: log,
	})
	start := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	f := &archiveFixture{
		store: &archiveStore{}, on: &archiveSwitch{}, texts: &archiveTexts{},
		cookie: &http.Cookie{Name: sessionName, Value: token},
	}
	f.contest = archive.Contest{
		ID: uuid.New(), Status: contests.StatusFinished, Visibility: contests.VisibilityOpen, StartsAt: &start, EndsAt: &end, Participants: 7,
		Titles:          map[string]string{"en": "Spring", "ro": "Primavara"},
		Descriptions:    map[string]string{"en": "About", "ro": "Despre"},
		Languages:       []string{"en", "ro"},
		DefaultLanguage: "en",
	}
	f.store.contest = f.contest
	service := archive.NewService(archive.Config{
		Repository: f.store, Switch: f.on, Stories: f.texts, Notes: f.texts, Questions: f.texts,
	})
	f.router = api.NewRouter(api.Deps{
		Logger: log,
		Modules: []api.Module{
			api.NewArchiveHandler(service, auth.NewLimiter(c), mw, log, "en"),
		},
	})
	return f
}

func (f *archiveFixture) get(method, path string, signedIn bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	if signedIn {
		req.AddCookie(f.cookie)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

var requestID = regexp.MustCompile(`"request_id":"[^"]*"`)

func withoutRequestID(rec *httptest.ResponseRecorder) string {
	return requestID.ReplaceAllString(rec.Body.String(), "")
}

// stableHeaders drops the one header that is unique per request.
func stableHeaders(rec *httptest.ResponseRecorder) http.Header {
	h := rec.Header().Clone()
	h.Del("X-Request-Id")
	return h
}

func TestArchiveIsAbsentWhileSwitchedOff(t *testing.T) {
	f := newArchiveFixture(t)
	unknown := f.get(http.MethodGet, "/api/v1/no-such-route", false)
	id := f.contest.ID.String()
	for _, tc := range []struct {
		name, method, path string
		signedIn           bool
	}{
		{"anonymous list", http.MethodGet, "/api/v1/archive/contests", false},
		{"signed-in list", http.MethodGet, "/api/v1/archive/contests", true},
		{"contest", http.MethodGet, "/api/v1/archive/contests/" + id, true},
		{"malformed id", http.MethodGet, "/api/v1/archive/contests/not-a-uuid", true},
		{"wrong method", http.MethodPost, "/api/v1/archive/contests", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.get(tc.method, tc.path, tc.signedIn)
			if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" || errorMessage(t, rec) != "Resource not found" {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
			if withoutRequestID(rec) != withoutRequestID(unknown) || !reflect.DeepEqual(stableHeaders(rec), stableHeaders(unknown)) {
				t.Errorf("differs from the router's own 404: %q %v vs %q %v",
					rec.Body.String(), rec.Header(), unknown.Body.String(), unknown.Header())
			}
		})
	}
	if f.store.lists.Load() != 0 {
		t.Error("a switched-off archive read the store")
	}
}

func TestArchiveSwitchReadFailureIsAnInternalError(t *testing.T) {
	f := newArchiveFixture(t)
	f.on.err = errors.New("settings unavailable")
	rec := f.get(http.MethodGet, "/api/v1/archive/contests", true)
	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "internal_error" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if f.store.lists.Load() != 0 {
		t.Error("the store was read although the switch could not be")
	}
}

func TestArchiveNeedsASession(t *testing.T) {
	f := newArchiveFixture(t)
	f.on.on.Store(true)
	rec := f.get(http.MethodGet, "/api/v1/archive/contests", false)
	if rec.Code != http.StatusUnauthorized || errorCode(t, rec) != "unauthenticated" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestArchiveReadsAreLimitedPerAccount(t *testing.T) {
	f := newArchiveFixture(t)
	f.on.on.Store(true)
	for i := range api.ArchiveReadsPerMinute {
		if rec := f.get(http.MethodGet, "/api/v1/archive/contests", true); rec.Code != http.StatusOK {
			t.Fatalf("read %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := f.get(http.MethodGet, "/api/v1/archive/contests", true)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "archive_too_often" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if got := f.store.lists.Load(); got != int64(api.ArchiveReadsPerMinute) {
		t.Errorf("the service saw %d reads, want %d", got, api.ArchiveReadsPerMinute)
	}
}

func TestArchiveContestChargesTheBudgetBeforeParsingTheID(t *testing.T) {
	f := newArchiveFixture(t)
	f.on.on.Store(true)
	for i := range api.ArchiveReadsPerMinute {
		if rec := f.get(http.MethodGet, "/api/v1/archive/contests/not-a-uuid", true); rec.Code != http.StatusNotFound {
			t.Fatalf("read %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := f.get(http.MethodGet, "/api/v1/archive/contests/not-a-uuid", true)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "archive_too_often" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestArchiveListRefusesALongSearch(t *testing.T) {
	f := newArchiveFixture(t)
	f.on.on.Store(true)
	rec := f.get(http.MethodGet, "/api/v1/archive/contests?q="+strings.Repeat("я", 101), true)
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_request" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestArchiveListPassesTheBoundedPage(t *testing.T) {
	f := newArchiveFixture(t)
	f.on.on.Store(true)
	if rec := f.get(http.MethodGet, "/api/v1/archive/contests?limit=500&offset=-1", true); rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if f.store.filter.Limit != archive.MaxPageSize || f.store.filter.Offset != 0 {
		t.Errorf("service saw %+v", f.store.filter)
	}
}

func TestArchiveListTitlesFollowTheVisitor(t *testing.T) {
	f := newArchiveFixture(t)
	f.on.on.Store(true)
	rec := f.get(http.MethodGet, "/api/v1/archive/contests?lang=ro", true)
	body := decode(t, rec)
	items := body["items"].([]any)
	if items[0].(map[string]any)["title"] != "Primavara" || body["total"] != float64(1) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestArchiveContestOutsideTheArchiveIsNotFound(t *testing.T) {
	f := newArchiveFixture(t)
	f.on.on.Store(true)
	f.store.err = archive.ErrNotFound
	rec := f.get(http.MethodGet, "/api/v1/archive/contests/"+f.contest.ID.String(), true)
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" || errorMessage(t, rec) != "Resource not found" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.get(http.MethodGet, "/api/v1/archive/contests/not-a-uuid", true); rec.Code != http.StatusNotFound {
		t.Errorf("malformed id: %d", rec.Code)
	}
}

func TestArchiveContestCarriesNoTasksUnlessAllowed(t *testing.T) {
	f := newArchiveFixture(t)
	f.on.on.Store(true)
	rec := f.get(http.MethodGet, "/api/v1/archive/contests/"+f.contest.ID.String(), true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := decode(t, rec)["tasks"]; ok {
		t.Errorf("tasks present: %s", rec.Body.String())
	}
}

func TestArchiveContestNegotiatesOneLanguage(t *testing.T) {
	f := newArchiveFixture(t)
	f.on.on.Store(true)
	f.store.contest.ArchiveTasks = true
	rec := f.get(http.MethodGet, "/api/v1/archive/contests/"+f.contest.ID.String()+"?lang=ro", true)
	body := decode(t, rec)
	tasks, ok := body["tasks"].(map[string]any)
	if !ok || body["title"] != "Primavara" || body["description"] != "Despre" || tasks["lang"] != "ro" {
		t.Fatalf("body = %s", rec.Body.String())
	}
	qs := tasks["questions"].([]any)
	if len(qs) != 1 || qs[0].(map[string]any)["title"] != "Q ro" {
		t.Errorf("questions = %v", qs)
	}
	if f.texts.lang != "ro" {
		t.Errorf("questions read in %q", f.texts.lang)
	}
	// A language the contest does not offer falls back to its own, not to
	// the raw visitor value, which would leave the task list empty.
	rec = f.get(http.MethodGet, "/api/v1/archive/contests/"+f.contest.ID.String()+"?lang=fr", true)
	if got := decode(t, rec)["tasks"].(map[string]any)["lang"]; got != "en" {
		t.Errorf("lang = %v, want en", got)
	}
}

func TestArchiveResponsesAreAnAllowList(t *testing.T) {
	// The declared fields, whatever their values: a zero-valued omitempty
	// field would never show in a response built from one fixture.
	declared := map[string][]string{
		"list item": {"id", "title", "status", "starts_at", "ends_at", "participants"},
		"contest":   {"id", "title", "description", "status", "starts_at", "ends_at", "participants", "tasks"},
		"tasks":     {"lang", "story_md", "schema_notes_md", "questions"},
		"question":  {"position", "kind", "points", "is_bonus", "title", "body_md", "choice_ids", "choices"},
	}
	for name, typ := range api.ArchiveWireTypes {
		rt := reflect.TypeOf(typ)
		var got []string
		for i := range rt.NumField() {
			tag, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
			if tag == "" || tag == "-" {
				t.Errorf("%s.%s has no json name", name, rt.Field(i).Name)
				continue
			}
			got = append(got, tag)
		}
		slices.Sort(got)
		want := slices.Sorted(slices.Values(declared[name]))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s fields = %v, want %v", name, got, want)
		}
	}

	f := newArchiveFixture(t)
	f.on.on.Store(true)
	f.store.contest.ArchiveTasks = true
	keys := func(m map[string]any) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	check := func(what string, got map[string]any, want ...string) {
		t.Helper()
		slices.Sort(want)
		if !reflect.DeepEqual(keys(got), want) {
			t.Errorf("%s fields = %v, want %v", what, keys(got), want)
		}
	}

	list := decode(t, f.get(http.MethodGet, "/api/v1/archive/contests", true))
	check("list item", list["items"].([]any)[0].(map[string]any),
		"id", "title", "status", "starts_at", "ends_at", "participants")

	contest := decode(t, f.get(http.MethodGet, "/api/v1/archive/contests/"+f.contest.ID.String(), true))
	check("contest", contest,
		"id", "title", "description", "status", "starts_at", "ends_at", "participants", "tasks")
	tasks := contest["tasks"].(map[string]any)
	check("tasks", tasks, "lang", "story_md", "schema_notes_md", "questions")
	check("question", tasks["questions"].([]any)[0].(map[string]any),
		"position", "kind", "points", "is_bonus", "title", "body_md", "choice_ids", "choices")
}

func TestArchiveResponsesAreNotIndexed(t *testing.T) {
	f := newArchiveFixture(t)
	f.on.on.Store(true)
	for _, path := range []string{"/api/v1/archive/contests", "/api/v1/archive/contests/" + f.contest.ID.String()} {
		if got := f.get(http.MethodGet, path, true).Header().Get("X-Robots-Tag"); got != "noindex" {
			t.Errorf("%s: X-Robots-Tag = %q", path, got)
		}
	}
}

func TestArchiveSwitchedOffSpendsNoBudget(t *testing.T) {
	f := newArchiveFixture(t)
	for range 2 * api.ArchiveReadsPerMinute {
		f.get(http.MethodGet, "/api/v1/archive/contests", true)
	}
	f.on.on.Store(true)
	for i := range api.ArchiveReadsPerMinute {
		if rec := f.get(http.MethodGet, "/api/v1/archive/contests", true); rec.Code != http.StatusOK {
			t.Fatalf("read %d after switching on: %d", i+1, rec.Code)
		}
	}
}
