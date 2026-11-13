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
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/connector"
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

func TestTheKeySetRefusesAnotherAlgorithm(t *testing.T) {
	j := newJWKS(t)
	ks := newKeySet(j, nil)
	claims := map[string]any{"sub": "someone"}
	public, err := x509.MarshalPKIXPublicKey(&j.published.PublicKey)
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
		"HS256 keyed with the public key (DER)": unsigned(t, map[string]any{"alg": "HS256", "typ": "JWT", "kid": keyID}, claims, hs256(public)),
		"HS256 keyed with the public key (PEM)": unsigned(t, map[string]any{"alg": "HS256", "typ": "JWT", "kid": keyID}, claims, hs256(pemKey)),
		"HS256 keyed with the modulus":          unsigned(t, map[string]any{"alg": "HS256", "typ": "JWT", "kid": keyID}, claims, hs256(j.published.N.Bytes())),
		"none":                                  unsigned(t, map[string]any{"alg": "none", "typ": "JWT", "kid": keyID}, claims, func(string) string { return "" }),
		"none, no kid":                          unsigned(t, map[string]any{"alg": "none"}, claims, func(string) string { return "" }),
	} {
		if err := verify(t.Context(), ks, token); err == nil {
			t.Errorf("%s: the token was verified", name)
		}
	}
}

func TestATokenUnderAKeyIDNotPublishedIsRefused(t *testing.T) {
	j := newJWKS(t)
	ks := newKeySet(j, nil)

	err := verify(t.Context(), ks, j.tokenUnder(t, "never-published"))
	if err == nil {
		t.Error("a token under an unpublished kid was verified")
	}
	if errors.Is(err, connector.ErrKeysUnavailable) {
		t.Errorf("verify = %v, want a refusal of the token, not an outage", err)
	}
}

func TestAForgedTokenIsRefused(t *testing.T) {
	j := newJWKS(t)
	ks := newKeySet(j, nil)

	if err := verify(t.Context(), ks, j.forged(t)); err == nil {
		t.Error("a token signed by a key the provider never published was verified")
	}
	if err := verify(t.Context(), ks, j.token(t)); err != nil {
		t.Errorf("a genuine token = %v, want it verified", err)
	}
}

// clock is a settable time for the key set's refresh bound.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func TestARotatedKeyIsPickedUpOnce(t *testing.T) {
	j := newJWKS(t)
	at := &clock{now: time.Now()}
	ks := newKeySet(j, at)
	if err := verify(t.Context(), ks, j.token(t)); err != nil {
		t.Fatalf("verify under the first key = %v", err)
	}
	if n := j.KeyFetches(); n != 1 {
		t.Fatalf("the keys were fetched %d times for the first token, want once", n)
	}

	j.Rotate()
	at.now = at.now.Add(connector.KeyRefreshInterval)
	for i := 0; i < 3; i++ {
		if err := verify(t.Context(), ks, j.tokenUnder(t, nextKeyID)); err != nil {
			t.Fatalf("verify %d under the rotated key = %v", i, err)
		}
	}
	if n := j.KeyFetches(); n != 2 {
		t.Errorf("the keys were fetched %d times, want once more for the rotated key", n)
	}
}

func TestAnUnknownKeyIDCannotMakeItRefetchWithoutBound(t *testing.T) {
	j := newJWKS(t)
	at := &clock{now: time.Now()}
	ks := newKeySet(j, at)
	if err := verify(t.Context(), ks, j.token(t)); err != nil {
		t.Fatalf("verify = %v", err)
	}

	for i := 0; i < 20; i++ {
		err := verify(t.Context(), ks, j.tokenUnder(t, fmt.Sprintf("unknown-%d", i)))
		if err == nil {
			t.Fatal("a token under an unknown kid was verified")
		}
		if errors.Is(err, connector.ErrKeysUnavailable) {
			t.Fatalf("verify = %v, want a refusal of the token, not an outage", err)
		}
	}
	if n := j.KeyFetches(); n != 1 {
		t.Errorf("twenty unknown kids within the interval fetched the keys %d times, want only the first fetch", n)
	}

	at.now = at.now.Add(connector.KeyRefreshInterval)
	for i := 0; i < 20; i++ {
		err := verify(t.Context(), ks, j.tokenUnder(t, "unknown-later"))
		if err == nil || errors.Is(err, connector.ErrKeysUnavailable) {
			t.Fatalf("verify after the interval = %v, want a refusal of the token, not an outage", err)
		}
	}
	if n := j.KeyFetches(); n != 2 {
		t.Errorf("after the interval the keys were fetched %d times in all, want one more", n)
	}

	// Known keys are still served from the cache.
	if err := verify(t.Context(), ks, j.token(t)); err != nil {
		t.Errorf("verify under the published key after the misses = %v", err)
	}
	if n := j.KeyFetches(); n != 2 {
		t.Errorf("a known kid fetched the keys again (%d)", n)
	}
}

