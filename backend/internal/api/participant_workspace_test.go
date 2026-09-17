package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/workspace"
	"github.com/devrdn/db-contest/backend/internal/workspace/workspacetest"
	"github.com/google/uuid"
)

// send makes a request with a JSON body (or none, for an empty body).
func (f *participantFixture) send(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

type workspaceTabBody struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Position  int    `json:"position"`
	UpdatedAt string `json:"updated_at"`
}

type workspaceBody struct {
	Notes struct {
		Body      string  `json:"body"`
		UpdatedAt *string `json:"updated_at"`
	} `json:"notes"`
	Tabs []workspaceTabBody `json:"tabs"`
}

type updatedBody struct {
	UpdatedAt string `json:"updated_at"`
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

func expectStatus(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, status, rec.Body.String())
	}
	if code != "" {
		if got := errorCode(t, rec); got != code {
			t.Fatalf("code = %q, want %q", got, code)
		}
	}
}

// workspaceContest stages a running contest in English and Russian and
// returns the /play prefix for it.
func (f *participantFixture) workspaceContest(t *testing.T) string {
	t.Helper()
	contestID := uuid.New()
	f.access.contest = contests.Contest{
		ID: contestID, Status: contests.StatusRunning, Timing: contests.TimingIndividual,
		Languages: []contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ru"}},
	}
	f.access.participant = contests.Participant{ID: uuid.New()}
	return "/contests/" + contestID.String() + "/play"
}

// loadWorkspace reads the workspace and returns it.
func (f *participantFixture) loadWorkspace(t *testing.T, play string) workspaceBody {
	t.Helper()
	rec := f.get(play + "/workspace")
	expectStatus(t, rec, http.StatusOK, "")
	return decodeBody[workspaceBody](t, rec)
}

func TestTheWorkspaceStartsWithOneTabNamedInTheRequestsLanguage(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)

	rec := f.get(play + "/workspace?lang=ru")
	expectStatus(t, rec, http.StatusOK, "")
	got := decodeBody[workspaceBody](t, rec)

	if len(got.Tabs) != 1 || got.Tabs[0].Title != "Запрос 1" || got.Tabs[0].Position != 0 {
		t.Fatalf("tabs = %+v, want one tab titled Запрос 1", got.Tabs)
	}
	if _, err := uuid.Parse(got.Tabs[0].ID); err != nil || got.Tabs[0].UpdatedAt == "" {
		t.Fatalf("tab = %+v, want an id and a time", got.Tabs[0])
	}
	if got.Notes.Body != "" || got.Notes.UpdatedAt != nil {
		t.Fatalf("notes = %+v, want empty and never saved", got.Notes)
	}
	// The workspace closes with the contest (admit refuses it), so there is
	// no read-only mode to report.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := raw["read_only"]; present {
		t.Fatalf("the response carries read_only: %s", rec.Body.String())
	}
}

// Reading the workspace is not reading the contest: it starts no clock, and
// it is charged to the read budget every participant read spends.
func TestReadingTheWorkspaceStartsNoClockAndSpendsTheReadBudget(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)
	f.loadWorkspace(t, play)

	if len(f.access.startedOnRead) != 0 {
		t.Fatalf("StartOnRead was called for %v", f.access.startedOnRead)
	}
	if f.access.admitReads != 1 {
		t.Fatalf("AdmitRead called %d times, want 1", f.access.admitReads)
	}

	f.access.admitReadErr = queryrunner.ErrTooManyQueries
	f.access.accessCalled = false
	expectStatus(t, f.get(play+"/workspace"), http.StatusTooManyRequests, "query_too_often")
	if f.access.accessCalled {
		t.Fatal("Access ran after the read budget refused")
	}
}

