package microsoft

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

// KeyRefreshInterval is the least time between two fetches of the signing
// keys. A token under an unknown key ID triggers a fetch, as key rotation
// requires, but no more often than this, so unknown key IDs cannot turn
// callbacks into requests to Microsoft.
const KeyRefreshInterval = time.Minute

var errUnknownKey = errors.New("no published key verifies the token")

// keySet is Microsoft's signing keys for one authority, fetched on the
// first token and again for a key ID it does not hold. It implements
// oidc.KeySet.
type keySet struct {
	url    string
	client *http.Client
	now    func() time.Time

	mu   sync.RWMutex
	keys []jose.JSONWebKey

	// fetchMu serialises fetches; tokens under a held key never wait on it.
	fetchMu   sync.Mutex
	fetchedAt time.Time
}

func newKeySet(url string, client *http.Client, now func() time.Time) *keySet {
	if now == nil {
		now = time.Now
	}
	return &keySet{url: url, client: client, now: now}
}

// VerifySignature returns the payload of raw, signed RS256 by a published
// key under its key ID.
func (s *keySet) VerifySignature(ctx context.Context, raw string) ([]byte, error) {
	jws, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.RS256})
	if err != nil {
		return nil, fmt.Errorf("parse the token: %w", err)
	}
	if len(jws.Signatures) != 1 {
		return nil, errors.New("the token does not carry exactly one signature")
	}
	kid := jws.Signatures[0].Header.KeyID

	s.mu.RLock()
	held := s.keys
	s.mu.RUnlock()
	if payload, ok := verifyWith(jws, held, kid); ok {
		return payload, nil
	}
	held, err = s.refresh(ctx, kid)
	if err != nil {
		return nil, err
	}
	if payload, ok := verifyWith(jws, held, kid); ok {
		return payload, nil
	}
	return nil, errUnknownKey
}

// refresh fetches the keys unless they were fetched within
// KeyRefreshInterval, or another fetch meanwhile brought kid; a failed
// fetch spends the interval too.
func (s *keySet) refresh(ctx context.Context, kid string) ([]jose.JSONWebKey, error) {
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()

	s.mu.RLock()
	held := s.keys
	s.mu.RUnlock()
	if holds(held, kid) || (!s.fetchedAt.IsZero() && s.now().Before(s.fetchedAt.Add(KeyRefreshInterval))) {
		return held, nil
	}
	s.fetchedAt = s.now()
	fetched, err := s.fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch the signing keys: %w", err)
	}
	s.mu.Lock()
	s.keys = fetched
	s.mu.Unlock()
	return fetched, nil
}

func (s *keySet) fetch(ctx context.Context) ([]jose.JSONWebKey, error) {
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
	// so one new key type at Microsoft does not stop every sign-in.
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

// verifyWith tries the keys published under kid; Microsoft names the key of
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
