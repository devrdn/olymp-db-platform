package google_test

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/connector"
	"github.com/devrdn/olymp-db-platform/backend/internal/federation"
	"github.com/devrdn/olymp-db-platform/backend/internal/google"
	"github.com/devrdn/olymp-db-platform/backend/internal/google/googletest"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
)

const (
	clientID    = googletest.ClientID
	otherClient = "999999999999-zyxwvutsrqponmlk.apps.example.test"
	subject     = "110169484474386276334"
	redirectURL = "https://contest.example.edu/api/v1/auth/google/callback"
	verifier    = "vvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvv"
)

func newClient(t *testing.T, p *googletest.Provider, edit func(*google.Config)) *google.Client {
	t.Helper()
	cfg := google.Config{
		ClientID: clientID, ClientSecret: googletest.ClientSecret, RedirectURL: redirectURL,
		AuthURL: p.URL + "/auth", TokenURL: p.URL + "/token", KeysURL: p.URL + "/certs",
	}
	if edit != nil {
		edit(&cfg)
	}
	c, err := google.New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return c
}

// exchange has the provider answer one code with a token of claims signed
// by its published key, and exchanges it as the callback would.
func exchange(t *testing.T, c *google.Client, p *googletest.Provider, claims map[string]any) (federation.Claims, error) {
	t.Helper()
	return exchangeToken(t, c, p, p.Sign(t, claims))
}

func exchangeToken(t *testing.T, c *google.Client, p *googletest.Provider, idToken string) (federation.Claims, error) {
	t.Helper()
	p.Issue("the-code", idToken, googletest.Challenge(verifier))
	return c.Exchange(t.Context(), "the-code", connector.State{Nonce: "the-nonce", Verifier: verifier})
}

// with exchanges a valid token after edit has changed its claims.
func with(t *testing.T, edit func(map[string]any), configure func(*google.Config)) federation.Claims {
	t.Helper()
	p := googletest.NewProvider(t)
	c := newClient(t, p, configure)
	claims := p.Claims(clientID, subject, "the-nonce")
	edit(claims)
	got, err := exchange(t, c, p, claims)
	if err != nil {
		t.Fatalf("Exchange() = %v", err)
	}
	return got
}