func TestNotesAreSavedAndReadBack(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)

	rec := f.send(http.MethodPut, play+"/notes", `{"body":"the butler had a key"}`)
	expectStatus(t, rec, http.StatusOK, "")
	if decodeBody[updatedBody](t, rec).UpdatedAt == "" {
		t.Fatalf("no updated_at in %s", rec.Body.String())
	}

	got := f.loadWorkspace(t, play)
	if got.Notes.Body != "the butler had a key" || got.Notes.UpdatedAt == nil {
		t.Fatalf("notes = %+v", got.Notes)
	}
}

func TestTabsAreCreatedRenamedEditedReorderedAndDeleted(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)
	first := f.loadWorkspace(t, play).Tabs[0]

	rec := f.send(http.MethodPost, play+"/tabs", `{}`)
	expectStatus(t, rec, http.StatusCreated, "")
	second := decodeBody[workspaceTabBody](t, rec)
	if second.Title != "Query 2" || second.Position != 1 {
		t.Fatalf("created %+v, want Query 2 at 1", second)
	}

	rec = f.send(http.MethodPost, play+"/tabs", `{"title":"  joins "}`)
	expectStatus(t, rec, http.StatusCreated, "")
	third := decodeBody[workspaceTabBody](t, rec)
	if third.Title != "joins" {
		t.Fatalf("created %+v, want joins", third)
	}

	rec = f.send(http.MethodPatch, play+"/tabs/"+second.ID, `{"title":"suspects","body":"SELECT 1"}`)
	expectStatus(t, rec, http.StatusOK, "")
	if decodeBody[updatedBody](t, rec).UpdatedAt == "" {
		t.Fatalf("no updated_at in %s", rec.Body.String())
	}

	rec = f.send(http.MethodPut, play+"/tabs/order", `{"ids":["`+third.ID+`","`+second.ID+`","`+first.ID+`"]}`)
	expectStatus(t, rec, http.StatusNoContent, "")

	rec = f.send(http.MethodDelete, play+"/tabs/"+first.ID, "")
	expectStatus(t, rec, http.StatusNoContent, "")

	got := f.loadWorkspace(t, play).Tabs
	if len(got) != 2 || got[0].ID != third.ID || got[1].ID != second.ID ||
		got[1].Title != "suspects" || got[1].Body != "SELECT 1" {
		t.Fatalf("tabs = %+v", got)
	}
}

