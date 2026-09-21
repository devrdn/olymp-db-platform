package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/profile"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// profileStore is an in-memory profile.Store.
type profileStore struct {
	summary    profile.Summary
	enrolments []profile.Enrolment
	activity   profile.Activity
}

func (s *profileStore) Summary(context.Context, uuid.UUID) (profile.Summary, error) {
	return s.summary, nil
}

func (s *profileStore) Enrolments(_ context.Context, _ uuid.UUID, limit int) ([]profile.Enrolment, error) {
	out := s.enrolments
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *profileStore) Activity(context.Context, uuid.UUID) (profile.Activity, error) {
	return s.activity, nil
}

// profileResults is the leaderboard as the profile reads it, counting how
// many contests it was asked about: the list must ask about none.
type profileResults struct {
	own  map[uuid.UUID]leaderboard.Own
	asks int
}

func (r *profileResults) Own(_ context.Context, contestID, _ uuid.UUID) (leaderboard.Own, error) {
	r.asks++
	own, ok := r.own[contestID]
	if !ok {
		return leaderboard.Own{}, leaderboard.ErrNotAParticipant
	}
	return own, nil
}

// profileWatch is the monitoring read side, answering with one of everything
// and remembering the last queries filter it was asked.
type profileWatch struct {
	lastQueries monitor.QueriesQuery
	// registrations maps a registration to its contest, so a read of
	// somebody else's is refused the way the real store refuses it.
	registrations map[uuid.UUID]uuid.UUID
}

func (w *profileWatch) Queries(_ context.Context, q monitor.QueriesQuery) (monitor.QueriesPage, error) {
	q, err := q.Normalize()
	if err != nil {
		return monitor.QueriesPage{}, err
	}
	w.lastQueries = q
	return monitor.QueriesPage{Items: []monitor.LoggedQuery{
		{At: conteststest.FixtureNow, QueryData: monitor.QueryData{ID: 3, SQL: "select 1",
			Status: string(queryrunner.StatusOK), IP: "192.0.2.1"}},
		{At: conteststest.FixtureNow, QueryData: monitor.QueryData{ID: 4, SQL: "drop table suspects",
			Status: string(queryrunner.StatusRejected), Error: "syntax error at or near", IP: "192.0.2.1"}},
		{At: conteststest.FixtureNow, QueryData: monitor.QueryData{ID: 5, SQL: "select oops",
			Status: "error", Error: `ERROR: column "oops" does not exist (SQLSTATE 42703)`, IP: "192.0.2.1"}},
	}}, nil
}

func (w *profileWatch) Answers(context.Context, uuid.UUID, uuid.UUID) (monitor.Answers, error) {
	return monitor.Answers{Questions: []monitor.QuestionAttempts{{QuestionOrd: 1, Attempts: []monitor.Attempt{{
		AnswerData: monitor.AnswerData{AttemptNo: 1, Value: "42", Correct: true, PointsAwarded: 10},
		At:         conteststest.FixtureNow,
		Queries: []monitor.LoggedQuery{{At: conteststest.FixtureNow, QueryData: monitor.QueryData{
			ID: 3, SQL: "select 1", Status: "error", Error: "ERROR: boom (SQLSTATE 42703)", IP: "192.0.2.1"}}},
	}}}}}, nil
}

func (w *profileWatch) Workspace(context.Context, uuid.UUID, uuid.UUID) (monitor.Workspace, error) {
	return monitor.Workspace{Notes: monitor.Notes{Body: "the butler did it"},
		Tabs:      []monitor.Tab{{ID: uuid.New(), Title: "Query 1", Body: "select 1"}},
		Revisions: []monitor.RevisionInfo{{ID: 5, Document: "notes", Size: 5}}}, nil
}

// profileHistory is the query log the CSV download streams.
type profileHistory struct {
	rows []queryrunner.HistoryEntry
}

