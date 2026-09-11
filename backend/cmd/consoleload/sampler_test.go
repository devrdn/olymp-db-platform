package main

import (
	"testing"
	"time"
)

func TestParseCPUTime(t *testing.T) {
	for text, want := range map[string]time.Duration{
		"0:01.25":     1250 * time.Millisecond,
		"12:03.50":    12*time.Minute + 3500*time.Millisecond,
		"1:02:03":     time.Hour + 2*time.Minute + 3*time.Second,
		"2-01:00:00":  49 * time.Hour,
		"00:00:00.00": 0,
	} {
		got, err := parseCPUTime(text)
		if err != nil {
			t.Errorf("parseCPUTime(%q): %v", text, err)
			continue
		}
		// Float arithmetic: equal to the millisecond is what matters.
		if got.Round(time.Millisecond) != want {
			t.Errorf("parseCPUTime(%q) = %s, want %s", text, got, want)
		}
	}
	if _, err := parseCPUTime("x:yy"); err == nil {
		t.Error("parseCPUTime accepted garbage")
	}
}

func TestParseSize(t *testing.T) {
	for text, want := range map[string]int64{
		"512MiB":   512 << 20,
		"1.5GiB":   3 << 29,
		"2GB":      2e9,
		"12.3kB":   12300,
		" 0B ":     0,
		"1.25 MiB": 1310720,
	} {
		got, err := parseSize(text)
		if err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", text, got, err, want)
		}
	}
	if _, err := parseSize("12"); err == nil {
		t.Error("parseSize accepted a number with no unit")
	}
}
