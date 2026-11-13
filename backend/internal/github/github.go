// Package github is the GitHub connector of external sign-in: the OAuth 2.0
// authorisation-code flow with PKCE against GitHub, the two API reads that
// say who the person is, and the rule deciding whether GitHub vouches for
// their email. GitHub is not OpenID Connect, so there is no ID token: the
// identity is the numeric account id under GitHub's issuer.
//
// GitHub vouches for no organisation's directory, so a sign-in never
// carries HomeDirectory. The package does not decide which account the
// person enters (federation does), serve HTTP or throttle callers
// (internal/api does), or open the session (auth does).
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"golang.org/x/oauth2"

	"github.com/devrdn/db-contest/backend/internal/connector"
	"github.com/devrdn/db-contest/backend/internal/federation"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// Issuer is what every GitHub identity is keyed under.
const Issuer = "https://github.com"

// GitHub's addresses; Config replaces them only in tests.
const (
	DefaultWebURL = "https://github.com"
	DefaultAPIURL = "https://api.github.com"
)

type Config struct {
	ClientID, ClientSecret string
	// RedirectURL is the callback's absolute address, as registered.
	RedirectURL string
	// WebURL and APIURL empty are the Default values.
	WebURL, APIURL string
	// HTTPClient nil is connector.NewHTTPClient().
	HTTPClient *http.Client
}

// Client runs the flow for one OAuth app. It contacts GitHub only during a
// callback.
type Client struct {
	oauth  oauth2.Config
	apiURL string
	http   *http.Client
}

func New(cfg Config) (*Client, error) {
	if cfg.WebURL == "" {
		cfg.WebURL = DefaultWebURL
	}
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = connector.NewHTTPClient()
	}
	return &Client{
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Scopes:       []string{"user:email"},
			Endpoint: oauth2.Endpoint{
				AuthURL:   cfg.WebURL + "/login/oauth/authorize",
				TokenURL:  cfg.WebURL + "/login/oauth/access_token", // #nosec G101 -- an endpoint address, not a credential
				AuthStyle: oauth2.AuthStyleInParams,
			},
		},
		apiURL: cfg.APIURL,
		http:   cfg.HTTPClient,
	}, nil
}

func (cfg Config) validate() error {
	switch {
	case cfg.ClientID == "" || cfg.ClientSecret == "":
		return errors.New("github: a client ID and its secret are both required")
	case !absolute(cfg.RedirectURL):
		return fmt.Errorf("github: the redirect URL %q is not an absolute address", cfg.RedirectURL)
	case !absolute(cfg.WebURL) || !absolute(cfg.APIURL):
		return errors.New("github: the web and API URLs must be absolute addresses")
	}
	return nil
}

func absolute(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// AuthCodeURL is where the browser is sent to sign in, asking for st's state
// with st's PKCE challenge. st.Nonce is not used: OAuth has no ID token to
// carry one.
func (c *Client) AuthCodeURL(st connector.State) string {
	// select_account: on a shared machine a live GitHub session would
	// otherwise sign the next person in without asking.
	return c.oauth.AuthCodeURL(st.State, oauth2.S256ChallengeOption(st.Verifier),
		oauth2.SetAuthURLParam("prompt", "select_account"))
}

// Exchange trades the code for an access token, reads the person's account
// and emails with it, and returns the claims federation resolves. The token
// lives only for this call.
func (c *Client) Exchange(ctx context.Context, code string, st connector.State) (federation.Claims, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.http)
	token, err := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return federation.Claims{}, fmt.Errorf("%w: %w", connector.ErrExchangeFailed, err)
	}
	if token.AccessToken == "" {
		return federation.Claims{}, fmt.Errorf("%w: the answer carries no access token", connector.ErrExchangeFailed)
	}

	var account struct {
		// ID is decoded as a json.Number: a float64 rounds an id above 2^53
		// and would merge two people.
		ID    any    `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if status, err := c.get(ctx, token.AccessToken, "/user", &account); err != nil || status != http.StatusOK {
		return federation.Claims{}, failed("the account could not be read", status, err)
	}
	id, ok := account.ID.(json.Number)
	if !ok {
		return federation.Claims{}, fmt.Errorf("%w: the account carries no numeric id", federation.ErrInvalidClaims)
	}
	n, err := strconv.ParseInt(id.String(), 10, 64)
	if err != nil || n <= 0 {
		return federation.Claims{}, fmt.Errorf("%w: the account id is not a positive integer", federation.ErrInvalidClaims)
	}

	email, trusted, err := c.primaryEmail(ctx, token.AccessToken)
	if err != nil {
		return federation.Claims{}, err
	}
	return federation.Claims{
		Provider:     users.ProviderGitHub,
		Issuer:       Issuer,
		Subject:      strconv.FormatInt(n, 10),
		Email:        email,
		Name:         connector.Cut(account.Name, federation.MaxNameLength),
		LoginHint:    connector.Cut(account.Login, federation.MaxLoginHintLength),
		EmailTrusted: trusted,
	}, nil
}

// primaryEmail is the account's primary address and whether GitHub verified
// it. An account the app may not read the emails of, because an organisation
// withholds the scope or the app lacks it (403, 404), signs in with none.
func (c *Client) primaryEmail(ctx context.Context, token string) (string, bool, error) {
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	status, err := c.get(ctx, token, "/user/emails", &emails)
	switch {
	case status == http.StatusForbidden || status == http.StatusNotFound:
		return "", false, nil
	case err != nil || status != http.StatusOK:
		return "", false, failed("the emails could not be read", status, err)
	}
	for _, e := range emails {
		if e.Primary {
			return e.Email, e.Email != "" && e.Verified, nil
		}
	}
	return "", false, nil
}

// get reads path from the API as the token's owner into out when the answer
// is 200, and returns the status.
func (c *Client) get(ctx context.Context, token, path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "db-contest")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	return resp.StatusCode, dec.Decode(out)
}

func failed(what string, status int, err error) error {
	if err != nil {
		return fmt.Errorf("%w: %s: %w", connector.ErrExchangeFailed, what, err)
	}
	return fmt.Errorf("%w: %s: status %d", connector.ErrExchangeFailed, what, status)
}
