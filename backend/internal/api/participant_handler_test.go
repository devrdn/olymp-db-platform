package api_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/devrdn/db-contest/backend/internal/workspace"
	"github.com/devrdn/db-contest/backend/internal/workspace/workspacetest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// fakeAccess answers Access with whatever a test staged; admission is tested in
// internal/queryproxy.
type fakeAccess struct {
	participant contests.Participant
	contest     contests.Contest
	// mu guards err: events tests change it while a connection's goroutine
	// calls Access on every resync tick.
	mu  sync.Mutex
	err error
	// gotContestID records what Access was asked about.
	gotContestID uuid.UUID
	// admitReadErr is what AdmitRead answers (nil admits); accessCalled records
	// whether Access was reached.
	admitReadErr error
	accessCalled bool
	// admitReads counts AdmitRead calls.
	admitReads int
	// delay, when set, is how long AccessForEvents waits before answering,
	// standing for slow resync lookups.
	delay time.Duration
	// schema and schemaErr are what Schema answers; schemaAsked and schemaFor
	// record what it was handed.
	schema      provisioning.Schema
	schemaErr   error
	schemaAsked uuid.UUID
	schemaFor   uuid.UUID
	// startedOnRead and startedFrom record StartOnRead calls; startOnReadErr is
	// its answer.
	startedOnRead  []uuid.UUID
	startedFrom    []netip.Addr
	startOnReadErr error
	// gateNow is the instant AccessForEvents asks the gate about (zero is the
	// wall clock), so fixed-date contests do not expire as the calendar moves.
	gateNow time.Time
}

func (a *fakeAccess) StartOnRead(_ context.Context, _ contests.Contest, participant contests.Participant, addr netip.Addr) (contests.Participant, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.startedOnRead = append(a.startedOnRead, participant.ID)
	a.startedFrom = append(a.startedFrom, addr)
	if a.startOnReadErr != nil {
		return contests.Participant{}, a.startOnReadErr
	}
	return participant, nil
}

func (a *fakeAccess) Schema(_ context.Context, contest contests.Contest, participant contests.Participant, _ netip.Addr) (provisioning.Schema, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.schemaAsked = contest.ID
	a.schemaFor = participant.ID
	if a.schemaErr != nil {
		return provisioning.Schema{}, a.schemaErr
	}
	return a.schema, nil
}

func (a *fakeAccess) AdmitRead(uuid.UUID) error {
	a.mu.Lock()
	a.admitReads++
	a.mu.Unlock()
	return a.admitReadErr
}

func (a *fakeAccess) Access(_ context.Context, contestID, _ uuid.UUID, _ netip.Addr) (contests.Participant, contests.Contest, error) {
	a.accessCalled = true
	a.gotContestID = contestID
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.participant, a.contest, a.err
}

// AccessForEvents mirrors queryproxy.Service.AccessForEvents: a staged err is a
// failed lookup with a zero Standing; otherwise the real gate decides, so
// events tests see the Standing a staged state produces.
func (a *fakeAccess) AccessForEvents(ctx context.Context, contestID, userID uuid.UUID, addr netip.Addr) (contests.Participant, contests.Contest, contests.Standing, error) {
	a.mu.Lock()
	delay := a.delay
	a.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return contests.Participant{}, contests.Contest{}, contests.Standing{}, ctx.Err()
		}
	}
	participant, contest, err := a.Access(ctx, contestID, userID, addr)
	if err != nil {
		return contests.Participant{}, contests.Contest{}, contests.Standing{}, err
	}
	now := a.gateNow
	if now.IsZero() {
		now = time.Now()
	}
	standing := contests.NewGate(0).StandingOf(contest, participant, now, addr)
	if !standing.MayWait() {
		return contests.Participant{}, contests.Contest{}, standing, standing.Refusal()
	}
	return participant, contest, standing, nil
}

func (a *fakeAccess) setDelay(d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.delay = d
}

func (a *fakeAccess) setErr(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

func (a *fakeAccess) setParticipant(p contests.Participant) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.participant = p
}

func (a *fakeAccess) setContest(c contests.Contest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.contest = c
}

// fakeHistory answers History with whatever a test staged.
type fakeHistory struct {
	items []queryrunner.HistoryEntry
	total int
	err   error
	// gotRegistration, gotLimit and gotOffset record what History was asked.
	gotRegistration uuid.UUID
	gotLimit        int
	gotOffset       int
	called          bool
	// exported, exportErr and gotExportRegistration are the same three facts
	// for the CSV download.
	exported              []queryrunner.HistoryEntry
	exportErr             error
	gotExportRegistration uuid.UUID
	exportCalled          bool
	// exportTruncated reports a log longer than one download may carry.
	exportTruncated bool
	// exportGate, when set, holds the read inside ExportHistory until closed,
	// so two downloads can overlap.
	exportGate chan struct{}
	// gotExportDeadline is the deadline the handler put on the read.
	gotExportDeadline time.Time
	// inside counts reads held at exportGate; mu guards the fields two
	// goroutines touch.
	mu     sync.Mutex
	inside int
}

