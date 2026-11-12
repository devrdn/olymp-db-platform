package microsoft_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/federation"
	"github.com/devrdn/db-contest/backend/internal/microsoft"
	"github.com/devrdn/db-contest/backend/internal/microsoft/microsofttest"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/platform/sentineltest"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
)

// Made-up tenants and applications; none exists at Microsoft.
const (
	clientID     = "11111111-2222-4333-8444-555555555555"
	otherClient  = "66666666-7777-4888-9999-aaaaaaaaaaaa"
	clientSecret = "a-test-client-secret"
	homeTenant   = "0a0a0a0a-1b1b-4c2c-8d3d-4e4e4e4e4e4e"
	otherTenant  = "f0f0f0f0-e1e1-4d2d-9c3c-b4b4b4b4b4b4"
	consumer     = microsofttest.ConsumerTenant
	objectID     = "abababab-cdcd-4efe-8a0a-121212121212"
	redirectURL  = "https://contest.example.edu/api/v1/auth/microsoft/callback"
)

func TestEveryExportedErrorIsListed(t *testing.T) {
	sentineltest.AssertListed(t, ".")
}

func newClient(t *testing.T, p *microsofttest.Provider, edit func(*microsoft.Config)) *microsoft.Client {
	t.Helper()
	cfg := microsoft.Config{
		ClientID: clientID, ClientSecret: clientSecret,
		Tenant: microsoft.TenantCommon, HomeTenant: homeTenant,
		RedirectURL: redirectURL, BaseURL: p.URL,
	}
	if edit != nil {
		edit(&cfg)
	}
	c, err := microsoft.New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return c
}

// exchange has the provider answer one code with a token of claims signed
// by its published key, and exchanges it as the callback would.
func exchange(t *testing.T, c *microsoft.Client, p *microsofttest.Provider, claims map[string]any) (federation.Claims, error) {
	t.Helper()
	return exchangeToken(t, c, p, p.Sign(t, claims))
}

func exchangeToken(t *testing.T, c *microsoft.Client, p *microsofttest.Provider, idToken string) (federation.Claims, error) {
	t.Helper()
	p.Issue("the-code", idToken, microsofttest.Challenge(strings.Repeat("v", 43)))
	return c.Exchange(t.Context(), "the-code", microsoft.State{Nonce: "the-nonce", Verifier: strings.Repeat("v", 43)})
}

func TestTheAuthorizeURLNamesTheTenantAndAsksWithPKCE(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, func(cfg *microsoft.Config) { cfg.Tenant = microsoft.TenantOrganizations })
	st := microsoft.State{State: "the-state", Nonce: "the-nonce", Verifier: strings.Repeat("v", 43)}

	raw := c.AuthCodeURL(st)

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != p.URL+"/organizations/oauth2/v2.0/authorize" {
		t.Errorf("authorize endpoint = %s, want the organizations authority", got)
	}
	sum := sha256.Sum256([]byte(st.Verifier))
	want := map[string]string{
		"client_id": clientID, "redirect_uri": redirectURL, "response_type": "code",
		"scope": "openid email profile", "state": "the-state", "nonce": "the-nonce",
		"code_challenge_method": "S256", "code_challenge": base64.RawURLEncoding.EncodeToString(sum[:]),
		// Shared machines: a live Microsoft session must not sign the next
		// person in unasked.
		"prompt": "select_account",
	}
	for key, value := range want {
		if got := u.Query().Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
	if u.Query().Has("client_secret") || strings.Contains(raw, clientSecret) {
		t.Error("the authorize URL carries the client secret")
	}
}

func TestStartingUpContactsNobody(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)
	_ = c.AuthCodeURL(microsoft.State{State: "s", Nonce: "n", Verifier: strings.Repeat("v", 43)})

	if n := p.Requests(); n != 0 {
		t.Errorf("the provider was asked %d times before any callback, want none, so its outage cannot stop the boot", n)
	}
}

