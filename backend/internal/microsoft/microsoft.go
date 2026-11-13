// Package microsoft is the Microsoft connector of external sign-in: the
// OpenID Connect authorisation-code flow with PKCE against Microsoft's
// identity platform, the checks on the ID token it returns, and the rule
// deciding whether Microsoft vouches for the email in it. It hands
// federation the verified claims, keyed on the token's tenant and object ID.
//
// It does not decide which account the person enters (federation does),
// serve HTTP or throttle callers (internal/api does), or open the session
// (auth does).
package microsoft

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/devrdn/db-contest/backend/internal/federation"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// DefaultBaseURL is Microsoft's identity platform; Config.BaseURL replaces it
// only in tests.
const DefaultBaseURL = "https://login.microsoftonline.com"

// The authorities MICROSOFT_TENANT may name besides one tenant ID.
const (
	TenantCommon        = "common"
	TenantOrganizations = "organizations"
	TenantConsumers     = "consumers"
)

// ConsumerTenant is the tenant every personal Microsoft account signs in to.
const ConsumerTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"

// Outbound calls: the token exchange and the signing keys. A response past
// maxResponseBytes is refused as it arrives (CLAUDE.md rule 12); a token
// answer and a key set are a few kilobytes.
const (
	httpTimeout      = 10 * time.Second
	maxResponseBytes = 256 << 10
)

var (
	// ErrSignInRefused means Microsoft sent the person back without a code:
	// they declined, or Microsoft refused the request.
	ErrSignInRefused = errors.New("the sign-in at Microsoft was declined or refused")
	// ErrExchangeFailed means the token endpoint refused the code, could not
	// be reached, or answered without an ID token.
	ErrExchangeFailed = errors.New("the authorisation code could not be exchanged")
	// ErrKeysUnavailable means Microsoft's signing keys could not be fetched,
	// so no token can be verified: an outage, not a forgery.
	ErrKeysUnavailable = errors.New("the signing keys could not be fetched")
	// ErrInvalidToken refuses an ID token that is not signed by Microsoft,
	// is for another application, has expired, does not carry this sign-in's
	// nonce, or names an issuer other than its own tenant's.
	ErrInvalidToken = errors.New("the ID token is not valid")
	// ErrTenantNotAllowed refuses a token from a tenant MICROSOFT_TENANT
	// does not admit.
	ErrTenantNotAllowed = errors.New("this installation does not admit accounts of that tenant")
	// ErrStateInvalid refuses a state cookie this installation did not sign,
	// or one whose state does not match the callback's.
	ErrStateInvalid = errors.New("the sign-in state is not valid")
	// ErrStateExpired refuses a state cookie older than StateLifetime.
	ErrStateExpired = errors.New("the sign-in took too long")
)

// Errors lists every sentinel this package declares (CLAUDE.md rule 1). A
// token without an object ID is federation.ErrInvalidClaims.
func Errors() []error {
	return []error{ErrSignInRefused, ErrExchangeFailed, ErrKeysUnavailable, ErrInvalidToken, ErrTenantNotAllowed, ErrStateInvalid, ErrStateExpired}
}

// tenantID is a tenant or object ID as Microsoft writes it in a token.
var tenantID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Config struct {
	ClientID, ClientSecret string
	// Tenant is common, organizations, consumers or one tenant ID: the
	// authority asked, and who the callback admits.
	Tenant string
	// HomeTenant is the installation's own tenant ID, whose members' emails
	// are trusted; empty trusts no tenant as such.
	HomeTenant string
	// RedirectURL is the callback's absolute address, as registered.
	RedirectURL string
	// BaseURL empty is DefaultBaseURL.
	BaseURL string
	// HTTPClient nil is NewHTTPClient().
	HTTPClient *http.Client
	// Now nil is time.Now.
	Now func() time.Time
}

// Client runs the flow for one application registration. It contacts
// Microsoft only during a callback: the endpoints are the authority's fixed
// v2.0 paths, and the signing keys are fetched on the first token and cached.
type Client struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	keys     *keySet
	http     *http.Client
	base     string
	tenant   string
	home     string
}

