package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// Fetches of the signing keys. A token under a key ID the set does not hold
// triggers a fetch, as key rotation requires, but no sooner than
// KeyRefreshInterval after a fetch that brought keys, so unknown key IDs
// cannot turn callbacks into requests to the provider; after a failed one the
// next is allowed after KeyFetchBackoff, so an outage is not prolonged.
const (
	KeyRefreshInterval = time.Minute
	KeyFetchBackoff    = 5 * time.Second
)

var (
	errUnknownKey  = errors.New("no published key verifies the token")
	errNoUsableKey = errors.New("the key set holds no usable signing key")
)

// KeySet is a provider's signing keys for one authority, fetched on the
// first token and again for a key ID it does not hold. Exchange calls
// Prepare, which may fetch; VerifySignature (oidc.KeySet) reads only what is
// held, since go-oidc would flatten a fetch failure into an invalid token.
type KeySet struct {
	url    string
	client *http.Client
	now    func() time.Time

	mu   sync.RWMutex
	keys []jose.JSONWebKey

	// fetchMu serialises fetches, so concurrent misses share one; tokens
	// under a held key never wait on it.
	fetchMu   sync.Mutex
	nextFetch time.Time
	lastErr   error
}

func NewKeySet(url string, client *http.Client, now func() time.Time) *KeySet {
	if now == nil {
		now = time.Now
	}
	return &KeySet{url: url, client: client, now: now}
}

func (s *KeySet) held() []jose.JSONWebKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.keys
}

// Prepare fetches the keys when raw names a key ID the set does not hold
// and a fetch is allowed. It returns ErrKeysUnavailable when the keys could
// not be fetched, now or at the last try within the backoff; a token it
// cannot parse is left to VerifySignature to refuse.
func (s *KeySet) Prepare(ctx context.Context, raw string) error {
	jws, err := parse(raw)
	if err != nil {
		return nil
	}
	kid := jws.Signatures[0].Header.KeyID
	if kid == "" || holds(s.held(), kid) {
		return nil
	}

	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()
	if holds(s.held(), kid) {
		return nil
	}
	if s.now().Before(s.nextFetch) {
		if s.lastErr != nil {
			return fmt.Errorf("%w: %w", ErrKeysUnavailable, s.lastErr)
		}
		return nil
	}
	// The fetch is the set's, shared by every caller waiting on it: one
	// browser leaving must not cancel it. The client's timeout bounds it.
	fetched, err := s.fetch(context.WithoutCancel(ctx))
	if err == nil && len(fetched) == 0 {
		err = errNoUsableKey
	}
	if err != nil {
		// The held keys stay: an empty or broken answer must not undo a
		// good set.
		s.nextFetch, s.lastErr = s.now().Add(KeyFetchBackoff), err
		return fmt.Errorf("%w: %w", ErrKeysUnavailable, err)
	}
	s.nextFetch, s.lastErr = s.now().Add(KeyRefreshInterval), nil
	s.mu.Lock()
	s.keys = fetched
	s.mu.Unlock()
	return nil
}

// VerifySignature returns the payload of raw, signed RS256 by a held key
// under its key ID.
func (s *KeySet) VerifySignature(_ context.Context, raw string) ([]byte, error) {
	jws, err := parse(raw)
	if err != nil {
		return nil, err
	}
	if payload, ok := verifyWith(jws, s.held(), jws.Signatures[0].Header.KeyID); ok {
		return payload, nil
	}
	return nil, errUnknownKey
}

func parse(raw string) (*jose.JSONWebSignature, error) {
	jws, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return nil, fmt.Errorf("parse the token: %w", err)
	}
	if len(jws.Signatures) != 1 {
		return nil, errors.New("the token does not carry exactly one signature")
	}
	return jws, nil
}

func (s *KeySet) fetch(ctx context.Context) ([]jose.JSONWebKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("answered %s", resp.Status)
	}
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, err
	}
	// A key this package cannot read is skipped rather than failing the set,
	// so one new key type at the provider does not stop every sign-in.
	var keys []jose.JSONWebKey
	for _, raw := range set.Keys {
		var key jose.JSONWebKey
		if key.UnmarshalJSON(raw) != nil || !key.IsPublic() || (key.Use != "" && key.Use != "sig") {
			continue
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func holds(keys []jose.JSONWebKey, kid string) bool {
	for _, key := range keys {
		if key.KeyID == kid {
			return true
		}
	}
	return false
}

// verifyWith tries the keys published under kid; the provider names the key of
// every token, so a token without one matches none.
func verifyWith(jws *jose.JSONWebSignature, keys []jose.JSONWebKey, kid string) ([]byte, bool) {
	if kid == "" {
		return nil, false
	}
	for _, key := range keys {
		if key.KeyID != kid {
			continue
		}
		if payload, err := jws.Verify(key); err == nil {
			return payload, true
		}
	}
	return nil, false
}
