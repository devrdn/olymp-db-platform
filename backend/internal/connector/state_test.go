package connector_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/connector"
)

var (
	secret  = []byte(strings.Repeat("k", 32))
	started = time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)
)

func sealer(at *time.Time) *connector.StateSealer {
	return connector.NewStateSealer(secret, "microsoft", func() time.Time { return *at })
}

func TestANewStateIsFreshEachTime(t *testing.T) {
	a, err := connector.NewState("/contests")
	if err != nil {
		t.Fatalf("NewState() = %v", err)
	}
	b, _ := connector.NewState("/contests")
	if a.State == b.State || a.Nonce == b.Nonce || a.Verifier == b.Verifier {
		t.Errorf("two states share a value: %+v and %+v", a, b)
	}
	if len(a.State) < 22 || len(a.Nonce) < 22 {
		t.Errorf("state %q, nonce %q: want at least 128 random bits each", a.State, a.Nonce)
	}
	if len(a.Verifier) < 43 || len(a.Verifier) > 128 {
		t.Errorf("verifier is %d characters, want 43 to 128 (RFC 7636)", len(a.Verifier))
	}
	if a.Return != "/contests" {
		t.Errorf("return = %q", a.Return)
	}
}

func TestTheStateCookieIsSignedAndExpires(t *testing.T) {
	now := started
	s := sealer(&now)
	st, err := connector.NewState("/contests/42?tab=standings")
	if err != nil {
		t.Fatalf("NewState() = %v", err)
	}
	sealed := s.Seal(st)

	opened, err := s.Open(sealed)
	if err != nil {
		t.Fatalf("Open() of a fresh cookie = %v", err)
	}
	if opened.State != st.State || opened.Nonce != st.Nonce || opened.Verifier != st.Verifier || opened.Return != st.Return {
		t.Errorf("opened %+v, want %+v", opened, st)
	}
	if strings.Contains(sealed, string(secret)) {
		t.Error("the cookie carries the secret")
	}

	now = started.Add(connector.StateLifetime)
	if _, err := s.Open(sealed); err != nil {
		t.Errorf("Open() at exactly ten minutes = %v, want it still valid", err)
	}
	now = started.Add(connector.StateLifetime + time.Second)
	if _, err := s.Open(sealed); !errors.Is(err, connector.ErrStateExpired) {
		t.Errorf("Open() past ten minutes = %v, want ErrStateExpired, whatever Max-Age the browser kept", err)
	}
	now = started.Add(-2 * time.Minute)
	if _, err := s.Open(sealed); !errors.Is(err, connector.ErrStateInvalid) {
		t.Errorf("Open() of a cookie issued in the future = %v, want ErrStateInvalid", err)
	}

	now = started
	payload, mac, _ := strings.Cut(sealed, ".")
	for name, forged := range map[string]string{
		"empty":            "",
		"no signature":     payload,
		"another payload":  base64.RawURLEncoding.EncodeToString([]byte(`{"s":"x","n":"y","v":"z","r":"/","t":1}`)) + "." + mac,
		"a cut signature":  payload + "." + mac[:len(mac)-2],
		"two dots":         sealed + ".x",
		"not base64":       "!!!." + mac,
		"oversized":        strings.Repeat("a", 8<<10) + "." + mac,
		"another secret":   connector.NewStateSealer([]byte(strings.Repeat("o", 32)), "microsoft", func() time.Time { return now }).Seal(st),
		"the raw secret":   payload + "." + rawSecretMAC(payload),
		"payload not JSON": base64.RawURLEncoding.EncodeToString([]byte("plain")) + "." + derivedMAC("plain"),
	} {
		if _, err := s.Open(forged); !errors.Is(err, connector.ErrStateInvalid) {
			t.Errorf("%s: Open() = %v, want ErrStateInvalid", name, err)
		}
	}
}

// rawSecretMAC signs with DEVICE_COOKIE_SECRET itself, which the sealer must
// never use as its key.
func rawSecretMAC(payload string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// derivedMAC signs as the sealer does, so only the payload is wrong.
func derivedMAC(plain string) string {
	k := hmac.New(sha256.New, secret)
	k.Write([]byte("microsoft-state"))
	m := hmac.New(sha256.New, k.Sum(nil))
	m.Write([]byte(base64.RawURLEncoding.EncodeToString([]byte(plain))))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Anything but a path on this site returns to the sign-in page, which sends a
// signed-in visitor on as a password sign-in would.
func TestTheReturnPathStaysOnThisSite(t *testing.T) {
	if connector.DefaultReturnPath != "/login" {
		t.Errorf("DefaultReturnPath = %q, want /login", connector.DefaultReturnPath)
	}
	for raw, want := range map[string]string{
		"":                       connector.DefaultReturnPath,
		"/":                      "/",
		"/contests/42":           "/contests/42",
		"/contests?tab=mine#top": "/contests?tab=mine#top",
		"//evil.example":         connector.DefaultReturnPath,
		"///evil.example":        connector.DefaultReturnPath,
		"/\\evil.example":        connector.DefaultReturnPath,
		"\\\\evil.example":       connector.DefaultReturnPath,
		"https://evil.example/":  connector.DefaultReturnPath,
		"evil.example":           connector.DefaultReturnPath,
		"javascript:alert(1)":    connector.DefaultReturnPath,
		"/ok\r\nSet-Cookie: x=y": connector.DefaultReturnPath,
		"/tab\there":             connector.DefaultReturnPath,
		"/del\x7f":               connector.DefaultReturnPath,
		"/a b":                   connector.DefaultReturnPath,
		"/caf\u00e9":             connector.DefaultReturnPath,
		"/caf%C3%A9?q=%20":       "/caf%C3%A9?q=%20",
		"/" + strings.Repeat("a", connector.MaxReturnPathLength): connector.DefaultReturnPath,
	} {
		if got := connector.ReturnPath(raw); got != want {
			t.Errorf("ReturnPath(%.40q) = %q, want %q", raw, got, want)
		}
	}
}

func TestTheStateKeyIsDerivedFromTheSecretAndTheProvider(t *testing.T) {
	now := started
	st, _ := connector.NewState("/")
	sealed := connector.NewStateSealer(secret, "microsoft", func() time.Time { return now }).Seal(st)

	payload, mac, _ := strings.Cut(sealed, ".")
	k := hmac.New(sha256.New, secret)
	k.Write([]byte("microsoft-state"))
	m := hmac.New(sha256.New, k.Sum(nil))
	m.Write([]byte(payload))
	if want := base64.RawURLEncoding.EncodeToString(m.Sum(nil)); mac != want {
		t.Error("the cookie is not signed with HMAC-SHA256(secret, \"microsoft-state\")")
	}
	if _, err := connector.NewStateSealer(secret, "google", func() time.Time { return now }).Open(sealed); !errors.Is(err, connector.ErrStateInvalid) {
		t.Errorf("Open() under another provider = %v, want ErrStateInvalid", err)
	}
}