func (h *fakeHistory) ExportHistory(ctx context.Context, registrationID uuid.UUID, yield func(queryrunner.HistoryEntry) error) (bool, error) {
	h.mu.Lock()
	h.exportCalled = true
	h.gotExportRegistration = registrationID
	h.gotExportDeadline, _ = ctx.Deadline()
	gate, truncated, failure, rows := h.exportGate, h.exportTruncated, h.exportErr, h.exported
	h.inside++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.inside--
		h.mu.Unlock()
	}()

	if gate != nil {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-gate:
		}
	}
	for _, entry := range rows {
		if err := yield(entry); err != nil {
			return false, err
		}
	}
	return truncated, failure
}

// awaitInsideExport blocks until a read is inside ExportHistory; without it the
// concurrency test would depend on goroutine scheduling.
func (h *fakeHistory) awaitInsideExport(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		in := h.inside
		h.mu.Unlock()
		if in > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no export ever started")
}

func (h *fakeHistory) History(_ context.Context, registrationID uuid.UUID, limit, offset int) ([]queryrunner.HistoryEntry, int, error) {
	h.called = true
	h.gotRegistration = registrationID
	h.gotLimit = limit
	h.gotOffset = offset
	return h.items, h.total, h.err
}

// fakeSubmitter answers Submit with whatever a test staged.
type fakeSubmitter struct {
	outcome contests.SubmitOutcome
	err     error
	// gotCmd records what Submit was asked.
	gotCmd contests.SubmitCommand
	called bool
}

func (s *fakeSubmitter) Submit(_ context.Context, cmd contests.SubmitCommand) (contests.SubmitOutcome, error) {
	s.called = true
	s.gotCmd = cmd
	return s.outcome, s.err
}

// fixtureAnswersPerMinute is low enough to reach in a few requests and high
// enough that one or two answers never meet it.
const fixtureAnswersPerMinute = 3

// answerRate is the real fixed-window limiter over the test's cache.
func answerRate(c cache.Cache, perMinute int) api.AnswerRate {
	return api.AnswerRate{Limiter: auth.NewLimiter(c), PerMinute: perMinute}
}

// failingLimiter is an answer throttle whose counter cannot be kept.
type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string, int, time.Duration) (bool, error) {
	return false, errors.New("cache unreachable")
}

// participantFixture mounts the participant endpoints with a fake Access and a
// real Reader over in-memory stores.
type participantFixture struct {
	router         http.Handler
	access         *fakeAccess
	history        *fakeHistory
	submitter      *fakeSubmitter
	stories        *conteststest.Stories
	questions      *conteststest.Questions
	submissions    *conteststest.Submissions
	workspaceStore *failingWorkspace
	watcher        *recordingWatcher
	signalStore    *signalStore
	logs           *logBuffer
	cookie         *http.Cookie
}

const fixtureUserAgent = "fixture-browser/1.0"

type recordingWatcher struct {
	mu     sync.Mutex
	visits []monitor.Visit
}

func (w *recordingWatcher) Observe(_ context.Context, visit monitor.Visit) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.visits = append(w.visits, visit)
}

func (w *recordingWatcher) seen() []monitor.Visit {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]monitor.Visit(nil), w.visits...)
}

func newParticipantFixture(t *testing.T) *participantFixture {
	t.Helper()

	userRepo := userstest.New()
	userRepo.GrantRole("student")
	actor := userRepo.Add(users.User{Login: "student", FullName: "Student", Status: users.StatusActive, Roles: []string{"student"}})

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	logs := &logBuffer{}
	log := logging.New("error", logs)
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
	submissions := conteststest.NewSubmissions()
	submissions.Clock = func() time.Time { return conteststest.FixtureNow }
	reader := contests.NewReader(stories, questions, conteststest.NewAttempts(submissions), nil)
	access := &fakeAccess{}
	history := &fakeHistory{}
	submitter := &fakeSubmitter{}

	workspaceStore := &failingWorkspace{Repository: workspacetest.NewRepository()}
	workspaces := workspace.NewService(workspaceStore, auth.NewLimiter(c))
	watcher := &recordingWatcher{}
	signals := &signalStore{}

	router := chi.NewRouter()
	api.NewParticipantHandler(access, reader, history, submitter, answerRate(c, fixtureAnswersPerMinute), mw, log, "en").
		WithWorkspace(workspaces).
		WithWatcher(watcher).
		WithSignals(monitor.NewSignals(auth.NewLimiter(c), signals)).
		Mount(router)

	return &participantFixture{
		router: router, access: access, history: history, submitter: submitter,
		stories: stories, questions: questions, submissions: submissions,
		workspaceStore: workspaceStore,
		watcher:        watcher,
		signalStore:    signals,
		logs:           logs,
		cookie:         &http.Cookie{Name: auth.SessionCookieName, Value: token},
	}
}

func (f *participantFixture) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("User-Agent", fixtureUserAgent)
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

