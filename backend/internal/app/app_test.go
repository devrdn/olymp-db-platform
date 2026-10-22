package app

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/config"
	"github.com/devrdn/db-contest/backend/internal/platform/mail"
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

func TestTheMailSenderIsBuiltFromTheConfiguration(t *testing.T) {
	off, err := newMailSender(config.Config{})
	if err != nil || off.Enabled() {
		t.Errorf("without SMTP_HOST: enabled=%v err=%v, want a disabled sender", off != nil && off.Enabled(), err)
	}

	on, err := newMailSender(config.Config{
		SMTPHost: "smtp.usm.md", SMTPPort: 587, SMTPUsername: "olympiad@usm.md", SMTPPassword: "secret",
		SMTPFrom: "Olimpiada USM <olympiad@usm.md>", SMTPTLS: mail.TLSStartTLS,
	})
	if err != nil {
		t.Fatalf("newMailSender() = %v", err)
	}
	if !on.Enabled() || on.From() != "olympiad@usm.md" {
		t.Errorf("enabled=%v from=%q, want an enabled sender from olympiad@usm.md", on.Enabled(), on.From())
	}
}