func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = NewHTTPClient()
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	authority := base + "/" + cfg.Tenant

	keys := newKeySet(authority+"/discovery/v2.0/keys", cfg.HTTPClient, cfg.Now)
	// The issuer differs for every tenant under common, so go-oidc cannot
	// check it; Exchange checks it against the token's own tenant.
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
				AuthURL:   authority + "/oauth2/v2.0/authorize",
				TokenURL:  authority + "/oauth2/v2.0/token",
				AuthStyle: oauth2.AuthStyleInParams,
			},
		},
		verifier: verifier,
		keys:     keys,
		http:     cfg.HTTPClient,
		base:     base,
		tenant:   cfg.Tenant,
		home:     cfg.HomeTenant,
	}, nil
}

func (cfg Config) validate() error {
	switch {
	case cfg.ClientID == "" || cfg.ClientSecret == "":
		return errors.New("microsoft: a client ID and its secret are both required")
	case !absolute(cfg.RedirectURL):
		return fmt.Errorf("microsoft: the redirect URL %q is not an absolute address", cfg.RedirectURL)
	case !absolute(cfg.BaseURL):
		return fmt.Errorf("microsoft: the base URL %q is not an absolute address", cfg.BaseURL)
	case !ValidTenant(cfg.Tenant):
		return fmt.Errorf("microsoft: the tenant %q is not common, organizations, consumers or a tenant ID", cfg.Tenant)
	case cfg.HomeTenant != "" && !tenantID.MatchString(cfg.HomeTenant):
		return fmt.Errorf("microsoft: the home tenant %q is not a tenant ID", cfg.HomeTenant)
	case cfg.HomeTenant != "" && !admits(cfg.Tenant, cfg.HomeTenant):
		return fmt.Errorf("microsoft: the tenant %q never admits the home tenant", cfg.Tenant)
	}
	return nil
}

// ValidTenant reports whether s may be MICROSOFT_TENANT.
func ValidTenant(s string) bool {
	switch s {
	case TenantCommon, TenantOrganizations, TenantConsumers:
		return true
	}
	return tenantID.MatchString(s)
}

// admits reports whether the authority named by tenant lets tid in.
func admits(tenant, tid string) bool {
	switch tenant {
	case TenantCommon:
		return true
	case TenantOrganizations:
		return tid != ConsumerTenant
	case TenantConsumers:
		return tid == ConsumerTenant
	}
	return tid == tenant
}

func absolute(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// NewHTTPClient is the client for Microsoft's endpoints: bounded in time and
// in the size of what it reads.
func NewHTTPClient() *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{Timeout: httpTimeout, Transport: boundedTransport{base: base, max: maxResponseBytes}}
}

var errResponseTooLarge = fmt.Errorf("the response is larger than %d bytes", maxResponseBytes)

type boundedTransport struct {
	base http.RoundTripper
	max  int64
}

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &boundedBody{ReadCloser: resp.Body, left: t.max}
	return resp, nil
}

type boundedBody struct {
	io.ReadCloser
	left int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	// One byte past the bound is read to tell a body of exactly max bytes
	// from a longer one.
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return 0, errResponseTooLarge
	}
	return n, err
}

// AuthCodeURL is where the browser is sent to sign in: the configured
// authority, asking for st's state and nonce with st's PKCE challenge.
func (c *Client) AuthCodeURL(st State) string {
	// select_account: on a shared machine a live Microsoft session would
	// otherwise sign the next person in without asking.
	return c.oauth.AuthCodeURL(st.State, oauth2.S256ChallengeOption(st.Verifier), oidc.Nonce(st.Nonce),
		oauth2.SetAuthURLParam("prompt", "select_account"))
}

// idTokenClaims is what the connector reads of a v2.0 ID token.
type idTokenClaims struct {
	Issuer            string          `json:"iss"`
	TenantID          string          `json:"tid"`
	ObjectID          string          `json:"oid"`
	Email             string          `json:"email"`
	PreferredUsername string          `json:"preferred_username"`
	Name              string          `json:"name"`
	DomainVerified    json.RawMessage `json:"xms_edov"`
	// IdentityProvider is absent or iss for the tenant's own members, and
	// names the home of a guest.
	IdentityProvider string `json:"idp"`
	// AccountType is 1 for a guest and 0 for a member, when present.
	AccountType json.RawMessage `json:"acct"`
}

// maxAccountTypeLength bounds acct as written in the token; 0 and 1 need a
// few bytes in any form.
const maxAccountTypeLength = 16

