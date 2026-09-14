package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

// boardStandings answers with the entries whose score was reached before the
// cutoff, which is what the real aggregate does, so a test can put an answer
// on either side of a freeze and read the body.
type boardStandings struct {
	contests *conteststest.Contests
	entries  []leaderboard.Entry
	// icpc answers an ICPC query.
	icpc *icpcBoard
}

func (s *boardStandings) ICPCStandings(_ context.Context, q leaderboard.Query) ([]leaderboard.Entry, leaderboard.Grid, error) {
	if s.icpc == nil {
		return nil, leaderboard.Grid{}, nil
	}
	entries, grid := s.icpc.standings(q)
	return entries, grid, nil
}

func (s *boardStandings) Standings(_ context.Context, q leaderboard.Query) ([]leaderboard.Entry, error) {
	var out []leaderboard.Entry
	for _, e := range s.entries {
		if e.LastScoredAt != nil && !e.LastScoredAt.Before(q.Cutoff) {
			continue
		}
		if e.Disqualified && !q.IncludeDisqualified {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *boardStandings) MarkRevealed(ctx context.Context, contestID uuid.UUID, at time.Time) (time.Time, bool, error) {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return time.Time{}, false, err
	}
	if c.LeaderboardRevealedAt != nil {
		return *c.LeaderboardRevealedAt, false, nil
	}
	c.LeaderboardRevealedAt = &at
	s.contests.Put(c)
	return at, true, nil
}

// icpcBoard holds raw ICPC answers and computes rows and a grid from them
// the way the real query does — the cutoff on solves and wrong counts, the
// pending window only when asked and only on a question unsolved by the
// cutoff, each question's earliest solve among the entrants that are not
// disqualified — so a test can put answers on either side of a freeze and
// read the body.
type icpcBoard struct {
	questions  int
	start      time.Time
	penaltyMin int
	entrants   []icpcEntrant
}

type icpcEntrant struct {
	registration uuid.UUID
	login        string
	disqualified bool
	answers      []icpcAnswer
}

type icpcAnswer struct {
	question int
	at       time.Time
	correct  bool
}

// standings is a stand-in for the handler tests only: it follows the rules
// closely enough that a leak through the service or the handler shows in the
// body, and it ignores what the handler cannot affect (registration times, the
// row bound, deleted accounts). The real rules are pinned against the database
// by the TestICPCStandings* tests in internal/postgres/leaderboard_test.go —
// the minute rounding, the individual start, what a wrong attempt costs,
// hidden questions, the pending window, the registration cutoff, the earliest
// solve over the whole contest and the order LIMIT cuts in.
func (b *icpcBoard) standings(q leaderboard.Query) ([]leaderboard.Entry, leaderboard.Grid) {
	grid := leaderboard.Grid{Questions: b.questions, FirstSolves: make([]*time.Time, b.questions)}
	var out []leaderboard.Entry
	for _, p := range b.entrants {
		e := leaderboard.Entry{Registration: p.registration, Login: p.login, Disqualified: p.disqualified,
			Cells: make([]leaderboard.Cell, b.questions)}
		for i := range e.Cells {
			cell := &e.Cells[i]
			for _, a := range p.answers {
				if a.question == i && a.correct && a.at.Before(q.Cutoff) && (cell.SolvedAt == nil || a.at.Before(*cell.SolvedAt)) {
					solved := a.at
					cell.SolvedAt = &solved
				}
			}
			for _, a := range p.answers {
				if a.question != i {
					continue
				}
				if !a.correct && a.at.Before(q.Cutoff) && (cell.SolvedAt == nil || a.at.Before(*cell.SolvedAt)) {
					cell.Wrong++
				}
				if q.Pending != nil && cell.SolvedAt == nil && !a.at.Before(q.Pending.From) && a.at.Before(q.Pending.Until) {
					cell.Pending++
				}
			}
			if cell.SolvedAt != nil {
				cell.Minute = int(cell.SolvedAt.Sub(b.start) / time.Minute)
				e.Solved++
				e.Penalty += cell.Minute + b.penaltyMin*cell.Wrong
				if e.LastSolvedAt == nil || cell.SolvedAt.After(*e.LastSolvedAt) {
					e.LastSolvedAt = cell.SolvedAt
				}
				if !p.disqualified && (grid.FirstSolves[i] == nil || cell.SolvedAt.Before(*grid.FirstSolves[i])) {
					grid.FirstSolves[i] = cell.SolvedAt
				}
			}
		}
		if p.disqualified && !q.IncludeDisqualified {
			continue
		}
		out = append(out, e)
	}
	return out, grid
}

type boardFixture struct {
	router    http.Handler
	stores    *conteststest.Fixture
	standings *boardStandings
	sink      *conteststest.Sink
	now       time.Time
	organizer users.User
	student   users.User
	cookies   map[uuid.UUID]*http.Cookie
}

func newBoardFixture(t *testing.T) *boardFixture {
	t.Helper()
	stores := conteststest.NewFixture()
	f := &boardFixture{
		stores: stores, sink: conteststest.NewSink(), now: conteststest.FixtureNow,
		cookies: map[uuid.UUID]*http.Cookie{},
	}
	f.standings = &boardStandings{contests: stores.Contests}
	f.organizer = stores.Users.Add(users.User{Login: "organizer", FullName: "Olga Organizer", Status: users.StatusActive})
	f.student = stores.Users.Add(users.User{Login: "student", FullName: "Sasha Student", Status: users.StatusActive})
	stranger := stores.Users.Add(users.User{Login: "stranger", FullName: "Stan Stranger", Status: users.StatusActive})

	c := cache.NewMemory(10000)
	t.Cleanup(func() { _ = c.Close() })
	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	for _, u := range []users.User{f.organizer, f.student, stranger} {
		token, err := sessions.Create(t.Context(), auth.Principal{UserID: u.ID, Login: u.Login})
		if err != nil {
			t.Fatal(err)
		}
		f.cookies[u.ID] = &http.Cookie{Name: auth.SessionCookieName, Value: token}
	}
	f.cookies[uuid.Nil] = f.cookies[stranger.ID]

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users, Authorizer: rbac.New(contestRoles{stores}),
		Cookies: auth.NewCookieWriter(false), Logger: log,
	})
	service := leaderboard.NewService(leaderboard.Config{
		Contests: stores.Contests, Participants: stores.Registrations, Standings: f.standings,
		Audit: audit.New(f.sink), UnitOfWork: stores.UnitOfWork, Now: func() time.Time { return f.now },
	})
	router := chi.NewRouter()
	api.NewLeaderboardHandler(service, auth.NewLimiter(c), mw, log, "en").Mount(router)
	f.router = router
	return f
}

