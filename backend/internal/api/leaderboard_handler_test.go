package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
