package connector

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// StateLifetime is how long a sign-in may take from start to callback. The
// cookie's own timestamp enforces it, since a browser may keep a cookie past
// its Max-Age.
const StateLifetime = 10 * time.Minute

// MaxReturnPathLength bounds the page a sign-in returns to, in bytes.
const MaxReturnPathLength = 512

// DefaultReturnPath is the interface's sign-in page, where a sign-in without
// a usable return path ends: it sends a signed-in visitor on as a password
// sign-in would, so where an account lands is decided in one place.
const DefaultReturnPath = "/login"

const (
	// stateKeyLabelSuffix follows the provider's name in the label the state
	// key is derived under, so a state cookie can never pass for a device
	// cookie or for another provider's.
	stateKeyLabelSuffix = "-state"
	// maxSealedLength is what a browser keeps of one cookie; a longer value
	// was not sealed here.
	maxSealedLength = 4096
	// futureSkew tolerates replicas whose clocks disagree slightly.
	futureSkew  = time.Minute
	randomBytes = 16
)

// State is what a sign-in carries from start to callback: the state and
// nonce sent to the provider, the PKCE verifier, and the page to return to.
type State struct {
	State, Nonce, Verifier, Return string
}

// NewState draws a fresh state, nonce and verifier for a sign-in returning
// to returnPath, or to DefaultReturnPath when that path is not this site's.
func NewState(returnPath string) (State, error) {
	state, err := random()
	if err != nil {
		return State{}, err
	}
	nonce, err := random()
	if err != nil {
		return State{}, err
	}
	return State{State: state, Nonce: nonce, Verifier: oauth2.GenerateVerifier(), Return: ReturnPath(returnPath)}, nil
}

func random() (string, error) {
	b := make([]byte, randomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("draw the sign-in state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ReturnPath is raw when it is a path on this site, and DefaultReturnPath
// otherwise: it must
// start with one slash, not two (another host), and be printable ASCII
// without a space or a backslash, which browsers read as a slash. A browser
// percent-encodes anything else in a path, so nothing it sends is lost.
func ReturnPath(raw string) string {
	if len(raw) > MaxReturnPathLength || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return DefaultReturnPath
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] <= ' ' || raw[i] > '~' || raw[i] == '\\' {
			return DefaultReturnPath
		}
	}
	return raw
}

// StateSealer signs a State into a cookie value and opens it again.
type StateSealer struct {
	key []byte
	now func() time.Time
}

// NewStateSealer keys the sealer with HMAC-SHA256(secret, provider+"-state");
// secret is DEVICE_COOKIE_SECRET and provider a users.Provider* id, a constant
// chosen by the caller. now nil is time.Now.
func NewStateSealer(secret []byte, provider string, now func() time.Time) *StateSealer {
	if now == nil {
		now = time.Now
	}
	derive := hmac.New(sha256.New, secret)
	derive.Write([]byte(provider + stateKeyLabelSuffix))
	return &StateSealer{key: derive.Sum(nil), now: now}
}

type sealedState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Return   string `json:"r"`
	IssuedAt int64  `json:"t"`
}

// Seal is st, stamped with the time, as base64url(JSON).base64url(MAC).
func (s *StateSealer) Seal(st State) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// Encoding a struct of strings and an integer cannot fail.
	_ = enc.Encode(sealedState{State: st.State, Nonce: st.Nonce, Verifier: st.Verifier, Return: st.Return, IssuedAt: s.now().Unix()})
	payload := base64.RawURLEncoding.EncodeToString(bytes.TrimSpace(buf.Bytes()))
	return payload + "." + s.mac(payload)
}

// Open returns the State sealed in value, refusing ErrStateInvalid for one
// this sealer did not sign and ErrStateExpired for one older than
// StateLifetime.
func (s *StateSealer) Open(value string) (State, error) {
	if len(value) > maxSealedLength || strings.Count(value, ".") != 1 {
		return State{}, ErrStateInvalid
	}
	payload, mac, _ := strings.Cut(value, ".")
	if !hmac.Equal([]byte(mac), []byte(s.mac(payload))) {
		return State{}, ErrStateInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return State{}, ErrStateInvalid
	}
	var sealed sealedState
	if err := json.Unmarshal(raw, &sealed); err != nil || sealed.State == "" || sealed.Nonce == "" || sealed.Verifier == "" {
		return State{}, ErrStateInvalid
	}
	issued := time.Unix(sealed.IssuedAt, 0)
	now := s.now()
	switch {
	case issued.After(now.Add(futureSkew)):
		return State{}, ErrStateInvalid
	case now.After(issued.Add(StateLifetime)):
		return State{}, ErrStateExpired
	}
	return State{State: sealed.State, Nonce: sealed.Nonce, Verifier: sealed.Verifier, Return: ReturnPath(sealed.Return)}, nil
}

func (s *StateSealer) mac(payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
