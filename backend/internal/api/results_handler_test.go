package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// resultsStore is the report's own reads. A read signals started, when set,
// and waits on hold, when set, so a test can keep an export running.
type resultsStore struct {
	emails  map[uuid.UUID]string
	started chan struct{}
	hold    chan struct{}
	reads   atomic.Int64
}

func (s *resultsStore) read() {
	s.reads.Add(1)
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	if s.hold != nil {
		<-s.hold
	}
}

func (s *resultsStore) PointsBreakdown(context.Context, uuid.UUID, time.Time) (map[uuid.UUID]leaderboard.Breakdown, error) {
	s.read()
	return nil, nil
}

func (s *resultsStore) Emails(_ context.Context, _ uuid.UUID, registrations []uuid.UUID) (map[uuid.UUID]string, error) {
	s.read()
	out := map[uuid.UUID]string{}
	for _, id := range registrations {
		if email, ok := s.emails[id]; ok {
			out[id] = email
		}
	}
	return out, nil
}

type inlineSnapshots struct{}

func (inlineSnapshots) ReadSnapshot(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// downTrail is an audit trail that cannot be written.
type downTrail struct{}

func (downTrail) Append(context.Context, audit.Entry) error {
	return errors.New("the trail is down")
}

func (downTrail) AppendMany(context.Context, []audit.Entry) error {
	return errors.New("the trail is down")
}

// recordingResults counts the exports the handler asked the service for.
type recordingResults struct {
	*leaderboard.Service
	exports atomic.Int64
}

func (r *recordingResults) Export(ctx context.Context, actorID uuid.UUID, req leaderboard.ExportRequest) (leaderboard.Report, error) {
	r.exports.Add(1)
	return r.Service.Export(ctx, actorID, req)
}

type resultsOptions struct {
	maxRows int
	slots   *api.ExportSlots
	// trail nil records into the fixture's sink.
	trail audit.Sink
}

type resultsFixture struct {
	router    http.Handler
	stores    *conteststest.Fixture
	standings *boardStandings
	reports   *resultsStore
	results   *recordingResults
	sink      *conteststest.Sink
	now       time.Time
	owner     users.User
	student   users.User
	admin     users.User
	cookies   map[uuid.UUID]*http.Cookie
}

func newResultsFixture(t *testing.T) *resultsFixture {
	t.Helper()
	return newResultsFixtureWith(t, resultsOptions{})
}

func newResultsFixtureWith(t *testing.T, opts resultsOptions) *resultsFixture {
	t.Helper()
	stores := conteststest.NewFixture()
	f := &resultsFixture{
		stores: stores, sink: conteststest.NewSink(), now: conteststest.FixtureNow,
		reports: &resultsStore{}, cookies: map[uuid.UUID]*http.Cookie{},
	}
	f.standings = &boardStandings{contests: stores.Contests}
	f.owner = stores.Users.Add(users.User{Login: "organizer", FullName: "Olga Organizer", Status: users.StatusActive})
	f.student = stores.Users.Add(users.User{Login: "student", FullName: "Sasha Student", Status: users.StatusActive})
	const adminRole = "administrator"
	stores.Users.GrantRole(adminRole, rbac.PermissionContestAdminAll, rbac.PermissionUsersManage)
	f.admin = stores.Users.Add(users.User{Login: "admin", FullName: "Ada Admin", Status: users.StatusActive, Roles: []string{adminRole}})

	c := cache.NewMemory(10000)
	t.Cleanup(func() { _ = c.Close() })
	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	for _, u := range []users.User{f.owner, f.student, f.admin} {
		token, err := sessions.Create(t.Context(), auth.Principal{UserID: u.ID, Login: u.Login})
		if err != nil {
			t.Fatal(err)
		}
		f.cookies[u.ID] = &http.Cookie{Name: auth.SessionCookieName, Value: token}
	}

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users, Authorizer: rbac.New(contestRoles{stores}),
		Cookies: auth.NewCookieWriter(false), Logger: log,
	})
	var trail audit.Sink = f.sink
	if opts.trail != nil {
		trail = opts.trail
	}
	f.results = &recordingResults{Service: leaderboard.NewService(leaderboard.Config{
		Contests: stores.Contests, Participants: stores.Registrations, Standings: f.standings,
		Audit: audit.New(trail), UnitOfWork: stores.UnitOfWork, Now: func() time.Time { return f.now },
		MaxRows: opts.maxRows, Reports: f.reports, Questions: stores.Questions, Snapshots: inlineSnapshots{},
	})}
	router := chi.NewRouter()
	api.NewResultsHandler(f.results, mw, log).WithExportSlots(opts.slots).Mount(router)
	f.router = router
	return f
}