func TestTheExchangeSendsTheVerifierAndTheSecret(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	if _, err := exchange(t, c, p, p.Claims(clientID, homeTenant, objectID, "the-nonce")); err != nil {
		t.Fatalf("Exchange() = %v", err)
	}
	form := p.LastTokenRequest()
	for key, value := range map[string]string{
		"grant_type": "authorization_code", "code": "the-code", "code_verifier": strings.Repeat("v", 43),
		"client_id": clientID, "client_secret": clientSecret, "redirect_uri": redirectURL,
	} {
		if got := form.Get(key); got != value {
			t.Errorf("token request %s = %q, want %q", key, got, value)
		}
	}
}

func TestTheCallbackSignsInAVerifiedTokenAsItsObjectID(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)
	claims := p.Claims(clientID, homeTenant, objectID, "the-nonce")
	claims["email"] = "Ana@Example.edu"

	got, err := exchange(t, c, p, claims)

	if err != nil {
		t.Fatalf("Exchange() = %v", err)
	}
	want := federation.Claims{
		Provider: users.ProviderMicrosoft, Issuer: p.Issuer(homeTenant), Subject: objectID,
		Email: "Ana@Example.edu", Name: "Ana Popescu", LoginHint: "ana@example.edu", EmailTrusted: true,
	}
	if got != want {
		t.Errorf("claims = %+v\nwant %+v", got, want)
	}
}

