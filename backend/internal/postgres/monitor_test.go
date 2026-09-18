package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

// monitorFixture is one participant of one contest.
type monitorFixture struct {
	contest, registration uuid.UUID
}

func newMonitorFixture(t *testing.T, ctx context.Context) monitorFixture {
	t.Helper()
	user := makeUser(t, ctx, "monitor-"+uuid.NewString()[:8])
	contest := makeContest(t, ctx, user.ID)
	return monitorFixture{contest: contest, registration: makeRegistration(t, ctx, contest, user.ID)}
}

func (f monitorFixture) event(payload monitor.Payload) monitor.Event {
	return monitor.Event{Contest: f.contest, Registration: f.registration, Payload: payload}
}

type storedEvent struct {
	contest  uuid.UUID
	kind     string
	payload  map[string]any
	clientAt *time.Time
}

func storedEvents(t *testing.T, ctx context.Context, registration uuid.UUID) []storedEvent {
	t.Helper()
	rows, err := storage.QuerierFrom(ctx, testPool).Query(ctx, `
		SELECT contest_id, kind, payload, client_at FROM participant_events
		WHERE registration_id = $1 ORDER BY id`, registration)
	if err != nil {
		t.Fatalf("read the events: %v", err)
	}
	defer rows.Close()
	var found []storedEvent
	for rows.Next() {
		var e storedEvent
		var payload []byte
		if err := rows.Scan(&e.contest, &e.kind, &payload, &e.clientAt); err != nil {
			t.Fatalf("scan an event: %v", err)
		}
		if err := json.Unmarshal(payload, &e.payload); err != nil {
			t.Fatalf("payload %s: %v", payload, err)
		}
		found = append(found, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the events: %v", err)
	}
	return found
}

func TestMonitorStoresABatchOfEventsInOrder(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		store := NewMonitor(testPool)
		f := newMonitorFixture(t, ctx)
		claimed := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

		left := f.event(monitor.PageLeft{AwayMs: 4200})
		left.ClientAt = &claimed
		batch := []monitor.Event{
			left,
			f.event(monitor.Paste{Target: monitor.PasteEditor, Chars: 700, Text: strings.Repeat("x", 700)}),
			f.event(monitor.IPChanged{From: netip.MustParseAddr("192.0.2.1"), To: netip.MustParseAddr("2001:db8::1")}),
		}
		if err := store.InsertEvents(ctx, batch); err != nil {
			t.Fatalf("InsertEvents() = %v", err)
		}

		got := storedEvents(t, ctx, f.registration)
		if len(got) != 3 {
			t.Fatalf("stored %d events, want 3", len(got))
		}
		for _, e := range got {
			if e.contest != f.contest {
				t.Fatalf("an event was stored against contest %s, want %s", e.contest, f.contest)
			}
		}
		if got[0].kind != "page_left" || got[0].payload["away_ms"] != float64(4200) {
			t.Fatalf("first event = %+v", got[0])
		}
		if got[0].clientAt == nil || !got[0].clientAt.Equal(claimed) {
			t.Fatalf("the browser's claimed time = %v, want %v", got[0].clientAt, claimed)
		}
		// Normalised on the way in, even by a caller that forgot to.
		if text, _ := got[1].payload["text"].(string); got[1].kind != "paste" || utf8.RuneCountInString(text) != monitor.MaxPasteTextRunes {
			t.Fatalf("second event = %s with %d characters of text", got[1].kind, utf8.RuneCountInString(text))
		}
		if got[2].kind != "ip_changed" || got[2].payload["from"] != "192.0.2.1" || got[2].payload["to"] != "2001:db8::1" {
			t.Fatalf("third event = %+v", got[2])
		}
		if got[2].clientAt != nil {
			t.Fatalf("a server event has a browser time: %v", got[2].clientAt)
		}
	})
}

