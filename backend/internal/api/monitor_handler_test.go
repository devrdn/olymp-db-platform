package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// watchStore is an in-memory monitor.WatchStore: it knows which contest each
// registration belongs to, answers with one of everything, and remembers the
// last feed and queries filters it was asked, so a test can see a parameter
// arrive.
type watchStore struct {
	mu            sync.Mutex
	registrations map[uuid.UUID]uuid.UUID // registration → contest
	lastFeed      monitor.FeedQuery
	lastQueries   monitor.QueriesQuery
}

func (s *watchStore) Roster(_ context.Context, contest uuid.UUID, _ int) (monitor.Roster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roster := monitor.Roster{}
	for reg, c := range s.registrations {
		if c == contest {
			roster.Rows = append(roster.Rows, monitor.RosterRow{Registration: reg, Login: "student",
				Status: contests.RegistrationActive, Addresses: 2, LargePastes: 1})
		}
	}
	return roster, nil
}

func (s *watchStore) Participant(_ context.Context, contest, registration uuid.UUID) (monitor.Participant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.registrations[registration]; !ok || c != contest {
		return monitor.Participant{}, monitor.ErrParticipantNotFound
	}
	return monitor.Participant{Registration: registration, Contest: contest, Login: "student"}, nil
}

func (s *watchStore) Feed(_ context.Context, q monitor.FeedQuery) (monitor.FeedPage, error) {
	s.mu.Lock()
	s.lastFeed = q
	s.mu.Unlock()
	return monitor.FeedPage{Items: []monitor.FeedItem{{
		Source: monitor.SourceEvent, ID: "7", At: conteststest.FixtureNow, Kind: string(monitor.KindPaste),
		Registration: q.Registration, Login: "student", FullName: "=HYPERLINK(\"http://x\")", Data: json.RawMessage(`{"target":"editor","chars":3,"text":"abc"}`),
	}}}, nil
}

func (s *watchStore) Queries(_ context.Context, q monitor.QueriesQuery) (monitor.QueriesPage, error) {
	s.mu.Lock()
	s.lastQueries = q
	s.mu.Unlock()
	return monitor.QueriesPage{Items: []monitor.LoggedQuery{{At: conteststest.FixtureNow,
		QueryData: monitor.QueryData{ID: 3, SQL: "select 1", Status: "ok", IP: "192.0.2.1"}}}}, nil
}

func (s *watchStore) Answers(context.Context, uuid.UUID, uuid.UUID, int) (monitor.Answers, error) {
	return monitor.Answers{Questions: []monitor.QuestionAttempts{{QuestionOrd: 1, Attempts: []monitor.Attempt{{
		AnswerData: monitor.AnswerData{AttemptNo: 1, Value: "42", Correct: true}, At: conteststest.FixtureNow,
		Queries: []monitor.LoggedQuery{{At: conteststest.FixtureNow, QueryData: monitor.QueryData{ID: 3, SQL: "select 1"}}},
	}}}}}, nil
}

func (s *watchStore) Workspace(context.Context, uuid.UUID) (monitor.Workspace, error) {
	return monitor.Workspace{Notes: monitor.Notes{Body: "notes"},
		Tabs:      []monitor.Tab{{ID: uuid.New(), Title: "Query 1", Body: "select 1"}},
		Revisions: []monitor.RevisionInfo{{ID: 5, Document: "notes", Size: 5}}}, nil
}

func (s *watchStore) Revision(_ context.Context, _ uuid.UUID, id int64) (monitor.RevisionBody, error) {
	if id != 5 {
		return monitor.RevisionBody{}, monitor.ErrRevisionNotFound
	}
	return monitor.RevisionBody{RevisionInfo: monitor.RevisionInfo{ID: 5, Document: "notes"}, Body: "notes"}, nil
}

type monitorFixture struct {
	router    http.Handler
	store     *watchStore
	sink      *conteststest.Sink
	organizer users.User
	student   users.User
	rival     users.User // owns another contest
	cookies   map[uuid.UUID]*http.Cookie
	contest   contests.Contest
	reg       uuid.UUID
	otherReg  uuid.UUID // the rival contest's participant
}