func TestTheCallbackRefusesAnIssuerThatDoesNotMatchItsTenant(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	for name, iss := range map[string]string{
		"another tenant":   p.Issuer(otherTenant),
		"another host":     "https://login.example.edu/" + homeTenant + "/v2.0",
		"the v1 issuer":    p.URL + "/" + homeTenant + "/",
		"the template":     p.URL + "/{tenantid}/v2.0",
		"a trailing slash": p.Issuer(homeTenant) + "/",
	} {
		t.Run(name, func(t *testing.T) {
			claims := p.Claims(clientID, homeTenant, objectID, "the-nonce")
			claims["iss"] = iss
			if _, err := exchange(t, c, p, claims); !errors.Is(err, microsoft.ErrInvalidToken) {
				t.Errorf("Exchange() = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestATenantThatIsNotAnIdentifierIsRefused(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	for _, tid := range []string{"", "common", "../" + homeTenant, strings.ToUpper(homeTenant)} {
		claims := p.Claims(clientID, tid, objectID, "the-nonce")
		if _, err := exchange(t, c, p, claims); !errors.Is(err, microsoft.ErrInvalidToken) {
			t.Errorf("tid %q: Exchange() = %v, want ErrInvalidToken", tid, err)
		}
	}
}

func TestTheCallbackRefusesAnotherAudience(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	if _, err := exchange(t, c, p, p.Claims(otherClient, homeTenant, objectID, "the-nonce")); !errors.Is(err, microsoft.ErrInvalidToken) {
		t.Errorf("Exchange() of a token for another application = %v, want ErrInvalidToken", err)
	}
}

func TestTheCallbackRefusesABadSignature(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)
	claims := p.Claims(clientID, homeTenant, objectID, "the-nonce")

	if _, err := exchangeToken(t, c, p, p.SignWithUnpublishedKey(t, claims)); !errors.Is(err, microsoft.ErrInvalidToken) {
		t.Errorf("Exchange() of a forged token = %v, want ErrInvalidToken", err)
	}

	// A genuine token whose claims were rewritten after signing.
	parts := strings.Split(p.Sign(t, claims), ".")
	claims["oid"] = "cdcdcdcd-abab-4efe-8a0a-343434343434"
	parts[1] = strings.Split(p.Sign(t, claims), ".")[1]
	if _, err := exchangeToken(t, c, p, strings.Join(parts, ".")); !errors.Is(err, microsoft.ErrInvalidToken) {
		t.Errorf("Exchange() of a rewritten token = %v, want ErrInvalidToken", err)
	}
}

func TestTheCallbackRefusesAWrongNonce(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	for _, nonce := range []string{"another-nonce", ""} {
		if _, err := exchange(t, c, p, p.Claims(clientID, homeTenant, objectID, nonce)); !errors.Is(err, microsoft.ErrInvalidToken) {
			t.Errorf("nonce %q: Exchange() = %v, want ErrInvalidToken", nonce, err)
		}
	}
}

func TestAnExpiredTokenIsRefused(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)
	claims := p.Claims(clientID, homeTenant, objectID, "the-nonce")
	claims["exp"] = time.Now().Add(-time.Minute).Unix()

	if _, err := exchange(t, c, p, claims); !errors.Is(err, microsoft.ErrInvalidToken) {
		t.Errorf("Exchange() of an expired token = %v, want ErrInvalidToken", err)
	}
}

func TestATokenWithoutAnObjectIDIsRefused(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	for _, oid := range []string{"", "not-an-object-id", strings.Repeat("a", 300)} {
		claims := p.Claims(clientID, homeTenant, oid, "the-nonce")
		if oid == "" {
			delete(claims, "oid")
		}
		if _, err := exchange(t, c, p, claims); !errors.Is(err, federation.ErrInvalidClaims) {
			t.Errorf("oid %.20q: Exchange() = %v, want federation.ErrInvalidClaims", oid, err)
		}
	}
}

func TestARefusedCodeIsAFailedExchange(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	_, err := c.Exchange(t.Context(), "a-code-never-issued", microsoft.State{Nonce: "n", Verifier: strings.Repeat("v", 43)})
	if !errors.Is(err, microsoft.ErrExchangeFailed) {
		t.Errorf("Exchange() of an unknown code = %v, want ErrExchangeFailed", err)
	}

	p.Answer("no-id-token", []byte(`{"access_token":"a","token_type":"Bearer","expires_in":3600}`))
	_, err = c.Exchange(t.Context(), "no-id-token", microsoft.State{Nonce: "n", Verifier: strings.Repeat("v", 43)})
	if !errors.Is(err, microsoft.ErrExchangeFailed) {
		t.Errorf("Exchange() answered without an ID token = %v, want ErrExchangeFailed", err)
	}
}

// The answer is valid but for its size: under oauth2's own 1 MiB cut, so
// only the connector's bound refuses it.
func TestAnOversizedTokenResponseIsRefusedAsItArrives(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)
	body, err := json.Marshal(map[string]any{
		"access_token": "a", "token_type": "Bearer", "expires_in": 3600,
		"id_token": p.Sign(t, p.Claims(clientID, homeTenant, objectID, "the-nonce")),
		"padding":  strings.Repeat("a", 300<<10),
	})
	if err != nil {
		t.Fatal(err)
	}
	p.Answer("large", body)

	_, err = c.Exchange(t.Context(), "large", microsoft.State{Nonce: "the-nonce", Verifier: strings.Repeat("v", 43)})
	if !errors.Is(err, microsoft.ErrExchangeFailed) {
		t.Errorf("Exchange() of a 300 KiB answer = %v, want ErrExchangeFailed", err)
	}
}

func TestAnUnreachableProviderIsAFailedExchange(t *testing.T) {
	c, err := microsoft.New(microsoft.Config{
		ClientID: clientID, ClientSecret: clientSecret, Tenant: microsoft.TenantCommon,
		RedirectURL: redirectURL, BaseURL: "http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatalf("New() with an unreachable provider = %v, want it built", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := c.Exchange(ctx, "code", microsoft.State{Nonce: "n", Verifier: strings.Repeat("v", 43)}); !errors.Is(err, microsoft.ErrExchangeFailed) {
		t.Errorf("Exchange() = %v, want ErrExchangeFailed", err)
	}
}

func TestTheHTTPClientHasATimeout(t *testing.T) {
	if got := microsoft.NewHTTPClient().Timeout; got <= 0 || got > 15*time.Second {
		t.Errorf("timeout = %s, want a few seconds", got)
	}
}

func TestASingleTenantInstallationRefusesAnotherTenant(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, func(cfg *microsoft.Config) { cfg.Tenant = homeTenant; cfg.HomeTenant = "" })

	if _, err := exchange(t, c, p, p.Claims(clientID, homeTenant, objectID, "the-nonce")); err != nil {
		t.Errorf("Exchange() for the installation's tenant = %v", err)
	}
	for _, tid := range []string{otherTenant, consumer} {
		if _, err := exchange(t, c, p, p.Claims(clientID, tid, objectID, "the-nonce")); !errors.Is(err, microsoft.ErrTenantNotAllowed) {
			t.Errorf("tid %s: Exchange() = %v, want ErrTenantNotAllowed", tid, err)
		}
	}
}

func TestAnOrganizationsInstallationRefusesAPersonalAccount(t *testing.T) {
	p := microsofttest.NewProvider(t)
	orgs := newClient(t, p, func(cfg *microsoft.Config) { cfg.Tenant = microsoft.TenantOrganizations })
	personal := newClient(t, p, func(cfg *microsoft.Config) { cfg.Tenant = microsoft.TenantConsumers; cfg.HomeTenant = "" })
	common := newClient(t, p, nil)

	for _, tc := range []struct {
		name    string
		client  *microsoft.Client
		tid     string
		allowed bool
	}{
		{"organizations, work account", orgs, otherTenant, true},
		{"organizations, personal account", orgs, consumer, false},
		{"consumers, personal account", personal, consumer, true},
		{"consumers, work account", personal, otherTenant, false},
		{"common, work account", common, otherTenant, true},
		{"common, personal account", common, consumer, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := exchange(t, tc.client, p, p.Claims(clientID, tc.tid, objectID, "the-nonce"))
			switch {
			case tc.allowed && err != nil:
				t.Errorf("Exchange() = %v, want it admitted", err)
			case !tc.allowed && !errors.Is(err, microsoft.ErrTenantNotAllowed):
				t.Errorf("Exchange() = %v, want ErrTenantNotAllowed", err)
			}
		})
	}
}