// contest seeds a contest the organizer owns and the student is registered in.
func (f *boardFixture) contest(t *testing.T, status string, freezeMin *int) (contests.Contest, contests.Participant) {
	t.Helper()
	c := f.stores.SeedContest(status)
	c.LeaderboardFreezeMin = freezeMin
	f.stores.Contests.Put(c)
	if err := f.stores.Managers.Grant(t.Context(), contests.Manager{
		ContestID: c.ID, UserID: f.organizer.ID, Role: rbac.RoleOwner, GrantedBy: f.organizer.ID,
	}); err != nil {
		t.Fatal(err)
	}
	me, err := f.stores.Registrations.Add(t.Context(), c.ID, f.student.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c, me
}

// get sends a request as who (uuid.Nil is the stranger; nil pointer is nobody).
func (f *boardFixture) request(method, path string, who *uuid.UUID, addr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	if addr != "" {
		req.RemoteAddr = addr + ":5000"
	}
	if who != nil {
		req.AddCookie(f.cookies[*who])
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func scoredAt(t time.Time) *time.Time { return &t }

func TestThePublicTableNeedsNoSessionAndCarriesNoIdentifiers(t *testing.T) {
	f := newBoardFixture(t)
	c, me := f.contest(t, contests.StatusRunning, nil)
	f.standings.entries = []leaderboard.Entry{{
		Registration: me.ID, Login: "student", FullName: "Sasha Student",
		Points: 12, Solved: 2, LastScoredAt: scoredAt(f.now.Add(-time.Minute)),
	}}

	rec := f.request(http.MethodGet, "/contests/"+c.ID.String()+"/leaderboard", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{me.ID.String(), f.student.ID.String(), "Sasha Student", "is_you"} {
		if strings.Contains(body, secret) {
			t.Errorf("the public table carries %q: %s", secret, body)
		}
	}
	if got := decode(t, rec); got["state"] != "live" || !strings.Contains(body, `"label":"student"`) {
		t.Errorf("body = %s, want a live table labelled by login", body)
	}
	if rec.Header().Get("X-Robots-Tag") != "noindex" {
		t.Errorf("X-Robots-Tag = %q, want noindex", rec.Header().Get("X-Robots-Tag"))
	}
}

// The table must not be a way to learn which contests exist.
func TestThePublicTableAnswersTheSameForADraftAMissingContestAndNonsense(t *testing.T) {
	f := newBoardFixture(t)
	draft, _ := f.contest(t, contests.StatusDraft, nil)

	var bodies []string
	for _, id := range []string{draft.ID.String(), uuid.New().String(), "not-a-uuid"} {
		rec := f.request(http.MethodGet, "/contests/"+id+"/leaderboard", nil, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", id, rec.Code)
		}
		bodies = append(bodies, errorCode(t, rec))
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Errorf("codes = %v, want one code for all three", bodies)
	}
}

// Refusals count: a caller that is already refused keeps spending its own
// budget, and the budget is spent before the contest is looked up.
func TestThePublicTableIsLimitedPerAddress(t *testing.T) {
	f := newBoardFixture(t)
	c, _ := f.contest(t, contests.StatusRunning, nil)
	path := "/contests/" + c.ID.String() + "/leaderboard"

	for i := range api.LeaderboardPublicPerMinute {
		if rec := f.request(http.MethodGet, path, nil, "203.0.113.7"); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, rec.Code)
		}
	}
	rec := f.request(http.MethodGet, path, nil, "203.0.113.7")
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "leaderboard_too_often" {
		t.Fatalf("status = %d (%s), want 429 leaderboard_too_often", rec.Code, rec.Body.String())
	}
	if rec := f.request(http.MethodGet, path, nil, "203.0.113.8"); rec.Code != http.StatusOK {
		t.Errorf("another address: status = %d, want 200", rec.Code)
	}
}

func TestThePublicTableLimitsAnIPv6NetworkAsOneAddress(t *testing.T) {
	f := newBoardFixture(t)
	c, _ := f.contest(t, contests.StatusRunning, nil)
	path := "/contests/" + c.ID.String() + "/leaderboard"

	for i := range api.LeaderboardPublicPerMinute {
		host := fmt.Sprintf("[2001:db8:1:2::%x]", i+1)
		if rec := f.request(http.MethodGet, path, nil, host); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, rec.Code)
		}
	}
	if rec := f.request(http.MethodGet, path, nil, "[2001:db8:1:2::ffff]"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("another host of the same /64: status = %d, want 429", rec.Code)
	}
}