// submit records one answer through the store the reader's attempt stats derive
// from.
func (f *participantFixture) submit(t *testing.T, registrationID uuid.UUID, q contests.Question, correct bool) {
	t.Helper()
	if _, err := f.submissions.Insert(t.Context(), contests.SubmissionRequest{
		RegistrationID: registrationID, QuestionID: q.ID, Value: "an answer",
		IsCorrect: correct, Points: q.Points, MaxAttempts: q.MaxAttempts,
		Deadline: conteststest.FixtureNow.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Insert() = %v", err)
	}
}

// playContest stages a running contest with one visible question and a story
// in English, and returns its identifier.
func (f *participantFixture) playContest(t *testing.T) uuid.UUID {
	t.Helper()
	contestID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	if _, err := f.stories.Save(context.Background(), contestID, map[string]string{"en": "A body in the stacks."}); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	return contestID
}

// Under individual timing reading starts the clock; the gate admitting the
// start needs the caller's address.
func TestReadingTheStoryOrTheQuestionsStartsTheClock(t *testing.T) {
	for _, path := range []string{"/play/story", "/play/questions"} {
		t.Run(path, func(t *testing.T) {
			f := newParticipantFixture(t)
			contestID := f.playContest(t)

			rec := f.get("/contests/" + contestID.String() + path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			if len(f.access.startedOnRead) != 1 || f.access.startedOnRead[0] != f.access.participant.ID {
				t.Fatalf("StartOnRead asked for %v, want exactly the resolved registration", f.access.startedOnRead)
			}
			// httptest.NewRequest's own RemoteAddr, 192.0.2.1:1234.
			if want := netip.MustParseAddr("192.0.2.1"); len(f.access.startedFrom) != 1 || f.access.startedFrom[0] != want {
				t.Fatalf("StartOnRead asked from %v, want %v", f.access.startedFrom, want)
			}
		})
	}
}

func TestARefusedContentReadStartsNoClock(t *testing.T) {
	for name, given := range map[string]struct {
		path  string
		stage func(f *participantFixture)
		want  int
	}{
		"questions over the rate":           {"/play/questions", func(f *participantFixture) { f.access.admitReadErr = queryrunner.ErrTooManyQueries }, http.StatusTooManyRequests},
		"story from an address not allowed": {"/play/story", func(f *participantFixture) { f.access.err = contests.ErrAddressNotAllowed }, http.StatusForbidden},
		"a story that does not exist": {"/play/story", func(f *participantFixture) {
			_ = f.stories.Delete(context.Background(), f.access.contest.ID)
		}, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			f := newParticipantFixture(t)
			contestID := f.playContest(t)
			given.stage(f)

			rec := f.get("/contests/" + contestID.String() + given.path)
			if rec.Code != given.want {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, given.want, rec.Body.String())
			}
			if len(f.access.startedOnRead) != 0 {
				t.Fatalf("StartOnRead was reached by a refused read")
			}
		})
	}
}

// The window can close between admission and the read.
func TestAContentReadWhoseClockCannotStartIsRefused(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := f.playContest(t)
	f.access.startOnReadErr = contests.ErrContestNotRunning

	rec := f.get("/contests/" + contestID.String() + "/play/questions")
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "contest_not_running" {
		t.Fatalf("status = %d, body %s; want 409 contest_not_running", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Who did it?") {
		t.Fatal("the question was sent although the clock could not start")
	}
}

// The query log is the participant's own record, not the contest, and an
// answer starts the clock inside Submit: neither asks StartOnRead.
func TestTheQueryLogAndAnswersDoNotStartTheClockOnRead(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := f.playContest(t)

	if rec := f.get("/contests/" + contestID.String() + "/play/log"); rec.Code != http.StatusOK {
		t.Fatalf("log: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if rec := f.post("/contests/"+contestID.String()+"/questions/"+uuid.New().String()+"/answer", `{"value":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("answer: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if len(f.access.startedOnRead) != 0 {
		t.Fatalf("StartOnRead asked for %v, want nothing", f.access.startedOnRead)
	}
}

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

// A dense ordinal would reveal how many questions are hidden and where (§6.1);
// items already arrive in display order.
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

// One representative refusal: the mapping is one table (errortable.go).
func TestAccessRefusalsAreAnsweredFromTheSharedTable(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.err = contests.ErrAddressNotAllowed
	contestID := uuid.New()

	for _, path := range []string{
		"/contests/" + contestID.String() + "/play/story",
		"/contests/" + contestID.String() + "/play/questions",
		"/contests/" + contestID.String() + "/play/log",
	} {
		rec := f.get(path)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403 (body: %s)", path, rec.Code, rec.Body.String())
		}
		if code := errorCode(t, rec); code != "address_not_allowed" {
			t.Fatalf("%s: code = %q, want address_not_allowed", path, code)
		}
		if message := errorMessage(t, rec); message != "This contest is only available from the university network" {
			t.Fatalf("%s: message = %q", path, message)
		}
	}
}

// The pool's own warning does not say participants are being refused.
func TestAFullGameClusterOnTheSchemaPanelIsLogged(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.schemaErr = fmt.Errorf("%w: %w", queryproxy.ErrNoRoomForDatabase, provisioning.ErrClusterFull)

	rec := f.get("/contests/" + uuid.New().String() + "/play/schema")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "game_cluster_full" {
		t.Fatalf("code = %q, want game_cluster_full", code)
	}
	if !f.logs.loggedError("the game cluster has no room") {
		t.Fatal("a full game cluster was answered without being logged")
	}
}

// Not a 404, which would confirm the contest exists.
func TestAParticipantOfAnotherContestLearnsNothingAboutThisOne(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.err = contests.ErrNotAParticipant

	rec := f.get("/contests/" + uuid.New().String() + "/play/story")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "not_a_participant" {
		t.Fatalf("code = %q, want not_a_participant", code)
	}
}

// Refused before Access runs its lookups, with the sentinel the console maps to
// 429.
func TestARateLimitRefusalIsA429AndNeverReachesAccess(t *testing.T) {
	for _, path := range []string{"story", "questions", "log"} {
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
			if retry := rec.Header().Get("Retry-After"); retry != "60" {
				t.Fatalf("Retry-After = %q, want 60", retry)
			}
		})
	}
}

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

