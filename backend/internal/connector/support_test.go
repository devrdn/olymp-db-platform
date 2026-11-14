package connector_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/devrdn/olymp-db-platform/backend/internal/connector"
)

// The key set is exercised against a local JWKS endpoint, not through any
// one provider's adapter.
const (
	keyID     = "published-key"
	nextKeyID = "next-key"
)

var (
	keysOnce               sync.Once
	published, next, other *rsa.PrivateKey
)

// testKeys are generated once per test binary: an RSA key costs tens of
// milliseconds.
func testKeys() (*rsa.PrivateKey, *rsa.PrivateKey, *rsa.PrivateKey) {
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

// jwks serves a key set at URL and signs tokens as the provider behind it
// would.
type jwks struct {
	URL string

	published, next, other *rsa.PrivateKey

	mu         sync.Mutex
	rotated    bool
	gate       chan struct{}
	status     int
	body       []byte
	keyFetches int
}

func newJWKS(t testing.TB) *jwks {
	t.Helper()
	j := &jwks{}
	j.published, j.next, j.other = testKeys()
	server := httptest.NewServer(http.HandlerFunc(j.serve))
	t.Cleanup(server.Close)
	j.URL = server.URL
	return j
}

func (j *jwks) serve(w http.ResponseWriter, _ *http.Request) {
	j.mu.Lock()
	j.keyFetches++
	gate, status, body := j.gate, j.status, j.body
	j.mu.Unlock()
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
	j.mu.Lock()
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: &j.published.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig",
	}}}
	if j.rotated {
		set.Keys = append(set.Keys, jose.JSONWebKey{
			Key: &j.next.PublicKey, KeyID: nextKeyID, Algorithm: string(jose.RS256), Use: "sig",
		})
	}
	j.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(set)
}

func signed(t testing.TB, key *rsa.PrivateKey, kid string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	jws, err := signer.Sign([]byte(`{"sub":"someone"}`))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	raw, err := jws.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return raw
}

// token is signed with the key the provider publishes.
func (j *jwks) token(t testing.TB) string { t.Helper(); return signed(t, j.published, keyID) }

// forged is signed under the published key's ID with a key never published.
func (j *jwks) forged(t testing.TB) string { t.Helper(); return signed(t, j.other, keyID) }

// tokenUnder is signed with the key Rotate publishes, under kid.
func (j *jwks) tokenUnder(t testing.TB, kid string) string {
	t.Helper()
	return signed(t, j.next, kid)
}

// Rotate publishes the next key beside the first.
func (j *jwks) Rotate() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.rotated = true
}

// HoldKeys makes requests for the keys wait, counted as they arrive, until
// release is called.
func (j *jwks) HoldKeys() (release func()) {
	gate := make(chan struct{})
	j.mu.Lock()
	j.gate = gate
	j.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			j.mu.Lock()
			j.gate = nil
			j.mu.Unlock()
			close(gate)
		})
	}
}

// FailKeys makes the keys answer status; 0 serves them again.
func (j *jwks) FailKeys(status int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.status = status
}

// ServeKeys makes the keys answer body as it is; nil serves the real set.
func (j *jwks) ServeKeys(body []byte) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.body = body
}

func (j *jwks) KeyFetches() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.keyFetches
}

func newKeySet(j *jwks, at *clock) *connector.KeySet {
	now := (func() time.Time)(nil)
	if at != nil {
		now = at.Now
	}
	return connector.NewKeySet(j.URL, connector.NewHTTPClient(), now)
}

// verify is what an adapter does with a token: prepare, then verify.
func verify(ctx context.Context, ks *connector.KeySet, raw string) error {
	if err := ks.Prepare(ctx, raw); err != nil {
		return err
	}
	_, err := ks.VerifySignature(ctx, raw)
	return err
}