func TestAParticipantIsToldWhichRowIsTheirs(t *testing.T) {
	f := newBoardFixture(t)
	c, me := f.contest(t, contests.StatusRunning, nil)
	f.standings.entries = []leaderboard.Entry{
		{Registration: uuid.New(), Login: "rival", Points: 20, LastScoredAt: scoredAt(f.now.Add(-2 * time.Minute))},
		{Registration: me.ID, Login: "student", Points: 10, LastScoredAt: scoredAt(f.now.Add(-time.Minute))},
	}
	path := "/contests/" + c.ID.String() + "/play/leaderboard"

	student := f.student.ID
	rec := f.request(http.MethodGet, path, &student, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	rows, _ := decode(t, rec)["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want 2", rows)
	}
	first, second := rows[0].(map[string]any), rows[1].(map[string]any)
	if first["is_you"] == true || second["is_you"] != true {
		t.Errorf("rows = %v, want only the second marked", rows)
	}

	stranger := uuid.Nil
	if rec := f.request(http.MethodGet, path, &stranger, ""); rec.Code != http.StatusForbidden {
		t.Errorf("stranger: status = %d, want 403", rec.Code)
	}
	if rec := f.request(http.MethodGet, path, nil, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("nobody: status = %d, want 401", rec.Code)
	}
}

