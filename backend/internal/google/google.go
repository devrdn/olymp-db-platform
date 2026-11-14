// Package google is the Google connector of external sign-in: the OpenID
// Connect authorisation-code flow with PKCE against Google, the checks on
// the ID token it returns, and the rule deciding whether Google vouches for
// the email in it. It hands federation the verified claims, keyed on the
// token's subject under Google's issuer.
//
// It does not decide which account the person enters (federation does),
// serve HTTP or throttle callers (internal/api does), or open the session
// (auth does).
package google

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/devrdn/olymp-db-platform/backend/internal/connector"
	"github.com/devrdn/olymp-db-platform/backend/internal/federation"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
)

// Issuer is the canonical issuer of Google's ID tokens; Google also writes
// it without the scheme, and both name one identity.
const Issuer = "https://accounts.google.com"

// Google's endpoints; Config replaces them only in tests.
const (
	DefaultAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	DefaultTokenURL = "https://oauth2.googleapis.com/token" // #nosec G101 -- an endpoint address, not a credential
	DefaultKeysURL  = "https://www.googleapis.com/oauth2/v3/certs"
)

// bareIssuer is the issuer's other spelling.
const bareIssuer = "accounts.google.com"

// maxHostedDomainLength bounds hd: a domain name is at most 253 bytes.
const maxHostedDomainLength = 253

// domain is a lowercase DNS name of two or more labels.
var domain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

type Config struct {
	ClientID, ClientSecret string
	// HomeDomain is the installation's own Google Workspace domain
	// (GOOGLE_HOME_DOMAIN), lowercase; empty means no domain is its own.
	HomeDomain string
	// RedirectURL is the callback's absolute address, as registered.
	RedirectURL string
	// AuthURL, TokenURL and KeysURL empty are the Default values.
	AuthURL, TokenURL, KeysURL string
	// HTTPClient nil is connector.NewHTTPClient().
	HTTPClient *http.Client
	// Now nil is time.Now.
	Now func() time.Time
}

// Client runs the flow for one OAuth client. It contacts Google only during
// a callback: the signing keys are fetched on the first token and cached.
type Client struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	keys     *connector.KeySet
	http     *http.Client
	home     string
}

func New(cfg Config) (*Client, error) {
	if cfg.AuthURL == "" {
		cfg.AuthURL = DefaultAuthURL
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = DefaultTokenURL
	}
	if cfg.KeysURL == "" {
		cfg.KeysURL = DefaultKeysURL
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = connector.NewHTTPClient()
	}

	keys := connector.NewKeySet(cfg.KeysURL, cfg.HTTPClient, cfg.Now)
	// Google writes its issuer two ways, which go-oidc cannot check; Exchange
	// checks both.
	verifier := oidc.NewVerifier("", keys, &oidc.Config{
		ClientID:             cfg.ClientID,
		SkipIssuerCheck:      true,
		SupportedSigningAlgs: []string{oidc.RS256},
		Now:                  cfg.Now,
	})
	return &Client{
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
			Endpoint: oauth2.Endpoint{
				AuthURL:   cfg.AuthURL,
				TokenURL:  cfg.TokenURL,
				AuthStyle: oauth2.AuthStyleInParams,
			},
		},
		verifier: verifier,
		keys:     keys,
		http:     cfg.HTTPClient,
		home:     cfg.HomeDomain,
	}, nil
}

func (cfg Config) validate() error {
	switch {
	case cfg.ClientID == "" || cfg.ClientSecret == "":
		return errors.New("google: a client ID and its secret are both required")
	case !absolute(cfg.RedirectURL):
		return fmt.Errorf("google: the redirect URL %q is not an absolute address", cfg.RedirectURL)
	case !absolute(cfg.AuthURL) || !absolute(cfg.TokenURL) || !absolute(cfg.KeysURL):
		return errors.New("google: the authorize, token and keys URLs must be absolute addresses")
	case cfg.HomeDomain != "" && (len(cfg.HomeDomain) > maxHostedDomainLength || !domain.MatchString(cfg.HomeDomain)):
		return fmt.Errorf("google: the home domain %q is not a lowercase domain", cfg.HomeDomain)
	}
	return nil
}