// Exchange trades the code for an ID token, verifies it against st, and
// returns the claims federation resolves. The signature is checked against
// the published keys (keySet), the audience and expiry by go-oidc, and the
// issuer, nonce and tenant here.
func (c *Client) Exchange(ctx context.Context, code string, st State) (federation.Claims, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.http)
	token, err := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return federation.Claims{}, fmt.Errorf("%w: %w", ErrExchangeFailed, err)
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" {
		return federation.Claims{}, fmt.Errorf("%w: the answer carries no ID token", ErrExchangeFailed)
	}
	if err := c.keys.Prepare(ctx, raw); err != nil {
		return federation.Claims{}, err
	}
	idToken, err := c.verifier.Verify(ctx, raw)
	if err != nil {
		return federation.Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	var claims idTokenClaims
	if err := idToken.Claims(&claims); err != nil {
		return federation.Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	if !tenantID.MatchString(claims.TenantID) {
		return federation.Claims{}, fmt.Errorf("%w: the tenant is not a tenant ID", ErrInvalidToken)
	}
	// Under common any tenant's key signs, so a tenant could otherwise speak
	// for another; its issuer must be its own.
	if claims.Issuer != c.base+"/"+claims.TenantID+"/v2.0" {
		return federation.Claims{}, fmt.Errorf("%w: the issuer is not the token's own tenant", ErrInvalidToken)
	}
	if st.Nonce == "" || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(st.Nonce)) != 1 {
		return federation.Claims{}, fmt.Errorf("%w: the nonce is not this sign-in's", ErrInvalidToken)
	}
	if !admits(c.tenant, claims.TenantID) {
		return federation.Claims{}, fmt.Errorf("%w: tenant %s", ErrTenantNotAllowed, claims.TenantID)
	}
	if len(claims.IdentityProvider) > federation.MaxIssuerLength || !utf8.ValidString(claims.IdentityProvider) ||
		strings.ContainsFunc(claims.IdentityProvider, unicode.IsControl) {
		return federation.Claims{}, fmt.Errorf("%w: the identity provider is not a bounded name", ErrInvalidToken)
	}
	if len(claims.AccountType) > maxAccountTypeLength {
		return federation.Claims{}, fmt.Errorf("%w: the account type is not a bounded value", ErrInvalidToken)
	}
	if !tenantID.MatchString(claims.ObjectID) {
		return federation.Claims{}, fmt.Errorf("%w: the token carries no object ID", federation.ErrInvalidClaims)
	}
	return c.federated(claims), nil
}

// federated keys the identity on the issuer and the object ID, never sub:
// sub is pairwise per application registration, so moving the installation
// to another registration would change every person's. The display values
// are cut to federation's bounds; the email is passed whole and refused
// there if it is not an address.
func (c *Client) federated(t idTokenClaims) federation.Claims {
	email := t.Email
	// A guest carries the home tenant's tid with an address the tenant never
	// verified, so only the tenant's own members are vouched for by it.
	// idp names a guest's home only when Microsoft emits it, so acct = 1
	// marks a guest as well.
	guest := t.IdentityProvider != "" && t.IdentityProvider != t.Issuer || isOne(t.AccountType)
	home := c.home != "" && t.TenantID == c.home && !guest
	// The home tenant's usernames are its members' addresses; anyone else's
	// are whatever their tenant chose and never taken for an email.
	if email == "" && home {
		email = t.PreferredUsername
	}
	hint := t.PreferredUsername
	if hint == "" {
		hint = email
	}
	return federation.Claims{
		Provider:     users.ProviderMicrosoft,
		Issuer:       t.Issuer,
		Subject:      t.ObjectID,
		Email:        email,
		Name:         cut(t.Name, federation.MaxNameLength),
		LoginHint:    cut(hint, federation.MaxLoginHintLength),
		EmailTrusted: email != "" && (home || domainVerified(t.DomainVerified) || t.TenantID == ConsumerTenant),
		// A verified domain of another tenant vouches for the address, not
		// for membership of this installation's directory.
		HomeDirectory: home,
	}
}

// isOne reports whether raw is the number 1 or the string "1".
func isOne(raw json.RawMessage) bool {
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return n == 1
	}
	var s string
	return json.Unmarshal(raw, &s) == nil && s == "1"
}

// domainVerified reads xms_edov, Microsoft's statement that the tenant owns
// the email's domain, written as a boolean or as the string "true" or "1".
func domainVerified(raw json.RawMessage) bool {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var s string
	return json.Unmarshal(raw, &s) == nil && (s == "true" || s == "1")
}

// cut shortens s to at most max bytes without splitting a letter.
func cut(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