func trustOf(t *testing.T, p *microsofttest.Provider, c *microsoft.Client, edit func(map[string]any)) federation.Claims {
	t.Helper()
	claims := p.Claims(clientID, homeTenant, objectID, "the-nonce")
	edit(claims)
	got, err := exchange(t, c, p, claims)
	if err != nil {
		t.Fatalf("Exchange() = %v", err)
	}
	return got
}

func TestTheHomeTenantEmailIsTrusted(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	got := trustOf(t, p, c, func(m map[string]any) { m["email"] = "ana@example.edu" })
	if !got.EmailTrusted || got.Email != "ana@example.edu" {
		t.Errorf("home tenant with email: %+v, want it trusted", got)
	}

	got = trustOf(t, p, c, func(m map[string]any) { m["preferred_username"] = "ion@example.edu" })
	if !got.EmailTrusted || got.Email != "ion@example.edu" {
		t.Errorf("home tenant without email: %+v, want its username as a trusted email", got)
	}
}

func TestAForeignTenantEmailIsNotTrusted(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	got := trustOf(t, p, c, func(m map[string]any) {
		m["tid"], m["iss"] = otherTenant, p.Issuer(otherTenant)
		m["email"] = "ana@example.edu"
	})
	if got.EmailTrusted {
		t.Errorf("foreign tenant: %+v, want the email untrusted", got)
	}

	got = trustOf(t, p, c, func(m map[string]any) {
		m["tid"], m["iss"] = otherTenant, p.Issuer(otherTenant)
		m["preferred_username"] = "ana@example.edu"
	})
	if got.Email != "" || got.EmailTrusted {
		t.Errorf("foreign tenant without email: %+v, want its username not taken for an email", got)
	}

	noHome := newClient(t, p, func(cfg *microsoft.Config) { cfg.HomeTenant = "" })
	got = trustOf(t, p, noHome, func(m map[string]any) { m["email"] = "ana@example.edu" })
	if got.EmailTrusted {
		t.Errorf("no home tenant configured: %+v, want the email untrusted", got)
	}
}

func TestADomainVerifiedEmailIsTrusted(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)
	foreign := func(m map[string]any) {
		m["tid"], m["iss"] = otherTenant, p.Issuer(otherTenant)
		m["email"] = "ana@example.edu"
	}

	for value, trusted := range map[any]bool{true: true, "true": true, "1": true, false: false, "false": false, 0: false} {
		got := trustOf(t, p, c, func(m map[string]any) { foreign(m); m["xms_edov"] = value })
		if got.EmailTrusted != trusted {
			t.Errorf("xms_edov %#v: trusted = %v, want %v", value, got.EmailTrusted, trusted)
		}
	}
}