// contest seeds a contest the owner owns and the student is registered in.
func (f *resultsFixture) contest(t *testing.T, status string) (contests.Contest, contests.Participant) {
	t.Helper()
	c := f.stores.SeedContest(status)
	if err := f.stores.Managers.Grant(t.Context(), contests.Manager{
		ContestID: c.ID, UserID: f.owner.ID, Role: rbac.RoleOwner, GrantedBy: f.owner.ID,
	}); err != nil {
		t.Fatal(err)
	}
	me, err := f.stores.Registrations.Add(t.Context(), c.ID, f.student.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c, me
}

// table puts three rows on a points table: the student first, a runner-up,
// and a disqualified entrant who scored the most. It returns their
// registrations in that order.
func (f *resultsFixture) table(me contests.Participant) []uuid.UUID {
	second, disqualified := uuid.New(), uuid.New()
	f.standings.entries = []leaderboard.Entry{
		{Registration: me.ID, Login: "student", FullName: "Sasha Student", Points: 12, Solved: 2,
			LastScoredAt: scoredAt(f.now.Add(-10 * time.Minute))},
		{Registration: second, Login: "runner", FullName: "Rita Runner", Points: 8, Solved: 1,
			LastScoredAt: scoredAt(f.now.Add(-20 * time.Minute))},
		{Registration: disqualified, Login: "cheat", FullName: "Chet Cheat", Disqualified: true, Points: 20, Solved: 3,
			LastScoredAt: scoredAt(f.now.Add(-30 * time.Minute))},
	}
	return []uuid.UUID{me.ID, second, disqualified}
}

func (f *resultsFixture) preview(c contests.Contest, who uuid.UUID) *httptest.ResponseRecorder {
	return f.do(http.MethodGet, "/contests/"+c.ID.String()+"/results", who, "")
}

func (f *resultsFixture) export(c contests.Contest, who uuid.UUID, body string) *httptest.ResponseRecorder {
	return f.do(http.MethodPost, "/contests/"+c.ID.String()+"/results/export", who, body)
}

func (f *resultsFixture) do(method, path string, who uuid.UUID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookies[who])
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// nothingRead fails the test if the handler asked for an export or anything
// was read or recorded.
func (f *resultsFixture) nothingRead(t *testing.T) {
	t.Helper()
	if n := f.results.exports.Load(); n != 0 {
		t.Errorf("the handler asked for %d exports, want none", n)
	}
	if n := f.standings.reads.Load() + f.reports.reads.Load(); n != 0 {
		t.Errorf("%d reads, want none", n)
	}
	if len(f.sink.Entries) != 0 {
		t.Errorf("recorded %v, want nothing", f.sink.Actions())
	}
}

func exportBody(t *testing.T, format, detail string, ids []uuid.UUID, fields ...string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"format": format, "detail": detail, "registrations": ids, "fields": fields})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (%s)", rec.Code, status, rec.Body.String())
	}
	if code != "" {
		if got := errorCode(t, rec); got != code {
			t.Fatalf("code = %q, want %q", got, code)
		}
	}
}

