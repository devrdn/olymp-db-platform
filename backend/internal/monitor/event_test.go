package monitor_test

import (
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/google/uuid"
)

func event(payload monitor.Payload) monitor.Event {
	return monitor.Event{Contest: uuid.New(), Registration: uuid.New(), Payload: payload}
}

func normalized(t *testing.T, e monitor.Event) monitor.Event {
	t.Helper()
	got, err := e.Normalize()
	if err != nil {
		t.Fatalf("Normalize() = %v", err)
	}
	return got
}

func TestEveryKindCarriesItsOwnName(t *testing.T) {
	for want, payload := range map[monitor.Kind]monitor.Payload{
		monitor.KindPageLeft:        monitor.PageLeft{},
		monitor.KindPaste:           monitor.Paste{},
		monitor.KindIPChanged:       monitor.IPChanged{},
		monitor.KindParallelSession: monitor.ParallelSession{},
		monitor.KindTabCreated:      monitor.TabCreated{},
		monitor.KindTabRenamed:      monitor.TabRenamed{},
		monitor.KindTabDeleted:      monitor.TabDeleted{},
	} {
		if got := event(payload).Kind(); got != want {
			t.Errorf("%T.Kind() = %q, want %q", payload, got, want)
		}
	}
}

func TestOnlyPageLeftAndPasteComeFromTheBrowser(t *testing.T) {
	for kind, want := range map[monitor.Kind]bool{
		monitor.KindPageLeft:        true,
		monitor.KindPaste:           true,
		monitor.KindIPChanged:       false,
		monitor.KindParallelSession: false,
		monitor.KindTabCreated:      false,
		monitor.KindTabRenamed:      false,
		monitor.KindTabDeleted:      false,
	} {
		if got := kind.FromBrowser(); got != want {
			t.Errorf("%s.FromBrowser() = %t, want %t", kind, got, want)
		}
	}
}

func TestAnAbsenceShorterThanASecondIsNotRecorded(t *testing.T) {
	_, err := event(monitor.PageLeft{AwayMs: 999}).Normalize()
	if !errors.Is(err, monitor.ErrAwayTooShort) {
		t.Fatalf("999 ms away: err = %v, want ErrAwayTooShort", err)
	}
	got := normalized(t, event(monitor.PageLeft{AwayMs: 1000}))
	if got.Payload.(monitor.PageLeft).AwayMs != 1000 {
		t.Fatalf("one second away became %+v", got.Payload)
	}
}

func TestAnAbsenceIsCappedAtADay(t *testing.T) {
	got := normalized(t, event(monitor.PageLeft{AwayMs: 48 * time.Hour.Milliseconds()}))
	if away := got.Payload.(monitor.PageLeft).AwayMs; away != monitor.MaxAway.Milliseconds() {
		t.Fatalf("two days away = %d ms, want the cap %d", away, monitor.MaxAway.Milliseconds())
	}
}

func TestAPasteKeepsOnlyTheBeginningOfTheText(t *testing.T) {
	text := strings.Repeat("я", monitor.MaxPasteTextRunes+100)
	got := normalized(t, event(monitor.Paste{Target: monitor.PasteEditor, Chars: 600, Text: text}))
	paste := got.Payload.(monitor.Paste)
	if n := utf8.RuneCountInString(paste.Text); n != monitor.MaxPasteTextRunes {
		t.Fatalf("pasted text kept %d characters, want %d", n, monitor.MaxPasteTextRunes)
	}
	if paste.Chars != 600 {
		t.Fatalf("chars = %d, want the count the browser reported", paste.Chars)
	}
}

func TestAPasteCountIsBounded(t *testing.T) {
	low := normalized(t, event(monitor.Paste{Target: monitor.PasteNotes, Chars: -5}))
	if chars := low.Payload.(monitor.Paste).Chars; chars != 0 {
		t.Fatalf("a negative count became %d, want 0", chars)
	}
	high := normalized(t, event(monitor.Paste{Target: monitor.PasteNotes, Chars: monitor.MaxPasteChars + 1}))
	if chars := high.Payload.(monitor.Paste).Chars; chars != monitor.MaxPasteChars {
		t.Fatalf("an enormous count became %d, want %d", chars, monitor.MaxPasteChars)
	}
}