// The freeze is enforced in what leaves the server: a score reached after the
// freeze is not in the body, in any field.
func TestNothingScoredAfterTheFreezeLeavesTheServer(t *testing.T) {
	f := newBoardFixture(t)
	freeze := 30
	c, me := f.contest(t, contests.StatusRunning, &freeze)
	freezeAt := c.EndsAt.Add(-30 * time.Minute)
	f.now = freezeAt.Add(10 * time.Minute)
	f.standings.entries = []leaderboard.Entry{
		{Registration: me.ID, Login: "student", Points: 4, LastScoredAt: scoredAt(freezeAt.Add(-time.Minute))},
		{Registration: uuid.New(), Login: "latecomer", Points: 777, LastScoredAt: scoredAt(freezeAt.Add(time.Minute))},
	}

	student := f.student.ID
	for _, rec := range []*httptest.ResponseRecorder{
		f.request(http.MethodGet, "/contests/"+c.ID.String()+"/leaderboard", nil, ""),
		f.request(http.MethodGet, "/contests/"+c.ID.String()+"/play/leaderboard", &student, ""),
	} {
		body := rec.Body.String()
		if rec.Code != http.StatusOK || strings.Contains(body, "777") || strings.Contains(body, "latecomer") {
			t.Errorf("status %d, body = %s, want a frozen table without the late score", rec.Code, body)
		}
		if body := decode(t, rec); body["state"] != "frozen" || body["ends_at"] == nil {
			t.Errorf("state = %v, ends_at = %v, want frozen with the contest's end", body["state"], body["ends_at"])
		}
	}

	organizer := f.organizer.ID
	live := f.request(http.MethodGet, "/contests/"+c.ID.String()+"/leaderboard/live", &organizer, "")
	if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), "777") {
		t.Errorf("staff: status %d, body = %s, want the late score on the live table", live.Code, live.Body.String())
	}
}

func TestTheLiveTableIsForTheContestsStaffOnly(t *testing.T) {
	f := newBoardFixture(t)
	c, _ := f.contest(t, contests.StatusRunning, nil)
	path := "/contests/" + c.ID.String() + "/leaderboard/live"

	student := f.student.ID
	if rec := f.request(http.MethodGet, path, &student, ""); rec.Code != http.StatusForbidden {
		t.Errorf("participant: status = %d, want 403", rec.Code)
	}
	if rec := f.request(http.MethodGet, path, nil, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("nobody: status = %d, want 401", rec.Code)
	}
}

