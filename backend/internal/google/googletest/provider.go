// Package googletest is a local stand-in for Google's sign-in endpoints: the
// token endpoint, the published signing keys, and ID tokens signed as Google
// signs them. No test contacts Google.
package googletest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// The application the provider recognises, and the issuer it signs under.
const (
	ClientID     = "123456789012-abcdefghijklmnop.apps.example.test"
	ClientSecret = "a-test-client-secret"
	Issuer       = "https://accounts.google.com"
)

// KeyID names the key the provider publishes.
const KeyID = "published-key"

var (
	keysOnce         sync.Once
	published, other *rsa.PrivateKey
)

// keys are generated once per test binary: an RSA key costs tens of
// milliseconds.
func keys() (*rsa.PrivateKey, *rsa.PrivateKey) {
	keysOnce.Do(func() {
		for _, k := range []**rsa.PrivateKey{&published, &other} {
			var err error
			if *k, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
				panic(err)
			}
		}
	})
	return published, other
}

// Provider serves /token and /certs.
type Provider struct {
	URL string

	published, other *rsa.PrivateKey

	mu         sync.Mutex
	tokens     map[string]issued
	keysStatus int
	requests   int
	lastForm   url.Values
}

// issued is a code's ID token and the PKCE challenge it was issued for.
type issued struct {
	idToken, challenge string
}

func NewProvider(t testing.TB) *Provider {
	t.Helper()
	p := &Provider{tokens: map[string]issued{}}
	p.published, p.other = keys()
	server := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(server.Close)
	p.URL = server.URL
	return p
}

func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.requests++
	status := p.keysStatus
	p.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/certs":
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &p.published.PublicKey, KeyID: KeyID, Algorithm: string(jose.RS256), Use: "sig",
		}}})
	case r.Method == http.MethodPost && r.URL.Path == "/token":
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
	p.mu.Lock()
	p.lastForm = r.PostForm
	grant, known := p.tokens[r.PostForm.Get("code")]
	p.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.PostForm.Get("client_id") != ClientID || r.PostForm.Get("client_secret") != ClientSecret:
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
	case !known || r.PostForm.Get("grant_type") != "authorization_code":
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"unknown code"}`))
	case subtle.ConstantTimeCompare([]byte(Challenge(r.PostForm.Get("code_verifier"))), []byte(grant.challenge)) != 1:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Bad Request"}`))
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access", "token_type": "Bearer", "expires_in": 3600,
			"scope": "openid https://www.googleapis.com/auth/userinfo.email", "id_token": grant.idToken,
		})
	}
}

// Challenge is the S256 PKCE challenge of verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Claims is a valid claim set for the person sub, as Google issues it to
// clientID; tests change what they test.
func (p *Provider) Claims(clientID, sub, nonce string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": Issuer, "aud": clientID, "sub": sub, "nonce": nonce,
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
		"email": "ana@example.edu", "email_verified": true, "name": "Ana Popescu",
	}
}

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
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", KeyID))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return token
}

// Issue makes the token endpoint answer code with idToken, for a request
// whose code_verifier matches challenge (S256), and refuse it otherwise.
func (p *Provider) Issue(code, idToken, challenge string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokens[code] = issued{idToken: idToken, challenge: challenge}
}

// FailKeys makes the signing keys answer status; 0 serves them again.
func (p *Provider) FailKeys(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keysStatus = status
}

// Requests counts every request the provider received.
func (p *Provider) Requests() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

// LastTokenRequest is the form of the last request to the token endpoint.
func (p *Provider) LastTokenRequest() url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastForm
}