func TestAPersonalAccountEmailIsTrusted(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	got := trustOf(t, p, c, func(m map[string]any) {
		m["tid"], m["iss"] = consumer, p.Issuer(consumer)
		m["email"] = "ana@example.edu"
	})
	if !got.EmailTrusted {
		t.Errorf("personal account: %+v, want the email trusted", got)
	}
}

func TestAnOverlongNameIsCutToItsBound(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	got := trustOf(t, p, c, func(m map[string]any) {
		m["name"] = strings.Repeat("ă", federation.MaxNameLength)
		m["preferred_username"] = strings.Repeat("u", 2*federation.MaxLoginHintLength)
	})
	if len(got.Name) > federation.MaxNameLength || len(got.LoginHint) > federation.MaxLoginHintLength {
		t.Errorf("name %d bytes, hint %d bytes; want them within federation's bounds", len(got.Name), len(got.LoginHint))
	}
	if !strings.HasPrefix(got.Name, "ăă") || strings.ContainsRune(got.Name, '�') {
		t.Errorf("name of %d bytes cut mid-letter or lost", len(got.Name))
	}
}

// Two registrations of the application (two client IDs) see one person
// under two pairwise subjects and one object ID; the identity is the latter,
// so moving an installation to another registration orphans nobody.
func TestTheIdentityIsTheObjectIDNotThePairwiseSubject(t *testing.T) {
	p := microsofttest.NewProvider(t)
	first := newClient(t, p, nil)
	second := newClient(t, p, func(cfg *microsoft.Config) { cfg.ClientID = otherClient })

	before := p.Claims(clientID, otherTenant, objectID, "the-nonce")
	after := p.Claims(otherClient, otherTenant, objectID, "the-nonce")
	if before["sub"] == after["sub"] || before["aud"] == after["aud"] {
		t.Fatal("the two tokens must differ in sub and aud")
	}
	a, err := exchange(t, first, p, before)
	if err != nil {
		t.Fatalf("first registration: %v", err)
	}
	b, err := exchange(t, second, p, after)
	if err != nil {
		t.Fatalf("second registration: %v", err)
	}
	if a.Issuer != b.Issuer || a.Subject != b.Subject || a.Subject != objectID {
		t.Fatalf("identities %s|%s and %s|%s, want one, keyed on the object ID", a.Issuer, a.Subject, b.Issuer, b.Subject)
	}

	repo := userstest.New()
	repo.GrantRole(users.RoleStudent)
	accounts := users.NewService(repo, audit.New(discard{}), &userstest.SpyUnitOfWork{}, passwordtest.NewHasher()).
		WithExternalIdentities(repo)
	resolver := federation.NewService(federation.Config{
		Policy: federation.Policy{CreateAccounts: true}, Store: repo, Accounts: accounts, PendingLogins: noPending{},
	})
	entered, err := resolver.Resolve(t.Context(), a)
	if err != nil {
		t.Fatalf("Resolve() through the first registration = %v", err)
	}
	again, err := resolver.Resolve(t.Context(), b)
	if err != nil || again.ID != entered.ID {
		t.Errorf("Resolve() through the second registration = %v, %v; want account %v", again.ID, err, entered.ID)
	}
}

func TestNewRefusesAnIncompleteConfiguration(t *testing.T) {
	base := microsoft.Config{
		ClientID: clientID, ClientSecret: clientSecret, Tenant: microsoft.TenantCommon, RedirectURL: redirectURL,
	}
	for name, edit := range map[string]func(*microsoft.Config){
		"no client ID":         func(c *microsoft.Config) { c.ClientID = "" },
		"no secret":            func(c *microsoft.Config) { c.ClientSecret = "" },
		"no redirect":          func(c *microsoft.Config) { c.RedirectURL = "" },
		"an unknown tenant":    func(c *microsoft.Config) { c.Tenant = "everyone" },
		"a malformed home":     func(c *microsoft.Config) { c.HomeTenant = "example.edu" },
		"a relative base":      func(c *microsoft.Config) { c.BaseURL = "login.microsoftonline.com" },
		"a relative redirect":  func(c *microsoft.Config) { c.RedirectURL = "/api/v1/auth/microsoft/callback" },
		"an uppercase tenant":  func(c *microsoft.Config) { c.Tenant = strings.ToUpper(homeTenant) },
		"no tenant":            func(c *microsoft.Config) { c.Tenant = "" },
		"a home outside it":    func(c *microsoft.Config) { c.Tenant = otherTenant; c.HomeTenant = homeTenant },
		"a home for consumers": func(c *microsoft.Config) { c.Tenant = microsoft.TenantConsumers; c.HomeTenant = homeTenant },
	} {
		cfg := base
		edit(&cfg)
		if _, err := microsoft.New(cfg); err == nil {
			t.Errorf("%s: New() accepted %+v", name, cfg)
		}
	}
	if _, err := microsoft.New(base); err != nil {
		t.Errorf("New() of a complete configuration = %v", err)
	}
}

