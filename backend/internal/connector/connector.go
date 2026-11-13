// Package connector is what every sign-in provider's adapter shares: the
// errors of the flow, the sign-in state and its sealed cookie, the signing
// keys of an OpenID Connect provider, and the bounded HTTP client for calls
// to a provider.
//
// It does not speak to any one provider, decide which account a person
// enters (federation does), or serve HTTP (internal/api does).
package connector

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"
)

// Outbound calls: the token exchange, the keys and a profile. A response past
// maxResponseBytes is refused as it arrives (CLAUDE.md rule 12); every
// answer these calls read is a few kilobytes.
const (
	httpTimeout      = 10 * time.Second
	maxResponseBytes = 256 << 10
)

var (
	// ErrSignInRefused means the provider sent the person back without a
	// code: they declined, or the provider refused the request.
	ErrSignInRefused = errors.New("the sign-in at the provider was declined or refused")
	// ErrExchangeFailed means the token endpoint refused the code, could not
	// be reached, answered without what the flow needs, or the provider's
	// answer to it could not be read.
	ErrExchangeFailed = errors.New("the authorisation code could not be exchanged")
	// ErrKeysUnavailable means the provider's signing keys could not be
	// fetched, so no token can be verified: an outage, not a forgery.
	ErrKeysUnavailable = errors.New("the signing keys could not be fetched")
	// ErrInvalidToken refuses an ID token that is not signed by the provider,
	// is for another application, has expired, does not carry this sign-in's
	// nonce, or names an issuer other than the one expected.
	ErrInvalidToken = errors.New("the ID token is not valid")
	// ErrStateInvalid refuses a state cookie this installation did not sign,
	// or one whose state does not match the callback's.
	ErrStateInvalid = errors.New("the sign-in state is not valid")
	// ErrStateExpired refuses a state cookie older than StateLifetime.
	ErrStateExpired = errors.New("the sign-in took too long")
)

// Errors lists every sentinel this package declares (CLAUDE.md rule 1).
func Errors() []error {
	return []error{ErrSignInRefused, ErrExchangeFailed, ErrKeysUnavailable, ErrInvalidToken, ErrStateInvalid, ErrStateExpired}
}

// NewHTTPClient is the client for a provider's endpoints: bounded in time and
// in the size of what it reads.
func NewHTTPClient() *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{Timeout: httpTimeout, Transport: boundedTransport{base: base, max: maxResponseBytes}}
}

var errResponseTooLarge = fmt.Errorf("the response is larger than %d bytes", maxResponseBytes)

type boundedTransport struct {
	base http.RoundTripper
	max  int64
}

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &boundedBody{ReadCloser: resp.Body, left: t.max}
	return resp, nil
}

type boundedBody struct {
	io.ReadCloser
	left int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	// One byte past the bound is read to tell a body of exactly max bytes
	// from a longer one.
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return 0, errResponseTooLarge
	}
	return n, err
}

// Cut shortens s to at most max bytes without splitting a letter.
func Cut(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