func newMonitorFixture(t *testing.T) *monitorFixture {
	t.Helper()
	stores := conteststest.NewFixture()
	f := &monitorFixture{cookies: map[uuid.UUID]*http.Cookie{},
		store: &watchStore{registrations: map[uuid.UUID]uuid.UUID{}}, sink: conteststest.NewSink()}
	f.organizer = stores.Users.Add(users.User{Login: "organizer", FullName: "Olga", Status: users.StatusActive})
	f.student = stores.Users.Add(users.User{Login: "student", FullName: "Sasha", Status: users.StatusActive})
	f.rival = stores.Users.Add(users.User{Login: "rival", FullName: "Roma", Status: users.StatusActive})

	c := cache.NewMemory(10000)
	t.Cleanup(func() { _ = c.Close() })
	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	for _, u := range []users.User{f.organizer, f.student, f.rival} {
		token, err := sessions.Create(t.Context(), auth.Principal{UserID: u.ID, Login: u.Login})
		if err != nil {
			t.Fatal(err)
		}
		f.cookies[u.ID] = &http.Cookie{Name: auth.SessionCookieName, Value: token}
	}

	seed := func(owner users.User) (contests.Contest, uuid.UUID) {
		contest := stores.SeedContest(contests.StatusRunning)
		if err := stores.Managers.Grant(t.Context(), contests.Manager{
			ContestID: contest.ID, UserID: owner.ID, Role: rbac.RoleOwner, GrantedBy: owner.ID,
		}); err != nil {
			t.Fatal(err)
		}
		p, err := stores.Registrations.Add(t.Context(), contest.ID, f.student.ID)
		if err != nil {
			t.Fatal(err)
		}
		f.store.registrations[p.ID] = contest.ID
		return contest, p.ID
	}
	f.contest, f.reg = seed(f.organizer)
	_, f.otherReg = seed(f.rival)

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users, Authorizer: rbac.New(contestRoles{stores}),
		Cookies: auth.NewCookieWriter(false), Logger: log,
	})
	router := chi.NewRouter()
	watch := monitor.NewWatchService(monitor.WatchConfig{Store: f.store, Audit: audit.New(f.sink), Marks: c})
	api.NewMonitorHandler(watch, auth.NewLimiter(c), mw, log).Mount(router)
	f.router = router
	return f
}