func TestAPasteTargetIsOneOfThree(t *testing.T) {
	for _, target := range []monitor.PasteTarget{monitor.PasteEditor, monitor.PasteAnswer, monitor.PasteNotes} {
		if _, err := event(monitor.Paste{Target: target}).Normalize(); err != nil {
			t.Errorf("target %q: %v", target, err)
		}
	}
	for _, target := range []monitor.PasteTarget{"", "console", "EDITOR"} {
		if _, err := event(monitor.Paste{Target: target}).Normalize(); !errors.Is(err, monitor.ErrPasteTarget) {
			t.Errorf("target %q: err = %v, want ErrPasteTarget", target, err)
		}
	}
}

func TestTextThatJSONBCannotStoreIsCleaned(t *testing.T) {
	// jsonb refuses the NUL character outright and a broken UTF-8 sequence
	// has no text to store: either would be a failed insert for the whole
	// batch rather than one odd event.
	got := normalized(t, event(monitor.Paste{Target: monitor.PasteEditor, Text: "a\x00b\xffc"}))
	text := got.Payload.(monitor.Paste).Text
	if strings.ContainsRune(text, 0) || !utf8.ValidString(text) {
		t.Fatalf("text = %q, want no NUL and valid UTF-8", text)
	}
}

func TestAUserAgentIsCut(t *testing.T) {
	got := normalized(t, event(monitor.ParallelSession{
		OtherIP:   netip.MustParseAddr("10.0.0.2"),
		UserAgent: strings.Repeat("x", monitor.MaxUserAgentRunes+1),
	}))
	if n := utf8.RuneCountInString(got.Payload.(monitor.ParallelSession).UserAgent); n != monitor.MaxUserAgentRunes {
		t.Fatalf("user agent kept %d characters, want %d", n, monitor.MaxUserAgentRunes)
	}
}

func TestAddressEventsNeedAddresses(t *testing.T) {
	addr := netip.MustParseAddr("192.0.2.1")
	for name, payload := range map[string]monitor.Payload{
		"ip_changed without from":           monitor.IPChanged{To: addr},
		"ip_changed without to":             monitor.IPChanged{From: addr},
		"parallel_session without other_ip": monitor.ParallelSession{},
	} {
		if _, err := event(payload).Normalize(); !errors.Is(err, monitor.ErrEventInvalid) {
			t.Errorf("%s: err = %v, want ErrEventInvalid", name, err)
		}
	}
}

func TestTabEventsNeedATabAndCutTheirTitles(t *testing.T) {
	long := strings.Repeat("t", monitor.MaxTabTitleRunes+10)
	if _, err := event(monitor.TabCreated{Title: "x"}).Normalize(); !errors.Is(err, monitor.ErrEventInvalid) {
		t.Fatalf("tab_created without a tab: err = %v, want ErrEventInvalid", err)
	}
	renamed := normalized(t, event(monitor.TabRenamed{TabID: uuid.New(), From: long, To: long})).Payload.(monitor.TabRenamed)
	if utf8.RuneCountInString(renamed.From) != monitor.MaxTabTitleRunes || utf8.RuneCountInString(renamed.To) != monitor.MaxTabTitleRunes {
		t.Fatalf("renamed titles were not cut: %+v", renamed)
	}
	deleted := normalized(t, event(monitor.TabDeleted{TabID: uuid.New(), Title: long})).Payload.(monitor.TabDeleted)
	if utf8.RuneCountInString(deleted.Title) != monitor.MaxTabTitleRunes {
		t.Fatalf("deleted title was not cut: %+v", deleted)
	}
}

func TestAnEventBelongsToARegistrationInAContest(t *testing.T) {
	for name, e := range map[string]monitor.Event{
		"no contest":      {Registration: uuid.New(), Payload: monitor.PageLeft{AwayMs: 5000}},
		"no registration": {Contest: uuid.New(), Payload: monitor.PageLeft{AwayMs: 5000}},
		"no payload":      {Contest: uuid.New(), Registration: uuid.New()},
	} {
		if _, err := e.Normalize(); !errors.Is(err, monitor.ErrEventInvalid) {
			t.Errorf("%s: err = %v, want ErrEventInvalid", name, err)
		}
	}
}