func (h *profileHistory) History(context.Context, uuid.UUID, int, int) ([]queryrunner.HistoryEntry, int, error) {
	return h.rows, len(h.rows), nil
}

func (h *profileHistory) ExportHistory(_ context.Context, _ uuid.UUID, yield func(queryrunner.HistoryEntry) error) (bool, error) {
	for _, row := range h.rows {
		if err := yield(row); err != nil {
			return false, err
		}
	}
	return false, nil
}

type profileFixture struct {
	router  http.Handler
	store   *profileStore
	results *profileResults
	watch   *profileWatch
	history *profileHistory
	stores  *conteststest.Fixture
	student users.User
	rival   users.User
	cookies map[uuid.UUID]*http.Cookie
	now     time.Time
	// finished is a contest that has ended for the student, running one that
	// has not, and outside one the student is not on at all.
	finished contests.Contest
	running  contests.Contest
	outside  contests.Contest
}

func newProfileFixture(t *testing.T) *profileFixture {
	t.Helper()
	stores := conteststest.NewFixture()
	f := &profileFixture{
		store: &profileStore{}, results: &profileResults{own: map[uuid.UUID]leaderboard.Own{}},
		watch: &profileWatch{registrations: map[uuid.UUID]uuid.UUID{}}, history: &profileHistory{},
		stores: stores, cookies: map[uuid.UUID]*http.Cookie{}, now: conteststest.FixtureNow,
	}
	f.student = stores.Users.Add(users.User{Login: "student", FullName: "Sasha", Status: users.StatusActive})
	f.rival = stores.Users.Add(users.User{Login: "rival", FullName: "Roma", Status: users.StatusActive})

	c := cache.NewMemory(10000)
	t.Cleanup(func() { _ = c.Close() })
	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	for _, u := range []users.User{f.student, f.rival} {
		token, err := sessions.Create(t.Context(), auth.Principal{UserID: u.ID, Login: u.Login})
		if err != nil {
			t.Fatal(err)
		}
		f.cookies[u.ID] = &http.Cookie{Name: auth.SessionCookieName, Value: token}
	}

	enrol := func(status string, who users.User) contests.Contest {
		contest := stores.SeedContest(status)
		p, err := stores.Registrations.Add(t.Context(), contest.ID, who.ID)
		if err != nil {
			t.Fatal(err)
		}
		f.watch.registrations[p.ID] = contest.ID
		// The store answers for the account asking, so only the student's own
		// registrations are on their list — and it counts the row's own
		// numbers with it.
		if who.ID == f.student.ID {
			f.store.enrolments = append(f.store.enrolments, profile.Enrolment{Contest: contest, Participant: p,
				Result: profile.Result{Scoring: contests.ScoringPoints, Points: 20, Solved: 2}})
		}
		return contest
	}
	f.finished = enrol(contests.StatusFinished, f.student)
	f.running = enrol(contests.StatusRunning, f.student)
	f.outside = enrol(contests.StatusFinished, f.rival)
	f.results.own[f.finished.ID] = leaderboard.Own{
		State: leaderboard.StateFinal, Open: true, Scoring: contests.ScoringPoints, Place: 2, Participants: 9,
		Row: leaderboard.Row{Entry: leaderboard.Entry{Points: 20, Solved: 2}},
	}

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users, Authorizer: rbac.New(noRoles{}),
		Cookies: auth.NewCookieWriter(false), Logger: log,
	})
	service := profile.NewService(profile.Config{
		Store: f.store, Contests: stores.Contests, Participants: stores.Registrations,
		Results: f.results, Attempts: f.watch, Now: func() time.Time { return f.now },
	})
	router := chi.NewRouter()
	api.NewProfileHandler(service, f.watch, f.history, auth.NewLimiter(c), mw, log, "en").Mount(router)
	f.router = router
	return f
}

