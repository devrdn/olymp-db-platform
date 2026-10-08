package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/google/uuid"
)

// signalStore records the batches of browser signals the handler stored.
type signalStore struct {
	mu      sync.Mutex
	batches [][]monitor.Event
	fail    error
}

func (s *signalStore) InsertEvents(_ context.Context, events []monitor.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.batches = append(s.batches, append([]monitor.Event(nil), events...))
	return nil
}

func (s *signalStore) stored() []monitor.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []monitor.Event
	for _, batch := range s.batches {
		all = append(all, batch...)
	}
	return all
}

// signalsBody wraps events into the request body.
func signalsBody(events ...string) string {
	return `{"events":[` + strings.Join(events, ",") + `]}`
}

const (
	goodAbsence = `{"kind":"page_left","away_ms":4000}`
	goodPaste   = `{"kind":"paste","target":"editor","chars":8,"text":"SELECT 1"}`
)

func TestSignalsAreStoredForTheAdmittedRegistration(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)
	claimed := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)

	rec := f.send(http.MethodPost, play+"/signals", signalsBody(
		`{"kind":"page_left","away_ms":4000,"client_at":"`+claimed.Format(time.RFC3339Nano)+`"}`,
		goodPaste,
	))
	expectStatus(t, rec, http.StatusNoContent, "")
	if got := rec.Header().Get("X-Signals-Kept"); got != "2" {
		t.Fatalf("X-Signals-Kept = %q, want 2", got)
	}

	stored := f.signalStore.stored()
	if len(f.signalStore.batches) != 1 || len(stored) != 2 {
		t.Fatalf("stored %d events in %d batches, want 2 in one", len(stored), len(f.signalStore.batches))
	}
	for _, e := range stored {
		if e.Contest != f.access.contest.ID || e.Registration != f.access.participant.ID {
			t.Fatalf("event %+v is not the admitted registration's", e)
		}
	}
	if got := stored[0].Payload.(monitor.PageLeft); got.AwayMs != 4000 {
		t.Fatalf("page_left = %+v", got)
	}
	if stored[0].ClientAt == nil || !stored[0].ClientAt.Equal(claimed) {
		t.Fatalf("client_at = %v, want %v", stored[0].ClientAt, claimed)
	}
	if got := stored[1].Payload.(monitor.Paste); got != (monitor.Paste{Target: monitor.PasteEditor, Chars: 8, Text: "SELECT 1"}) {
		t.Fatalf("paste = %+v", got)
	}
	// A signal is a participant request like any other /play one.
	if len(f.watcher.visits) != 1 {
		t.Fatalf("the watcher heard of %d visits, want 1", len(f.watcher.visits))
	}
	// Posting signals is not reading the contest, and not a read either.
	if len(f.access.startedOnRead) != 0 || f.access.admitReads != 0 {
		t.Fatalf("signals started a clock (%v) or spent the read budget (%d)", f.access.startedOnRead, f.access.admitReads)
	}
}

// A paste is recorded whatever it held. Text with a NUL character cannot be
// stored as it is, but refusing the batch for it would let a participant hide
// a paste by pasting one, so the character is dropped (monitor.Paste) and the
// paste kept — the events travel as raw JSON past the body's own NUL check.
func TestAPasteHoldingANULIsKeptNotRefused(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)

	rec := f.send(http.MethodPost, play+"/signals", signalsBody(
		`{"kind":"paste","target":"editor","chars":5,"text":"SEL\u0000T"}`,
	))
	expectStatus(t, rec, http.StatusNoContent, "")

	stored := f.signalStore.stored()
	if len(stored) != 1 {
		t.Fatalf("stored %d events, want the paste", len(stored))
	}
	if got := stored[0].Payload.(monitor.Paste).Text; got != "SELT" {
		t.Fatalf("paste text = %q, want the NUL dropped", got)
	}
}

