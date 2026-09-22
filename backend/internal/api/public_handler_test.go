package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/showcase"
)

// showcaseRepo is the landing page's storage as the handler tests need it:
// whatever a test put in it, and a count of how often it was asked — a
// refusal must cost no database work at all.
type showcaseRepo struct {
	numbers  showcase.Numbers
	contests []showcase.Contest
	reads    atomic.Int64
}

func (r *showcaseRepo) Numbers(context.Context) (showcase.Numbers, error) {
	r.reads.Add(1)
	return r.numbers, nil
}

func (r *showcaseRepo) Recent(context.Context, int) ([]showcase.Contest, error) {
	r.reads.Add(1)
	return r.contests, nil
}

type publicFixture struct {
	router   http.Handler
	showcase *showcaseRepo
}

func newPublicFixture(t *testing.T) *publicFixture {
	t.Helper()
	f := &publicFixture{showcase: &showcaseRepo{}}

	c := cache.NewMemory(10000)
	t.Cleanup(func() { _ = c.Close() })
	log := logging.New("error", io.Discard)
	service := showcase.NewService(showcase.Config{Repository: f.showcase})

	router := chi.NewRouter()
	api.NewPublicHandler(service, auth.NewLimiter(c), log, "en").Mount(router)
	f.router = router
	return f
}

// get asks as a visitor does: no session, no cookie, one address.
func (f *publicFixture) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
	req.RemoteAddr = "203.0.113.7:5000"
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func TestPublicReadsAnswerWithoutASession(t *testing.T) {
	f := newPublicFixture(t)
	f.showcase.numbers = showcase.Numbers{Contests: 3, Participants: 40, Queries: 900, Solved: 120}

	rec := f.get("/public/stats")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Queries int64 `json:"queries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.Queries != 900 {
		t.Errorf("queries = %d, want 900", body.Queries)
	}
}

func TestPublicReadsAreRefusedPastTheirBudget(t *testing.T) {
	f := newPublicFixture(t)

	var last *httptest.ResponseRecorder
	for i := 0; i <= api.PublicReadsPerMinute; i++ {
		last = f.get("/public/stats")
	}

	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once the budget is spent", last.Code)
	}
	if f.showcase.reads.Load() > int64(api.PublicReadsPerMinute) {
		t.Errorf("the repository was read %d times: a refusal must cost no database work", f.showcase.reads.Load())
	}
	if code := errorCode(t, last); code != "public_too_often" {
		t.Errorf("code = %q, want public_too_often", code)
	}
}

// The contest list is the same read and the same budget.
func TestTheContestListAnswersWithoutASession(t *testing.T) {
	f := newPublicFixture(t)
	starts := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	ends := starts.Add(3 * time.Hour)
	f.showcase.contests = []showcase.Contest{{
		ID: uuid.New(), Status: contests.StatusFinished, StartsAt: &starts, EndsAt: &ends,
		TableOpen: true, DefaultLanguage: "en", Titles: map[string]string{"en": "The Library Murder"},
	}}

	rec := f.get("/public/contests")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			Title     string `json:"title"`
			Status    string `json:"status"`
			StartsAt  string `json:"starts_at"`
			EndsAt    string `json:"ends_at"`
			TableOpen bool   `json:"table_open"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("body carries %d contests, want 1: %s", len(body.Items), rec.Body.String())
	}
	got := body.Items[0]
	if got.Title != "The Library Murder" || got.Status != contests.StatusFinished || !got.TableOpen {
		t.Errorf("contest = %+v, want the finished contest with its table open", got)
	}
	if got.StartsAt != "2026-09-20T09:00:00Z" || got.EndsAt != "2026-09-20T12:00:00Z" {
		t.Errorf("window = %s..%s, want the contest's own", got.StartsAt, got.EndsAt)
	}
}

func TestADraftNeverReachesTheLandingPage(t *testing.T) {
	f := newPublicFixture(t)
	draft := uuid.New()
	f.showcase.contests = []showcase.Contest{
		{ID: draft, Status: contests.StatusDraft, DefaultLanguage: "en",
			Titles: map[string]string{"en": "Not published yet"}},
		{ID: uuid.New(), Status: contests.StatusRunning, DefaultLanguage: "en",
			Titles: map[string]string{"en": "The Library Murder"}},
	}

	rec := f.get("/public/contests")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{draft.String(), "Not published yet"} {
		if strings.Contains(body, secret) {
			t.Errorf("the landing page carries %q: %s", secret, body)
		}
	}
}

// Nothing on this page belongs in a search index: the JSON is the page's
// material, not the page.
func TestThePublicReadsAreNotIndexed(t *testing.T) {
	f := newPublicFixture(t)
	for _, path := range []string{"/public/stats", "/public/contests"} {
		if got := f.get(path).Header().Get("X-Robots-Tag"); got != "noindex" {
			t.Errorf("%s: X-Robots-Tag = %q, want noindex", path, got)
		}
	}
}

// The title follows the visitor, the way every other read of a contest's
// text does.
func TestTheListIsTitledInTheVisitorsLanguage(t *testing.T) {
	f := newPublicFixture(t)
	f.showcase.contests = []showcase.Contest{{
		ID: uuid.New(), Status: contests.StatusRunning, DefaultLanguage: "ro",
		Titles: map[string]string{"ro": "Olimpiada", "en": "The Olympiad"},
	}}

	if body := f.get("/public/contests?lang=en").Body.String(); !strings.Contains(body, "The Olympiad") {
		t.Errorf("?lang=en answered %s, want the English title", body)
	}
	// A language nobody wrote this contest in: its own default title, never
	// an empty row.
	if body := f.get("/public/contests?lang=de").Body.String(); !strings.Contains(body, "Olimpiada") {
		t.Errorf("?lang=de answered %s, want the contest's own default title", body)
	}
	// No preference at all is the installation's default locale, which this
	// contest does speak.
	if body := f.get("/public/contests").Body.String(); !strings.Contains(body, "The Olympiad") {
		t.Errorf("with no preference the body was %s, want the installation's default locale", body)
	}
}