func TestOnlyABrowserSignalKeepsTheTimeTheBrowserClaimed(t *testing.T) {
	claimed := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

	signal := event(monitor.PageLeft{AwayMs: 5000})
	signal.ClientAt = &claimed
	if got := normalized(t, signal); got.ClientAt == nil || !got.ClientAt.Equal(claimed) {
		t.Fatalf("a browser signal lost its claimed time: %v", got.ClientAt)
	}

	server := event(monitor.TabDeleted{TabID: uuid.New(), Title: "a"})
	server.ClientAt = &claimed
	if got := normalized(t, server); got.ClientAt != nil {
		t.Fatalf("a server event kept a browser time: %v", got.ClientAt)
	}

	// A claim no clock could make is dropped rather than stored: the column
	// cannot hold every time.Time, and a failed insert would lose the batch.
	absurd := time.Date(300000, 1, 1, 0, 0, 0, 0, time.UTC)
	wild := event(monitor.PageLeft{AwayMs: 5000})
	wild.ClientAt = &absurd
	if got := normalized(t, wild); got.ClientAt != nil {
		t.Fatalf("an absurd claimed time was kept: %v", got.ClientAt)
	}
}

// A paste's repeat count is at most a batch's worth, and a count of one is
// no count at all.
func TestAPasteRepeatCountIsBounded(t *testing.T) {
	for given, want := range map[int]int{-3: 0, 1: 0, 2: 2, monitor.MaxBatchEvents + 1: monitor.MaxBatchEvents} {
		got := normalized(t, event(monitor.Paste{Target: monitor.PasteEditor, Count: given}))
		if count := got.Payload.(monitor.Paste).Count; count != want {
			t.Errorf("a repeat count of %d became %d, want %d", given, count, want)
		}
	}
}

func TestPayloadsMarshalToTheDesignedFields(t *testing.T) {
	for want, payload := range map[string]monitor.Payload{
		`{"away_ms":5000}`:                                                      monitor.PageLeft{AwayMs: 5000},
		`{"target":"answer","chars":3,"text":"abc"}`:                            monitor.Paste{Target: monitor.PasteAnswer, Chars: 3, Text: "abc"},
		`{"target":"editor","chars":3,"text":"abc","count":4}`:                  monitor.Paste{Target: monitor.PasteEditor, Chars: 3, Text: "abc", Count: 4},
		`{"from":"192.0.2.1","to":"2001:db8::1"}`:                               monitor.IPChanged{From: netip.MustParseAddr("192.0.2.1"), To: netip.MustParseAddr("2001:db8::1")},
		`{"other_ip":"192.0.2.9","user_agent":"Firefox"}`:                       monitor.ParallelSession{OtherIP: netip.MustParseAddr("192.0.2.9"), UserAgent: "Firefox"},
		`{"tab_id":"00000000-0000-0000-0000-000000000001","title":"a"}`:         monitor.TabCreated{TabID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Title: "a"},
		`{"tab_id":"00000000-0000-0000-0000-000000000001","from":"a","to":"b"}`: monitor.TabRenamed{TabID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), From: "a", To: "b"},
	} {
		got, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %T: %v", payload, err)
		}
		if string(got) != want {
			t.Errorf("%T = %s, want %s", payload, got, want)
		}
	}
}

func TestABatchIsBounded(t *testing.T) {
	batch := make([]monitor.Event, monitor.MaxBatchEvents+1)
	if err := monitor.CheckBatch(batch); !errors.Is(err, monitor.ErrBatchTooLarge) {
		t.Fatalf("a batch of %d: err = %v, want ErrBatchTooLarge", len(batch), err)
	}
	if err := monitor.CheckBatch(batch[:monitor.MaxBatchEvents]); err != nil {
		t.Fatalf("a batch of exactly the limit: %v", err)
	}
}
