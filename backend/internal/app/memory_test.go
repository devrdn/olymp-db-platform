package app

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
)

func TestAMemoryLimitBelowWhatHashingNeedsIsWarnedAbout(t *testing.T) {
	// Four slots need 256 MiB on top of the base, so 512 MiB is too low.
	var buf bytes.Buffer
	log := logging.New("info", &buf)

	warnIfMemoryLimitTooLow(log, 512<<20, 4)

	out := buf.String()
	if !strings.Contains(out, "GOMEMLIMIT") || !strings.Contains(out, "WARN") {
		t.Fatalf("no warning naming GOMEMLIMIT was logged: %q", out)
	}
	want := baseMemory + 4*password.MemoryPerHash
	if !strings.Contains(out, "1280") || want != 1280<<20 {
		t.Errorf("the warning does not state the %d MiB needed: %q", want>>20, out)
	}
}

func TestAMemoryLimitThatCoversHashingIsNotWarnedAbout(t *testing.T) {
	var buf bytes.Buffer
	log := logging.New("info", &buf)

	warnIfMemoryLimitTooLow(log, 1280<<20, 4)
	warnIfMemoryLimitTooLow(log, math.MaxInt64, 64) // GOMEMLIMIT not set

	if buf.Len() != 0 {
		t.Errorf("a sufficient or absent limit was warned about: %q", buf.String())
	}
}
