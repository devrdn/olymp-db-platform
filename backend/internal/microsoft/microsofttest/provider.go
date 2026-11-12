// Package microsofttest is a local stand-in for Microsoft's identity
// platform: the token endpoint and the signing keys of every authority, and
// ID tokens signed as Microsoft signs them. No test contacts Microsoft.
package microsofttest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// ConsumerTenant is the tenant of every personal Microsoft account.
const ConsumerTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"

const keyID = "published-key"

var (
	keysOnce         sync.Once
	published, other *rsa.PrivateKey
)

// keys are generated once per test binary: an RSA key costs tens of
// milliseconds.
func keys() (*rsa.PrivateKey, *rsa.PrivateKey) {
	keysOnce.Do(func() {
		var err error
		if published, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if other, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return published, other
}

// Provider serves /{tenant}/oauth2/v2.0/token and
// /{tenant}/discovery/v2.0/keys for any tenant segment.
type Provider struct {
	URL string

	published, other *rsa.PrivateKey

	mu        sync.Mutex
	tokens    map[string]string
	bodies    map[string][]byte
	requests  int
	exchanges int
	lastForm  url.Values
}

func NewProvider(t testing.TB) *Provider {
	t.Helper()
	p := &Provider{tokens: map[string]string{}, bodies: map[string][]byte{}}
	p.published, p.other = keys()
	server := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(server.Close)
	p.URL = server.URL
	return p
}

func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.requests++
	p.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/discovery/v2.0/keys"):
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &p.published.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig",
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
		p.token(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	code := r.PostForm.Get("code")
	p.mu.Lock()
	p.exchanges++
	p.lastForm = r.PostForm
	idToken, known := p.tokens[code]
	body, raw := p.bodies[code]
	p.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case raw:
		_, _ = w.Write(body)
	case !known || r.PostForm.Get("grant_type") != "authorization_code":
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"unknown code"}`))
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-" + code, "token_type": "Bearer", "expires_in": 3600,
			"scope": "openid email profile", "id_token": idToken,
		})
	}
}

// Claims is a valid claim set for one person at tenant tid, as the v2.0
// endpoint issues it to clientID; tests change what they test.
func (p *Provider) Claims(clientID, tid, oid, nonce string) map[string]any {
	now := time.Now()
	return map[string]any{
		"ver": "2.0", "iss": p.Issuer(tid), "aud": clientID, "tid": tid, "oid": oid,
		"sub": "pairwise-" + clientID + "-" + oid, "nonce": nonce,
		"iat": now.Add(-time.Minute).Unix(), "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
		"name": "Ana Popescu", "preferred_username": "ana@example.edu",
	}
}

// Issuer is the v2.0 issuer of tenant tid at this provider.
func (p *Provider) Issuer(tid string) string { return p.URL + "/" + tid + "/v2.0" }

// Sign signs claims with the key the provider publishes.
func (p *Provider) Sign(t testing.TB, claims map[string]any) string {
	t.Helper()
	return sign(t, p.published, claims)
}

// SignWithUnpublishedKey signs under the published key's ID with a key the
// provider never published, as a forger would.
func (p *Provider) SignWithUnpublishedKey(t testing.TB, claims map[string]any) string {
	t.Helper()
	return sign(t, p.other, claims)
}

func sign(t testing.TB, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return token
}

// Issue makes the token endpoint answer code with idToken.
func (p *Provider) Issue(code, idToken string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokens[code] = idToken
}

// Answer makes the token endpoint answer code with body as it is.
func (p *Provider) Answer(code string, body []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bodies[code] = body
}

// Requests counts every request the provider received.
func (p *Provider) Requests() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

// Exchanges counts the requests to the token endpoint.
func (p *Provider) Exchanges() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exchanges
}

// LastTokenRequest is the form of the last request to the token endpoint.
func (p *Provider) LastTokenRequest() url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastForm
}