func absolute(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// AuthCodeURL is where the browser is sent to sign in, asking for st's state
// and nonce with st's PKCE challenge.
func (c *Client) AuthCodeURL(st connector.State) string {
	// select_account: on a shared machine a live Google session would
	// otherwise sign the next person in without asking.
	return c.oauth.AuthCodeURL(st.State, oauth2.S256ChallengeOption(st.Verifier), oidc.Nonce(st.Nonce),
		oauth2.SetAuthURLParam("prompt", "select_account"))
}

// idTokenClaims is what the connector reads of an ID token.
type idTokenClaims struct {
	Issuer        string          `json:"iss"`
	Subject       string          `json:"sub"`
	Email         string          `json:"email"`
	EmailVerified json.RawMessage `json:"email_verified"`
	Name          string          `json:"name"`
	// HostedDomain is present only for a Google Workspace account.
	HostedDomain string `json:"hd"`
}

// Exchange trades the code for an ID token, verifies it against st, and
// returns the claims federation resolves. The signature is checked against
// the published keys (connector.KeySet), the audience and expiry by go-oidc,
// and the issuer and nonce here.
func (c *Client) Exchange(ctx context.Context, code string, st connector.State) (federation.Claims, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.http)
	token, err := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return federation.Claims{}, fmt.Errorf("%w: %w", connector.ErrExchangeFailed, err)
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		return federation.Claims{}, fmt.Errorf("%w: the answer carries no ID token", connector.ErrExchangeFailed)
	}
	if err := c.keys.Prepare(ctx, raw); err != nil {
		return federation.Claims{}, err
	}
	idToken, err := c.verifier.Verify(ctx, raw)
	if err != nil {
		return federation.Claims{}, fmt.Errorf("%w: %w", connector.ErrInvalidToken, err)
	}
	var claims idTokenClaims
	if err := idToken.Claims(&claims); err != nil {
		return federation.Claims{}, fmt.Errorf("%w: %w", connector.ErrInvalidToken, err)
	}

	if claims.Issuer != Issuer && claims.Issuer != bareIssuer {
		return federation.Claims{}, fmt.Errorf("%w: the issuer is not Google", connector.ErrInvalidToken)
	}
	if st.Nonce == "" || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(st.Nonce)) != 1 {
		return federation.Claims{}, fmt.Errorf("%w: the nonce is not this sign-in's", connector.ErrInvalidToken)
	}
	if claims.HostedDomain != "" && (len(claims.HostedDomain) > maxHostedDomainLength ||
		!domain.MatchString(asciiLower(claims.HostedDomain))) {
		return federation.Claims{}, fmt.Errorf("%w: the hosted domain is not an ASCII domain name", connector.ErrInvalidToken)
	}
	if claims.Subject == "" {
		return federation.Claims{}, fmt.Errorf("%w: the token carries no subject", federation.ErrInvalidClaims)
	}
	return c.federated(claims), nil
}

// federated keys the identity on Google's canonical issuer and sub, so the
// two spellings of the issuer in a token never split one person in two. The
// display values are cut to federation's bounds; the email is passed whole
// and checked there, refused if trusted and dropped if not.
func (c *Client) federated(t idTokenClaims) federation.Claims {
	verified := emailVerified(t.EmailVerified)
	return federation.Claims{
		Provider:  users.ProviderGoogle,
		Issuer:    Issuer,
		Subject:   t.Subject,
		Email:     t.Email,
		Name:      connector.Cut(t.Name, federation.MaxNameLength),
		LoginHint: connector.Cut(t.Email, federation.MaxLoginHintLength),
		// Google says email_verified is authoritative only for a Gmail
		// address or a hosted domain; any other consumer account may carry a
		// verified address it does not own.
		EmailTrusted: t.Email != "" && verified && (t.HostedDomain != "" || gmail(t.Email)),
		// Exchange let only an ASCII hd through, so EqualFold folds ASCII only.
		HomeDirectory: c.home != "" && verified && strings.EqualFold(t.HostedDomain, c.home),
	}
}

// gmail reports whether email's domain is Google's own mail service.
func gmail(email string) bool {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	host := email[at+1:]
	return strings.EqualFold(host, "gmail.com") || strings.EqualFold(host, "googlemail.com")
}

// emailVerified reads email_verified, written as a boolean or as the string
// "true".
func emailVerified(raw json.RawMessage) bool {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var s string
	return json.Unmarshal(raw, &s) == nil && s == "true"
}

// asciiLower lowercases A to Z only. Unicode folding would turn U+212A
// KELVIN SIGN into k, on the comparison that admits staff.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}