func TestTheAuthorizeURLAsksWithPKCEAndANonce(t *testing.T) {
	p := googletest.NewProvider(t)
	c := newClient(t, p, nil)
	st := connector.State{State: "the-state", Nonce: "the-nonce", Verifier: verifier}

	raw := c.AuthCodeURL(st)

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != p.URL+"/auth" {
		t.Errorf("authorize endpoint = %s", got)
	}
	want := map[string]string{
		"client_id": clientID, "redirect_uri": redirectURL, "response_type": "code",
		"scope": "openid email profile", "state": "the-state", "nonce": "the-nonce",
		"code_challenge_method": "S256", "code_challenge": googletest.Challenge(verifier),
		"prompt": "select_account",
	}
	for key, value := range want {
		if got := u.Query().Get(key); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
	if u.Query().Has("client_secret") || strings.Contains(raw, googletest.ClientSecret) {
		t.Error("the authorize URL carries the client secret")
	}
}

func TestTheDefaultsAreGoogles(t *testing.T) {
	c, err := google.New(google.Config{ClientID: clientID, ClientSecret: googletest.ClientSecret, RedirectURL: redirectURL})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	raw := c.AuthCodeURL(connector.State{State: "s", Nonce: "n", Verifier: verifier})
	if !strings.HasPrefix(raw, google.DefaultAuthURL+"?") {
		t.Errorf("authorize URL = %s, want it under %s", raw, google.DefaultAuthURL)
	}
	if google.Issuer != "https://accounts.google.com" ||
		google.DefaultTokenURL != "https://oauth2.googleapis.com/token" ||
		google.DefaultKeysURL != "https://www.googleapis.com/oauth2/v3/certs" {
		t.Error("an endpoint constant is not Google's")
	}
}

func TestStartingUpContactsNobody(t *testing.T) {
	p := googletest.NewProvider(t)
	c := newClient(t, p, nil)
	_ = c.AuthCodeURL(connector.State{State: "s", Nonce: "n", Verifier: verifier})

	if n := p.Requests(); n != 0 {
		t.Errorf("the provider was asked %d times before any callback, want none, so its outage cannot stop the boot", n)
	}
}

func TestTheExchangeSendsTheVerifierAndTheSecret(t *testing.T) {
	p := googletest.NewProvider(t)
	c := newClient(t, p, nil)

	if _, err := exchange(t, c, p, p.Claims(clientID, subject, "the-nonce")); err != nil {
		t.Fatalf("Exchange() = %v", err)
	}
	form := p.LastTokenRequest()
	for key, value := range map[string]string{
		"grant_type": "authorization_code", "code": "the-code", "code_verifier": verifier,
		"client_id": clientID, "client_secret": googletest.ClientSecret, "redirect_uri": redirectURL,
	} {
		if got := form.Get(key); got != value {
			t.Errorf("token request %s = %q, want %q", key, got, value)
		}
	}
}

func TestAVerifiedTokenIsTheSubjectAtGoogle(t *testing.T) {
	got := with(t, func(m map[string]any) {}, nil)

	want := federation.Claims{
		Provider: users.ProviderGoogle, Issuer: "https://accounts.google.com", Subject: subject,
		Email: "ana@example.edu", Name: "Ana Popescu", LoginHint: "ana@example.edu",
	}
	if got != want {
		t.Errorf("claims = %+v\nwant %+v", got, want)
	}
}

func TestBothIssuerSpellingsNameOneIdentity(t *testing.T) {
	var got [2]federation.Claims
	for i, iss := range []string{"https://accounts.google.com", "accounts.google.com"} {
		got[i] = with(t, func(m map[string]any) { m["iss"] = iss }, nil)
	}
	if got[0].Issuer != google.Issuer || got[1].Issuer != google.Issuer || got[0].Subject != got[1].Subject {
		t.Errorf("claims = %+v and %+v, want one identity under the canonical issuer", got[0], got[1])
	}
}

func TestAnotherIssuerIsRefused(t *testing.T) {
	p := googletest.NewProvider(t)
	c := newClient(t, p, nil)
	for _, iss := range []string{"https://accounts.example.com", "http://accounts.google.com", "https://accounts.google.com/", ""} {
		claims := p.Claims(clientID, subject, "the-nonce")
		claims["iss"] = iss
		if _, err := exchange(t, c, p, claims); !errors.Is(err, connector.ErrInvalidToken) {
			t.Errorf("iss %q: Exchange() = %v, want ErrInvalidToken", iss, err)
		}
	}
}

func TestAnotherAudienceIsRefused(t *testing.T) {
	p := googletest.NewProvider(t)
	c := newClient(t, p, nil)

	if _, err := exchange(t, c, p, p.Claims(otherClient, subject, "the-nonce")); !errors.Is(err, connector.ErrInvalidToken) {
		t.Errorf("Exchange() of a token for another application = %v, want ErrInvalidToken", err)
	}
}

func TestAWrongNonceIsRefused(t *testing.T) {
	p := googletest.NewProvider(t)
	c := newClient(t, p, nil)

	for _, nonce := range []string{"another-nonce", ""} {
		if _, err := exchange(t, c, p, p.Claims(clientID, subject, nonce)); !errors.Is(err, connector.ErrInvalidToken) {
			t.Errorf("nonce %q: Exchange() = %v, want ErrInvalidToken", nonce, err)
		}
	}
}

func TestAnExpiredTokenIsRefused(t *testing.T) {
	p := googletest.NewProvider(t)
	c := newClient(t, p, nil)
	claims := p.Claims(clientID, subject, "the-nonce")
	claims["exp"] = time.Now().Add(-time.Minute).Unix()

	if _, err := exchange(t, c, p, claims); !errors.Is(err, connector.ErrInvalidToken) {
		t.Errorf("Exchange() of an expired token = %v, want ErrInvalidToken", err)
	}
}

func TestAnUnpublishedKeyIsRefused(t *testing.T) {
	p := googletest.NewProvider(t)
	c := newClient(t, p, nil)

	if _, err := exchangeToken(t, c, p, p.SignWithUnpublishedKey(t, p.Claims(clientID, subject, "the-nonce"))); !errors.Is(err, connector.ErrInvalidToken) {
		t.Errorf("Exchange() of a forged token = %v, want ErrInvalidToken", err)
	}
}

func TestATokenWithoutASubjectIsRefused(t *testing.T) {
	p := googletest.NewProvider(t)
	c := newClient(t, p, nil)
	claims := p.Claims(clientID, subject, "the-nonce")
	delete(claims, "sub")

	if _, err := exchange(t, c, p, claims); !errors.Is(err, federation.ErrInvalidClaims) {
		t.Errorf("Exchange() = %v, want federation.ErrInvalidClaims", err)
	}
}

func TestAKeyOutageIsKeysUnavailable(t *testing.T) {
	p := googletest.NewProvider(t)
	c := newClient(t, p, nil)
	p.FailKeys(http.StatusServiceUnavailable)

	if _, err := exchange(t, c, p, p.Claims(clientID, subject, "the-nonce")); !errors.Is(err, connector.ErrKeysUnavailable) {
		t.Errorf("Exchange() while the keys are down = %v, want ErrKeysUnavailable", err)
	}
}

func TestARefusedCodeIsAFailedExchange(t *testing.T) {
	p := googletest.NewProvider(t)
	c := newClient(t, p, nil)

	_, err := c.Exchange(t.Context(), "a-code-never-issued", connector.State{Nonce: "n", Verifier: verifier})
	if !errors.Is(err, connector.ErrExchangeFailed) {
		t.Errorf("Exchange() of an unknown code = %v, want ErrExchangeFailed", err)
	}
}

func TestAVerifiedEmailIsTrusted(t *testing.T) {
	for name, tc := range map[string]struct {
		email string
		hd    string
	}{
		"gmail":             {email: "ana@gmail.com"},
		"googlemail, cased": {email: "ana@GoogleMail.com"},
		"hosted domain":     {email: "ana@example.edu", hd: "example.edu"},
	} {
		for form, verified := range map[string]any{"boolean": true, "string": "true"} {
			t.Run(name+", "+form, func(t *testing.T) {
				got := with(t, func(m map[string]any) {
					m["email"], m["email_verified"] = tc.email, verified
					if tc.hd != "" {
						m["hd"] = tc.hd
					}
				}, nil)
				if !got.EmailTrusted || got.Email != tc.email {
					t.Errorf("claims = %+v, want %s trusted", got, tc.email)
				}
			})
		}
	}
}

func TestAnUnverifiedEmailIsNotTrusted(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"false":  func(m map[string]any) { m["email"], m["email_verified"] = "ana@gmail.com", false },
		"string": func(m map[string]any) { m["email"], m["email_verified"] = "ana@gmail.com", "false" },
		"absent": func(m map[string]any) { m["email"] = "ana@gmail.com"; delete(m, "email_verified") },
		"hosted, unverified": func(m map[string]any) {
			m["email"], m["email_verified"], m["hd"] = "ana@example.edu", false, "example.edu"
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := with(t, edit, nil); got.EmailTrusted {
				t.Errorf("claims = %+v, want the email not trusted", got)
			}
		})
	}
}

