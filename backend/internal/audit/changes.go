package audit

import (
	"encoding/json"
	"reflect"
	"time"
)

// The bounds one record may not exceed.
//
// The trail is kept for a year and read in a browser. A single entry must not
// be able to grow until the page stops opening — and a value is noted rather
// than truncated, because half a value in a record people rely on is worse
// than an honest gap.
const (
	maxChangedFields = 50
	maxChangeValue   = 512
)

// Changes is the set of fields an update actually altered.
//
// Recorded instead of the new state, because a form sends every field: writing
// all of them makes each save look like a rewrite of the whole contest, and
// buries the one line that moved. An empty set is itself a fact — "saved,
// nothing changed" — and the payload says so rather than leaving it
// indistinguishable from a real edit.
//
// Fields are named by the caller and never derived from a struct. That is a
// boundary rather than a style: reference answers are recorded as a count
// precisely so the trail cannot become somewhere to look them up, and a diff
// that walked a value with reflection would hand back exactly what was left
// out on purpose (see docs/ARCHITECTURE.md §9.2).
type Changes struct {
	fields map[string]any
	// counted is every field offered, so the bound is applied to what was
	// asked for rather than to what happened to fit.
	counted int
}

// NewChanges returns an empty change set.
func NewChanges() *Changes {
	return &Changes{fields: map[string]any{}}
}

// Set records a field, if it moved.
//
// Values are normalised to what would be stored before they are compared, so
// the question answered is "would the record differ", not "are these the same
// Go value". That is what keeps one instant expressed in two time zones from
// reporting a schedule change on every save.
func (c *Changes) Set(field string, before, after any) {
	from, to := normalise(before), normalise(after)
	if reflect.DeepEqual(from, to) {
		return
	}

	c.counted++
	if len(c.fields) >= maxChangedFields {
		return
	}
	c.fields[field] = map[string]any{"from": bounded(from), "to": bounded(to)}
}

// Between records what differs between two shapes of the same thing.
//
// The call site says "these two", once, instead of repeating the list of
// fields it compares — that list belongs beside the type it describes, where
// it reads as a decision about what may be recorded rather than as
// boilerplate. It is still a hand-written map, so the boundary holds: a field
// that is not named cannot be recorded, and nothing walks a value with
// reflection (see docs/ARCHITECTURE.md §9.2).
func Between(before, after map[string]any) *Changes {
	changes := NewChanges()

	// The union, not just one side. Both maps normally come from one function
	// and carry the same keys; when they do not, ignoring the odd field would
	// hide the very edit somebody is looking for.
	for field, value := range after {
		changes.Set(field, before[field], value)
	}
	for field, value := range before {
		if _, present := after[field]; !present {
			changes.Set(field, value, nil)
		}
	}
	return changes
}

// Empty reports whether anything moved.
func (c *Changes) Empty() bool { return len(c.fields) == 0 }

// Payload is the audit entry's body.
//
// Plain maps all the way down, so redaction — which removes password-shaped
// keys at any depth by walking maps — reaches into it. A struct here would be
// a hole in that.
func (c *Changes) Payload() map[string]any {
	if c.Empty() {
		return map[string]any{"changed": false}
	}
	return map[string]any{"changes": c.fields}
}

// normalise turns a value into the shape it would be stored in.
//
// Pointers are followed so that setting and clearing a field reads as a value
// against nil, and times become the one instant format the API speaks — both
// so they compare correctly and so the record is readable a year later.
func normalise(value any) any {
	if value == nil {
		return nil
	}

	switch typed := value.(type) {
	case time.Time:
		return typed.UTC().Format(time.RFC3339)
	case *time.Time:
		if typed == nil {
			return nil
		}
		return typed.UTC().Format(time.RFC3339)
	}

	reflected := reflect.ValueOf(value)
	if reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return nil
		}
		return normalise(reflected.Elem().Interface())
	}
	return value
}

// bounded replaces a value too large to keep with a note of its size.
func bounded(value any) any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxChangeValue {
		size := len(encoded)
		if err != nil {
			size = -1
		}
		return map[string]any{"omitted_bytes": size}
	}
	return value
}
