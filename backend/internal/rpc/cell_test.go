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
		// PostgreSQL's own spelling for bytea, so what is shown can be pasted
		// back into a query.
		"bytes": {[]byte{0xde, 0xad}, `\xdead`, false},
	} {
		t.Run(name, func(t *testing.T) {
			text, null := render(given.value)
			if text != given.text || null != given.null {
				t.Fatalf("render = (%q, %v), want (%q, %v)", text, null, given.text, given.null)
			}
		})
	}
}

// The driver hands back its own types for columns Go has no equivalent for.
// Printing one of those directly gives a dump of its fields rather than the
// number that was in the column, which is why driver.Valuer is unwrapped.
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

// A NULL arriving inside a driver type is still a NULL, not the empty string.
func TestANullDriverValueStaysNull(t *testing.T) {
	if _, null := render(pgtype.Numeric{}); !null {
		t.Fatal("a null numeric was not rendered as null")
	}
}