func (f *profileFixture) get(path string, who *users.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if who != nil {
		req.AddCookie(f.cookies[who.ID])
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// contestRoutes is every route under one contest, as the participant asks it.
func contestRoutes(id uuid.UUID) []string {
	base := "/me/contests/" + id.String()
	return []string{base + "/report", base + "/queries", base + "/answers", base + "/workspace", base + "/log.csv"}
}

func TestTheProfileIsForTheSignedInAccountOnly(t *testing.T) {
	f := newProfileFixture(t)
	paths := append([]string{"/me/summary", "/me/contests"}, contestRoutes(f.finished.ID)...)
	for _, path := range paths {
		if rec := f.get(path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s, nobody: %d, want 401", path, rec.Code)
		}
		if rec := f.get(path, &f.student); rec.Code != http.StatusOK {
			t.Errorf("%s, the participant: %d %s, want 200", path, rec.Code, rec.Body.String())
		}
	}
}

// One 404 for a contest that does not exist, one somebody else is on, one
// still running for the caller, and an identifier that is not a UUID. The
// profile never says which (design §2.2).
func TestEveryContestThatIsNotTheCallersFinishedOneIsTheSame404(t *testing.T) {
	f := newProfileFixture(t)
	f.now = conteststest.FixtureNow
	cases := map[string]string{
		"a contest that does not exist": uuid.NewString(),
		"somebody else's contest":       f.outside.ID.String(),
		"a contest still running":       f.running.ID.String(),
		"not an identifier at all":      "not-a-uuid",
	}
	for name, id := range cases {
		for _, path := range contestRoutes(uuid.Nil) {
			path = strings.Replace(path, uuid.Nil.String(), id, 1)
			rec := f.get(path, &f.student)
			if rec.Code != http.StatusNotFound || errorCode(t, rec) != "profile_contest_not_found" {
				t.Errorf("%s (%s): %d %s, want 404 profile_contest_not_found",
					path, name, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestTheSummaryIsTheAccountsFourNumbers(t *testing.T) {
	f := newProfileFixture(t)
	f.store.summary = profile.Summary{Contests: 4, Finished: 3, Queries: 120, Solved: 9}

	rec := f.get("/me/summary", &f.student)
	var body struct {
		Contests int `json:"contests"`
		Finished int `json:"finished"`
		Queries  int `json:"queries"`
		Solved   int `json:"solved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Contests != 4 || body.Finished != 3 || body.Queries != 120 || body.Solved != 9 {
		t.Errorf("summary: %s", rec.Body.String())
	}
}

type profileContestsBody struct {
	Truncated bool `json:"truncated"`
	Items     []struct {
		ContestID uuid.UUID `json:"contest_id"`
		Title     string    `json:"title"`
		Status    string    `json:"status"`
		Over      bool      `json:"over"`
		StartsAt  string    `json:"starts_at"`
		Result    *struct {
			Scoring   string `json:"scoring"`
			Points    int    `json:"points"`
			Solved    int    `json:"solved"`
			Penalty   *int   `json:"penalty"`
			State     string `json:"state"`
			PlaceOpen bool   `json:"place_open"`
		} `json:"result"`
	} `json:"items"`
}

func (f *profileFixture) list(t *testing.T) profileContestsBody {
	t.Helper()
	rec := f.get("/me/contests", &f.student)
	if rec.Code != http.StatusOK {
		t.Fatalf("contests: %d %s", rec.Code, rec.Body.String())
	}
	var body profileContestsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestTheListCarriesTheResultOfAFinishedContestAndNothingOfARunningOne(t *testing.T) {
	f := newProfileFixture(t)
	body := f.list(t)
	if len(body.Items) != 2 {
		t.Fatalf("the list has %d items, want the student’s two: %+v", len(body.Items), body)
	}
	byID := map[uuid.UUID]int{}
	for i, item := range body.Items {
		byID[item.ContestID] = i
	}
	done := body.Items[byID[f.finished.ID]]
	if !done.Over || done.Result == nil || done.Result.Points != 20 || done.Result.Solved != 2 {
		t.Errorf("the finished contest reads %+v", done)
	}
	if !done.Result.PlaceOpen || done.Result.State != leaderboard.StateFinal {
		t.Errorf("the finished contest's table reads %+v, want final and open", done.Result)
	}
	if done.Title != "The Library Murder" || done.Status != contests.StatusFinished || done.StartsAt == "" {
		t.Errorf("the finished contest is missing its own facts: %+v", done)
	}
	running := body.Items[byID[f.running.ID]]
	if running.Over || running.Result != nil {
		t.Errorf("the running contest reads %+v, want no result at all", running)
	}
}

// The list names no place at all — not even on an open table — and costs no
// standings computation to say so. What it carries is the participant's own
// numbers and whether the report has a place to show (design §2.1).
func TestTheListNamesNoPlaceAndAsksTheLeaderboardNothing(t *testing.T) {
	f := newProfileFixture(t)
	rec := f.get("/me/contests", &f.student)
	if rec.Code != http.StatusOK {
		t.Fatalf("contests: %d %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{`"place"`, `"participants"`} {
		if strings.Contains(rec.Body.String(), field) {
			t.Errorf("the list carries %s: %s", field, rec.Body.String())
		}
	}
	if f.results.asks != 0 {
		t.Errorf("the leaderboard was asked %d times for a list, want none", f.results.asks)
	}
}

// The freeze is not walked round: the participant's own numbers are shown,
// and the row says the table is shut.
func TestAFrozenTableIsReportedShutOnTheList(t *testing.T) {
	f := newProfileFixture(t)
	freeze := 30
	c := f.finished
	c.LeaderboardFreezeMin = &freeze
	f.stores.Contests.Put(c)
	f.store.enrolments[0].Contest = c

	for _, item := range f.list(t).Items {
		if item.ContestID != f.finished.ID {
			continue
		}
		if item.Result == nil || item.Result.Points != 20 {
			t.Fatalf("the frozen contest reads %+v, want the own result", item.Result)
		}
		if item.Result.PlaceOpen {
			t.Errorf("the frozen contest says its table is open: %+v", item.Result)
		}
		if item.Result.State != leaderboard.StateFrozen {
			t.Errorf("state = %s, want frozen so the interface can say why", item.Result.State)
		}
		return
	}
	t.Fatal("the finished contest is not on the list")
}

func TestTheReportCarriesTheResultTheActivityAndTheQuestions(t *testing.T) {
	f := newProfileFixture(t)
	last := conteststest.FixtureNow.Add(40 * time.Minute)
	f.store.activity = profile.Activity{Queries: 31, Successful: 24, LastAnswerAt: &last}
	started := conteststest.FixtureNow
	p, err := f.stores.Registrations.ByUser(t.Context(), f.finished.ID, f.student.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.StartedAt = &started
	f.stores.Registrations.Put(p)

	rec := f.get("/me/contests/"+f.finished.ID.String()+"/report", &f.student)
	var body struct {
		Result struct {
			Points    int   `json:"points"`
			PlaceOpen bool  `json:"place_open"`
			Place     *int  `json:"place"`
			Penalty   *int  `json:"penalty"`
			Truncated *bool `json:"truncated"`
		} `json:"result"`
		Queries    int `json:"queries"`
		Successful int `json:"successful_queries"`
		WorkedMs   *int64
		Questions  []struct {
			QuestionID uuid.UUID `json:"question_id"`
			Ord        int       `json:"ord"`
			Attempts   int       `json:"attempts"`
			Solved     bool      `json:"solved"`
			SolvedAt   string    `json:"solved_at"`
			Points     int       `json:"points"`
			Penalty    int       `json:"penalty"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	if body.Result.Points != 20 || !body.Result.PlaceOpen || body.Result.Place == nil || *body.Result.Place != 2 {
		t.Errorf("result: %s", rec.Body.String())
	}
	if body.Queries != 31 || body.Successful != 24 {
		t.Errorf("activity: %s", rec.Body.String())
	}
	if len(body.Questions) != 1 || !body.Questions[0].Solved || body.Questions[0].Attempts != 1 ||
		body.Questions[0].Points != 10 || body.Questions[0].SolvedAt == "" {
		t.Errorf("questions: %s", rec.Body.String())
	}
	if body.Questions[0].Penalty != 0 {
		t.Errorf("a points contest charges no minutes: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"worked_ms":2400000`) {
		t.Errorf("the time worked is missing or wrong: %s", rec.Body.String())
	}
}

// In ICPC scoring a question carries the minutes it cost, not points: the
// server writes points_awarded = 0 on every ICPC submission, so a report
// without this field could only ever show nought.
func TestTheICPCReportCarriesEachQuestionsPenaltyMinutes(t *testing.T) {
	f := newProfileFixture(t)
	contest := f.finished
	contest.Scoring, contest.ICPCPenaltyMin = contests.ScoringICPC, 20
	f.stores.Contests.Put(contest)
	solvedAt := conteststest.FixtureNow
	f.results.own[contest.ID] = leaderboard.Own{
		State: leaderboard.StateFinal, Open: true, Scoring: contests.ScoringICPC, Place: 2, Participants: 9,
		Questions: 1,
		Row: leaderboard.Row{Entry: leaderboard.Entry{Solved: 1, Penalty: 50, Cells: []leaderboard.Cell{
			{SolvedAt: &solvedAt, Minute: 30, Wrong: 1},
		}}},
	}

	rec := f.get("/me/contests/"+contest.ID.String()+"/report", &f.student)
	var body struct {
		Result struct {
			Scoring string `json:"scoring"`
			Points  int    `json:"points"`
			Penalty *int   `json:"penalty"`
		} `json:"result"`
		Questions []struct {
			Points  int `json:"points"`
			Penalty int `json:"penalty"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	if body.Result.Scoring != contests.ScoringICPC || body.Result.Penalty == nil || *body.Result.Penalty != 50 {
		t.Fatalf("result: %s", rec.Body.String())
	}
	if len(body.Questions) != 1 || body.Questions[0].Penalty != 50 || body.Questions[0].Points != 10 {
		t.Errorf("questions: %s", rec.Body.String())
	}
}

// The participant's own queries, without the address, and with the error
// text the play screen shows rather than the organiser's.
func TestTheQueriesTabHidesTheAddressAndRedactsLikeTheConsole(t *testing.T) {
	f := newProfileFixture(t)
	rec := f.get("/me/contests/"+f.finished.ID.String()+"/queries", &f.student)
	if rec.Code != http.StatusOK {
		t.Fatalf("queries: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "192.0.2.1") || strings.Contains(rec.Body.String(), `"ip"`) {
		t.Errorf("the participant is shown their own address: %s", rec.Body.String())
	}
	var body struct {
		Items []struct {
			SQL    string `json:"sql"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 3 {
		t.Fatalf("queries: %s", rec.Body.String())
	}
	if body.Items[1].Error != "syntax error at or near" {
		t.Errorf("a refused query keeps the validator's own words: %+v", body.Items[1])
	}
	if body.Items[2].Error != "" {
		t.Errorf("the database's own words reach the participant: %+v", body.Items[2])
	}
}

func TestTheProfileQueriesTabPassesItsFiltersOnAndRefusesBadOnes(t *testing.T) {
	f := newProfileFixture(t)
	cursor := monitor.Cursor{At: conteststest.FixtureNow, Source: monitor.SourceQuery, ID: "12"}
	base := "/me/contests/" + f.finished.ID.String() + "/queries?"
	if rec := f.get(base+"status=error&q=suspects&limit=10&cursor="+cursor.Encode(), &f.student); rec.Code != http.StatusOK {
		t.Fatalf("queries: %d %s", rec.Code, rec.Body.String())
	}
	got := f.watch.lastQueries
	if got.Status != "error" || got.Search != "suspects" || got.Limit != 10 ||
		got.Before == nil || got.Before.Compare(cursor) != 0 || got.Contest != f.finished.ID {
		t.Errorf("the store was asked %+v", got)
	}
	for query, code := range map[string]string{
		"status=fine":                   "profile_invalid_filter",
		"q=" + strings.Repeat("x", 201): "profile_invalid_filter",
		"cursor=garbage":                "profile_invalid_cursor",
	} {
		rec := f.get(base+query, &f.student)
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != code {
			t.Errorf("queries?%s: %d %s, want 400 %s", query, rec.Code, rec.Body.String(), code)
		}
	}
}

// The answers tab shows the same attempts the organiser sees, and the
// queries under them are redacted for the participant like their own log.
func TestTheAnswersTabRedactsTheQueriesUnderEachAttempt(t *testing.T) {
	f := newProfileFixture(t)
	rec := f.get("/me/contests/"+f.finished.ID.String()+"/answers", &f.student)
	if rec.Code != http.StatusOK {
		t.Fatalf("answers: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "192.0.2.1") || strings.Contains(rec.Body.String(), "SQLSTATE") {
		t.Errorf("the answers tab leaks the address or the database's words: %s", rec.Body.String())
	}
}

// The notes and tabs as they were left. The revision history is the
// organiser's, and is not on this route at all (design §2.2).
func TestTheWorkspaceTabCarriesNoRevisionHistory(t *testing.T) {
	f := newProfileFixture(t)
	rec := f.get("/me/contests/"+f.finished.ID.String()+"/workspace", &f.student)
	if rec.Code != http.StatusOK {
		t.Fatalf("workspace: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "revision") {
		t.Errorf("the workspace tab carries the revision history: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "the butler did it") {
		t.Errorf("the workspace tab carries no notes: %s", rec.Body.String())
	}
}

func TestTheLogIsDownloadedAsCSVAfterTheContest(t *testing.T) {
	f := newProfileFixture(t)
	duration, rowCount := 7, 3
	f.history.rows = []queryrunner.HistoryEntry{
		{SQL: "select 1", Status: queryrunner.StatusOK, DurationMs: &duration, RowCount: &rowCount,
			ExecutedAt: conteststest.FixtureNow},
		{SQL: "select oops", Status: queryrunner.StatusError, Error: `ERROR: column "oops" does not exist`,
			ExecutedAt: conteststest.FixtureNow},
	}

	rec := f.get("/me/contests/"+f.finished.ID.String()+"/log.csv", &f.student)
	if rec.Code != http.StatusOK {
		t.Fatalf("log.csv: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), f.finished.ID.String()) {
		t.Errorf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "executed_at,status,duration_ms,row_count,error,sql") {
		t.Errorf("the file has no header row: %s", body)
	}
	if !strings.Contains(body, "select 1") || strings.Contains(body, "does not exist") {
		t.Errorf("the file is wrong: %s", body)
	}
}

func TestTheProfilesReadsAreBudgeted(t *testing.T) {
	f := newProfileFixture(t)
	var last *httptest.ResponseRecorder
	for range api.ProfileReadsPerMinute + 1 {
		// A refused read spends the budget too: every one of these is a 404.
		last = f.get("/me/contests/"+uuid.NewString()+"/report", &f.student)
	}
	if last.Code != http.StatusTooManyRequests || errorCode(t, last) != "profile_too_often" ||
		last.Header().Get("Retry-After") == "" {
		t.Fatalf("the read past the budget: %d %s, want 429 profile_too_often with Retry-After",
			last.Code, last.Body.String())
	}
	if rec := f.get("/me/summary", &f.student); rec.Code != http.StatusTooManyRequests {
		t.Errorf("another route after the budget is spent: %d, want 429", rec.Code)
	}
	// Somebody else's budget is their own.
	if rec := f.get("/me/summary", &f.rival); rec.Code != http.StatusOK {
		t.Errorf("another account: %d, want 200", rec.Code)
	}
}

// Reading one's own profile is not access to anybody else's data, so it
// leaves no audit trail (design §3).
func TestReadingOnesOwnProfileIsNotAudited(t *testing.T) {
	f := newProfileFixture(t)
	for _, path := range append([]string{"/me/summary", "/me/contests"}, contestRoutes(f.finished.ID)...) {
		if rec := f.get(path, &f.student); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
	if entries := f.stores.Audit.Entries; len(entries) != 0 {
		t.Errorf("the trail took %d entries for a participant reading their own profile", len(entries))
	}
}

// reportResult reads the report's result object as JSON, so a test can see
// what is absent as well as what is there.
func (f *profileFixture) reportResult(t *testing.T) map[string]any {
	t.Helper()
	rec := f.get("/me/contests/"+f.finished.ID.String()+"/report", &f.student)
	if rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Result
}

// Winner mode has one place. Everybody else is on the table unplaced, and
// an unplaced row's place is null — never nought, which reads as a place
// (the contest's own table says it the same way).
func TestTheReportInWinnerModeNamesAPlaceOnlyForTheWinner(t *testing.T) {
	f := newProfileFixture(t)
	f.results.own[f.finished.ID] = leaderboard.Own{
		State: leaderboard.StateFinal, Open: true, Scoring: contests.ScoringWinner, Place: 1, Participants: 9,
		Row: leaderboard.Row{Entry: leaderboard.Entry{Points: 10, Solved: 1}, Place: 1, Winner: true},
	}
	won := f.reportResult(t)
	if won["place"] != float64(1) || won["participants"] != float64(9) || won["winner"] != true {
		t.Errorf("the winner's report reads %+v, want first of nine and the winner's mark", won)
	}

	f = newProfileFixture(t)
	f.results.own[f.finished.ID] = leaderboard.Own{
		State: leaderboard.StateFinal, Open: true, Scoring: contests.ScoringWinner, Place: 0, Participants: 9,
		Row: leaderboard.Row{Entry: leaderboard.Entry{Points: 30, Solved: 3}},
	}
	lost := f.reportResult(t)
	if lost["place"] != nil || lost["participants"] != nil {
		t.Errorf("a non-winner's report reads %+v, want no place at all", lost)
	}
	if _, marked := lost["winner"]; marked {
		t.Errorf("a non-winner's report is marked as the winner: %+v", lost)
	}
	if lost["points"] != float64(30) || lost["place_open"] != true {
		t.Errorf("a non-winner's report reads %+v, want their own numbers on an open table", lost)
	}
}

// A frozen winner-mode table says nothing about who won.
func TestTheReportSaysNothingOfAFrozenWinnerModeTable(t *testing.T) {
	f := newProfileFixture(t)
	f.results.own[f.finished.ID] = leaderboard.Own{
		State: leaderboard.StateFrozen, Open: false, Scoring: contests.ScoringWinner,
		Row: leaderboard.Row{Entry: leaderboard.Entry{Points: 10, Solved: 1}},
	}
	got := f.reportResult(t)
	if got["place"] != nil || got["participants"] != nil || got["place_open"] != false {
		t.Errorf("the frozen report reads %+v, want no place", got)
	}
	if _, marked := got["winner"]; marked {
		t.Errorf("the frozen report names a winner: %+v", got)
	}
	if got["state"] != leaderboard.StateFrozen || got["points"] != float64(10) {
		t.Errorf("the frozen report reads %+v, want the caller's own numbers and the state", got)
	}
}

// A registration the table has no row for is sent result: null — never an
// object of zeroes.
//
// The table is bounded (leaderboard.DefaultMaxRows), so every participant of
// a large contest below the cut reaches this, as does one disqualified before
// the table was computed. A zeroed object would carry scoring "" and state
// "", neither of which any reader can name, and a client parsing them
// strictly gets an error page instead of the report it asked for.
func TestTheReportOfARowTheTableDoesNotCarryHasNoResult(t *testing.T) {
	f := newProfileFixture(t)
	delete(f.results.own, f.finished.ID)

	rec := f.get("/me/contests/"+f.finished.ID.String()+"/report", &f.student)
	if rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Result    map[string]any `json:"result"`
		Queries   int            `json:"queries"`
		Questions []struct {
			Ord int `json:"ord"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Result != nil {
		t.Errorf("result = %+v, want null", body.Result)
	}
	if !strings.Contains(rec.Body.String(), `"result":null`) {
		t.Errorf("the report does not send a null result: %s", rec.Body.String())
	}
	// The report is still the participant's own work; only the standing is
	// missing.
	if len(body.Questions) != 1 {
		t.Errorf("questions = %+v, want the participant's own answers", body.Questions)
	}
}

// A published contest whose window has never opened has a table state of its
// own, and the profile carries it rather than dressing it up as something
// else.
//
// The combination is reachable: DisqualifyParticipant allows a published
// contest, and a disqualified registration is over for its participant
// (profile.Over), so the row is shown with a result — of a table
// leaderboard.Decide calls not_started. Normalising it here would have the
// API say the table is live or frozen when it is neither; what the interface
// needs is the true state and a sentence of its own for it.
func TestAContestThatNeverStartedSaysSoOnTheListAndTheReport(t *testing.T) {
	f := newProfileFixture(t)
	c := f.stores.SeedContest(contests.StatusPublished)
	starts := f.now.Add(24 * time.Hour)
	ends := starts.Add(2 * time.Hour)
	c.StartsAt, c.EndsAt = &starts, &ends
	f.stores.Contests.Put(c)
	p, err := f.stores.Registrations.Add(t.Context(), c.ID, f.student.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.Status = contests.RegistrationDisqualified
	f.stores.Registrations.Put(p)
	f.store.enrolments = []profile.Enrolment{{Contest: c, Participant: p,
		Result: profile.Result{Scoring: contests.ScoringPoints}}}

	body := f.list(t)
	if len(body.Items) != 1 {
		t.Fatalf("the list has %d items, want the one contest: %+v", len(body.Items), body)
	}
	row := body.Items[0]
	if !row.Over || row.Result == nil {
		t.Fatalf("the disqualified row reads %+v, want it over with a result", row)
	}
	if row.Result.State != leaderboard.StateNotStarted || row.Result.PlaceOpen {
		t.Errorf("the row's table reads %+v, want %s and shut", row.Result, leaderboard.StateNotStarted)
	}

	f.results.own[c.ID] = leaderboard.Own{State: leaderboard.StateNotStarted, Scoring: contests.ScoringPoints}
	rec := f.get("/me/contests/"+c.ID.String()+"/report", &f.student)
	if rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
	var report struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Result["state"] != leaderboard.StateNotStarted || report.Result["place_open"] != false {
		t.Errorf("the report's result reads %+v, want %s and shut", report.Result, leaderboard.StateNotStarted)
	}
}

// Nothing under /me is for a search engine: it is one account's own record
// of what it did.
func TestNoProfileRouteIsIndexed(t *testing.T) {
	f := newProfileFixture(t)
	for _, path := range append([]string{"/me/summary", "/me/contests"}, contestRoutes(f.finished.ID)...) {
		rec := f.get(path, &f.student)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		if strings.Contains(path, ".csv") {
			// A download is not a page a crawler indexes, and its headers
			// are the file's.
			continue
		}
		if rec.Header().Get("X-Robots-Tag") != "noindex" {
			t.Errorf("%s: X-Robots-Tag = %q, want noindex", path, rec.Header().Get("X-Robots-Tag"))
		}
	}
}