// CLAUDE.md rule 1: each workspace refusal is its own code and status.
func TestWorkspaceRefusalsBecomeTheDocumentedStatusAndCode(t *testing.T) {
	longNotes := `{"body":"` + strings.Repeat("a", workspace.MaxNotesRunes+1) + `"}`
	longBody := `{"body":"` + strings.Repeat("a", sqlpolicy.MaxQueryBytes+1) + `"}`

	for _, tc := range []struct {
		name   string
		method string
		// path is relative to /play; {tab} is the workspace's first tab.
		path   string
		body   string
		status int
		code   string
	}{
		{"notes too long", http.MethodPut, "/notes", longNotes, http.StatusBadRequest, "workspace_notes_too_long"},
		{"notes with a NUL", http.MethodPut, "/notes", `{"body":"a\u0000b"}`, http.StatusBadRequest, "workspace_text_invalid"},
		{"notes without a body", http.MethodPut, "/notes", `{}`, http.StatusBadRequest, "invalid_request"},
		{"notes not JSON", http.MethodPut, "/notes", `body`, http.StatusBadRequest, "invalid_request"},
		{"tab body too long", http.MethodPatch, "/tabs/{tab}", longBody, http.StatusBadRequest, "workspace_tab_too_long"},
		{"blank title", http.MethodPatch, "/tabs/{tab}", `{"title":"  "}`, http.StatusBadRequest, "workspace_title_invalid"},
		{"title too long", http.MethodPost, "/tabs", `{"title":"` + strings.Repeat("a", workspace.MaxTitleRunes+1) + `"}`, http.StatusBadRequest, "workspace_title_invalid"},
		{"title with a newline", http.MethodPost, "/tabs", `{"title":"a\nb"}`, http.StatusBadRequest, "workspace_title_invalid"},
		{"empty change", http.MethodPatch, "/tabs/{tab}", `{}`, http.StatusBadRequest, "invalid_request"},
		{"unknown tab on update", http.MethodPatch, "/tabs/" + uuid.NewString(), `{"body":"x"}`, http.StatusNotFound, "workspace_tab_not_found"},
		{"unknown tab on delete", http.MethodDelete, "/tabs/" + uuid.NewString(), ``, http.StatusNotFound, "workspace_tab_not_found"},
		{"bad tab id on update", http.MethodPatch, "/tabs/not-a-uuid", `{"body":"x"}`, http.StatusBadRequest, "invalid_tab_id"},
		{"bad tab id on delete", http.MethodDelete, "/tabs/not-a-uuid", ``, http.StatusBadRequest, "invalid_tab_id"},
		{"bad tab id in an order", http.MethodPut, "/tabs/order", `{"ids":["not-a-uuid"]}`, http.StatusBadRequest, "invalid_tab_id"},
		{"order missing a tab", http.MethodPut, "/tabs/order", `{"ids":[]}`, http.StatusBadRequest, "workspace_order_mismatch"},
		{"order with a stranger", http.MethodPut, "/tabs/order", `{"ids":["` + uuid.NewString() + `"]}`, http.StatusBadRequest, "workspace_order_mismatch"},
		{"order with a duplicate", http.MethodPut, "/tabs/order", `{"ids":["{tab}","{tab}"]}`, http.StatusBadRequest, "workspace_order_mismatch"},
		{"last tab", http.MethodDelete, "/tabs/{tab}", ``, http.StatusConflict, "workspace_last_tab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newParticipantFixture(t)
			play := f.workspaceContest(t)
			tab := f.loadWorkspace(t, play).Tabs[0]

			path := strings.ReplaceAll(tc.path, "{tab}", tab.ID)
			body := strings.ReplaceAll(tc.body, "{tab}", tab.ID)
			expectStatus(t, f.send(tc.method, play+path, body), tc.status, tc.code)
		})
	}
}

func TestTheEleventhTabIsA409(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)
	f.loadWorkspace(t, play)
	for i := 1; i < workspace.MaxTabs; i++ {
		expectStatus(t, f.send(http.MethodPost, play+"/tabs", `{}`), http.StatusCreated, "")
	}
	expectStatus(t, f.send(http.MethodPost, play+"/tabs", `{}`), http.StatusConflict, "workspace_tab_limit")
}

// Writes have a budget of their own, spent before anything is looked up, and
// never the read budget the SQL console shares: autosave must not take a
// participant's queries away from them.
func TestWritesPastTheirRateAreA429BeforeAnyLookup(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)

	for i := 0; i < workspace.WritesPerMinute; i++ {
		// Refused writes spend the budget too: every other one names no body.
		body := `{"body":"x"}`
		if i%2 == 1 {
			body = `{}`
		}
		if rec := f.send(http.MethodPut, play+"/notes", body); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("write #%d refused for its rate", i+1)
		}
	}
	if f.access.admitReads != 0 {
		t.Fatalf("writes spent the read budget %d times", f.access.admitReads)
	}

	f.access.accessCalled = false
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPut, "/notes", `{"body":"x"}`},
		{http.MethodPost, "/tabs", `{}`},
		{http.MethodPatch, "/tabs/" + uuid.NewString(), `{"body":"x"}`},
		{http.MethodDelete, "/tabs/" + uuid.NewString(), ``},
		{http.MethodPut, "/tabs/order", `{"ids":[]}`},
	} {
		rec := f.send(tc.method, play+tc.path, tc.body)
		expectStatus(t, rec, http.StatusTooManyRequests, "workspace_too_often")
		if rec.Header().Get("Retry-After") != "60" {
			t.Fatalf("%s %s: Retry-After = %q, want 60", tc.method, tc.path, rec.Header().Get("Retry-After"))
		}
	}
	if f.access.accessCalled {
		t.Fatal("Access ran for a write refused for its rate")
	}
}

