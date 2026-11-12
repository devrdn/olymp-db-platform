// Package microsofttest is a local stand-in for Microsoft's identity
// platform: the token endpoint and the signing keys of every authority, and
// ID tokens signed as Microsoft signs them. No test contacts Microsoft.
package microsofttest

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// ConsumerTenant is the tenant of every personal Microsoft account.
const ConsumerTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"

// KeyID names the key the provider publishes from the start; NextKeyID the
// one Rotate publishes beside it.
const (
	KeyID     = "published-key"
	NextKeyID = "next-key"
)

var (
	keysOnce               sync.Once
	published, next, other *rsa.PrivateKey
)

// keys are generated once per test binary: an RSA key costs tens of
// milliseconds.
func keys() (*rsa.PrivateKey, *rsa.PrivateKey, *rsa.PrivateKey) {
	keysOnce.Do(func() {
		for _, k := range []**rsa.PrivateKey{&published, &next, &other} {
			var err error
			if *k, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
				panic(err)
			}
		}
	})
	return published, next, other
}

// Provider serves /{tenant}/oauth2/v2.0/token and
// /{tenant}/discovery/v2.0/keys for any tenant segment.
type Provider struct {
	URL string

	published, next, other *rsa.PrivateKey

	mu         sync.Mutex
	tokens     map[string]issued
	bodies     map[string][]byte
	rotated    bool
	keysGate   chan struct{}
	keysStatus int
	keysBody   []byte
	requests   int
	exchanges  int
	keyFetches int
	lastForm   url.Values
}

// issued is a code's ID token and the PKCE challenge it was issued for.
type issued struct {
	idToken, challenge string
}

func NewProvider(t testing.TB) *Provider {
	t.Helper()
	p := &Provider{tokens: map[string]issued{}, bodies: map[string][]byte{}}
	p.published, p.next, p.other = keys()
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
		p.mu.Lock()
		p.keyFetches++
		gate, status, body := p.keysGate, p.keysStatus, p.keysBody
		p.mu.Unlock()
		if gate != nil {
			<-gate
		}
		switch {
		case status != 0:
			w.WriteHeader(status)
			return
		case body != nil:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		p.mu.Lock()
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &p.published.PublicKey, KeyID: KeyID, Algorithm: string(jose.RS256), Use: "sig",
		}}}
		if p.rotated {
			set.Keys = append(set.Keys, jose.JSONWebKey{
				Key: &p.next.PublicKey, KeyID: NextKeyID, Algorithm: string(jose.RS256), Use: "sig",
			})
		}
		p.mu.Unlock()
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
	grant, known := p.tokens[code]
	body, raw := p.bodies[code]
	p.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case raw:
		_, _ = w.Write(body)
	case !known || r.PostForm.Get("grant_type") != "authorization_code":
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"unknown code"}`))
	case subtle.ConstantTimeCompare([]byte(Challenge(r.PostForm.Get("code_verifier"))), []byte(grant.challenge)) != 1:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"the code_verifier does not match the code_challenge"}`))
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-" + code, "token_type": "Bearer", "expires_in": 3600,
			"scope": "openid email profile", "id_token": grant.idToken,
		})
	}
}

// Challenge is the S256 PKCE challenge of verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
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
	return sign(t, p.published, KeyID, claims)
}

// SignWithUnpublishedKey signs under the published key's ID with a key the
// provider never published, as a forger would.
func (p *Provider) SignWithUnpublishedKey(t testing.TB, claims map[string]any) string {
	t.Helper()
	return sign(t, p.other, KeyID, claims)
}

// SignWithNextKey signs with the key Rotate publishes, under kid.
func (p *Provider) SignWithNextKey(t testing.TB, kid string, claims map[string]any) string {
	t.Helper()
	return sign(t, p.next, kid, claims)
}

// PublishedKey is the public half of the key published under KeyID.
func (p *Provider) PublishedKey() *rsa.PublicKey { return &p.published.PublicKey }

// Rotate publishes the next key under NextKeyID beside the first, as
// Microsoft announces a key before it signs with it.
func (p *Provider) Rotate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rotated = true
}

func sign(t testing.TB, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
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

// HoldKeys makes requests for the signing keys wait, counted as they
// arrive, until release is called.
func (p *Provider) HoldKeys() (release func()) {
	gate := make(chan struct{})
	p.mu.Lock()
	p.keysGate = gate
	p.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.keysGate = nil
			p.mu.Unlock()
			close(gate)
		})
	}
}

// FailKeys makes the signing keys answer status; 0 serves them again.
func (p *Provider) FailKeys(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keysStatus = status
}

// ServeKeys makes the signing keys answer body as it is; nil serves the
// real set again.
func (p *Provider) ServeKeys(body []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keysBody = body
}

// KeyFetches counts the requests for the signing keys.
func (p *Provider) KeyFetches() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.keyFetches
}

// LastTokenRequest is the form of the last request to the token endpoint.
func (p *Provider) LastTokenRequest() url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastForm
}