func TestMonitorRefusesABatchItCannotStoreWhole(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		store := NewMonitor(testPool)
		f := newMonitorFixture(t, ctx)

		tooMany := make([]monitor.Event, monitor.MaxBatchEvents+1)
		for i := range tooMany {
			tooMany[i] = f.event(monitor.PageLeft{AwayMs: 5000})
		}
		if err := store.InsertEvents(ctx, tooMany); !errors.Is(err, monitor.ErrBatchTooLarge) {
			t.Fatalf("a batch of %d: err = %v, want ErrBatchTooLarge", len(tooMany), err)
		}

		invalid := []monitor.Event{f.event(monitor.PageLeft{AwayMs: 5000}), f.event(monitor.Paste{Target: "console"})}
		if err := store.InsertEvents(ctx, invalid); !errors.Is(err, monitor.ErrPasteTarget) {
			t.Fatalf("a batch with an invalid event: err = %v, want ErrPasteTarget", err)
		}
		if got := storedEvents(t, ctx, f.registration); len(got) != 0 {
			t.Fatalf("a refused batch stored %d events", len(got))
		}

		if err := store.InsertEvents(ctx, nil); err != nil {
			t.Fatalf("an empty batch: %v", err)
		}
	})
}

type storedRevision struct {
	id                   int64
	title                *string
	body                 string
	startedAt, updatedAt time.Time
}

func storedRevisions(t *testing.T, ctx context.Context, registration uuid.UUID, document string) []storedRevision {
	t.Helper()
	rows, err := storage.QuerierFrom(ctx, testPool).Query(ctx, `
		SELECT id, title, body, started_at, updated_at FROM workspace_revisions
		WHERE registration_id = $1 AND document = $2 ORDER BY id`, registration, document)
	if err != nil {
		t.Fatalf("read the revisions: %v", err)
	}
	defer rows.Close()
	var found []storedRevision
	for rows.Next() {
		var r storedRevision
		if err := rows.Scan(&r.id, &r.title, &r.body, &r.startedAt, &r.updatedAt); err != nil {
			t.Fatalf("scan a revision: %v", err)
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the revisions: %v", err)
	}
	return found
}

func TestARevisionYoungerThanThirtySecondsIsRewrittenInPlace(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		store := NewMonitor(testPool)
		f := newMonitorFixture(t, ctx)
		start := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
		save := func(body string, at time.Time) {
			t.Helper()
			if err := store.RecordRevision(ctx, monitor.Revision{
				Registration: f.registration, Document: monitor.DocumentNotes, Body: body, At: at,
			}); err != nil {
				t.Fatalf("RecordRevision(%q) = %v", body, err)
			}
		}

		save("a", start)
		save("ab", start.Add(29*time.Second+999*time.Millisecond))
		got := storedRevisions(t, ctx, f.registration, monitor.DocumentNotes)
		if len(got) != 1 {
			t.Fatalf("a save within 30 s of the revision's start made %d revisions, want 1", len(got))
		}
		if got[0].body != "ab" || !got[0].startedAt.Equal(start) || !got[0].updatedAt.Equal(start.Add(29*time.Second+999*time.Millisecond)) {
			t.Fatalf("the revision after the second save = %+v", got[0])
		}
		if got[0].title != nil {
			t.Fatalf("the notes' revision has a title: %q", *got[0].title)
		}

		// Exactly thirty seconds after the revision began: a new one.
		save("abc", start.Add(30*time.Second))
		got = storedRevisions(t, ctx, f.registration, monitor.DocumentNotes)
		if len(got) != 2 {
			t.Fatalf("a save 30 s after the revision's start made %d revisions, want 2", len(got))
		}
		if got[0].body != "ab" || got[1].body != "abc" || !got[1].startedAt.Equal(start.Add(30*time.Second)) {
			t.Fatalf("revisions = %+v", got)
		}
	})
}

