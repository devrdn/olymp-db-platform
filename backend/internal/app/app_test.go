package app

import (
	"context"
	"errors"
	"io"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/api"
	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/connector"
	"github.com/devrdn/olymp-db-platform/backend/internal/federation"
	"github.com/devrdn/olymp-db-platform/backend/internal/mailing"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/cache"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/config"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/logging"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/mail"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/metrics"
	"github.com/devrdn/olymp-db-platform/backend/internal/postgres"
	"github.com/devrdn/olymp-db-platform/backend/internal/recovery"
	"github.com/devrdn/olymp-db-platform/backend/internal/signup"
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

// idleConnector stands for a configured provider; the list only asks whether
// there is one.
type idleConnector struct{}

func (idleConnector) AuthCodeURL(connector.State) string { return "" }

func (idleConnector) Exchange(context.Context, string, connector.State) (federation.Claims, error) {
	return federation.Claims{}, nil
}

func TestConfiguredProviderIDsListsOnlyThoseWithAConnector(t *testing.T) {
	stub := idleConnector{}
	for name, tc := range map[string]struct {
		providers []api.SignInProvider
		want      []string
	}{
		"Microsoft only": {[]api.SignInProvider{{ID: "microsoft", Connector: stub}, {ID: "google"}, {ID: "github"}}, []string{"microsoft"}},
		"none":           {[]api.SignInProvider{{ID: "microsoft"}, {ID: "google"}}, []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := configuredProviderIDs(tc.providers); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("configuredProviderIDs() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestEachConnectorIsBuiltOnlyWhenConfigured(t *testing.T) {
	off, err := signInProviders(config.Config{MicrosoftTenant: "common"})
	if err != nil {
		t.Fatalf("signInProviders() = %v", err)
	}
	wantIDs := []string{"microsoft", "google", "github"}
	wantVariables := [][]string{
		{"MICROSOFT_CLIENT_ID", "MICROSOFT_CLIENT_SECRET"},
		{"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET"},
		{"GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET"},
	}
	if len(off) != len(wantIDs) {
		t.Fatalf("%d providers, want %d", len(off), len(wantIDs))
	}
	for i, p := range off {
		if p.ID != wantIDs[i] || !reflect.DeepEqual(p.Variables, wantVariables[i]) {
			t.Errorf("provider %d = %q %v, want %q %v", i, p.ID, p.Variables, wantIDs[i], wantVariables[i])
		}
		if p.Connector != nil {
			t.Errorf("%s has a connector without configuration", p.ID)
		}
		if p.States == nil {
			t.Errorf("%s has no state sealer", p.ID)
		}
	}

	for i, tc := range []struct {
		id  string
		cfg config.Config
	}{
		{"microsoft", config.Config{MicrosoftClientID: "11111111-2222-4333-8444-555555555555", MicrosoftClientSecret: "a-test-client-secret", MicrosoftTenant: "common"}},
		{"google", config.Config{GoogleClientID: "app.apps.example", GoogleClientSecret: "a-test-client-secret"}},
		{"github", config.Config{GitHubClientID: "Iv1.abc123", GitHubClientSecret: "a-test-client-secret"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			tc.cfg.PublicURL = "https://contest.example.edu"
			on, err := signInProviders(tc.cfg)
			if err != nil {
				t.Fatalf("signInProviders() = %v", err)
			}
			for j, p := range on {
				if (p.Connector != nil) != (j == i) {
					t.Errorf("%s: connector present = %v, want %v", p.ID, p.Connector != nil, j == i)
				}
			}
			authorize, err := url.Parse(on[i].Connector.AuthCodeURL(connector.State{State: "s", Nonce: "n", Verifier: strings.Repeat("v", 43)}))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := authorize.Query().Get("redirect_uri"), "https://contest.example.edu/api/v1/auth/"+tc.id+"/callback"; got != want {
				t.Errorf("redirect_uri = %q, want %q", got, want)
			}
		})
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

// Password sign-up needs mail; with the switch on and mail off only
// Microsoft creates accounts, which an operator should hear at start-up.
func TestSelfSignupWithoutMailIsWarnedAtStartUp(t *testing.T) {
	for name, tc := range map[string]struct {
		signup, mail, warned bool
	}{
		"sign-up on, mail off": {true, false, true},
		"sign-up on, mail on":  {true, true, false},
		"sign-up off":          {false, false, false},
	} {
		var logs strings.Builder
		warnSignupWithoutMail(logging.New("info", &logs), tc.signup, tc.mail)
		if got := strings.Contains(logs.String(), "SELF_SIGNUP_ENABLED") && strings.Contains(logs.String(), `"level":"WARN"`); got != tc.warned {
			t.Errorf("%s: warned %v, want %v:\n%s", name, got, tc.warned, logs.String())
		}
	}
}
