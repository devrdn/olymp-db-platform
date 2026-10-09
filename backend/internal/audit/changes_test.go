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
	changes := NewChanges()
	changes.Set("timing", "fixed", "fixed")

	if !changes.Empty() {
		t.Errorf("Payload() = %v, want nothing recorded", changes.Payload())
	}
}

func TestSavingWithNoChangesIsItselfARecord(t *testing.T) {
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