func TestAMissingStoryIsA404(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}
	// No story saved, so the reader answers contests.ErrStoryNotFound.

	rec := f.get("/contests/" + contestID.String() + "/play/story")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "story_not_found" {
		t.Fatalf("code = %q, want story_not_found", code)
	}
}

// The response names the language it served (§6.2).
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

	// A language the contest does not offer falls back to the contest's
	// default, not the installation's.
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

	rec = f.get("/contests/" + contestID.String() + "/play/story?lang=ru")
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Lang != "ru" || payload.BodyMD != "Тело в архиве." {
		t.Fatalf("payload = %+v, want the Russian story", payload)
	}
}

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
	f.submit(t, registrationID, q, false)
	f.submit(t, registrationID, q, false)

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

// The only way to tell "closed because solved" from "closed because every
// attempt is spent" without re-submitting.
func TestQuestionsCarryCorrectAndPointsAwarded(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	registrationID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}},
	}
	f.access.participant = contests.Participant{ID: registrationID}

	q := f.questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	// One wrong attempt at a penalty of two: 8 is won, not the question's face
	// value.
	for _, correct := range []bool{false, true} {
		if _, err := f.submissions.Insert(t.Context(), contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "an answer",
			IsCorrect: correct, Points: q.Points, PenaltyPerAttempt: 2,
			Deadline: conteststest.FixtureNow.Add(time.Hour),
		}); err != nil {
			t.Fatalf("Insert() = %v", err)
		}
	}

	rec := f.get("/contests/" + contestID.String() + "/play/questions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			Correct       bool `json:"correct"`
			PointsAwarded int  `json:"points_awarded"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items = %+v, want 1", payload.Items)
	}
	if got := payload.Items[0]; !got.Correct || got.PointsAwarded != 8 {
		t.Fatalf("item = %+v, want {Correct: true, PointsAwarded: 8}", got)
	}
}

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

// chi silently lets the later Mount win on shared paths, so both handlers are
// assembled as app.go does.
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

	router := chi.NewRouter()
	api.NewContestsHandler(stores.Service, mw, log, "en").Mount(router)
	reader := contests.NewReader(stores.Stories, stores.Questions, conteststest.NewAttempts(stores.Submissions), stores.Sequence)
	access := &fakeAccess{err: contests.ErrNotAParticipant}
	api.NewParticipantHandler(access, reader, &fakeHistory{}, stores.Service, answerRate(c, fixtureAnswersPerMinute), mw, log, "en").Mount(router)

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	// The staff shape (a contest UUID and every translation), not the
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

	// The fake Access refuses everyone, so not_a_participant proves the request
	// reached this handler's admission and not the staff route's RBAC, which
	// would answer a bare 403.
	participant := do("/contests/" + contestID.String() + "/play/story")
	if participant.Code != http.StatusForbidden {
		t.Fatalf("the participant story endpoint = %d, want 403 (body: %s)", participant.Code, participant.Body.String())
	}
	if code := errorCode(t, participant); code != "not_a_participant" {
		t.Fatalf("code = %q, want not_a_participant (proving this handler, not the staff route's RBAC, answered)", code)
	}
}

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
	// httptest's RemoteAddr: Submit asks the gate from the caller's address.
	if want := netip.MustParseAddr("192.0.2.1"); got.Address != want {
		t.Fatalf("Address = %v, want %v (the caller's)", got.Address, want)
	}
}

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

// One representative refusal: TestEveryContestsErrorHasItsAnswer walks the
// table. The queryproxy cases are admission refusals Submit passes through.
func TestAnswerRefusalsBecomeTheDocumentedStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    string
		wantMessage string
	}{
		{"question not found", contests.ErrQuestionNotFound, http.StatusNotFound, "question_not_found",
			"No such question in this contest"},
		{"not a participant", contests.ErrNotAParticipant, http.StatusForbidden, "not_a_participant", ""},
		{"contest not running", contests.ErrContestNotRunning, http.StatusConflict, "contest_not_running", ""},
		{"contest ended", contests.ErrContestEnded, http.StatusConflict, "contest_ended", ""},
		// Submit asks the participation gate itself, so every refusal of the
		// gate can come back from it, not only from Access.
		{"deadline passed", contests.ErrDeadlinePassed, http.StatusConflict, "deadline_passed", ""},
		{"address not allowed", contests.ErrAddressNotAllowed, http.StatusForbidden, "address_not_allowed", ""},
		{"participant finished", contests.ErrParticipantFinished, http.StatusConflict, "contest_finished", ""},
		// Removed between admission and the answer: told "not taking part",
		// never participant_not_found, which the play screen does not know.
		{"registration removed mid-answer", fmt.Errorf("start the participant's clock: %w", contests.ErrParticipantNotFound),
			http.StatusForbidden, "not_a_participant", "The caller is not taking part in this contest"},
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
			if tc.wantMessage != "" {
				if message := errorMessage(t, rec); message != tc.wantMessage {
					t.Fatalf("message = %q, want %q", message, tc.wantMessage)
				}
			}
		})
	}
}

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

// Exhausting Submit's retry bound must surface as a 4xx, not a 500.
// ConflictsRemaining stands in for seven racers; the real race is tested
// against PostgreSQL in internal/postgres/submissions_test.go.
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

	// From Submit's side, its retry loop sees nothing but conflicts.
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
	reader := contests.NewReader(stores.Stories, stores.Questions, conteststest.NewAttempts(stores.Submissions), stores.Sequence)
	router := chi.NewRouter()
	// The real retry loop, not a fakeSubmitter.
	api.NewParticipantHandler(access, reader, &fakeHistory{}, stores.Service, answerRate(c2, fixtureAnswersPerMinute), mw, log, "en").Mount(router)

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

// An answer is a guess, and fast guesses solve a question by elimination.
func TestAnswersBeyondTheRegistrationsRateAreRefusedBeforeGrading(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	path := "/contests/" + contestID.String() + "/questions/" + uuid.New().String() + "/answer"

	for i := 0; i < fixtureAnswersPerMinute; i++ {
		if rec := f.post(path, `{"value":"x"}`); rec.Code != http.StatusOK {
			t.Fatalf("answer %d: status = %d, want 200 (body: %s)", i+1, rec.Code, rec.Body.String())
		}
	}

	f.submitter.called = false
	rec := f.post(path, `{"value":"x"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "answer_too_often" {
		t.Fatalf("code = %q, want answer_too_often", code)
	}
	if seconds, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || seconds <= 0 {
		t.Fatalf("Retry-After = %q, want a positive number of seconds", rec.Header().Get("Retry-After"))
	}
	if f.submitter.called {
		t.Fatal("Submit was reached by an answer over the rate")
	}
}

// CLAUDE.md rule 13: malformed and refused answers spend the budget too.
func TestARefusedAnswerStillCountsAgainstTheAnswerRate(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	path := "/contests/" + contestID.String() + "/questions/" + uuid.New().String() + "/answer"

	if rec := f.post(path, `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: status = %d, want 400", rec.Code)
	}
	if rec := f.post("/contests/"+contestID.String()+"/questions/not-a-uuid/answer", `{"value":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid question id: status = %d, want 400", rec.Code)
	}
	f.submitter.err = contests.ErrQuestionClosed
	if rec := f.post(path, `{"value":"x"}`); rec.Code != http.StatusConflict {
		t.Fatalf("closed question: status = %d, want 409", rec.Code)
	}

	f.submitter.err = nil
	f.submitter.called = false
	rec := f.post(path, `{"value":"x"}`)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "answer_too_often" {
		t.Fatalf("status = %d, body %s; want 429 answer_too_often", rec.Code, rec.Body.String())
	}
	if f.submitter.called {
		t.Fatal("Submit was reached by an answer over the rate")
	}
}

func TestTheAnswerRateIsKeptPerRegistration(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	path := "/contests/" + contestID.String() + "/questions/" + uuid.New().String() + "/answer"

	for i := 0; i <= fixtureAnswersPerMinute; i++ {
		f.post(path, `{"value":"x"}`)
	}

	f.access.participant = contests.Participant{ID: uuid.New()}
	if rec := f.post(path, `{"value":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("another registration: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

// A counter that cannot be kept is a protection not in place.
func TestAnAnswerIsRefusedWhenItsRateCannotBeCounted(t *testing.T) {
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	userRepo := userstest.New()
	userRepo.GrantRole("student")
	actor := userRepo.Add(users.User{Login: "student", FullName: "Student", Status: users.StatusActive, Roles: []string{"student"}})
	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: actor.ID, Login: actor.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: userRepo, Authorizer: rbac.New(noRoles{}),
		Cookies: auth.NewCookieWriter(false), Logger: log,
	})
	contestID := uuid.New()
	access := &fakeAccess{
		contest:     contests.Contest{ID: contestID, Status: contests.StatusRunning},
		participant: contests.Participant{ID: uuid.New()},
	}
	submitter := &fakeSubmitter{}
	router := chi.NewRouter()
	api.NewParticipantHandler(access, contests.NewReader(conteststest.NewStories(), conteststest.NewQuestions(), conteststest.NewAttempts(conteststest.NewSubmissions()), nil),
		&fakeHistory{}, submitter, api.AnswerRate{Limiter: failingLimiter{}, PerMinute: 6}, mw, log, "en").Mount(router)

	req := httptest.NewRequest(http.MethodPost, "/contests/"+contestID.String()+"/questions/"+uuid.New().String()+"/answer",
		strings.NewReader(`{"value":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", rec.Code, rec.Body.String())
	}
	if submitter.called {
		t.Fatal("Submit was reached although the answer rate could not be counted")
	}
}

// CLAUDE.md rule 13: the budget is checked before Submit reads the question.
func TestAnAnswerRefusedAsNotAChoiceStillSpendsTheRateBudget(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.submitter.err = contests.ErrNotAChoice

	rec := f.post("/contests/"+contestID.String()+"/questions/"+uuid.New().String()+"/answer", `{"value":"abc"}`)
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "answer_not_a_choice" {
		t.Fatalf("status = %d, body %s; want 400 answer_not_a_choice", rec.Code, rec.Body.String())
	}
	if f.access.admitReads != 1 || !f.submitter.called {
		t.Fatalf("AdmitRead called %d times, Submit called %v; want the budget spent once before Submit",
			f.access.admitReads, f.submitter.called)
	}
}

// Clamping limit and offset is History's job
// (queryrunner.NormalizeHistoryPage), so they pass through unmodified.
func TestQueryLogAsksHistoryForTheResolvedRegistrationAndThePagingParams(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	registrationID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: registrationID}

	rec := f.get("/contests/" + contestID.String() + "/play/log?limit=10&offset=20")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !f.history.called {
		t.Fatal("History was never called")
	}
	if f.history.gotRegistration != registrationID {
		t.Fatalf("registration = %s, want %s (from Access, never the request)", f.history.gotRegistration, registrationID)
	}
	if f.history.gotLimit != 10 || f.history.gotOffset != 20 {
		t.Fatalf("limit=%d offset=%d, want 10 and 20 straight from the query string", f.history.gotLimit, f.history.gotOffset)
	}
}

// A row still running carries no duration or row count rather than a false
// zero.
func TestQueryLogResponseCarriesTheHistoryEntries(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}

	duration, rows := 42, 7
	f.history.items = []queryrunner.HistoryEntry{
		{SQL: "SELECT * FROM suspects", Status: queryrunner.StatusOK, DurationMs: &duration, RowCount: &rows, ExecutedAt: time.Now()},
		{SQL: "SELECT pg_sleep(5)", Status: queryrunner.StatusRunning},
	}
	f.history.total = 2

	rec := f.get("/contests/" + contestID.String() + "/play/log")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			SQL        string `json:"sql"`
			Status     string `json:"status"`
			DurationMs *int   `json:"duration_ms"`
			RowCount   *int   `json:"row_count"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Total != 2 || len(payload.Items) != 2 {
		t.Fatalf("payload = %+v, want the two staged entries", payload)
	}
	if got := payload.Items[0]; got.SQL != "SELECT * FROM suspects" || got.Status != "ok" ||
		got.DurationMs == nil || *got.DurationMs != 42 || got.RowCount == nil || *got.RowCount != 7 {
		t.Fatalf("items[0] = %+v, want the completed entry's own outcome", got)
	}
	if got := payload.Items[1]; got.Status != "running" || got.DurationMs != nil || got.RowCount != nil {
		t.Fatalf("items[1] = %+v, want a running row with no duration or row count", got)
	}
}

// A log must not present a shortened query as what they wrote. A row that was
// not cut omits the flag rather than sending false.
func TestQueryLogSaysWhichRowsHadTheirStatementCut(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}

	f.history.items = []queryrunner.HistoryEntry{
		{SQL: "SELECT 1", Status: queryrunner.StatusOK, ExecutedAt: time.Now()},
		{SQL: "SELECT 'xxxx", Status: queryrunner.StatusOK, SQLTruncated: true, ExecutedAt: time.Now()},
	}
	f.history.total = 2

	rec := f.get("/contests/" + contestID.String() + "/play/log")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var payload struct {
		Items []struct {
			SQL       string `json:"sql"`
			Truncated *bool  `json:"sql_truncated"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("payload carried %d rows, want 2", len(payload.Items))
	}
	if payload.Items[0].Truncated != nil {
		t.Errorf("a whole statement was reported as cut: %+v", payload.Items[0])
	}
	if payload.Items[1].Truncated == nil || !*payload.Items[1].Truncated {
		t.Errorf("a cut statement was handed over as if it were whole: %+v", payload.Items[1])
	}
}