// A write never starts the participant's clock: only reading the contest does.
func TestAWriteStartsNoClock(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)
	expectStatus(t, f.send(http.MethodPut, play+"/notes", `{"body":"x"}`), http.StatusOK, "")
	expectStatus(t, f.send(http.MethodPost, play+"/tabs", `{}`), http.StatusCreated, "")
	if len(f.access.startedOnRead) != 0 {
		t.Fatalf("StartOnRead was called for %v", f.access.startedOnRead)
	}
}

// The workspace is admitted exactly as the rest of /play is: once the
// contest has ended for the participant, every workspace route — the read and
// each write — answers as /play/story does.
func TestWorkspaceAccessRefusalsAreTheParticipantRoutesOwn(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{queryproxy.ErrNotAParticipant, http.StatusForbidden, "not_a_participant"},
		{queryproxy.ErrContestNotRunning, http.StatusConflict, "contest_not_running"},
		{queryproxy.ErrFinished, http.StatusConflict, "contest_finished"},
		{queryproxy.ErrAddressNotAllowed, http.StatusForbidden, "address_not_allowed"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			f := newParticipantFixture(t)
			play := f.workspaceContest(t)
			tab := f.loadWorkspace(t, play).Tabs[0]
			f.access.err = tc.err
			before := f.workspaceStore.Calls()

			for _, route := range []struct{ method, path, body string }{
				{http.MethodGet, "/story", ""},
				{http.MethodGet, "/workspace", ""},
				{http.MethodPut, "/notes", `{"body":"x"}`},
				{http.MethodPost, "/tabs", `{}`},
				{http.MethodPatch, "/tabs/" + tab.ID, `{"body":"x"}`},
				{http.MethodDelete, "/tabs/" + tab.ID, ""},
				{http.MethodPut, "/tabs/order", `{"ids":["` + tab.ID + `"]}`},
			} {
				rec := f.send(route.method, play+route.path, route.body)
				if rec.Code != tc.status || errorCode(t, rec) != tc.code {
					t.Fatalf("%s %s: %d %s, want %d %s", route.method, route.path, rec.Code, rec.Body.String(), tc.status, tc.code)
				}
			}
			if f.workspaceStore.Calls() != before {
				t.Fatal("a refused participant reached the workspace store")
			}
		})
	}

	f := newParticipantFixture(t)
	expectStatus(t, f.get("/contests/not-a-uuid/play/workspace"), http.StatusBadRequest, "invalid_contest_id")
	expectStatus(t, f.send(http.MethodPut, "/contests/not-a-uuid/play/notes", `{"body":"x"}`), http.StatusBadRequest, "invalid_contest_id")
}

// failingWorkspace is the in-memory store, failing every call once err is
// set.
type failingWorkspace struct {
	*workspacetest.Repository
	err error
}

func (s *failingWorkspace) Load(ctx context.Context, registration uuid.UUID, firstTitle string) (workspace.Notes, []workspace.Tab, error) {
	if s.err != nil {
		return workspace.Notes{}, nil, s.err
	}
	return s.Repository.Load(ctx, registration, firstTitle)
}

func (s *failingWorkspace) SaveNotes(ctx context.Context, registration uuid.UUID, body string) (time.Time, error) {
	if s.err != nil {
		return time.Time{}, s.err
	}
	return s.Repository.SaveNotes(ctx, registration, body)
}

// A store that fails is an internal error, never one of the caller's codes.
func TestAWorkspaceStorageFailureIsA500(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)
	f.workspaceStore.err = errors.New("database is down")

	expectStatus(t, f.get(play+"/workspace"), http.StatusInternalServerError, "internal_error")
	expectStatus(t, f.send(http.MethodPut, play+"/notes", `{"body":"x"}`), http.StatusInternalServerError, "internal_error")
}
