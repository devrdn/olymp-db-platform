package monitor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/google/uuid"
)

var signalsNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func kinds(events []monitor.Event) []monitor.Kind {
	out := make([]monitor.Kind, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind())
	}
	return out
}

// One bad signal must not cost the browser the good ones beside it: every
// per-event refusal is a drop, and only the batch's own size refuses it.
func TestCleanBatchDropsBadEventsAndKeepsTheRest(t *testing.T) {
	contest, registration := uuid.New(), uuid.New()
	at := func(p monitor.Payload) monitor.Event {
		return monitor.Event{Contest: contest, Registration: registration, Payload: p}
	}
	batch := []monitor.Event{
		at(monitor.PageLeft{AwayMs: 5000}),
		at(monitor.PageLeft{AwayMs: 999}),                           // shorter than a second
		at(monitor.Paste{Target: "console", Chars: 3, Text: "abc"}), // unknown target
		at(monitor.Paste{Target: monitor.PasteNotes, Chars: 3, Text: "abc"}),
		at(monitor.TabDeleted{TabID: uuid.New(), Title: "forged"}), // a server kind
		{Contest: contest, Registration: registration},             // no payload
		at(monitor.IPChanged{}),                                    // a server kind, invalid too
	}

	kept, dropped, err := monitor.CleanBatch(batch, signalsNow)
	if err != nil {
		t.Fatalf("CleanBatch() = %v", err)
	}
	if got := kinds(kept); len(got) != 2 || got[0] != monitor.KindPageLeft || got[1] != monitor.KindPaste {
		t.Fatalf("kept %v, want page_left then paste", got)
	}
	if dropped != 5 {
		t.Fatalf("dropped = %d, want 5", dropped)
	}
}

func TestCleanBatchBoundsTheKeptEvents(t *testing.T) {
	long := make([]rune, monitor.MaxPasteTextRunes+10)
	for i := range long {
		long[i] = 'x'
	}
	batch := []monitor.Event{
		event(monitor.PageLeft{AwayMs: (48 * time.Hour).Milliseconds()}),
		event(monitor.Paste{Target: monitor.PasteEditor, Chars: len(long), Text: string(long)}),
	}
	kept, _, err := monitor.CleanBatch(batch, signalsNow)
	if err != nil {
		t.Fatalf("CleanBatch() = %v", err)
	}
	if got := kept[0].Payload.(monitor.PageLeft).AwayMs; got != monitor.MaxAway.Milliseconds() {
		t.Fatalf("away_ms = %d, want capped at a day", got)
	}
	if got := []rune(kept[1].Payload.(monitor.Paste).Text); len(got) != monitor.MaxPasteTextRunes {
		t.Fatalf("paste text kept %d characters, want %d", len(got), monitor.MaxPasteTextRunes)
	}
}

// A claimed time more than a day away from the server's is not a clock, it
// is noise: the event stays, the claim goes.
func TestCleanBatchIgnoresAClaimedTimeFarFromTheServers(t *testing.T) {
	near := signalsNow.Add(-23 * time.Hour)
	past := signalsNow.Add(-25 * time.Hour)
	future := signalsNow.Add(25 * time.Hour)

	batch := make([]monitor.Event, 0, 3)
	for _, claimed := range []time.Time{near, past, future} {
		e := event(monitor.PageLeft{AwayMs: 2000})
		e.ClientAt = &claimed
		batch = append(batch, e)
	}
	kept, dropped, err := monitor.CleanBatch(batch, signalsNow)
	if err != nil || dropped != 0 || len(kept) != 3 {
		t.Fatalf("CleanBatch() = %d kept, %d dropped, %v; want 3, 0, nil", len(kept), dropped, err)
	}
	if kept[0].ClientAt == nil || !kept[0].ClientAt.Equal(near) {
		t.Fatalf("a claim within a day was lost: %v", kept[0].ClientAt)
	}
	if kept[1].ClientAt != nil || kept[2].ClientAt != nil {
		t.Fatalf("claims more than a day away were kept: %v, %v", kept[1].ClientAt, kept[2].ClientAt)
	}
}