// Ours, not the participant's: fail's default, like queryproxy.ErrUnavailable.
func TestQueryLogReadFailureIsA500(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.history.err = errors.New("connection reset")

	rec := f.get("/contests/" + contestID.String() + "/play/log")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", rec.Code, rec.Body.String())
	}
}

// Whose rows is decided by the registration Access resolved, not the request
// (§9.1).
func TestTheQueryLogCSVIsThisParticipantsOwnSessionAsAFile(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	registration := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: registration}

	duration, rows := 12, 3
	f.history.exported = []queryrunner.HistoryEntry{
		{
			SQL: "SELECT * FROM guests", Status: queryrunner.StatusOK,
			DurationMs: &duration, RowCount: &rows,
			ExecutedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		},
		{
			// A statement carrying a comma and a quotation mark: CSV is only
			// a format if the quoting is real.
			SQL: `SELECT "name", 1 FROM guests`, Status: queryrunner.StatusRejected,
			Error:      "function_not_supported: pg_sleep",
			ExecutedAt: time.Date(2026, 3, 1, 10, 1, 0, 0, time.UTC),
		},
	}

	rec := f.get("/contests/" + contestID.String() + "/play/log.csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", got)
	}
	if f.history.gotExportRegistration != registration {
		t.Fatalf("the log was read for registration %s, want the one Access resolved (%s)",
			f.history.gotExportRegistration, registration)
	}

	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("the body is not CSV: %v (%s)", err, rec.Body.String())
	}
	if len(records) != 3 {
		t.Fatalf("the file has %d lines, want a header and two rows: %v", len(records), records)
	}
	if got := strings.Join(records[0], ","); got != "executed_at,status,duration_ms,row_count,error,sql" {
		t.Fatalf("header = %q", got)
	}
	if records[1][0] != "2026-03-01T10:00:00Z" || records[1][1] != "ok" ||
		records[1][2] != "12" || records[1][3] != "3" || records[1][5] != "SELECT * FROM guests" {
		t.Errorf("first row = %v", records[1])
	}
	// An empty cell is how CSV says "not recorded", as the JSON omits the
	// field.
	if records[2][2] != "" || records[2][3] != "" {
		t.Errorf("a rejected row reported a duration or a row count: %v", records[2])
	}
	if records[2][4] != "function_not_supported: pg_sleep" || records[2][5] != `SELECT "name", 1 FROM guests` {
		t.Errorf("second row = %v", records[2])
	}
}