func TestRevealIsRefusedBeforeTheFinishAndRecordedAfterIt(t *testing.T) {
	f := newBoardFixture(t)
	freeze := 30
	running, _ := f.contest(t, contests.StatusRunning, &freeze)
	finished, _ := f.contest(t, contests.StatusFinished, &freeze)
	organizer, student := f.organizer.ID, f.student.ID

	rec := f.request(http.MethodPost, "/contests/"+running.ID.String()+"/leaderboard/reveal", &organizer, "")
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "leaderboard_not_revealable" {
		t.Fatalf("running: status = %d (%s), want 409 leaderboard_not_revealable", rec.Code, rec.Body.String())
	}
	if rec := f.request(http.MethodPost, "/contests/"+finished.ID.String()+"/leaderboard/reveal", &student, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("participant: status = %d, want 403", rec.Code)
	}

	rec = f.request(http.MethodPost, "/contests/"+finished.ID.String()+"/leaderboard/reveal", &organizer, "")
	if rec.Code != http.StatusOK || decode(t, rec)["revealed_at"] == nil {
		t.Fatalf("finished: status = %d (%s), want 200 with revealed_at", rec.Code, rec.Body.String())
	}
	if len(f.sink.Entries) != 1 || f.sink.Entries[0].Action != audit.ActionContestLeaderboardReveal {
		t.Errorf("audit = %+v, want one reveal", f.sink.Entries)
	}
}

// The points table's response is byte for byte what it was before ICPC: no
// questions, no penalty, no cells, and every other field where it was.
func TestAPointsTableResponseIsUnchanged(t *testing.T) {
	f := newBoardFixture(t)
	c, me := f.contest(t, contests.StatusRunning, nil)
	f.standings.entries = []leaderboard.Entry{
		{Registration: me.ID, Login: "student", Points: 12, Solved: 2, LastScoredAt: scoredAt(f.now.Add(-time.Minute))},
		{Registration: uuid.New(), Login: "idle"},
	}

	public := f.request(http.MethodGet, "/contests/"+c.ID.String()+"/leaderboard", nil, "")
	want := `{"state":"live","scoring":"points","title":"The Library Murder","ends_at":"2026-03-01T12:00:00Z",` +
		`"generated_at":"2026-03-01T10:00:00Z","truncated":false,"rows":[` +
		`{"place":1,"label":"student","points":12,"solved":2,"last_scored_at":"2026-03-01T09:59:00Z"},` +
		`{"place":2,"label":"idle","points":0,"solved":0}]}` + "\n"
	if got := public.Body.String(); got != want {
		t.Errorf("public body =\n%s\nwant\n%s", got, want)
	}

	organizer := f.organizer.ID
	live := f.request(http.MethodGet, "/contests/"+c.ID.String()+"/leaderboard/live", &organizer, "")
	want = `{"shown":{"state":"live"},"scoring":"points","freeze_min":null,"names":"login",` +
		`"generated_at":"2026-03-01T10:00:00Z","truncated":false,"rows":[` +
		`{"place":1,"login":"student","full_name":"","points":12,"solved":2,"last_scored_at":"2026-03-01T09:59:00Z"},` +
		`{"place":2,"login":"idle","full_name":"","points":0,"solved":0}]}` + "\n"
	if got := live.Body.String(); got != want {
		t.Errorf("live body =\n%s\nwant\n%s", got, want)
	}
}

