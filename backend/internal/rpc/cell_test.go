package rpc

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestRenderingValuesForAConsole(t *testing.T) {
	moment := time.Date(2026, 9, 2, 14, 3, 0, 0, time.UTC)

	for name, given := range map[string]struct {
		value any
		text  string
		null  bool
	}{
		"a string":                    {"a knife", "a knife", false},
		"an empty string is not null": {"", "", false},
		"nothing at all":              {nil, "", true},
		"a number":                    {int64(42), "42", false},
		"a boolean":                   {true, "true", false},
		"a time":                      {moment, "2026-09-02T14:03:00Z", false},
		"bytes":                       {[]byte{0xde, 0xad}, `\xdead`, false},
	} {
		t.Run(name, func(t *testing.T) {
			text, null := render(given.value)
			if text != given.text || null != given.null {
				t.Fatalf("render = (%q, %v), want (%q, %v)", text, null, given.text, given.null)
			}
		})
	}
}

func TestADriverTypeIsRenderedAsItsValueRatherThanItsFields(t *testing.T) {
	var numeric pgtype.Numeric
	if err := numeric.Scan("1234.5678"); err != nil {
		t.Fatalf("building the value: %v", err)
	}

	text, null := render(numeric)

	if null {
		t.Fatal("a value was rendered as null")
	}
	if text != "1234.5678" {
		t.Fatalf("render = %q, want the number itself", text)
	}
}

func TestANullDriverValueStaysNull(t *testing.T) {
	if _, null := render(pgtype.Numeric{}); !null {
		t.Fatal("a null numeric was not rendered as null")
	}
}

// A console answer at the row bound: a thousand rows of eight columns.
func BenchmarkCellsFor(b *testing.B) {
	values := []any{"Alice", int64(42), nil, "2026-03-01", 3.5, "Library", nil, true}
	b.ReportAllocs()
	for b.Loop() {
		for range 1000 {
			_ = cellsFor(values)
		}
	}
}