// A zero-byte download is indistinguishable from a failed one.
func TestTheQueryLogCSVOfAParticipantWhoRanNothingIsAHeaderRow(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}

	rec := f.get("/contests/" + contestID.String() + "/play/log.csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("the body is not CSV: %v", err)
	}
	if len(records) != 1 || records[0][0] != "executed_at" {
		t.Fatalf("the file is %v, want exactly the header row", records)
	}
}

// Staff have no route to this one: it answers only about the caller's own
// registration.
func TestTheQueryLogCSVIsRefusedToSomebodyNotInTheContest(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.setErr(contests.ErrNotAParticipant)

	rec := f.get("/contests/" + uuid.New().String() + "/play/log.csv")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got := errorCode(t, rec); got != "not_a_participant" {
		t.Fatalf("code = %q", got)
	}
	if f.history.exportCalled {
		t.Fatal("the query log was read for a caller who is not in the contest")
	}
}

// CLAUDE.md rule 13: the most expensive read here is charged like a query,
// before any round trip.
func TestTheQueryLogCSVSpendsTheSameRateBudgetAQueryDoes(t *testing.T) {
	f := newParticipantFixture(t)
	f.access.admitReadErr = queryrunner.ErrTooManyQueries

	rec := f.get("/contests/" + uuid.New().String() + "/play/log.csv")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if f.access.accessCalled {
		t.Error("the contest was looked up for a caller the rate limit had already refused")
	}
	if f.history.exportCalled {
		t.Error("the query log was read for a caller the rate limit had already refused")
	}
}