// One bad signal must not cost the browser the good ones: every per-event
// problem is a drop, and the batch still answers 204.
func TestBadSignalsAreDroppedNotRefused(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)

	rec := f.send(http.MethodPost, play+"/signals", signalsBody(
		goodAbsence,
		`{"kind":"page_left","away_ms":999}`, // shorter than a second
		`{"kind":"paste","target":"console","chars":1,"text":"x"}`,           // unknown target
		`{"kind":"page_left","away_ms":"long"}`,                              // wrong type
		`{"kind":"launch_missiles"}`,                                         // unknown kind
		`{"kind":"ip_changed","from":"192.0.2.1","to":"192.0.2.2"}`,          // a server kind
		`{"kind":"tab_deleted","tab_id":"`+uuid.NewString()+`","title":"x"}`, // a server kind
		`"not an object"`,
		goodPaste,
	))
	expectStatus(t, rec, http.StatusNoContent, "")
	if got := rec.Header().Get("X-Signals-Kept"); got != "2" {
		t.Fatalf("X-Signals-Kept = %q, want 2", got)
	}
	stored := f.signalStore.stored()
	if len(stored) != 2 || stored[0].Kind() != monitor.KindPageLeft || stored[1].Kind() != monitor.KindPaste {
		t.Fatalf("stored %v, want the two good signals", kindsOf(stored))
	}
}

func TestABatchWithNothingToKeepStoresNothing(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)

	for _, body := range []string{signalsBody(), signalsBody(`{"kind":"page_left","away_ms":10}`)} {
		rec := f.send(http.MethodPost, play+"/signals", body)
		expectStatus(t, rec, http.StatusNoContent, "")
		if got := rec.Header().Get("X-Signals-Kept"); got != "0" {
			t.Fatalf("X-Signals-Kept = %q, want 0", got)
		}
	}
	if len(f.signalStore.batches) != 0 {
		t.Fatalf("%d batches were stored", len(f.signalStore.batches))
	}
}

func TestSignalFieldsAreBounded(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)
	long := strings.Repeat("я", monitor.MaxPasteTextRunes+100)

	rec := f.send(http.MethodPost, play+"/signals", signalsBody(
		`{"kind":"page_left","away_ms":`+fmt.Sprint((48*time.Hour).Milliseconds())+`}`,
		`{"kind":"paste","target":"notes","chars":600,"text":"`+long+`"}`,
	))
	expectStatus(t, rec, http.StatusNoContent, "")
	stored := f.signalStore.stored()
	if got := stored[0].Payload.(monitor.PageLeft).AwayMs; got != monitor.MaxAway.Milliseconds() {
		t.Fatalf("away_ms = %d, want capped at a day", got)
	}
	if got := len([]rune(stored[1].Payload.(monitor.Paste).Text)); got != monitor.MaxPasteTextRunes {
		t.Fatalf("paste text = %d characters, want %d", got, monitor.MaxPasteTextRunes)
	}
}

// The server stamps its own time; the browser's is kept as a claim only when
// it is within a day of the server's, and a claim that is not a time at all
// is ignored rather than costing the event.
func TestAClaimedTimeIsKeptOnlyWithinADay(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)
	now := time.Now().UTC()
	at := func(claim string) string {
		return `{"kind":"page_left","away_ms":2000,"client_at":"` + claim + `"}`
	}

	rec := f.send(http.MethodPost, play+"/signals", signalsBody(
		at(now.Add(-2*time.Hour).Format(time.RFC3339Nano)),
		at(now.Add(-25*time.Hour).Format(time.RFC3339Nano)),
		at(now.Add(25*time.Hour).Format(time.RFC3339Nano)),
		at("yesterday-ish"),
		goodAbsence,
	))
	expectStatus(t, rec, http.StatusNoContent, "")
	stored := f.signalStore.stored()
	if len(stored) != 5 {
		t.Fatalf("stored %d events, want 5", len(stored))
	}
	if stored[0].ClientAt == nil {
		t.Fatal("a claim two hours back was lost")
	}
	for i, e := range stored[1:] {
		if e.ClientAt != nil {
			t.Fatalf("event %d kept the claim %v", i+1, e.ClientAt)
		}
	}
}

func TestABatchOverFiftyEventsIsRefused(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)

	events := make([]string, monitor.MaxBatchEvents+1)
	for i := range events {
		events[i] = goodAbsence
	}
	rec := f.send(http.MethodPost, play+"/signals", signalsBody(events...))
	expectStatus(t, rec, http.StatusBadRequest, "signals_batch_too_large")
	if len(f.signalStore.batches) != 0 {
		t.Fatal("an oversized batch was stored")
	}
	// Counted before any per-event work or lookup.
	if f.access.accessCalled {
		t.Fatal("Access ran for a batch over the limit")
	}

	rec = f.send(http.MethodPost, play+"/signals", signalsBody(events[:monitor.MaxBatchEvents]...))
	expectStatus(t, rec, http.StatusNoContent, "")
}

