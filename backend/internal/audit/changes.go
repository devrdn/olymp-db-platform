package audit

import (
	"encoding/json"
	"reflect"
	"time"
)

// The bounds one record may not exceed. An oversized value is replaced by a
// note of its size rather than truncated.
const (
	maxChangedFields = 50
	maxChangeValue   = 512
)

// Changes is the set of fields an update actually altered, since a form sends
// every field. An empty set is recorded as such.
//
// Fields are named by the caller, never derived from a struct by reflection:
// reference answers are recorded only as a count, and a reflective diff would
// leak them into the trail.
type Changes struct {
	fields map[string]any
	// counted is every field offered, so the bound applies to what was asked.
	counted int
}

func NewChanges() *Changes {
	return &Changes{fields: map[string]any{}}
}

// Set records a field if it moved. Values are compared as they would be
// stored, so one instant in two time zones is not a change.
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

// Between records what differs between two hand-written maps of the same
// thing; a field not named in them cannot be recorded.
func Between(before, after map[string]any) *Changes {
	changes := NewChanges()

	// The union of keys, so a field present on one side only is not lost.
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

func (c *Changes) Empty() bool { return len(c.fields) == 0 }

// Payload is the audit entry's body: plain maps all the way down, so
// redaction, which walks maps, reaches into it.
func (c *Changes) Payload() map[string]any {
	if c.Empty() {
		return map[string]any{"changed": false}
	}
	return map[string]any{"changes": c.fields}
}

// normalise turns a value into the shape it would be stored in: pointers are
// followed and times become the API's instant format.
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