type discard struct{}

func (discard) Append(context.Context, audit.Entry) error       { return nil }
func (discard) AppendMany(context.Context, []audit.Entry) error { return nil }

type noPending struct{}

func (noPending) LoginHeld(context.Context, string, string, time.Time) (bool, error) {
	return false, nil
}

func TestTheExchangeSendsTheVerifierTheChallengeWasMadeFrom(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)
	p.Issue("the-code", p.Sign(t, p.Claims(clientID, homeTenant, objectID, "the-nonce")), microsofttest.Challenge(strings.Repeat("w", 43)))

	_, err := c.Exchange(t.Context(), "the-code", microsoft.State{Nonce: "the-nonce", Verifier: strings.Repeat("v", 43)})
	if !errors.Is(err, microsoft.ErrExchangeFailed) {
		t.Errorf("Exchange() with another sign-in's verifier = %v, want ErrExchangeFailed", err)
	}
}

func guest(m map[string]any) {
	// A guest is homed in another tenant, and the token says so in idp.
	m["idp"] = "https://sts.windows.net/" + otherTenant + "/"
}

func TestAGuestOfTheHomeTenantIsNotTrustedByTenant(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	for _, idp := range []string{"https://sts.windows.net/" + otherTenant + "/", "live.com", p.Issuer(otherTenant)} {
		got := trustOf(t, p, c, func(m map[string]any) { m["idp"] = idp; m["email"] = "ana@example.edu" })
		if got.EmailTrusted {
			t.Errorf("guest from %s: %+v, want the email untrusted", idp, got)
		}
		got = trustOf(t, p, c, func(m map[string]any) { m["idp"] = idp; m["preferred_username"] = "ana@example.edu" })
		if got.Email != "" || got.EmailTrusted {
			t.Errorf("guest from %s without email: %+v, want its username not taken for an email", idp, got)
		}
	}
}

func TestAMemberOfTheHomeTenantIsTrusted(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	for name, edit := range map[string]func(map[string]any){
		"no idp":           func(m map[string]any) {},
		"idp equal to iss": func(m map[string]any) { m["idp"] = p.Issuer(homeTenant) },
	} {
		got := trustOf(t, p, c, func(m map[string]any) { edit(m); m["email"] = "ana@example.edu" })
		if !got.EmailTrusted {
			t.Errorf("%s: %+v, want a member's email trusted", name, got)
		}
		got = trustOf(t, p, c, func(m map[string]any) { edit(m); m["preferred_username"] = "ion@example.edu" })
		if !got.EmailTrusted || got.Email != "ion@example.edu" {
			t.Errorf("%s without email: %+v, want the member's username as a trusted email", name, got)
		}
	}
}

func TestAGuestWithAVerifiedDomainIsTrusted(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	got := trustOf(t, p, c, func(m map[string]any) { guest(m); m["email"] = "ana@example.edu"; m["xms_edov"] = true })
	if !got.EmailTrusted {
		t.Errorf("guest with xms_edov: %+v, want the email trusted", got)
	}
}

func TestAnIdentityProviderOutOfBoundsIsRefused(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	for _, idp := range []string{strings.Repeat("a", 256), "live.com\n", "live\x00.com"} {
		claims := p.Claims(clientID, homeTenant, objectID, "the-nonce")
		claims["idp"] = idp
		if _, err := exchange(t, c, p, claims); !errors.Is(err, microsoft.ErrInvalidToken) {
			t.Errorf("idp %.20q: Exchange() = %v, want ErrInvalidToken", idp, err)
		}
	}
}