// The winner table's response is byte for byte what it was before ICPC, in
// the shape with the most optional fields: frozen, with a winner, an unplaced
// row and the caller's own row.
func TestAWinnerTableResponseIsUnchanged(t *testing.T) {
	f := newBoardFixture(t)
	freeze := 30
	c, me := f.contest(t, contests.StatusRunning, &freeze)
	c.Scoring = contests.ScoringWinner
	f.stores.Contests.Put(c)
	freezeAt := c.EndsAt.Add(-30 * time.Minute)
	f.now = freezeAt.Add(10 * time.Minute)
	f.standings.entries = []leaderboard.Entry{
		{Registration: uuid.New(), Login: "rival", Points: 20, LastScoredAt: scoredAt(freezeAt.Add(-30 * time.Minute))},
		{Registration: me.ID, Login: "student", Points: 5, Solved: 1,
			LastScoredAt: scoredAt(freezeAt.Add(-20 * time.Minute)), FinalAt: scoredAt(freezeAt.Add(-20 * time.Minute))},
		{Registration: uuid.New(), Login: "late", Points: 99, LastScoredAt: scoredAt(freezeAt.Add(5 * time.Minute))},
	}

	student := f.student.ID
	rec := f.request(http.MethodGet, "/contests/"+c.ID.String()+"/play/leaderboard", &student, "")
	want := `{"state":"frozen","scoring":"winner","title":"The Library Murder","frozen_at":"2026-03-01T11:30:00Z",` +
		`"ends_at":"2026-03-01T12:00:00Z","generated_at":"2026-03-01T11:40:00Z","truncated":false,"rows":[` +
		`{"place":1,"label":"student","points":5,"solved":1,"last_scored_at":"2026-03-01T11:10:00Z","winner":true,"is_you":true},` +
		`{"place":null,"label":"rival","points":20,"solved":0,"last_scored_at":"2026-03-01T11:00:00Z"}]}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("participant body =\n%s\nwant\n%s", got, want)
	}
}

// An ICPC table with nobody on it still names its questions, and the letters
// come with the grid rather than being counted from rows.
func TestAnICPCTableWithNobodyOnItStillNamesItsQuestions(t *testing.T) {
	f := newBoardFixture(t)
	freeze := 30
	c, _ := f.contest(t, contests.StatusRunning, &freeze)
	c.Scoring = contests.ScoringICPC
	f.stores.Contests.Put(c)
	f.standings.icpc = &icpcBoard{questions: 3, start: *c.StartsAt, penaltyMin: 20}

	organizer := f.organizer.ID
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"public": f.request(http.MethodGet, "/contests/"+c.ID.String()+"/leaderboard", nil, ""),
		"live":   f.request(http.MethodGet, "/contests/"+c.ID.String()+"/leaderboard/live", &organizer, ""),
	} {
		raw := rec.Body.String()
		if body := decodeICPC(t, rec); strings.Join(body.Questions, ",") != "A,B,C" || len(body.Rows) != 0 ||
			!strings.Contains(raw, `"rows":[]`) {
			t.Errorf("%s: body = %s, want questions A, B, C and no rows", name, raw)
		}
	}
}

// icpcContest seeds a running ICPC contest frozen 30 minutes before its end,
// with the clock ten minutes into the freeze, and a board of three questions
// on which the student and a rival have answered on both sides of the freeze.
//
// Before the freeze: the student solves A at minute 10 and gets B wrong once;
// the rival solves A at minute 20. After it: the rival solves B, the student
// solves B a minute later, and the student solves C.
func (f *boardFixture) icpcContest(t *testing.T) (contests.Contest, time.Time) {
	t.Helper()
	freeze := 30
	c, me := f.contest(t, contests.StatusRunning, &freeze)
	c.Scoring, c.ICPCPenaltyMin = contests.ScoringICPC, 20
	f.stores.Contests.Put(c)
	freezeAt := c.EndsAt.Add(-30 * time.Minute)
	f.now = freezeAt.Add(10 * time.Minute)
	minute := func(n int) time.Time { return c.StartsAt.Add(time.Duration(n) * time.Minute) }
	afterFreeze := func(n int) time.Time { return freezeAt.Add(time.Duration(n) * time.Minute) }

	f.standings.icpc = &icpcBoard{questions: 3, start: *c.StartsAt, penaltyMin: 20, entrants: []icpcEntrant{
		{registration: me.ID, login: "student", answers: []icpcAnswer{
			{question: 0, at: minute(10), correct: true},
			{question: 1, at: freezeAt.Add(-5 * time.Minute)},
			{question: 1, at: afterFreeze(2), correct: true},
			{question: 2, at: afterFreeze(1), correct: true},
		}},
		{registration: uuid.New(), login: "rival", answers: []icpcAnswer{
			{question: 0, at: minute(20), correct: true},
			{question: 1, at: afterFreeze(1), correct: true},
		}},
	}}
	return c, freezeAt
}

type icpcBody struct {
	State     string   `json:"state"`
	Questions []string `json:"questions"`
	Rows      []struct {
		Label   string          `json:"label"`
		Login   string          `json:"login"`
		Place   *int            `json:"place"`
		Solved  int             `json:"solved"`
		Penalty *int            `json:"penalty"`
		Cells   json.RawMessage `json:"cells"`
	} `json:"rows"`
}

func intString(n *int) string {
	if n == nil {
		return "absent"
	}
	return strconv.Itoa(*n)
}

func decodeICPC(t *testing.T, rec *httptest.ResponseRecorder) icpcBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body icpcBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return body
}

// A frozen ICPC table tells how many attempts came after the freeze on a
// question not solved before it, and nothing else about them: no solve, no
// minute, no attempt number, no first-solver mark, and no change to solved,
// penalty or order — even where the attempt was correct. Checked on the body,
// for the public table and the participant's copy.
func TestAFrozenICPCTableShowsOnlyHowManyAttemptsCameAfterTheFreeze(t *testing.T) {
	f := newBoardFixture(t)
	c, _ := f.icpcContest(t)
	student := f.student.ID

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"public":      f.request(http.MethodGet, "/contests/"+c.ID.String()+"/leaderboard", nil, ""),
		"participant": f.request(http.MethodGet, "/contests/"+c.ID.String()+"/play/leaderboard", &student, ""),
	} {
		raw := rec.Body.String()
		body := decodeICPC(t, rec)
		if body.State != leaderboard.StateFrozen || strings.Join(body.Questions, ",") != "A,B,C" {
			t.Fatalf("%s: state %s, questions %v; want frozen with A, B, C", name, body.State, body.Questions)
		}
		if len(body.Rows) != 2 || body.Rows[0].Label != "student" || body.Rows[1].Label != "rival" {
			t.Fatalf("%s: rows = %s, want student then rival", name, raw)
		}
		wantRows := []struct {
			solved, penalty int
			cells           string
		}{
			{1, 10, `[{"state":"solved","attempts":1,"minute":10,"first":true},` +
				`{"state":"pending","attempts":1,"pending":1},{"state":"pending","pending":1}]`},
			{1, 20, `[{"state":"solved","attempts":1,"minute":20,"first":false},` +
				`{"state":"pending","pending":1},{"state":"untried"}]`},
		}
		for i, want := range wantRows {
			row := body.Rows[i]
			if row.Solved != want.solved || row.Penalty == nil || *row.Penalty != want.penalty || row.Place == nil || *row.Place != i+1 {
				t.Errorf("%s: %s solved %d, penalty %s, place %s; want %d, %d, %d",
					name, row.Label, row.Solved, intString(row.Penalty), intString(row.Place), want.solved, want.penalty, i+1)
			}
			if string(row.Cells) != want.cells {
				t.Errorf("%s: %s cells =\n%s\nwant\n%s", name, row.Label, row.Cells, want.cells)
			}
		}
		// Minutes 151 and 152 are the solves after the freeze.
		for _, leak := range []string{`"minute":151`, `"minute":152`, `"attempts":2`} {
			if strings.Contains(raw, leak) {
				t.Errorf("%s: the body carries %s from after the freeze: %s", name, leak, raw)
			}
		}
	}
}

// Under sequential progression a question opens only once the one before it
// is closed — solved, or every attempt spent — so a pending attempt on B would
// say that A was closed after the freeze, and a pending A with attempts left
// beside it would say A was solved. A frozen sequential ICPC table therefore
// shows no pending attempts at all: only what was true at the freeze.
// Checked on the body, for the public table and the participant's copy.
func TestAFrozenSequentialICPCTableShowsNoPendingAttempts(t *testing.T) {
	f := newBoardFixture(t)
	freeze := 30
	c, me := f.contest(t, contests.StatusRunning, &freeze)
	c.Scoring, c.ICPCPenaltyMin = contests.ScoringICPC, 20
	c.QuestionMode, c.Progression = contests.QuestionModeMulti, contests.ProgressionSequential
	f.stores.Contests.Put(c)
	freezeAt := c.EndsAt.Add(-30 * time.Minute)
	f.now = freezeAt.Add(10 * time.Minute)
	afterFreeze := func(n int) time.Time { return freezeAt.Add(time.Duration(n) * time.Minute) }

	// Before the freeze the student gets A wrong once. After it they solve A,
	// which opens B, and try B.
	f.standings.icpc = &icpcBoard{questions: 2, start: *c.StartsAt, penaltyMin: 20, entrants: []icpcEntrant{
		{registration: me.ID, login: "student", answers: []icpcAnswer{
			{question: 0, at: freezeAt.Add(-5 * time.Minute)},
			{question: 0, at: afterFreeze(1), correct: true},
			{question: 1, at: afterFreeze(2)},
		}},
	}}
	student := f.student.ID

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"public":      f.request(http.MethodGet, "/contests/"+c.ID.String()+"/leaderboard", nil, ""),
		"participant": f.request(http.MethodGet, "/contests/"+c.ID.String()+"/play/leaderboard", &student, ""),
	} {
		raw := rec.Body.String()
		body := decodeICPC(t, rec)
		if body.State != leaderboard.StateFrozen || strings.Join(body.Questions, ",") != "A,B" || len(body.Rows) != 1 {
			t.Fatalf("%s: body = %s, want a frozen table of A, B with one row", name, raw)
		}
		if strings.Contains(raw, "pending") {
			t.Errorf("%s: a sequential table says pending: %s", name, raw)
		}
		row := body.Rows[0]
		wantCells := `[{"state":"failed","attempts":1},{"state":"untried"}]`
		if row.Solved != 0 || row.Penalty == nil || *row.Penalty != 0 || string(row.Cells) != wantCells {
			t.Errorf("%s: solved %d, penalty %s, cells\n%s\nwant 0, 0,\n%s", name, row.Solved, intString(row.Penalty), row.Cells, wantCells)
		}
		for _, leak := range []string{`"minute"`, `"attempts":2`, `"solved"`} {
			if strings.Contains(string(row.Cells), leak) {
				t.Errorf("%s: the cells carry %s from after the freeze: %s", name, leak, row.Cells)
			}
		}
	}
}

// The staff table is cut off now: it sees the solves after the freeze, marks
// the first solver by them, and never says pending.
func TestTheLiveICPCTableSeesTheResultsAndNoPending(t *testing.T) {
	f := newBoardFixture(t)
	c, _ := f.icpcContest(t)
	organizer := f.organizer.ID

	rec := f.request(http.MethodGet, "/contests/"+c.ID.String()+"/leaderboard/live", &organizer, "")
	raw := rec.Body.String()
	body := decodeICPC(t, rec)
	if strings.Contains(raw, "pending") {
		t.Errorf("the staff table says pending: %s", raw)
	}
	if strings.Join(body.Questions, ",") != "A,B,C" || len(body.Rows) != 2 || body.Rows[0].Login != "student" {
		t.Fatalf("body = %s, want questions A, B, C and the student first", raw)
	}
	student := body.Rows[0]
	wantCells := `[{"state":"solved","attempts":1,"minute":10,"first":true},` +
		`{"state":"solved","attempts":2,"minute":152,"first":false},{"state":"solved","attempts":1,"minute":151,"first":true}]`
	if student.Solved != 3 || student.Penalty == nil || *student.Penalty != 10+152+20+151 || string(student.Cells) != wantCells {
		t.Errorf("student solved %d, penalty %s, cells\n%s\nwant 3, %d,\n%s", student.Solved, intString(student.Penalty), student.Cells, 10+152+20+151, wantCells)
	}
	if rival := body.Rows[1]; !strings.Contains(string(rival.Cells), `{"state":"solved","attempts":1,"minute":151,"first":true}`) {
		t.Errorf("rival cells = %s, want B solved first at minute 151", rival.Cells)
	}
}
