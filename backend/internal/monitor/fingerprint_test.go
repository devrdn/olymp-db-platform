package monitor_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/monitor"
)

func TestFingerprintIgnoresCaseAndSpacing(t *testing.T) {
	base := monitor.Fingerprint(`SELECT name FROM suspects WHERE age > 30`)
	for _, variant := range []string{
		`select name from suspects where age > 30`,
		"  SELECT   name\nFROM\tsuspects\r\n  WHERE age > 30  ",
		"Select Name From Suspects Where Age > 30",
	} {
		if got := monitor.Fingerprint(variant); got != base {
			t.Errorf("Fingerprint(%q) = %d, want %d (same as the canonical spelling)", variant, got, base)
		}
	}
}

func TestFingerprintTellsDifferentTextApart(t *testing.T) {
	base := monitor.Fingerprint(`SELECT name FROM suspects WHERE age > 30`)
	for _, other := range []string{
		`SELECT name FROM suspects WHERE age > 31`,
		`SELECT name FROM suspect WHERE age > 30`,
		// Spacing inside a token is not spacing between tokens.
		`SELECT name FROM suspects WHERE age >30`,
		``,
	} {
		if got := monitor.Fingerprint(other); got == base {
			t.Errorf("Fingerprint(%q) collides with the canonical statement", other)
		}
	}
}

func TestFingerprintIsStable(t *testing.T) {
	// The value is stored and compared across processes and releases, so it
	// must not depend on anything but the text: this pins the algorithm.
	// FNV-1a 64 of "select 1", worked out independently of this package.
	const want = int64(214897735614764786)
	if got := monitor.Fingerprint("SELECT 1"); got != want {
		t.Fatalf("Fingerprint(\"SELECT 1\") = %d, want %d", got, want)
	}
}