// Before the first row the status line is unspent, so it reports the failure
// rather than a 200 with half a file.
func TestAQueryLogCSVThatFailsBeforeItStartsIsAnErrorNotAnEmptyFile(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.history.exportErr = errors.New("the database is away")

	rec := f.get("/contests/" + contestID.String() + "/play/log.csv")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); strings.HasPrefix(got, "text/csv") {
		t.Errorf("a failure was served as a CSV file: Content-Type = %q", got)
	}
}

// query_log.error_text is stored unsanitised, so the file goes through the same
// guard as the paged log or it would bypass the console's guards.
func TestTheQueryLogCSVNeverHandsBackAFailureOfOurs(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.history.exported = []queryrunner.HistoryEntry{
		{
			SQL:    "select * from guests;",
			Status: queryrunner.StatusError,
			Error: "connecting to the game database: failed to connect to `user=game_reader " +
				"database=game_pool_ce678159661a1_57c5ba38100c`: 127.0.0.1:5433 (localhost): " +
				`failed SASL auth: FATAL: password authentication failed for user "game_reader"`,
			ExecutedAt: time.Now().UTC(),
		},
		{
			// A validator refusal of their own text is about what they typed;
			// it stays.
			SQL:        "select pg_sleep(9);",
			Status:     queryrunner.StatusRejected,
			Error:      "function_not_supported: pg_sleep",
			ExecutedAt: time.Now().UTC(),
		},
	}

	rec := f.get("/contests/" + contestID.String() + "/play/log.csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"game_reader", "game_pool_ce678159661a1", "5433", "password authentication"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("the file carries %q: %s", secret, rec.Body.String())
		}
	}
	if !strings.Contains(rec.Body.String(), "function_not_supported: pg_sleep") {
		t.Fatalf("the validator's own words about the participant's query were dropped: %s", rec.Body.String())
	}
}

