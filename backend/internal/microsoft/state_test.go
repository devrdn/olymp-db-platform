package microsoft_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/microsoft"
)

var (
	secret  = []byte(strings.Repeat("k", 32))
	started = time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)
)

func sealer(at *time.Time) *microsoft.StateSealer {
	return microsoft.NewStateSealer(secret, func() time.Time { return *at })
}

func TestANewStateIsFreshEachTime(t *testing.T) {
	a, err := microsoft.NewState("/contests")
	if err != nil {
		t.Fatalf("NewState() = %v", err)
	}
	b, _ := microsoft.NewState("/contests")
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
	st, err := microsoft.NewState("/contests/42?tab=standings")
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

	now = started.Add(microsoft.StateLifetime)
	if _, err := s.Open(sealed); err != nil {
		t.Errorf("Open() at exactly ten minutes = %v, want it still valid", err)
	}
	now = started.Add(microsoft.StateLifetime + time.Second)
	if _, err := s.Open(sealed); !errors.Is(err, microsoft.ErrStateExpired) {
		t.Errorf("Open() past ten minutes = %v, want ErrStateExpired, whatever Max-Age the browser kept", err)
	}
	now = started.Add(-2 * time.Minute)
	if _, err := s.Open(sealed); !errors.Is(err, microsoft.ErrStateInvalid) {
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
		"another secret":   microsoft.NewStateSealer([]byte(strings.Repeat("o", 32)), func() time.Time { return now }).Seal(st),
		"the raw secret":   payload + "." + rawSecretMAC(payload),
		"payload not JSON": base64.RawURLEncoding.EncodeToString([]byte("plain")) + "." + derivedMAC("plain"),
	} {
		if _, err := s.Open(forged); !errors.Is(err, microsoft.ErrStateInvalid) {
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

func TestTheStateKeyIsDerivedFromTheSecret(t *testing.T) {
	now := started
	s := sealer(&now)
	st, _ := microsoft.NewState("/")
	payload, mac, _ := strings.Cut(s.Seal(st), ".")

	k := hmac.New(sha256.New, secret)
	k.Write([]byte("microsoft-state"))
	m := hmac.New(sha256.New, k.Sum(nil))
	m.Write([]byte(payload))
	if want := base64.RawURLEncoding.EncodeToString(m.Sum(nil)); mac != want {
		t.Error("the cookie is not signed with HMAC-SHA256(secret, \"microsoft-state\")")
	}
}

func TestTheReturnPathStaysOnThisSite(t *testing.T) {
	for raw, want := range map[string]string{
		"":                       "/",
		"/":                      "/",
		"/contests/42":           "/contests/42",
		"/contests?tab=mine#top": "/contests?tab=mine#top",
		"//evil.example":         "/",
		"///evil.example":        "/",
		"/\\evil.example":        "/",
		"\\\\evil.example":       "/",
		"https://evil.example/":  "/",
		"evil.example":           "/",
		"javascript:alert(1)":    "/",
		"/ok\r\nSet-Cookie: x=y": "/",
		"/tab\there":             "/",
		"/del\x7f":               "/",
		"/a b":                   "/",
		"/caf\u00e9":             "/",
		"/caf%C3%A9?q=%20":       "/caf%C3%A9?q=%20",
		"/" + strings.Repeat("a", microsoft.MaxReturnPathLength): "/",
	} {
		if got := microsoft.ReturnPath(raw); got != want {
			t.Errorf("ReturnPath(%.40q) = %q, want %q", raw, got, want)
		}
	}
}
