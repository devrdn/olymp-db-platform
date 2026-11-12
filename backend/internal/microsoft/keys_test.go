package microsoft_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/microsoft"
	"github.com/devrdn/db-contest/backend/internal/microsoft/microsofttest"
)

// unsigned is a JWT with header alg and an empty or given signature, built
// by hand since no library signs this way.
func unsigned(t *testing.T, header map[string]any, claims map[string]any, sign func(input string) string) string {
	t.Helper()
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	return input + "." + sign(input)
}

func TestTheCallbackRefusesAnotherAlgorithm(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)
	claims := p.Claims(clientID, homeTenant, objectID, "the-nonce")
	public, err := x509.MarshalPKIXPublicKey(p.PublishedKey())
	if err != nil {
		t.Fatal(err)
	}
	hs256 := func(key []byte) func(string) string {
		return func(input string) string {
			m := hmac.New(sha256.New, key)
			m.Write([]byte(input))
			return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
		}
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public})

	for name, token := range map[string]string{
		"HS256 keyed with the public key (DER)": unsigned(t, map[string]any{"alg": "HS256", "typ": "JWT", "kid": microsofttest.KeyID}, claims, hs256(public)),
		"HS256 keyed with the public key (PEM)": unsigned(t, map[string]any{"alg": "HS256", "typ": "JWT", "kid": microsofttest.KeyID}, claims, hs256(pemKey)),
		"HS256 keyed with the modulus":          unsigned(t, map[string]any{"alg": "HS256", "typ": "JWT", "kid": microsofttest.KeyID}, claims, hs256(p.PublishedKey().N.Bytes())),
		"none":                                  unsigned(t, map[string]any{"alg": "none", "typ": "JWT", "kid": microsofttest.KeyID}, claims, func(string) string { return "" }),
		"none, no kid":                          unsigned(t, map[string]any{"alg": "none"}, claims, func(string) string { return "" }),
	} {
		if _, err := exchangeToken(t, c, p, token); !errors.Is(err, microsoft.ErrInvalidToken) {
			t.Errorf("%s: Exchange() = %v, want ErrInvalidToken", name, err)
		}
	}
}

func TestATokenUnderAKeyIDNotPublishedIsRefused(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	token := p.SignWithNextKey(t, "never-published", p.Claims(clientID, homeTenant, objectID, "the-nonce"))
	if _, err := exchangeToken(t, c, p, token); !errors.Is(err, microsoft.ErrInvalidToken) {
		t.Errorf("Exchange() under an unpublished kid = %v, want ErrInvalidToken", err)
	}
}

// clock is a settable time for the key set's refresh bound.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func TestARotatedKeyIsPickedUpOnce(t *testing.T) {
	p := microsofttest.NewProvider(t)
	at := &clock{now: time.Now()}
	c := newClient(t, p, func(cfg *microsoft.Config) { cfg.Now = at.Now })
	if _, err := exchange(t, c, p, p.Claims(clientID, homeTenant, objectID, "the-nonce")); err != nil {
		t.Fatalf("Exchange() under the first key = %v", err)
	}
	if n := p.KeyFetches(); n != 1 {
		t.Fatalf("the keys were fetched %d times for the first token, want once", n)
	}

	p.Rotate()
	at.now = at.now.Add(microsoft.KeyRefreshInterval)
	for i := 0; i < 3; i++ {
		token := p.SignWithNextKey(t, microsofttest.NextKeyID, p.Claims(clientID, homeTenant, objectID, "the-nonce"))
		if _, err := exchangeToken(t, c, p, token); err != nil {
			t.Fatalf("Exchange() %d under the rotated key = %v", i, err)
		}
	}
	if n := p.KeyFetches(); n != 2 {
		t.Errorf("the keys were fetched %d times, want once more for the rotated key", n)
	}
}

func TestAnUnknownKeyIDCannotMakeItRefetchWithoutBound(t *testing.T) {
	p := microsofttest.NewProvider(t)
	at := &clock{now: time.Now()}
	c := newClient(t, p, func(cfg *microsoft.Config) { cfg.Now = at.Now })
	if _, err := exchange(t, c, p, p.Claims(clientID, homeTenant, objectID, "the-nonce")); err != nil {
		t.Fatalf("Exchange() = %v", err)
	}

	for i := 0; i < 20; i++ {
		token := p.SignWithNextKey(t, fmt.Sprintf("unknown-%d", i), p.Claims(clientID, homeTenant, objectID, "the-nonce"))
		if _, err := exchangeToken(t, c, p, token); !errors.Is(err, microsoft.ErrInvalidToken) {
			t.Fatalf("Exchange() under an unknown kid = %v, want ErrInvalidToken", err)
		}
	}
	if n := p.KeyFetches(); n != 1 {
		t.Errorf("twenty unknown kids within the interval fetched the keys %d times, want only the first fetch", n)
	}

	at.now = at.now.Add(microsoft.KeyRefreshInterval)
	for i := 0; i < 20; i++ {
		token := p.SignWithNextKey(t, "unknown-later", p.Claims(clientID, homeTenant, objectID, "the-nonce"))
		_, _ = exchangeToken(t, c, p, token)
	}
	if n := p.KeyFetches(); n != 2 {
		t.Errorf("after the interval the keys were fetched %d times in all, want one more", n)
	}

	// Known keys are still served from the cache.
	if _, err := exchange(t, c, p, p.Claims(clientID, homeTenant, objectID, "the-nonce")); err != nil {
		t.Errorf("Exchange() under the published key after the misses = %v", err)
	}
	if n := p.KeyFetches(); n != 2 {
		t.Errorf("a known kid fetched the keys again (%d)", n)
	}
}