// Otherwise a participant keeps what they believe is the whole record of their
// session.
func TestAQueryLogDownloadCutShortSaysSoInTheFileItself(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.history.exported = []queryrunner.HistoryEntry{
		{SQL: "SELECT 1", Status: queryrunner.StatusOK, ExecutedAt: time.Now().UTC()},
	}
	f.history.exportTruncated = true

	rec := f.get("/contests/" + contestID.String() + "/play/log.csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("the body is not CSV: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("the file has %d lines, want a header, one row and the notice: %v", len(records), records)
	}
	last := records[len(records)-1]
	if last[1] != "truncated" || last[4] == "" {
		t.Fatalf("the last line is %v; a bound that bound has to be visible to whoever opens the file", last)
	}
	// And a complete file carries no such line, or every download would look
	// like a partial one.
	f.history.exportTruncated = false
	whole, err := csv.NewReader(f.get("/contests/" + contestID.String() + "/play/log.csv").Body).ReadAll()
	if err != nil {
		t.Fatalf("the body is not CSV: %v", err)
	}
	if len(whole) != 2 {
		t.Fatalf("a complete file has %d lines, want a header and one row: %v", len(whole), whole)
	}
}

// The read holds a core-pool connection for as long as the client reads, so the
// handler sets its own deadline; the request context has none without a server
// WriteTimeout.
func TestAQueryLogDownloadHoldsItsConnectionForABoundedTime(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}

	before := time.Now()
	if rec := f.get("/contests/" + contestID.String() + "/play/log.csv"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if f.history.gotExportDeadline.IsZero() {
		t.Fatal("the read was given no deadline; a client reading a byte a second holds a pool connection for as long as it likes")
	}
	// The handler's figure is its own; this pins only that it is bounded. Ten
	// downloads held two minutes is a stall a contest recovers from.
	const tolerable = 2 * time.Minute
	if held := f.history.gotExportDeadline.Sub(before); held > tolerable {
		t.Fatalf("the read may hold its connection for %s, want at most %s", held, tolerable)
	}
}

// The rate budget allows thirty starts a minute and the core pool has ten
// connections, so slow-read downloads could hold every connection; one at a
// time per account closes that.
func TestASecondQueryLogDownloadWhileOneIsStillRunningIsRefused(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	held := make(chan struct{})
	f.history.exportGate = held

	started := make(chan struct{})
	first := make(chan int, 1)
	go func() {
		close(started)
		first <- f.get("/contests/" + contestID.String() + "/play/log.csv").Code
	}()
	<-started
	// So the second cannot pass merely because the first had not started.
	f.history.awaitInsideExport(t)

	second := f.get("/contests/" + contestID.String() + "/play/log.csv")
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("a second concurrent download answered %d, want 429: %s", second.Code, second.Body.String())
	}

	close(held)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("the first download answered %d, want 200", code)
	}
	// And the slot is given back, or one download would be all an account
	// ever gets.
	f.history.exportGate = nil
	if again := f.get("/contests/" + contestID.String() + "/play/log.csv"); again.Code != http.StatusOK {
		t.Fatalf("a download after the first finished answered %d, want 200: %s", again.Code, again.Body.String())
	}
}

// The session is named by a hash of its token, never the token.
func TestAnAdmittedPlayRequestIsObserved(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := f.playContest(t)

	if rec := f.get("/contests/" + contestID.String() + "/play/story"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	visits := f.watcher.seen()
	if len(visits) != 1 {
		t.Fatalf("the request was observed %d times, want once", len(visits))
	}
	got := visits[0]
	if got.Contest != contestID || got.Registration != f.access.participant.ID {
		t.Fatalf("visit filed under %v/%v, want %v/%v", got.Contest, got.Registration, contestID, f.access.participant.ID)
	}
	if got.Session != monitor.SessionTag(f.cookie.Value) || got.Session == f.cookie.Value {
		t.Fatalf("session = %q, want the tag of the token", got.Session)
	}
	if got.UserAgent != fixtureUserAgent || !got.Address.IsValid() {
		t.Fatalf("visit = %+v, want the request's browser and address", got)
	}
}

func TestARefusedPlayRequestIsNotObserved(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := f.playContest(t)
	f.access.err = contests.ErrContestNotRunning

	if rec := f.get("/contests/" + contestID.String() + "/play/story"); rec.Code == http.StatusOK {
		t.Fatalf("status = %d, want a refusal", rec.Code)
	}
	if visits := f.watcher.seen(); len(visits) != 0 {
		t.Fatalf("a refused request was observed: %+v", visits)
	}
}

// A spreadsheet evaluates a cell starting with =, +, - or @, and
// `=cmd|' /c calc'!A1` is valid SQL. The organiser's export defuses this too.
func TestTheQueryLogCSVDefusesSpreadsheetFormulas(t *testing.T) {
	f := newParticipantFixture(t)
	contestID := uuid.New()
	f.access.contest = contests.Contest{ID: contestID, Status: contests.StatusRunning}
	f.access.participant = contests.Participant{ID: uuid.New()}
	f.history.exported = []queryrunner.HistoryEntry{{
		SQL:        `=1+1`,
		Status:     queryrunner.StatusRejected,
		Error:      `@SUM(1)`,
		ExecutedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
	}}

	rec := f.get("/contests/" + contestID.String() + "/play/log.csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatalf("the body is not CSV: %v", err)
	}
	row := records[len(records)-1]
	for _, cell := range row {
		if cell != "" && strings.ContainsRune("=+-@", rune(cell[0])) {
			t.Errorf("cell %q is read as a formula by a spreadsheet; it needs the leading apostrophe", cell)
		}
	}
}