func TestASaveOfTheSameBodyWritesNothing(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		store := NewMonitor(testPool)
		f := newMonitorFixture(t, ctx)
		tab := monitor.TabDocument(uuid.New())
		start := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

		first := monitor.Revision{Registration: f.registration, Document: tab, Title: "Query 1", Body: "SELECT 1", At: start}
		if err := store.RecordRevision(ctx, first); err != nil {
			t.Fatalf("first save: %v", err)
		}
		// Inside the window and outside it: neither moves anything.
		for _, later := range []time.Duration{5 * time.Second, 5 * time.Minute} {
			again := first
			again.At = start.Add(later)
			if err := store.RecordRevision(ctx, again); err != nil {
				t.Fatalf("same body %s later: %v", later, err)
			}
		}

		got := storedRevisions(t, ctx, f.registration, tab)
		if len(got) != 1 || !got[0].updatedAt.Equal(start) {
			t.Fatalf("revisions after saving the same body = %+v, want the first one untouched", got)
		}
		if got[0].title == nil || *got[0].title != "Query 1" {
			t.Fatalf("the tab's title = %v, want Query 1", got[0].title)
		}
	})
}

func TestEachDocumentHasItsOwnHistory(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		store := NewMonitor(testPool)
		f := newMonitorFixture(t, ctx)
		tab := monitor.TabDocument(uuid.New())
		at := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

		for _, rev := range []monitor.Revision{
			{Registration: f.registration, Document: monitor.DocumentNotes, Body: "notes", At: at},
			{Registration: f.registration, Document: tab, Title: "Query 1", Body: "SELECT 1", At: at.Add(time.Second)},
		} {
			if err := store.RecordRevision(ctx, rev); err != nil {
				t.Fatalf("RecordRevision(%s) = %v", rev.Document, err)
			}
		}

		if notes := storedRevisions(t, ctx, f.registration, monitor.DocumentNotes); len(notes) != 1 || notes[0].body != "notes" {
			t.Fatalf("the notes' history = %+v", notes)
		}
		if tabs := storedRevisions(t, ctx, f.registration, tab); len(tabs) != 1 || tabs[0].body != "SELECT 1" {
			t.Fatalf("the tab's history = %+v", tabs)
		}
	})
}

func TestMonitorRefusesAnInvalidRevision(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		store := NewMonitor(testPool)
		f := newMonitorFixture(t, ctx)
		err := store.RecordRevision(ctx, monitor.Revision{Registration: f.registration, Document: "scratchpad", At: time.Now()})
		if !errors.Is(err, monitor.ErrRevisionInvalid) {
			t.Fatalf("err = %v, want ErrRevisionInvalid", err)
		}
	})
}

// A participant's events and history go with their registration, and a
// contest's events with the contest.
func TestMonitoringGoesWithTheRegistrationAndTheContest(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		store := NewMonitor(testPool)
		f := newMonitorFixture(t, ctx)
		other := newMonitorFixture(t, ctx)
		at := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

		for _, g := range []monitorFixture{f, other} {
			if err := store.InsertEvents(ctx, []monitor.Event{g.event(monitor.PageLeft{AwayMs: 2000})}); err != nil {
				t.Fatalf("InsertEvents() = %v", err)
			}
			if err := store.RecordRevision(ctx, monitor.Revision{
				Registration: g.registration, Document: monitor.DocumentNotes, Body: "x", At: at,
			}); err != nil {
				t.Fatalf("RecordRevision() = %v", err)
			}
		}

		q := storage.QuerierFrom(ctx, testPool)
		if _, err := q.Exec(ctx, `DELETE FROM registrations WHERE id = $1`, f.registration); err != nil {
			t.Fatalf("delete the registration: %v", err)
		}
		if got := storedEvents(t, ctx, f.registration); len(got) != 0 {
			t.Fatalf("%d events outlived their registration", len(got))
		}
		if got := storedRevisions(t, ctx, f.registration, monitor.DocumentNotes); len(got) != 0 {
			t.Fatalf("%d revisions outlived their registration", len(got))
		}

		if _, err := q.Exec(ctx, `DELETE FROM contests WHERE id = $1`, other.contest); err != nil {
			t.Fatalf("delete the contest: %v", err)
		}
		var left int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM participant_events WHERE contest_id = $1`, other.contest).Scan(&left); err != nil {
			t.Fatalf("count: %v", err)
		}
		if left != 0 {
			t.Fatalf("%d events outlived their contest", left)
		}
		if got := storedRevisions(t, ctx, other.registration, monitor.DocumentNotes); len(got) != 0 {
			t.Fatalf("%d revisions outlived their contest", len(got))
		}
	})
}