// A browser leaving mid-fetch must not cost everybody else the keys: the
// fetch is the key set's, not the first caller's.
func TestACancelledCallerDoesNotCancelTheKeyFetch(t *testing.T) {
	j := newJWKS(t)
	ks := newKeySet(j, nil)
	release := j.HoldKeys()
	defer release()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ks.Prepare(ctx, j.token(t))
	}()
	waitFor(t, func() bool { return j.KeyFetches() == 1 })
	cancel()
	release()
	<-done

	if err := verify(t.Context(), ks, j.token(t)); err != nil {
		t.Errorf("verify after the first caller left = %v, want the fetched keys to verify it", err)
	}
	if n := j.KeyFetches(); n != 1 {
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
	j := newJWKS(t)
	at := &clock{now: time.Now()}
	ks := newKeySet(j, at)
	j.FailKeys(http.StatusServiceUnavailable)

	if err := verify(t.Context(), ks, j.token(t)); !errors.Is(err, connector.ErrKeysUnavailable) {
		t.Fatalf("verify while the keys are down = %v, want ErrKeysUnavailable", err)
	}
	at.now = at.now.Add(connector.KeyFetchBackoff / 2)
	if err := verify(t.Context(), ks, j.token(t)); !errors.Is(err, connector.ErrKeysUnavailable) {
		t.Errorf("verify within the backoff = %v, want ErrKeysUnavailable, not an invalid token", err)
	}
	if n := j.KeyFetches(); n != 1 {
		t.Errorf("the keys were fetched %d times within the backoff, want once", n)
	}

	j.FailKeys(0)
	at.now = at.now.Add(connector.KeyFetchBackoff)
	if connector.KeyFetchBackoff*3/2 >= connector.KeyRefreshInterval {
		t.Fatal("the backoff must be shorter than the refresh interval")
	}
	if err := verify(t.Context(), ks, j.token(t)); err != nil {
		t.Errorf("verify after the backoff = %v, want the keys fetched again", err)
	}
	if n := j.KeyFetches(); n != 2 {
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
			j := newJWKS(t)
			at := &clock{now: time.Now()}
			ks := newKeySet(j, at)
			if err := verify(t.Context(), ks, j.token(t)); err != nil {
				t.Fatalf("first verify = %v", err)
			}

			j.ServeKeys([]byte(body))
			at.now = at.now.Add(connector.KeyRefreshInterval)
			if err := verify(t.Context(), ks, j.tokenUnder(t, "unknown")); !errors.Is(err, connector.ErrKeysUnavailable) {
				t.Errorf("verify when the set came back unusable = %v, want ErrKeysUnavailable", err)
			}
			if err := verify(t.Context(), ks, j.token(t)); err != nil {
				t.Errorf("verify under the held key = %v, want the held set kept", err)
			}

			j.ServeKeys(nil)
			j.Rotate()
			at.now = at.now.Add(connector.KeyFetchBackoff)
			if err := verify(t.Context(), ks, j.tokenUnder(t, nextKeyID)); err != nil {
				t.Errorf("verify after the backoff = %v, want the retry to bring the rotated key", err)
			}
		})
	}
}

func TestConcurrentMissesShareOneFetch(t *testing.T) {
	j := newJWKS(t)
	ks := newKeySet(j, nil)
	release := j.HoldKeys()
	defer release()

	const callers = 8
	token := j.token(t)
	errs := make(chan error, callers)
	var started sync.WaitGroup
	started.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			started.Done()
			errs <- verify(t.Context(), ks, token)
		}()
	}
	// Every caller is about to miss while the one fetch is held, so a second
	// fetch would show here rather than after the first had finished.
	started.Wait()
	waitFor(t, func() bool { return j.KeyFetches() >= 1 })
	time.Sleep(20 * time.Millisecond)
	release()
	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Errorf("verify = %v", err)
		}
	}
	if n := j.KeyFetches(); n != 1 {
		t.Errorf("%d concurrent misses fetched the keys %d times, want once", callers, n)
	}
}