// The body is bounded as the bytes arrive (CLAUDE.md rule 12): a huge array
// is refused before it is decoded, not counted after.
func TestAnOversizedBodyIsRefusedBeforeItIsDecoded(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)

	huge := signalsBody(`{"kind":"paste","target":"notes","chars":1,"text":"` + strings.Repeat("x", 300<<10) + `"}`)
	expectStatus(t, f.send(http.MethodPost, play+"/signals", huge), http.StatusBadRequest, "signals_batch_too_large")
	if f.access.accessCalled {
		t.Fatal("Access ran for a body refused for its size")
	}
}

func TestAMalformedSignalsBodyIsRefused(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)

	for _, body := range []string{``, `{`, `{}`, `{"events":null}`, `{"events":{}}`, `{"events":[],"extra":1}`} {
		expectStatus(t, f.send(http.MethodPost, play+"/signals", body), http.StatusBadRequest, "invalid_request")
	}
	expectStatus(t, f.send(http.MethodPost, "/contests/not-a-uuid/play/signals", signalsBody(goodAbsence)),
		http.StatusBadRequest, "invalid_contest_id")
}

// Signals are admitted exactly like the rest of /play.
func TestSignalsAreAdmittedLikeThePlayScreen(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{contests.ErrNotAParticipant, http.StatusForbidden, "not_a_participant"},
		{contests.ErrContestNotRunning, http.StatusConflict, "contest_not_running"},
		{contests.ErrContestEnded, http.StatusConflict, "contest_ended"},
		{contests.ErrParticipantFinished, http.StatusConflict, "contest_finished"},
		{contests.ErrDeadlinePassed, http.StatusConflict, "deadline_passed"},
		{contests.ErrAddressNotAllowed, http.StatusForbidden, "address_not_allowed"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			f := newParticipantFixture(t)
			play := f.workspaceContest(t)
			f.access.err = tc.err
			expectStatus(t, f.send(http.MethodPost, play+"/signals", signalsBody(goodAbsence)), tc.status, tc.code)
			if len(f.signalStore.batches) != 0 {
				t.Fatal("a refused participant's signals were stored")
			}
		})
	}
}

func TestSignalsNeedASignedInCaller(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)
	f.cookie.Value = "not-a-session"
	expectStatus(t, f.send(http.MethodPost, play+"/signals", signalsBody(goodAbsence)), http.StatusUnauthorized, "")
}

// Twelve batches a minute per account, refused ones included, checked before
// anything is looked up (CLAUDE.md rule 13).
func TestAnAccountSendsAtMostTwelveSignalBatchesAMinute(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)

	for i := 0; i < monitor.BatchesPerMinute; i++ {
		// Refused batches spend the budget too: every other one is malformed.
		body := signalsBody(goodAbsence)
		if i%2 == 1 {
			body = `{}`
		}
		if rec := f.send(http.MethodPost, play+"/signals", body); rec.Code == http.StatusTooManyRequests {
			t.Fatalf("batch #%d refused for its rate", i+1)
		}
	}

	f.access.accessCalled = false
	rec := f.send(http.MethodPost, play+"/signals", signalsBody(goodAbsence))
	expectStatus(t, rec, http.StatusTooManyRequests, "signals_too_often")
	if rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("Retry-After = %q, want 60", rec.Header().Get("Retry-After"))
	}
	if f.access.accessCalled {
		t.Fatal("Access ran for a batch refused for its rate")
	}
	// Its own budget: the workspace's and the read budget are untouched.
	if f.access.admitReads != 0 {
		t.Fatalf("signals spent the read budget %d times", f.access.admitReads)
	}
	expectStatus(t, f.send(http.MethodPut, play+"/notes", `{"body":"x"}`), http.StatusOK, "")
}

func TestAFailedSignalInsertIsAnInternalError(t *testing.T) {
	f := newParticipantFixture(t)
	play := f.workspaceContest(t)
	f.signalStore.fail = errors.New("database down")
	expectStatus(t, f.send(http.MethodPost, play+"/signals", signalsBody(goodAbsence)), http.StatusInternalServerError, "internal_error")
}

func kindsOf(events []monitor.Event) []monitor.Kind {
	out := make([]monitor.Kind, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind())
	}
	return out
}
