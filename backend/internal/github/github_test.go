package github_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/connector"
	"github.com/devrdn/db-contest/backend/internal/federation"
	"github.com/devrdn/db-contest/backend/internal/github"
	"github.com/devrdn/db-contest/backend/internal/github/githubtest"
	"github.com/devrdn/db-contest/backend/internal/users"
)

const (
	clientID    = githubtest.ClientID
	redirectURL = githubtest.RedirectURL
	verifier    = "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv"
)

func newClient(t *testing.T, p *githubtest.Provider) *github.Client {
	t.Helper()
	c, err := github.New(github.Config{
		ClientID: clientID, ClientSecret: githubtest.ClientSecret, RedirectURL: redirectURL,
		WebURL: p.URL, APIURL: p.URL,
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return c
}

func person() map[string]any {
	return map[string]any{"id": 583231, "login": "ana-p", "name": "Ana Popescu"}
}

func primary(address string, verified bool) map[string]any {
	return map[string]any{"email": address, "primary": true, "verified": verified}
}

func exchange(t *testing.T, c *github.Client, p *githubtest.Provider, user map[string]any, emails []map[string]any) (federation.Claims, error) {
	t.Helper()
	p.Issue("the-code", githubtest.Challenge(verifier), user, emails)
	return c.Exchange(t.Context(), "the-code", connector.State{Verifier: verifier})
}

func claimsOf(t *testing.T, user map[string]any, emails []map[string]any) federation.Claims {
	t.Helper()
	p := githubtest.NewProvider(t)
	got, err := exchange(t, newClient(t, p), p, user, emails)
	if err != nil {
		t.Fatalf("Exchange() = %v", err)
	}
	return got
}

func TestTheAuthorizeURLAsksForTheEmailScopeWithPKCE(t *testing.T) {
	p := githubtest.NewProvider(t)
	c := newClient(t, p)

	raw := c.AuthCodeURL(connector.State{State: "the-state", Nonce: "unused", Verifier: verifier})

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != p.URL+"/login/oauth/authorize" {
		t.Errorf("authorize endpoint = %s", got)
	}
	want := map[string]string{
		"client_id": clientID, "redirect_uri": redirectURL, "response_type": "code",
		"scope": "user:email", "state": "the-state",
		"code_challenge_method": "S256", "code_challenge": githubtest.Challenge(verifier),
		"prompt": "select_account",
	}
	for key, value := range want {
		if got := u.Query().Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
	if u.Query().Has("nonce") {
		t.Error("the authorize URL carries a nonce; GitHub is not OpenID Connect")
	}
	if u.Query().Has("client_secret") || strings.Contains(raw, githubtest.ClientSecret) {
		t.Error("the authorize URL carries the client secret")
	}
}

func TestTheDefaultsAreGitHubs(t *testing.T) {
	c, err := github.New(github.Config{ClientID: clientID, ClientSecret: githubtest.ClientSecret, RedirectURL: redirectURL})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	raw := c.AuthCodeURL(connector.State{State: "s", Verifier: verifier})
	if !strings.HasPrefix(raw, "https://github.com/login/oauth/authorize?") {
		t.Errorf("authorize URL = %s", raw)
	}
	if github.Issuer != "https://github.com" || github.DefaultWebURL != "https://github.com" ||
		github.DefaultAPIURL != "https://api.github.com" {
		t.Error("a constant is not GitHub's")
	}
}

func TestStartingUpContactsNobody(t *testing.T) {
	p := githubtest.NewProvider(t)
	c := newClient(t, p)
	_ = c.AuthCodeURL(connector.State{State: "s", Verifier: verifier})

	if n := p.Requests(); n != 0 {
		t.Errorf("the provider was asked %d times before any callback, want none", n)
	}
}

func TestTheExchangeSendsTheVerifierAndTheSecret(t *testing.T) {
	p := githubtest.NewProvider(t)
	if _, err := exchange(t, newClient(t, p), p, person(), nil); err != nil {
		t.Fatalf("Exchange() = %v", err)
	}
	form := p.LastRequest("/login/oauth/access_token").PostForm
	for key, value := range map[string]string{
		"grant_type": "authorization_code", "code": "the-code", "code_verifier": verifier,
		"client_id": clientID, "client_secret": githubtest.ClientSecret, "redirect_uri": redirectURL,
	} {
		if got := form.Get(key); got != value {
			t.Errorf("token request %s = %q, want %q", key, got, value)
		}
	}
}

func TestTheAPICallsCarryTheTokenAndTheVersion(t *testing.T) {
	p := githubtest.NewProvider(t)
	if _, err := exchange(t, newClient(t, p), p, person(), []map[string]any{primary("ana@example.edu", true)}); err != nil {
		t.Fatalf("Exchange() = %v", err)
	}
	for _, path := range []string{"/user", "/user/emails"} {
		r := p.LastRequest(path)
		if r == nil {
			t.Fatalf("%s was never read", path)
		}
		for header, value := range map[string]string{
			"Authorization": "Bearer gho_the-code", "Accept": "application/vnd.github+json",
			"X-GitHub-Api-Version": "2022-11-28", "User-Agent": "db-contest",
		} {
			if got := r.Header.Get(header); got != value {
				t.Errorf("%s %s = %q, want %q", path, header, got, value)
			}
		}
	}
}

func TestTheIdentityIsTheNumericUserID(t *testing.T) {
	got := claimsOf(t, person(), []map[string]any{primary("ana@example.edu", true)})

	want := federation.Claims{
		Provider: users.ProviderGitHub, Issuer: "https://github.com", Subject: "583231",
		Email: "ana@example.edu", EmailTrusted: true, Name: "Ana Popescu", LoginHint: "ana-p",
	}
	if got != want {
		t.Errorf("claims = %+v\nwant %+v", got, want)
	}
}

func TestAnIDBeyondFloatPrecisionIsKeptExactly(t *testing.T) {
	user := person()
	user["id"] = json.Number("9007199254740993")

	if got := claimsOf(t, user, nil); got.Subject != "9007199254740993" {
		t.Errorf("Subject = %q, want 9007199254740993", got.Subject)
	}
}

func TestAMissingZeroOrFractionalIDIsRefused(t *testing.T) {
	cases := map[string]any{
		"missing": nil, "zero": 0, "negative": -5, "fractional": json.Number("1.5"),
		"exponent": json.Number("1e3"), "quoted": "583231", "null": json.RawMessage("null"),
		"beyond int64": json.Number("9223372036854775808"),
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			user := person()
			if name == "missing" {
				delete(user, "id")
			} else {
				user["id"] = id
			}
			p := githubtest.NewProvider(t)
			if _, err := exchange(t, newClient(t, p), p, user, nil); !errors.Is(err, federation.ErrInvalidClaims) {
				t.Errorf("Exchange() = %v, want ErrInvalidClaims", err)
			}
		})
	}
}

func TestTheVerifiedPrimaryEmailIsTrusted(t *testing.T) {
	got := claimsOf(t, person(), []map[string]any{
		{"email": "other@example.org", "primary": false, "verified": true},
		primary("ana@example.edu", true),
	})
	if got.Email != "ana@example.edu" || !got.EmailTrusted {
		t.Errorf("Email = %q trusted %v, want the primary, trusted", got.Email, got.EmailTrusted)
	}
}

func TestAnUnverifiedPrimaryEmailIsNotTrusted(t *testing.T) {
	got := claimsOf(t, person(), []map[string]any{
		{"email": "other@example.org", "primary": false, "verified": true},
		primary("ana@example.edu", false),
	})
	if got.Email != "ana@example.edu" || got.EmailTrusted {
		t.Errorf("Email = %q trusted %v, want the primary, not trusted", got.Email, got.EmailTrusted)
	}
}

func TestAPrimaryWithoutAnAddressIsNotTrusted(t *testing.T) {
	got := claimsOf(t, person(), []map[string]any{primary("", true)})
	if got.Email != "" || got.EmailTrusted {
		t.Errorf("Email = %q trusted %v, want none", got.Email, got.EmailTrusted)
	}
}

func TestWithoutAPrimaryEmailTheSignInCarriesNone(t *testing.T) {
	got := claimsOf(t, person(), []map[string]any{{"email": "x@example.org", "primary": false, "verified": true}})
	if got.Email != "" || got.EmailTrusted {
		t.Errorf("Email = %q trusted %v, want none", got.Email, got.EmailTrusted)
	}
}

func TestWithoutItsEmailsTheSignInCarriesNoEmail(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		p := githubtest.NewProvider(t)
		p.EmailsStatus(status)
		got, err := exchange(t, newClient(t, p), p, person(), []map[string]any{primary("ana@example.edu", true)})
		if err != nil {
			t.Fatalf("status %d: Exchange() = %v", status, err)
		}
		if got.Email != "" || got.EmailTrusted || got.Subject != "583231" {
			t.Errorf("status %d: claims = %+v, want the identity without an email", status, got)
		}
	}
}

