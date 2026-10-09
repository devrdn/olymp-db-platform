package app

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/config"
)

func TestProbeURLTargetsTheConfiguredInternalListener(t *testing.T) {
	got := ProbeURL(":9091")

	if got != "http://127.0.0.1:9091/healthz" {
		t.Errorf("ProbeURL = %q, want the loopback form of the configured address", got)
	}
}

func TestProbeURLKeepsAnExplicitHost(t *testing.T) {
	got := ProbeURL("10.0.0.5:9090")

	if got != "http://10.0.0.5:9090/healthz" {
		t.Errorf("ProbeURL = %q, want the explicit host preserved", got)
	}
}

func TestProbeURLDefaultsToTheConfigDefault(t *testing.T) {
	got := ProbeURL("")

	if got != "http://127.0.0.1"+config.DefaultInternalAddr+"/healthz" {
		t.Errorf("ProbeURL = %q, want it derived from config.DefaultInternalAddr", got)
	}
}