func TestResultsAreForTheContestsMonitorsOnly(t *testing.T) {
	f := newResultsFixture(t)
	c, me := f.contest(t, contests.StatusRunning)
	ids := f.table(me)
	body := exportBody(t, "csv", "summary", ids, "full_name")

	wantStatus(t, f.preview(c, f.student.ID), http.StatusForbidden, "")
	wantStatus(t, f.export(c, f.student.ID, body), http.StatusForbidden, "")
	f.nothingRead(t)

	wantStatus(t, f.preview(c, f.owner.ID), http.StatusOK, "")
	wantStatus(t, f.export(c, f.owner.ID, body), http.StatusOK, "")
}

func TestThePreviewListsEveryRegistrationWithExportPlaces(t *testing.T) {
	f := newResultsFixture(t)
	c, me := f.contest(t, contests.StatusRunning)
	ids := f.table(me)

	rec := f.preview(c, f.owner.ID)
	wantStatus(t, rec, http.StatusOK, "")
	body := decode(t, rec)
	rows, _ := body["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (%s)", len(rows), rec.Body.String())
	}
	for i, want := range []struct {
		id    uuid.UUID
		place any
	}{{ids[0], 1.0}, {ids[1], 2.0}, {ids[2], nil}} {
		row := rows[i].(map[string]any)
		if row["registration_id"] != want.id.String() || row["place"] != want.place {
			t.Errorf("row %d = %v, want registration %s at place %v", i, row, want.id, want.place)
		}
		if _, ok := row["email"]; ok {
			t.Errorf("row %d carries an email: %v", i, row)
		}
	}
	if rows[2].(map[string]any)["disqualified"] != true {
		t.Errorf("the last row = %v, want the disqualified one", rows[2])
	}
	if body["detail"] != "summary" || body["registered"] != 3.0 || body["status"] != "preliminary" {
		t.Errorf("detail, registered, status = %v, %v, %v; want summary, 3, preliminary", body["detail"], body["registered"], body["status"])
	}
	if questions, ok := body["questions"].([]any); !ok || len(questions) != 0 {
		t.Errorf("questions = %v, want []", body["questions"])
	}
	if body["may_export_email"] != false {
		t.Errorf("may_export_email = %v for the owner, want false", body["may_export_email"])
	}

	adminView := decode(t, f.preview(c, f.admin.ID))
	if adminView["may_export_email"] != true {
		t.Errorf("may_export_email = %v for an account administrator, want true", adminView["may_export_email"])
	}
	for i, row := range adminView["rows"].([]any) {
		if _, ok := row.(map[string]any)["email"]; ok {
			t.Errorf("row %d of an account administrator's preview carries an email: %v", i, row)
		}
	}
}

func TestThePreviewIsNotRecorded(t *testing.T) {
	f := newResultsFixture(t)
	c, me := f.contest(t, contests.StatusFinished)
	f.table(me)

	wantStatus(t, f.preview(c, f.owner.ID), http.StatusOK, "")
	if len(f.sink.Entries) != 0 || f.results.exports.Load() != 0 || f.reports.reads.Load() != 0 {
		t.Errorf("a preview recorded %v, exported %d times and made %d report reads; want none",
			f.sink.Actions(), f.results.exports.Load(), f.reports.reads.Load())
	}
}

