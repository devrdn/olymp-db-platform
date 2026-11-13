package connector_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/connector"
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
		if _, err := exchangeToken(t, c, p, token); !errors.Is(err, connector.ErrInvalidToken) {
			t.Errorf("%s: Exchange() = %v, want ErrInvalidToken", name, err)
		}
	}
}

func TestATokenUnderAKeyIDNotPublishedIsRefused(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)

	token := p.SignWithNextKey(t, "never-published", p.Claims(clientID, homeTenant, objectID, "the-nonce"))
	if _, err := exchangeToken(t, c, p, token); !errors.Is(err, connector.ErrInvalidToken) {
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
	at.now = at.now.Add(connector.KeyRefreshInterval)
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
		if _, err := exchangeToken(t, c, p, token); !errors.Is(err, connector.ErrInvalidToken) {
			t.Fatalf("Exchange() under an unknown kid = %v, want ErrInvalidToken", err)
		}
	}
	if n := p.KeyFetches(); n != 1 {
		t.Errorf("twenty unknown kids within the interval fetched the keys %d times, want only the first fetch", n)
	}

	at.now = at.now.Add(connector.KeyRefreshInterval)
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

func validToken(t *testing.T, p *microsofttest.Provider) string {
	t.Helper()
	return p.Sign(t, p.Claims(clientID, homeTenant, objectID, "the-nonce"))
}

// A browser leaving mid-fetch must not cost everybody else the keys: the
// fetch is the key set's, not the first caller's.
func TestACancelledCallerDoesNotCancelTheKeyFetch(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)
	release := p.HoldKeys()
	defer release()

	ctx, cancel := context.WithCancel(t.Context())
	p.Issue("first", validToken(t, p), microsofttest.Challenge(strings.Repeat("v", 43)))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.Exchange(ctx, "first", connector.State{Nonce: "the-nonce", Verifier: strings.Repeat("v", 43)})
	}()
	waitFor(t, func() bool { return p.KeyFetches() == 1 })
	cancel()
	release()
	<-done

	if _, err := exchangeToken(t, c, p, validToken(t, p)); err != nil {
		t.Errorf("Exchange() after the first caller left = %v, want the fetched keys to verify it", err)
	}
	if n := p.KeyFetches(); n != 1 {
		t.Errorf("the keys were fetched %d times, want once", n)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAFailedKeyFetchIsRetriedAfterABackoff(t *testing.T) {
	p := microsofttest.NewProvider(t)
	at := &clock{now: time.Now()}
	c := newClient(t, p, func(cfg *microsoft.Config) { cfg.Now = at.Now })
	p.FailKeys(http.StatusServiceUnavailable)

	if _, err := exchangeToken(t, c, p, validToken(t, p)); !errors.Is(err, connector.ErrKeysUnavailable) {
		t.Fatalf("Exchange() while the keys are down = %v, want ErrKeysUnavailable", err)
	}
	at.now = at.now.Add(connector.KeyFetchBackoff / 2)
	if _, err := exchangeToken(t, c, p, validToken(t, p)); !errors.Is(err, connector.ErrKeysUnavailable) {
		t.Errorf("Exchange() within the backoff = %v, want ErrKeysUnavailable, not an invalid token", err)
	}
	if n := p.KeyFetches(); n != 1 {
		t.Errorf("the keys were fetched %d times within the backoff, want once", n)
	}

	p.FailKeys(0)
	at.now = at.now.Add(connector.KeyFetchBackoff)
	if connector.KeyFetchBackoff*3/2 >= connector.KeyRefreshInterval {
		t.Fatal("the backoff must be shorter than the refresh interval")
	}
	if _, err := exchangeToken(t, c, p, validToken(t, p)); err != nil {
		t.Errorf("Exchange() after the backoff = %v, want the keys fetched again", err)
	}
	if n := p.KeyFetches(); n != 2 {
		t.Errorf("the keys were fetched %d times, want a retry after the backoff", n)
	}
}

func TestAFetchWithoutAUsableKeyKeepsTheHeldKeys(t *testing.T) {
	for name, body := range map[string]string{
		"no keys":       `{"keys":[]}`,
		"no key list":   `{}`,
		"only unusable": `{"keys":[{"kty":"oct","k":"c2VjcmV0","kid":"x"},{"kty":"RSA","use":"enc","kid":"y","n":"AQAB","e":"AQAB"}]}`,
		"not a key set": `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			p := microsofttest.NewProvider(t)
			at := &clock{now: time.Now()}
			c := newClient(t, p, func(cfg *microsoft.Config) { cfg.Now = at.Now })
			if _, err := exchangeToken(t, c, p, validToken(t, p)); err != nil {
				t.Fatalf("first Exchange() = %v", err)
			}

			p.ServeKeys([]byte(body))
			at.now = at.now.Add(connector.KeyRefreshInterval)
			unknown := p.SignWithNextKey(t, "unknown", p.Claims(clientID, homeTenant, objectID, "the-nonce"))
			if _, err := exchangeToken(t, c, p, unknown); !errors.Is(err, connector.ErrKeysUnavailable) {
				t.Errorf("Exchange() when the set came back unusable = %v, want ErrKeysUnavailable", err)
			}
			if _, err := exchangeToken(t, c, p, validToken(t, p)); err != nil {
				t.Errorf("Exchange() under the held key = %v, want the held set kept", err)
			}

			p.ServeKeys(nil)
			p.Rotate()
			at.now = at.now.Add(connector.KeyFetchBackoff)
			rotated := p.SignWithNextKey(t, microsofttest.NextKeyID, p.Claims(clientID, homeTenant, objectID, "the-nonce"))
			if _, err := exchangeToken(t, c, p, rotated); err != nil {
				t.Errorf("Exchange() after the backoff = %v, want the retry to bring the rotated key", err)
			}
		})
	}
}

func TestConcurrentMissesShareOneFetch(t *testing.T) {
	p := microsofttest.NewProvider(t)
	c := newClient(t, p, nil)
	release := p.HoldKeys()
	defer release()

	const callers = 8
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		code := fmt.Sprintf("code-%d", i)
		p.Issue(code, validToken(t, p), microsofttest.Challenge(strings.Repeat("v", 43)))
		go func() {
			_, err := c.Exchange(t.Context(), code, connector.State{Nonce: "the-nonce", Verifier: strings.Repeat("v", 43)})
			errs <- err
		}()
	}
	// Every caller has its token and is waiting on the held keys.
	waitFor(t, func() bool { return p.Exchanges() == callers && p.KeyFetches() >= 1 })
	time.Sleep(20 * time.Millisecond)
	release()
	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Errorf("Exchange() = %v", err)
		}
	}
	if n := p.KeyFetches(); n != 1 {
		t.Errorf("%d concurrent misses fetched the keys %d times, want once", callers, n)
	}
}