func TestAFailingEmailsEndpointIsAFailedExchange(t *testing.T) {
	p := githubtest.NewProvider(t)
	p.EmailsStatus(http.StatusInternalServerError)

	if _, err := exchange(t, newClient(t, p), p, person(), nil); !errors.Is(err, connector.ErrExchangeFailed) {
		t.Errorf("Exchange() = %v, want ErrExchangeFailed", err)
	}
}

func TestAFailingUserEndpointIsAFailedExchange(t *testing.T) {
	p := githubtest.NewProvider(t)
	c, err := github.New(github.Config{
		ClientID: clientID, ClientSecret: githubtest.ClientSecret, RedirectURL: redirectURL,
		WebURL: p.URL, APIURL: p.URL + "/elsewhere",
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if _, err := exchange(t, c, p, person(), nil); !errors.Is(err, connector.ErrExchangeFailed) {
		t.Errorf("Exchange() = %v, want ErrExchangeFailed", err)
	}
}

func TestARefusedCodeIsAFailedExchange(t *testing.T) {
	p := githubtest.NewProvider(t)
	c := newClient(t, p)
	for name, call := range map[string]func() error{
		"unknown code": func() error {
			_, err := c.Exchange(t.Context(), "never-issued", connector.State{Verifier: verifier})
			return err
		},
		"wrong verifier": func() error {
			p.Issue("a-code", githubtest.Challenge(verifier), person(), nil)
			_, err := c.Exchange(t.Context(), "a-code", connector.State{Verifier: "another-verifier"})
			return err
		},
	} {
		if err := call(); !errors.Is(err, connector.ErrExchangeFailed) {
			t.Errorf("%s: Exchange() = %v, want ErrExchangeFailed", name, err)
		}
	}
}

func TestACodeWorksOnce(t *testing.T) {
	p := githubtest.NewProvider(t)
	c := newClient(t, p)
	if _, err := exchange(t, c, p, person(), nil); err != nil {
		t.Fatalf("first Exchange() = %v", err)
	}
	if _, err := c.Exchange(t.Context(), "the-code", connector.State{Verifier: verifier}); !errors.Is(err, connector.ErrExchangeFailed) {
		t.Errorf("second Exchange() = %v, want ErrExchangeFailed", err)
	}
}

func TestADisplayNameAndLoginAreCutToTheirBounds(t *testing.T) {
	user := person()
	user["name"] = strings.Repeat("n", federation.MaxNameLength+50)
	user["login"] = strings.Repeat("l", federation.MaxLoginHintLength+50)

	got := claimsOf(t, user, nil)
	if len(got.Name) > federation.MaxNameLength || len(got.LoginHint) > federation.MaxLoginHintLength {
		t.Errorf("name %d and login %d bytes exceed the bounds", len(got.Name), len(got.LoginHint))
	}
}

func TestGitHubNeverVouchesForTheHomeDirectory(t *testing.T) {
	got := claimsOf(t, person(), []map[string]any{primary("ana@example.edu", true)})
	if got.HomeDirectory {
		t.Error("HomeDirectory = true, want false: GitHub vouches for no organisation's directory")
	}
}

func TestNewRefusesAnIncompleteConfiguration(t *testing.T) {
	ok := github.Config{ClientID: clientID, ClientSecret: githubtest.ClientSecret, RedirectURL: redirectURL}
	cases := map[string]func(*github.Config){
		"no client ID":     func(c *github.Config) { c.ClientID = "" },
		"no secret":        func(c *github.Config) { c.ClientSecret = "" },
		"no redirect":      func(c *github.Config) { c.RedirectURL = "" },
		"relative to host": func(c *github.Config) { c.RedirectURL = "/api/v1/auth/github/callback" },
		"relative web URL": func(c *github.Config) { c.WebURL = "github.com" },
		"relative API URL": func(c *github.Config) { c.APIURL = "api.github.com" },
	}
	for name, edit := range cases {
		cfg := ok
		edit(&cfg)
		if _, err := github.New(cfg); err == nil {
			t.Errorf("%s: New() accepted the configuration", name)
		}
	}
	if _, err := github.New(ok); err != nil {
		t.Errorf("New() of a complete configuration = %v", err)
	}
}