// Google calls email_verified authoritative only for Gmail addresses and
// hosted domains; a consumer account may carry any verified address.
func TestAVerifiedConsumerAddressOutsideGmailIsNotTrusted(t *testing.T) {
	got := with(t, func(m map[string]any) { m["email"], m["email_verified"] = "ana@example.edu", true }, nil)

	if got.EmailTrusted || got.Email != "ana@example.edu" {
		t.Errorf("claims = %+v, want the address kept and not trusted", got)
	}
}

func TestTheHomeDomainIsTheHostedDomainIgnoringCase(t *testing.T) {
	got := with(t, func(m map[string]any) { m["hd"] = "Example.EDU" }, func(c *google.Config) { c.HomeDomain = "example.edu" })

	if !got.HomeDirectory {
		t.Errorf("claims = %+v, want the home directory", got)
	}
}

func TestTheHomeDirectoryNeedsAVerifiedEmail(t *testing.T) {
	got := with(t, func(m map[string]any) { m["hd"], m["email_verified"] = "example.edu", false }, func(c *google.Config) { c.HomeDomain = "example.edu" })

	if got.HomeDirectory {
		t.Errorf("claims = %+v, want no home directory for an unverified email", got)
	}
}

func TestAnotherOrNoHostedDomainIsNotHome(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(map[string]any)
		home string
	}{
		"another domain":      {func(m map[string]any) { m["hd"] = "other.example" }, "example.edu"},
		"no hosted domain":    {func(m map[string]any) {}, "example.edu"},
		"no home domain set":  {func(m map[string]any) { m["hd"] = "example.edu" }, ""},
		"a longer domain":     {func(m map[string]any) { m["hd"] = "mail.example.edu" }, "example.edu"},
		"a gmail address":     {func(m map[string]any) { m["email"] = "ana@gmail.com" }, "example.edu"},
		"a suffix of home":    {func(m map[string]any) { m["hd"] = "xexample.edu" }, "example.edu"},
		"an empty hosted one": {func(m map[string]any) { m["hd"] = "" }, "example.edu"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := with(t, tc.edit, func(c *google.Config) { c.HomeDomain = tc.home }); got.HomeDirectory {
				t.Errorf("claims = %+v, want no home directory", got)
			}
		})
	}
}

