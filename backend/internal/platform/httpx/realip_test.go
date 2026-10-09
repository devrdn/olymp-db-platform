package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func resolver(t *testing.T, trusted ...string) IPResolver {
	t.Helper()
	p, err := NewIPResolver(trusted)
	if err != nil {
		t.Fatalf("NewIPResolver(%v) returned error: %v", trusted, err)
	}
	return p
}

func request(remoteAddr, xff string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = remoteAddr
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

func TestDirectClientHeaderIsIgnored(t *testing.T) {
	p := resolver(t, "172.28.0.0/16")

	got := p.Resolve(request("203.0.113.7:41000", "10.0.0.1"))

	if got != "203.0.113.7" {
		t.Errorf("Resolve() = %q, want the peer address 203.0.113.7", got)
	}
}

func TestTrustedProxyYieldsTheForwardedClient(t *testing.T) {
	p := resolver(t, "172.28.0.0/16")

	got := p.Resolve(request("172.28.0.5:41000", "203.0.113.7"))

	if got != "203.0.113.7" {
		t.Errorf("Resolve() = %q, want the forwarded client 203.0.113.7", got)
	}
}

func TestClientPrependedSpoofIsNotTrusted(t *testing.T) {
	// The client sent "X-Forwarded-For: 10.9.9.9" and Caddy appended the real
	// peer.
	p := resolver(t, "172.28.0.0/16")

	got := p.Resolve(request("172.28.0.5:41000", "10.9.9.9, 203.0.113.7"))

	if got != "203.0.113.7" {
		t.Errorf("Resolve() = %q, want 203.0.113.7 (the spoofed 10.9.9.9 must not win)", got)
	}
}

func TestChainedTrustedProxiesAreSkipped(t *testing.T) {
	p := resolver(t, "172.28.0.0/16", "10.0.0.0/8")

	got := p.Resolve(request("172.28.0.5:41000", "203.0.113.7, 10.0.0.3"))

	if got != "203.0.113.7" {
		t.Errorf("Resolve() = %q, want 203.0.113.7 through two proxy hops", got)
	}
}

func TestAllForwardedEntriesTrustedFallsBackToPeer(t *testing.T) {
	p := resolver(t, "172.28.0.0/16")

	got := p.Resolve(request("172.28.0.5:41000", "172.28.0.9"))

	if got != "172.28.0.5" {
		t.Errorf("Resolve() = %q, want the peer when no untrusted hop exists", got)
	}
}

func TestMalformedForwardedEntryFallsBackToPeer(t *testing.T) {
	p := resolver(t, "172.28.0.0/16")

	got := p.Resolve(request("172.28.0.5:41000", "not-an-ip"))

	if got != "172.28.0.5" {
		t.Errorf("Resolve() = %q, want the peer for a malformed header", got)
	}
}

func TestNoTrustedProxiesMeansPeerAlways(t *testing.T) {
	p := resolver(t)

	got := p.Resolve(request("203.0.113.7:41000", "10.0.0.1"))

	if got != "203.0.113.7" {
		t.Errorf("Resolve() = %q, want the peer with an empty trust list", got)
	}
}

func TestBareIPInTrustListIsAccepted(t *testing.T) {
	p := resolver(t, "172.28.0.5")

	got := p.Resolve(request("172.28.0.5:41000", "203.0.113.7"))

	if got != "203.0.113.7" {
		t.Errorf("Resolve() = %q, want a bare IP to act as a /32", got)
	}
}

func TestInvalidTrustEntryIsRejectedAtConstruction(t *testing.T) {
	if _, err := NewIPResolver([]string{"not-a-cidr"}); err == nil {
		t.Error("NewIPResolver accepted an unparseable entry")
	}
}

func TestMiddlewareStoresTheResolvedAddressForClientIP(t *testing.T) {
	p := resolver(t, "172.28.0.0/16")
	var seen string
	handler := p.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = ClientIP(r)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), request("172.28.0.5:41000", "203.0.113.7"))

	if seen != "203.0.113.7" {
		t.Errorf("ClientIP() = %q, want the resolved client address", seen)
	}
}

func TestClientIPWithoutMiddlewareStillReturnsThePeer(t *testing.T) {
	got := ClientIP(request("203.0.113.7:41000", ""))

	if got != "203.0.113.7" {
		t.Errorf("ClientIP() = %q, want the RemoteAddr fallback", got)
	}
}
