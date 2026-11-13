package app

import (
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/microsoft"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/config"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/mail"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/recovery"
	"github.com/devrdn/db-contest/backend/internal/signup"
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

func TestWithMailOffNothingIsQueuedAndNoSenderRuns(t *testing.T) {
	off, err := newMailSender(config.Config{})
	if err != nil {
		t.Fatalf("newMailSender() = %v", err)
	}

	outbox, jobs := newMailOutbox(quiet(), off, postgres.NewMailOutbox(nil), 25, metrics.Noop{})

	// Only the sweep that erases messages queued before mail was turned off.
	if len(jobs) != 1 || jobs[0].name != "mail-expiry" {
		t.Errorf("mail is off and the jobs are %v, want only mail-expiry", jobNames(jobs))
	}
	err = outbox.Enqueue(t.Context(), mailing.Message{
		Kind: mailing.KindSignInDetails, To: "ana@usm.md", Subject: "Subject", Body: "Body",
	})
	if !errors.Is(err, mailing.ErrDisabled) {
		t.Errorf("Enqueue() with mail off = %v, want ErrDisabled", err)
	}
}

func TestWithMailOnAnEnqueueWakesTheDeliveryJob(t *testing.T) {
	on, err := newMailSender(config.Config{
		SMTPHost: "smtp.usm.md", SMTPPort: 587, SMTPFrom: "olympiad@usm.md", SMTPTLS: mail.TLSStartTLS,
	})
	if err != nil {
		t.Fatalf("newMailSender() = %v", err)
	}

	outbox, jobs := newMailOutbox(quiet(), on, postgres.NewMailOutbox(nil), 25, metrics.Noop{})

	if !outbox.Enabled() || len(jobs) != 1 || jobs[0].name != "mail-outbox" {
		t.Fatalf("enabled=%v jobs=%v, want an enabled outbox and the delivery job", outbox.Enabled(), jobNames(jobs))
	}
	outbox.Wake()
	select {
	case <-jobs[0].wake:
	case <-time.After(time.Second):
		t.Fatal("the delivery job does not listen to the outbox's wake channel")
	}
}

func jobNames(jobs []task) []string {
	var names []string
	for _, job := range jobs {
		names = append(names, job.name)
	}
	return names
}

func TestTheMicrosoftClientIsBuiltOnlyWhenConfigured(t *testing.T) {
	off, err := newMicrosoftClient(config.Config{MicrosoftTenant: "common"})
	if err != nil || off != nil {
		t.Errorf("without a client: %v, %v; want no connector", off, err)
	}

	on, err := newMicrosoftClient(config.Config{
		MicrosoftClientID: "11111111-2222-4333-8444-555555555555", MicrosoftClientSecret: "a-test-client-secret",
		MicrosoftTenant: "common", PublicURL: "https://contest.example.edu",
	})
	if err != nil || on == nil {
		t.Fatalf("newMicrosoftClient() = %v, %v", on, err)
	}
	authorize, err := url.Parse(on.AuthCodeURL(microsoft.State{State: "s", Nonce: "n", Verifier: strings.Repeat("v", 43)}))
	if err != nil {
		t.Fatal(err)
	}
	if got := authorize.Query().Get("redirect_uri"); got != "https://contest.example.edu/api/v1/auth/microsoft/callback" {
		t.Errorf("redirect_uri = %q, want the public URL's callback", got)
	}
	if got := authorize.Host + authorize.Path; got != "login.microsoftonline.com/common/oauth2/v2.0/authorize" {
		t.Errorf("authorize endpoint = %q, want Microsoft's for the configured tenant", got)
	}
}

// Sign-up mail goes to whatever address is typed, and is what a flood would
// use; recovery must keep its own budget through one.
func TestASignUpFloodLeavesRecoveryItsMailBudget(t *testing.T) {
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	signups, recoveries := selfServiceMail(auth.NewLimiter(c), logging.New("error", io.Discard))

	for range signup.MailsPerMinute {
		if ok, err := signups.Spend(t.Context()); !ok || err != nil {
			t.Fatalf("sign-up mail = %v, %v, want admitted", ok, err)
		}
	}
	if ok, _ := signups.Spend(t.Context()); ok {
		t.Fatal("sign-up mail past its minute was admitted")
	}

	for i := range recovery.MailsPerMinute {
		if ok, err := recoveries.Spend(t.Context()); !ok || err != nil {
			t.Fatalf("reset link %d after the flood = %v, %v, want admitted", i, ok, err)
		}
	}
}
