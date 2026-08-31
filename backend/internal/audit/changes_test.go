package audit

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAChangedFieldIsRecordedWithBothValues(t *testing.T) {
	changes := NewChanges()
	changes.Set("enrollment", "invite_only", "open")

	payload := changes.Payload()

	got, ok := payload["changes"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %v, want a changes map", payload)
	}
	field, ok := got["enrollment"].(map[string]any)
	if !ok {
		t.Fatalf("changes = %v, want the field as a map", got)
	}
	if field["from"] != "invite_only" || field["to"] != "open" {
		t.Errorf("field = %v, want invite_only → open", field)
	}
}

func TestAFieldThatDidNotChangeIsNotRecorded(t *testing.T) {
	// A form sends every field. Recording all of them makes each save look
	// like a rewrite of the contest, which is the opposite of readable.
	changes := NewChanges()
	changes.Set("timing", "fixed", "fixed")

	if !changes.Empty() {
		t.Errorf("Payload() = %v, want nothing recorded", changes.Payload())
	}
}

func TestSavingWithNoChangesIsItselfARecord(t *testing.T) {
	// An empty set is a fact — "saved, nothing moved" — and it must not be
	// indistinguishable from a real edit, so the payload still says so.
	changes := NewChanges()
	changes.Set("timing", "fixed", "fixed")

	payload := changes.Payload()

	if payload["changed"] != false {
		t.Errorf("payload = %v, want it to say nothing changed", payload)
	}
	if _, present := payload["changes"]; present {
		t.Errorf("payload = %v, want no empty changes map", payload)
	}
}

func TestTheSameInstantInAnotherZoneIsNotAChange(t *testing.T) {
	// time.Time carries a location and a monotonic reading, so two values for
	// one instant are not equal as Go structs. Comparing them raw would report
	// a schedule change every time a contest was saved.
	moscow := time.FixedZone("MSK", 3*60*60)
	before := time.Date(2026, 11, 8, 21, 0, 0, 0, time.UTC)
	after := before.In(moscow)

	changes := NewChanges()
	changes.Set("ends_at", before, after)

	if !changes.Empty() {
		t.Errorf("Payload() = %v, want the same instant to count as unchanged", changes.Payload())
	}
}

func TestATimeIsRecordedInTheOneFormatTheAPISpeaks(t *testing.T) {
	changes := NewChanges()
	changes.Set("ends_at", time.Date(2026, 11, 8, 21, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 8, 22, 0, 0, 0, time.UTC))

	field := changes.Payload()["changes"].(map[string]any)["ends_at"].(map[string]any)

	if field["from"] != "2026-11-08T21:00:00Z" {
		t.Errorf("from = %v, want an RFC 3339 instant", field["from"])
	}
}

func TestSettingAnEmptyValueIsAChange(t *testing.T) {
	// Clearing the network restriction is exactly the kind of edit somebody
	// asks about afterwards; it must not vanish for being an absence.
	var none *time.Time
	at := time.Date(2026, 11, 8, 21, 0, 0, 0, time.UTC)

	changes := NewChanges()
	changes.Set("starts_at", &at, none)

	field, ok := changes.Payload()["changes"].(map[string]any)["starts_at"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %v, want the cleared field recorded", changes.Payload())
	}
	if field["to"] != nil {
		t.Errorf("to = %v, want nil for a cleared value", field["to"])
	}
}

func TestListsAreComparedByTheirContents(t *testing.T) {
	changes := NewChanges()
	changes.Set("languages", []string{"en", "ro"}, []string{"en", "ro"})
	changes.Set("writable_tables", []string{"notes"}, []string{"notes", "evidence"})

	got := changes.Payload()["changes"].(map[string]any)

	if _, present := got["languages"]; present {
		t.Errorf("changes = %v, want an unchanged list left out", got)
	}
	if _, present := got["writable_tables"]; !present {
		t.Errorf("changes = %v, want the changed list recorded", got)
	}
}

func TestTheRecordIsPlainMapsSoRedactionCanWalkIt(t *testing.T) {
	// Redaction removes password-shaped keys at any depth, and it walks maps.
	// A struct here would be a hole in it, so the shape is deliberate.
	changes := NewChanges()
	changes.Set("enrollment", "invite_only", "open")

	encoded, err := json.Marshal(changes.Payload())
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if _, ok := round["changes"].(map[string]any)["enrollment"].(map[string]any); !ok {
		t.Errorf("payload = %s, want nested plain maps", encoded)
	}
}

func TestAnOversizedValueIsNotedRatherThanStored(t *testing.T) {
	// The trail is kept for a year and read in a browser. One entry must not
	// be able to grow until the page stops opening — and silently truncating
	// a value would put a half-value in a record people rely on.
	long := make([]string, 400)
	for i := range long {
		long[i] = "10.20.30.40/32"
	}

	changes := NewChanges()
	changes.Set("allowed_cidrs", []string{}, long)

	field := changes.Payload()["changes"].(map[string]any)["allowed_cidrs"].(map[string]any)
	to, ok := field["to"].(map[string]any)
	if !ok {
		t.Fatalf("to = %v, want the oversized value replaced by a note", field["to"])
	}
	if to["omitted_bytes"] == nil {
		t.Errorf("note = %v, want it to say how large the value was", to)
	}
}

func TestTheNumberOfFieldsIsBounded(t *testing.T) {
	changes := NewChanges()
	for i := range 200 {
		changes.Set(string(rune('a'+i%26))+string(rune('0'+i/26)), i, i+1)
	}

	got := changes.Payload()["changes"].(map[string]any)

	if len(got) > maxChangedFields {
		t.Errorf("recorded %d fields, want at most %d", len(got), maxChangedFields)
	}
}

func TestBetweenRecordsOnlyWhatDiffers(t *testing.T) {
	// The call site should say "these two shapes" once, not repeat the list of
	// fields it is comparing — the list belongs next to the type it describes.
	changes := Between(
		map[string]any{"enrollment": "invite_only", "timing": "fixed", "points": 5},
		map[string]any{"enrollment": "open", "timing": "fixed", "points": 5},
	)

	got := changes.Payload()["changes"].(map[string]any)

	if len(got) != 1 {
		t.Fatalf("changes = %v, want only the enrollment", got)
	}
	if got["enrollment"].(map[string]any)["to"] != "open" {
		t.Errorf("enrollment = %v, want it recorded", got["enrollment"])
	}
}

func TestBetweenTwoIdenticalShapesRecordsNothing(t *testing.T) {
	changes := Between(
		map[string]any{"timing": "fixed"},
		map[string]any{"timing": "fixed"},
	)

	if !changes.Empty() {
		t.Errorf("Payload() = %v, want nothing recorded", changes.Payload())
	}
}

func TestBetweenTreatsAFieldOnOneSideOnlyAsAChange(t *testing.T) {
	// The two shapes come from one function, so this should not happen — and
	// when it does, silently ignoring the field would hide the very edit
	// somebody is looking for.
	changes := Between(
		map[string]any{"points": 5},
		map[string]any{"points": 5, "max_attempts": 3},
	)

	got := changes.Payload()["changes"].(map[string]any)

	attempts, ok := got["max_attempts"].(map[string]any)
	if !ok {
		t.Fatalf("changes = %v, want the field that appeared", got)
	}
	if attempts["from"] != nil || attempts["to"] != 3 {
		t.Errorf("max_attempts = %v, want nil → 3", attempts)
	}
}