func TestCleanBatchRefusesABatchOverTheLimitBeforeLookingAtIt(t *testing.T) {
	batch := make([]monitor.Event, monitor.MaxBatchEvents+1)
	if _, _, err := monitor.CleanBatch(batch, signalsNow); !errors.Is(err, monitor.ErrBatchTooLarge) {
		t.Fatalf("a batch of %d: err = %v, want ErrBatchTooLarge", len(batch), err)
	}
	// Exactly the limit is a batch, even when every event in it is dropped.
	kept, dropped, err := monitor.CleanBatch(batch[:monitor.MaxBatchEvents], signalsNow)
	if err != nil || len(kept) != 0 || dropped != monitor.MaxBatchEvents {
		t.Fatalf("a batch of the limit: %d kept, %d dropped, %v", len(kept), dropped, err)
	}
}

// countingLimiter allows the first limit calls of each subject and refuses
// the rest, counting every call.
type countingLimiter struct {
	calls    map[string]int
	subjects []string
	fail     error
}

func (l *countingLimiter) Allow(_ context.Context, subject string, limit int, window time.Duration) (bool, error) {
	if l.calls == nil {
		l.calls = map[string]int{}
	}
	l.subjects = append(l.subjects, subject)
	if l.fail != nil {
		return false, l.fail
	}
	if window != time.Minute {
		return false, errors.New("unexpected window")
	}
	l.calls[subject]++
	return l.calls[subject] <= limit, nil
}

func TestAnAccountSendsAtMostTwelveBatchesAMinute(t *testing.T) {
	limiter := &countingLimiter{}
	signals := monitor.NewSignals(limiter, &eventLog{})
	account := uuid.New()

	for i := 0; i < monitor.BatchesPerMinute; i++ {
		if err := signals.AdmitBatch(t.Context(), account); err != nil {
			t.Fatalf("batch %d refused: %v", i+1, err)
		}
	}
	if err := signals.AdmitBatch(t.Context(), account); !errors.Is(err, monitor.ErrSignalsTooOften) {
		t.Fatalf("batch 13: err = %v, want ErrSignalsTooOften", err)
	}
	// Another account has its own budget, and the key is the account.
	if err := signals.AdmitBatch(t.Context(), uuid.New()); err != nil {
		t.Fatalf("another account refused: %v", err)
	}
	if limiter.subjects[0] != "signals:user:"+account.String() {
		t.Fatalf("subject = %q", limiter.subjects[0])
	}
}

func TestAnUnkeepableBudgetRefusesButIsNotTooOften(t *testing.T) {
	limiter := &countingLimiter{fail: errors.New("cache down")}
	err := monitor.NewSignals(limiter, &eventLog{}).AdmitBatch(t.Context(), uuid.New())
	if err == nil || errors.Is(err, monitor.ErrSignalsTooOften) {
		t.Fatalf("err = %v, want a failure that is not ErrSignalsTooOften", err)
	}
}

func TestRecordStoresOnlyWhatIsKeptInOneInsert(t *testing.T) {
	log := &eventLog{}
	signals := monitor.NewSignals(&countingLimiter{}, log).WithClock(func() time.Time { return signalsNow })

	kept, err := signals.Record(t.Context(), []monitor.Event{
		event(monitor.PageLeft{AwayMs: 3000}),
		event(monitor.PageLeft{AwayMs: 10}),
		event(monitor.Paste{Target: monitor.PasteAnswer, Chars: 1, Text: "a"}),
	})
	if err != nil || kept != 2 {
		t.Fatalf("Record() = %d, %v; want 2, nil", kept, err)
	}
	if log.inserts != 1 || len(log.all()) != 2 {
		t.Fatalf("%d inserts of %d events, want one insert of two", log.inserts, len(log.all()))
	}

	// Nothing kept is nothing written.
	if kept, err := signals.Record(t.Context(), []monitor.Event{event(monitor.PageLeft{AwayMs: 10})}); err != nil || kept != 0 {
		t.Fatalf("Record() = %d, %v; want 0, nil", kept, err)
	}
	if log.inserts != 1 {
		t.Fatalf("an empty batch was inserted")
	}
}

func TestRecordSurfacesAStoreFailure(t *testing.T) {
	log := &eventLog{fail: errors.New("database down")}
	_, err := monitor.NewSignals(&countingLimiter{}, log).Record(t.Context(), []monitor.Event{event(monitor.PageLeft{AwayMs: 3000})})
	if err == nil {
		t.Fatal("a failed insert was not reported")
	}
}