func TestTheHomeDirectoryNeedsEmailVerifiedPresent(t *testing.T) {
	got := with(t, func(m map[string]any) { m["hd"] = "example.edu"; delete(m, "email_verified") }, func(c *google.Config) { c.HomeDomain = "example.edu" })

	if got.HomeDirectory {
		t.Errorf("claims = %+v, want no home directory without email_verified", got)
	}
}

// Unicode case folding takes U+212A KELVIN SIGN for k and U+017F for s, so
// only an ASCII hosted domain may be compared with the home domain.
func TestAHostedDomainThatIsNotAnASCIIDomainIsRefused(t *testing.T) {
	p := googletest.NewProvider(t)
	c := newClient(t, p, func(c *google.Config) { c.HomeDomain = "kelvin.school.example.edu" })

	for name, hd := range map[string]string{
		"a kelvin sign":      "\u212aelvin.school.example.edu",
		"a long s":           "kelvin.\u017fchool.example.edu",
		"a replacement char": "kelvin.school.example\ufffd.edu",
		"one label":          "localhost",
		"a trailing dot":     "kelvin.school.example.edu.",
		"an underscore":      "kelvin_school.example.edu",
		"a space":            "kelvin school.example.edu",
		"a leading hyphen":   "-kelvin.school.example.edu",
	} {
		claims := p.Claims(clientID, subject, "the-nonce")
		claims["hd"] = hd
		if _, err := exchange(t, c, p, claims); !errors.Is(err, connector.ErrInvalidToken) {
			t.Errorf("%s: Exchange() = %v, want ErrInvalidToken", name, err)
		}
	}
}

func TestAnOverlongHostedDomainIsRefused(t *testing.T) {
	p := googletest.NewProvider(t)
	c := newClient(t, p, nil)

	for name, hd := range map[string]string{
		"too long":   strings.Repeat("a.", 126) + "ab",
		"a control":  "example.edu\n",
		"a bell":     "exam\x07ple.edu",
		"a line sep": "example\u2028.edu",
	} {
		claims := p.Claims(clientID, subject, "the-nonce")
		claims["hd"] = hd
		if _, err := exchange(t, c, p, claims); !errors.Is(err, connector.ErrInvalidToken) {
			t.Errorf("%s: Exchange() = %v, want ErrInvalidToken", name, err)
		}
	}
	claims := p.Claims(clientID, subject, "the-nonce")
	claims["hd"] = strings.Repeat("a.", 126) + "a"
	if _, err := exchange(t, c, p, claims); err != nil {
		t.Errorf("a hosted domain of 253 bytes: Exchange() = %v", err)
	}
}

func TestAnOverlongNameIsCutToItsBound(t *testing.T) {
	got := with(t, func(m map[string]any) {
		m["name"] = strings.Repeat("ă", federation.MaxNameLength)
		m["email"] = strings.Repeat("u", 2*federation.MaxLoginHintLength) + "@example.edu"
	}, nil)

	if len(got.Name) > federation.MaxNameLength || len(got.LoginHint) > federation.MaxLoginHintLength {
		t.Errorf("name %d bytes, hint %d bytes; want them within federation's bounds", len(got.Name), len(got.LoginHint))
	}
	if !strings.HasPrefix(got.Name, "ăă") || strings.ContainsRune(got.Name, '\uFFFD') {
		t.Errorf("name of %d bytes cut mid-letter or lost", len(got.Name))
	}
}

func TestNewRefusesAnIncompleteConfiguration(t *testing.T) {
	base := google.Config{ClientID: clientID, ClientSecret: googletest.ClientSecret, RedirectURL: redirectURL}
	for name, edit := range map[string]func(*google.Config){
		"no client ID":             func(c *google.Config) { c.ClientID = "" },
		"no secret":                func(c *google.Config) { c.ClientSecret = "" },
		"no redirect":              func(c *google.Config) { c.RedirectURL = "" },
		"a relative redirect":      func(c *google.Config) { c.RedirectURL = "/api/v1/auth/google/callback" },
		"an uppercase home":        func(c *google.Config) { c.HomeDomain = "Example.edu" },
		"a home that is no domain": func(c *google.Config) { c.HomeDomain = "not a domain" },
		"a relative keys URL":      func(c *google.Config) { c.KeysURL = "certs" },
	} {
		cfg := base
		edit(&cfg)
		if _, err := google.New(cfg); err == nil {
			t.Errorf("%s: New() accepted %+v", name, cfg)
		}
	}
	cfg := base
	cfg.HomeDomain = "example.edu"
	if _, err := google.New(cfg); err != nil {
		t.Errorf("New() of a complete configuration = %v", err)
	}
}