func TestAnExportAnswersCSVWithItsVersionAndFileName(t *testing.T) {
	f := newResultsFixture(t)
	c, me := f.contest(t, contests.StatusFinished)
	ids := f.table(me)

	rec := f.export(c, f.owner.ID, exportBody(t, "csv", "summary", ids, "login", "full_name"))
	wantStatus(t, rec, http.StatusOK, "")
	if got := rec.Header().Get("X-Results-Format"); got != "1" {
		t.Errorf("X-Results-Format = %q, want 1", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	wantName := `attachment; filename="results-` + c.ID.String() + "-" + f.now.UTC().Format("20060102T150405Z") + `.csv"`
	if got := rec.Header().Get("Content-Disposition"); got != wantName {
		t.Errorf("Content-Disposition = %q, want %q", got, wantName)
	}
	lines := strings.Split(rec.Body.String(), "\r\n")
	if lines[0] != "place,prize,registration_id,full_name,login,disqualified,solved,penalty,points,last_solved_at,bonus_credit,status" {
		t.Errorf("first line = %q, want the header", lines[0])
	}
	if !strings.HasPrefix(lines[1], "1,,"+ids[0].String()+",Sasha Student,student,0,") {
		t.Errorf("second line = %q, want the student first", lines[1])
	}
}

func TestAnExcelExportStartsWithABOM(t *testing.T) {
	f := newResultsFixture(t)
	c, me := f.contest(t, contests.StatusFinished)
	ids := f.table(me)

	body, err := json.Marshal(map[string]any{
		"format": "csv", "detail": "summary", "registrations": ids, "fields": []string{"login"},
		"csv": map[string]any{"excel": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.export(c, f.owner.ID, string(body))
	wantStatus(t, rec, http.StatusOK, "")
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("\xEF\xBB\xBFplace;prize;registration_id;login;")) {
		t.Errorf("body starts %q, want a byte order mark and semicolons", rec.Body.String()[:min(40, rec.Body.Len())])
	}
}

func TestAnExportAnswersJSONForThePDF(t *testing.T) {
	f := newResultsFixture(t)
	c, me := f.contest(t, contests.StatusRunning)
	c.Scoring = contests.ScoringICPC
	c.ICPCPenaltyMin = 20
	c.PrizePlaces = contests.PrizePlaces{First: 1, Second: 2, Third: 3}
	f.stores.Contests.Put(c)
	var questionIDs []uuid.UUID
	for i, title := range []string{"Who", "Where"} {
		q := f.stores.Questions.Put(contests.Question{
			ContestID: c.ID, Ord: i + 1, Kind: contests.KindFinal, Points: 10, IsVisible: true,
			Texts: map[string]contests.QuestionText{"en": {Title: title}},
		})
		questionIDs = append(questionIDs, q.ID)
	}
	start := *c.StartsAt
	runner := uuid.New()
	f.standings.icpc = &icpcBoard{questions: 2, ids: questionIDs, start: start, penaltyMin: 20, entrants: []icpcEntrant{
		{registration: me.ID, login: "student", answers: []icpcAnswer{
			{question: 0, at: start.Add(5 * time.Minute)},
			{question: 0, at: start.Add(12 * time.Minute), correct: true},
		}},
		{registration: runner, login: "runner", answers: []icpcAnswer{{question: 1, at: start.Add(30 * time.Minute)}}},
	}}
	ids := []uuid.UUID{me.ID, runner}

	rec := f.export(c, f.owner.ID, exportBody(t, "json", "detailed", ids, "full_name", "login"))
	wantStatus(t, rec, http.StatusOK, "")
	body := decode(t, rec)

	contest := body["contest"].(map[string]any)
	if contest["id"] != c.ID.String() || contest["scoring"] != "icpc" || contest["icpc_penalty_min"] != 20.0 ||
		contest["default_language"] != "en" || contest["status"] != "running" {
		t.Errorf("contest = %v", contest)
	}
	if titles := contest["titles"].(map[string]any); titles["en"] != "The Library Murder" {
		t.Errorf("titles = %v", titles)
	}
	if places := contest["prize_places"].(map[string]any); places["first"] != 1.0 || places["second"] != 2.0 || places["third"] != 3.0 {
		t.Errorf("prize_places = %v", places)
	}
	if contest["starts_at"] != start.UTC().Format(time.RFC3339) {
		t.Errorf("starts_at = %v", contest["starts_at"])
	}
	questions := body["questions"].([]any)
	if len(questions) != 2 {
		t.Fatalf("questions = %v, want two", questions)
	}
	if q := questions[0].(map[string]any); q["position"] != 1.0 || q["letter"] != "A" || q["bonus"] != false ||
		q["titles"].(map[string]any)["en"] != "Who" {
		t.Errorf("the first question = %v", q)
	}
	if body["status"] != "preliminary" || body["hidden_by_freeze"] != false || body["detail"] != "detailed" {
		t.Errorf("status, hidden_by_freeze, detail = %v, %v, %v", body["status"], body["hidden_by_freeze"], body["detail"])
	}
	if _, ok := body["may_export_email"]; ok {
		t.Errorf("an export carries may_export_email")
	}
	first := body["rows"].([]any)[0].(map[string]any)
	if first["place"] != 1.0 || first["penalty"] != 32.0 || first["points"] != nil || first["login"] != "student" {
		t.Errorf("the first row = %v", first)
	}
	if cell := first["cells"].([]any)[0].(map[string]any); cell["solved"] != true || cell["minute"] != 12.0 || cell["wrong"] != 1.0 {
		t.Errorf("the first cell = %v", cell)
	}

	fingerprint, _ := body["fingerprint"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(fingerprint) {
		t.Fatalf("fingerprint = %q, want eight lowercase hex digits", fingerprint)
	}
	again := decode(t, f.export(c, f.owner.ID, exportBody(t, "json", "detailed", ids, "full_name", "login")))
	if again["fingerprint"] != fingerprint {
		t.Errorf("fingerprint = %v the second time, want %s", again["fingerprint"], fingerprint)
	}
	other := decode(t, f.export(c, f.owner.ID, exportBody(t, "json", "detailed", ids[:1], "full_name", "login")))
	if other["fingerprint"] == fingerprint {
		t.Errorf("a different selection has the same fingerprint %s", fingerprint)
	}
}

func TestAnExportRefusesAMalformedRequest(t *testing.T) {
	f := newResultsFixture(t)
	c, me := f.contest(t, contests.StatusFinished)
	ids := f.table(me)
	tooMany := make([]uuid.UUID, leaderboard.MaxReportRegistrations+1)
	for i := range tooMany {
		tooMany[i] = uuid.New()
	}

	for name, body := range map[string]string{
		"an unknown format":    exportBody(t, "xlsx", "summary", ids, "login"),
		"no registrations":     exportBody(t, "csv", "summary", []uuid.UUID{}, "login"),
		"too many":             exportBody(t, "csv", "summary", tooMany, "login"),
		"a registration twice": exportBody(t, "csv", "summary", []uuid.UUID{ids[0], ids[0]}, "login"),
		"not an identifier":    `{"format":"csv","detail":"summary","registrations":["nope"],"fields":["login"]}`,
		"no fields":            exportBody(t, "csv", "summary", ids),
		"an unknown field":     exportBody(t, "csv", "summary", ids, "phone"),
		"an unknown detail":    exportBody(t, "csv", "full", ids, "login"),
		"an unknown key":       `{"format":"csv","detail":"summary","registrations":["` + ids[0].String() + `"],"fields":["login"],"extra":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			wantStatus(t, f.export(c, f.owner.ID, body), http.StatusBadRequest, "invalid_request")
		})
	}
	f.nothingRead(t)
}

func TestAnExportRefusesARegistrationOfAnotherContest(t *testing.T) {
	f := newResultsFixture(t)
	c, me := f.contest(t, contests.StatusFinished)
	ids := f.table(me)
	_, elsewhere := f.contest(t, contests.StatusFinished)

	rec := f.export(c, f.owner.ID, exportBody(t, "csv", "summary", []uuid.UUID{ids[0], elsewhere.ID}, "login"))
	wantStatus(t, rec, http.StatusUnprocessableEntity, "results_unknown_registration")
	if len(f.sink.Entries) != 0 {
		t.Errorf("recorded %v, want nothing", f.sink.Actions())
	}
}

func TestAnExportOfADraftHasNoRegistrationToName(t *testing.T) {
	f := newResultsFixture(t)
	c, me := f.contest(t, contests.StatusDraft)
	f.table(me)

	rec := f.export(c, f.owner.ID, exportBody(t, "csv", "summary", []uuid.UUID{me.ID}, "login"))
	wantStatus(t, rec, http.StatusUnprocessableEntity, "results_unknown_registration")

	preview := f.preview(c, f.owner.ID)
	wantStatus(t, preview, http.StatusOK, "")
	if rows, ok := decode(t, preview)["rows"].([]any); !ok || len(rows) != 0 {
		t.Errorf("a draft's preview rows = %v, want []", rows)
	}
	if len(f.sink.Entries) != 0 {
		t.Errorf("recorded %v, want nothing", f.sink.Actions())
	}
}

func TestAnExportOfEmailNeedsUsersManage(t *testing.T) {
	f := newResultsFixture(t)
	c, me := f.contest(t, contests.StatusFinished)
	ids := f.table(me)
	f.reports.emails = map[uuid.UUID]string{me.ID: "sasha@example.org"}
	body := exportBody(t, "csv", "summary", ids, "full_name", "email")

	wantStatus(t, f.export(c, f.owner.ID, body), http.StatusForbidden, "results_email_forbidden")
	f.nothingRead(t)

	rec := f.export(c, f.admin.ID, body)
	wantStatus(t, rec, http.StatusOK, "")
	lines := strings.Split(rec.Body.String(), "\r\n")
	if !strings.HasPrefix(lines[0], "place,prize,registration_id,full_name,email,") || !strings.Contains(lines[1], ",sasha@example.org,") {
		t.Errorf("file = %q, want an email column with the student's address", rec.Body.String())
	}
}

func TestAnExportRefusesAContestOverTheRowBound(t *testing.T) {
	f := newResultsFixtureWith(t, resultsOptions{maxRows: 1})
	c, me := f.contest(t, contests.StatusFinished)
	ids := f.table(me)[:2]
	f.standings.entries = f.standings.entries[:2]

	wantStatus(t, f.export(c, f.owner.ID, exportBody(t, "csv", "summary", ids, "login")), http.StatusConflict, "results_too_many_rows")
	wantStatus(t, f.preview(c, f.owner.ID), http.StatusConflict, "results_too_many_rows")
	if len(f.sink.Entries) != 0 {
		t.Errorf("recorded %v, want nothing", f.sink.Actions())
	}
}

// holdExport starts an export that stays inside its report reads until the
// returned release, and waits until it is there.
func (f *resultsFixture) holdExport(t *testing.T, c contests.Contest, who uuid.UUID, body string) (release func() *httptest.ResponseRecorder) {
	t.Helper()
	f.reports.started, f.reports.hold = make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(f.reports.hold) }) }
	t.Cleanup(open)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.export(c, who, body) }()
	select {
	case <-f.reports.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first export never reached its reads")
	}
	return func() *httptest.ResponseRecorder {
		open()
		return <-done
	}
}

// stalledReader is a client that stops reading: its first body write signals
// started and waits on hold.
type stalledReader struct {
	*httptest.ResponseRecorder
	started chan struct{}
	hold    chan struct{}
	once    sync.Once
}

func (w *stalledReader) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.hold
	return w.ResponseRecorder.Write(p)
}

func TestAResultsExportFreesItsSlotBeforeWriting(t *testing.T) {
	f := newResultsFixtureWith(t, resultsOptions{slots: api.NewExportSlots(1)})
	c, me := f.contest(t, contests.StatusFinished)
	body := exportBody(t, "csv", "summary", f.table(me), "login")

	req := httptest.NewRequest(http.MethodPost, "/contests/"+c.ID.String()+"/results/export", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookies[f.owner.ID])
	stalled := &stalledReader{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), hold: make(chan struct{})}
	var once sync.Once
	unstall := func() { once.Do(func() { close(stalled.hold) }) }
	t.Cleanup(unstall)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.router.ServeHTTP(stalled, req)
	}()
	select {
	case <-stalled.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first export never began writing")
	}

	wantStatus(t, f.export(c, f.admin.ID, body), http.StatusOK, "")
	wantStatus(t, f.export(c, f.owner.ID, body), http.StatusTooManyRequests, "results_export_too_often")

	unstall()
	<-done
	if stalled.Code != http.StatusOK {
		t.Errorf("the stalled export = %d, want 200", stalled.Code)
	}
}

func TestASecondResultsExportOfTheSameAccountIsRefused(t *testing.T) {
	f := newResultsFixture(t)
	c, me := f.contest(t, contests.StatusFinished)
	body := exportBody(t, "csv", "detailed", f.table(me), "login")

	release := f.holdExport(t, c, f.owner.ID, body)
	reads := f.reports.reads.Load()
	second := f.export(c, f.owner.ID, body)
	first := release()

	wantStatus(t, second, http.StatusTooManyRequests, "results_export_too_often")
	if f.reports.reads.Load() != reads || f.results.exports.Load() != 1 {
		t.Errorf("the refused export read or exported: reads %d → %d, exports %d", reads, f.reports.reads.Load(), f.results.exports.Load())
	}
	wantStatus(t, first, http.StatusOK, "")
	wantStatus(t, f.export(c, f.owner.ID, body), http.StatusOK, "")
}

func TestAResultsExportPastTheServiceBoundIsRefused(t *testing.T) {
	f := newResultsFixtureWith(t, resultsOptions{slots: api.NewExportSlots(1)})
	c, me := f.contest(t, contests.StatusFinished)
	body := exportBody(t, "csv", "detailed", f.table(me), "login")

	release := f.holdExport(t, c, f.admin.ID, body)
	refused := f.export(c, f.owner.ID, body)
	first := release()

	wantStatus(t, refused, http.StatusServiceUnavailable, "too_many_exports")
	if after, err := strconv.Atoi(refused.Header().Get("Retry-After")); err != nil || after <= 0 {
		t.Errorf("Retry-After = %q, want a positive number of seconds", refused.Header().Get("Retry-After"))
	}
	if f.results.exports.Load() != 1 {
		t.Errorf("exports = %d, want only the held one", f.results.exports.Load())
	}
	wantStatus(t, first, http.StatusOK, "")
}

func TestAResultsExportIsRecordedBeforeItsFirstByte(t *testing.T) {
	broken := newResultsFixtureWith(t, resultsOptions{trail: downTrail{}})
	c, me := broken.contest(t, contests.StatusFinished)
	ids := broken.table(me)

	rec := broken.export(c, broken.owner.ID, exportBody(t, "csv", "summary", ids, "login"))
	wantStatus(t, rec, http.StatusInternalServerError, "internal_error")
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("Content-Disposition = %q on a refused export", got)
	}
	if strings.Contains(rec.Body.String(), "registration_id") {
		t.Errorf("a refused export sent the file: %s", rec.Body.String())
	}

	f := newResultsFixture(t)
	c, me = f.contest(t, contests.StatusFinished)
	ids = f.table(me)
	wantStatus(t, f.export(c, f.owner.ID, exportBody(t, "csv", "summary", ids, "login")), http.StatusOK, "")
	if len(f.sink.Entries) != 1 || f.sink.Entries[0].Action != audit.ActionContestResultsExport ||
		f.sink.Entries[0].EntityID != c.ID.String() {
		t.Errorf("recorded %v, want one %s of the contest", f.sink.Entries, audit.ActionContestResultsExport)
	}
}

func TestAResultsExportOfAMissingContestIsNotFound(t *testing.T) {
	f := newResultsFixture(t)
	missing := contests.Contest{ID: uuid.New()}

	wantStatus(t, f.preview(missing, f.admin.ID), http.StatusNotFound, "not_found")
	wantStatus(t, f.export(missing, f.admin.ID, exportBody(t, "csv", "summary", []uuid.UUID{uuid.New()}, "login")),
		http.StatusNotFound, "not_found")
	if len(f.sink.Entries) != 0 {
		t.Errorf("recorded %v, want nothing", f.sink.Actions())
	}
}