func (f *monitorFixture) get(path string, who *users.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if who != nil {
		req.AddCookie(f.cookies[who.ID])
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *monitorFixture) base() string { return "/contests/" + f.contest.ID.String() + "/monitor" }

func (f *monitorFixture) one(reg uuid.UUID) string {
	return f.base() + "/participants/" + reg.String()
}

// Every route, as the organiser asks it.
func (f *monitorFixture) routes() []string {
	one := f.one(f.reg)
	return []string{
		f.base() + "/participants", f.base() + "/feed", one, one + "/timeline", one + "/queries",
		one + "/answers", one + "/workspace", one + "/workspace/revisions/5",
		f.base() + "/export.csv", one + "/export.csv",
	}
}

func TestMonitoringIsForTheContestsStaffOnly(t *testing.T) {
	f := newMonitorFixture(t)
	for _, path := range f.routes() {
		if rec := f.get(path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s, nobody: %d, want 401", path, rec.Code)
		}
		if rec := f.get(path, &f.student); rec.Code != http.StatusForbidden {
			t.Errorf("%s, the participant: %d, want 403", path, rec.Code)
		}
		// Staff of another contest are nobody here.
		if rec := f.get(path, &f.rival); rec.Code != http.StatusForbidden {
			t.Errorf("%s, another contest's owner: %d, want 403", path, rec.Code)
		}
		if rec := f.get(path, &f.organizer); rec.Code != http.StatusOK {
			t.Errorf("%s, the organiser: %d %s, want 200", path, rec.Code, rec.Body.String())
		}
	}
}

func TestAnotherContestsRegistrationIsNotFound(t *testing.T) {
	f := newMonitorFixture(t)
	for _, reg := range []string{f.otherReg.String(), uuid.NewString(), "not-a-uuid"} {
		one := f.base() + "/participants/" + reg
		for _, path := range []string{one, one + "/timeline", one + "/queries", one + "/answers",
			one + "/workspace", one + "/workspace/revisions/5", one + "/export.csv",
			f.base() + "/feed?participant=" + reg} {
			rec := f.get(path, &f.organizer)
			if rec.Code != http.StatusNotFound || errorCode(t, rec) != "monitor_participant_not_found" {
				t.Errorf("%s: %d %s, want 404 monitor_participant_not_found", path, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestAnUnknownRevisionIsNotFound(t *testing.T) {
	f := newMonitorFixture(t)
	for _, id := range []string{"6", "0", "-1", "x"} {
		rec := f.get(f.one(f.reg)+"/workspace/revisions/"+id, &f.organizer)
		if rec.Code != http.StatusNotFound || errorCode(t, rec) != "monitor_revision_not_found" {
			t.Errorf("revision %s: %d %s, want 404 monitor_revision_not_found", id, rec.Code, rec.Body.String())
		}
	}
}

func TestTheFeedPassesEveryFilterOn(t *testing.T) {
	f := newMonitorFixture(t)
	at := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	cursor := monitor.Cursor{At: at, Source: monitor.SourceQuery, ID: "9"}
	params := url.Values{
		"after": {cursor.Encode()}, "kinds": {"query,paste"}, "participant": {f.reg.String()},
		"from": {"2026-09-18T09:00:00Z"}, "until": {"2026-09-18T11:00:00Z"}, "limit": {"25"},
	}
	rec := f.get(f.base()+"/feed?"+params.Encode(), &f.organizer)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: %d %s", rec.Code, rec.Body.String())
	}
	got := f.store.lastFeed
	if got.Registration != f.reg || got.Contest != f.contest.ID || got.Limit != 25 ||
		strings.Join(got.Kinds, ",") != "query,paste" || got.After == nil || got.After.Compare(cursor) != 0 ||
		!got.From.Equal(at.Add(-time.Hour)) || !got.Until.Equal(at.Add(time.Hour)) {
		t.Errorf("the store was asked %+v", got)
	}

	before := monitor.Cursor{At: at, Source: monitor.SourceAnswer, ID: uuid.NewString()}
	if rec := f.get(f.one(f.reg)+"/timeline?before="+before.Encode(), &f.organizer); rec.Code != http.StatusOK {
		t.Fatalf("timeline: %d", rec.Code)
	}
	if got := f.store.lastFeed; got.Registration != f.reg || got.Before == nil || got.Before.Compare(before) != 0 {
		t.Errorf("the timeline asked %+v", got)
	}

	var body struct {
		Items []struct {
			Cursor string          `json:"cursor"`
			Kind   string          `json:"kind"`
			At     string          `json:"at"`
			Data   json.RawMessage `json:"data"`
		} `json:"items"`
		Newest string `json:"newest"`
		Oldest string `json:"oldest"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].Kind != "paste" || !strings.Contains(string(body.Items[0].Data), `"chars":3`) ||
		body.Newest != body.Items[0].Cursor || body.Oldest != body.Items[0].Cursor || body.Items[0].At == "" {
		t.Errorf("feed body: %s", rec.Body.String())
	}
	if _, err := monitor.ParseCursor(body.Newest); err != nil {
		t.Errorf("the feed's own cursor does not parse: %v", err)
	}
}

func TestTheFeedRefusesWhatItCannotServe(t *testing.T) {
	f := newMonitorFixture(t)
	c := monitor.Cursor{At: time.Now(), Source: monitor.SourceQuery, ID: "1"}.Encode()
	cases := map[string]string{
		"after=nonsense":                                       "monitor_invalid_cursor",
		"before=" + url.QueryEscape("bm9wZQ"):                  "monitor_invalid_cursor",
		"kinds=coffee":                                         "monitor_invalid_filter",
		"kinds=" + strings.Repeat("query,", 100):               "monitor_invalid_filter",
		"from=yesterday":                                       "monitor_invalid_filter",
		"from=2026-09-18T11:00:00Z&until=2026-09-18T10:00:00Z": "monitor_invalid_filter",
		"after=" + c + "&before=" + c:                          "monitor_invalid_filter",
	}
	for query, code := range cases {
		for _, path := range []string{f.base() + "/feed?", f.one(f.reg) + "/timeline?"} {
			rec := f.get(path+query, &f.organizer)
			if rec.Code != http.StatusBadRequest || errorCode(t, rec) != code {
				t.Errorf("%s%s: %d %s, want 400 %s", path, query, rec.Code, rec.Body.String(), code)
			}
		}
	}
}

func TestTheQueriesTabPassesItsFiltersOnAndRefusesBadOnes(t *testing.T) {
	f := newMonitorFixture(t)
	cursor := monitor.Cursor{At: time.Now().UTC(), Source: monitor.SourceQuery, ID: "12"}
	path := f.one(f.reg) + "/queries?" + url.Values{
		"status": {"error"}, "q": {"100%"}, "cursor": {cursor.Encode()}, "limit": {"10"},
	}.Encode()
	rec := f.get(path, &f.organizer)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ip":"192.0.2.1"`) {
		t.Fatalf("queries: %d %s", rec.Code, rec.Body.String())
	}
	got := f.store.lastQueries
	if got.Status != "error" || got.Search != "100%" || got.Limit != 10 || got.Before == nil ||
		got.Before.Compare(cursor) != 0 || got.Registration != f.reg {
		t.Errorf("the store was asked %+v", got)
	}

	event := monitor.Cursor{At: time.Now(), Source: monitor.SourceEvent, ID: "1"}.Encode()
	for query, code := range map[string]string{
		"status=fine":                   "monitor_invalid_filter",
		"q=" + strings.Repeat("x", 201): "monitor_invalid_filter",
		"cursor=" + event:               "monitor_invalid_filter",
		"cursor=garbage":                "monitor_invalid_cursor",
	} {
		rec := f.get(f.one(f.reg)+"/queries?"+query, &f.organizer)
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != code {
			t.Errorf("queries?%s: %d %s, want 400 %s", query, rec.Code, rec.Body.String(), code)
		}
	}
}

func TestTheParticipantsTableCarriesTheFlags(t *testing.T) {
	f := newMonitorFixture(t)
	rec := f.get(f.base()+"/participants", &f.organizer)
	var body struct {
		Rows []struct {
			RegistrationID uuid.UUID     `json:"registration_id"`
			Flags          monitor.Flags `json:"flags"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Rows) != 1 || body.Rows[0].RegistrationID != f.reg ||
		!body.Rows[0].Flags.MultipleIPs || !body.Rows[0].Flags.LargePaste || body.Rows[0].Flags.LongAbsence {
		t.Errorf("table: %s", rec.Body.String())
	}
}

func TestTheOrganisersReadsAreBudgeted(t *testing.T) {
	f := newMonitorFixture(t)
	var last *httptest.ResponseRecorder
	for range 241 {
		// A refused read spends the budget too: half of these are 404s.
		last = f.get(f.base()+"/participants/"+uuid.NewString(), &f.organizer)
	}
	if last.Code != http.StatusTooManyRequests || errorCode(t, last) != "monitor_too_often" || last.Header().Get("Retry-After") == "" {
		t.Fatalf("the 241st read: %d %s, want 429 monitor_too_often with Retry-After", last.Code, last.Body.String())
	}
	if rec := f.get(f.base()+"/participants", &f.organizer); rec.Code != http.StatusTooManyRequests {
		t.Errorf("another route after the budget is spent: %d, want 429", rec.Code)
	}
	// Somebody else's budget is their own.
	if rec := f.get("/contests/"+f.contest.ID.String()+"/monitor/participants", &f.student); rec.Code != http.StatusForbidden {
		t.Errorf("a participant: %d, want 403 before any budget", rec.Code)
	}
}

func TestACursorOutsideAnyClockIsRefused(t *testing.T) {
	f := newMonitorFixture(t)
	for _, at := range []time.Time{time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
		time.UnixMicro(-1 << 62), time.UnixMicro(1 << 62)} {
		c := monitor.Cursor{At: at, Source: monitor.SourceQuery, ID: "1"}.Encode()
		for _, path := range []string{f.base() + "/feed?after=" + c, f.one(f.reg) + "/timeline?before=" + c,
			f.one(f.reg) + "/queries?cursor=" + c} {
			rec := f.get(path, &f.organizer)
			if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "monitor_invalid_cursor" {
				t.Errorf("%s: %d %s, want 400 monitor_invalid_cursor", path, rec.Code, rec.Body.String())
			}
		}
	}
}
