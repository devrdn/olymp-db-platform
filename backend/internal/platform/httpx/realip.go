package httpx

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// IPResolver determines the address a request came from behind a reverse
// proxy, where RemoteAddr is always the proxy (CLAUDE.md rule 9).
//
// X-Forwarded-For is trusted only when the TCP peer is a configured proxy.
// The header is walked from the right, skipping trusted hops; the first
// untrusted entry is the client, and everything left of it is client noise.
type IPResolver struct {
	trusted []netip.Prefix
}

// NewIPResolver builds a resolver from CIDR prefixes; a bare address is a
// single-host prefix. An unparseable entry is an error, so a typo in
// TRUSTED_PROXIES fails startup instead of silently trusting nobody.
func NewIPResolver(trusted []string) (IPResolver, error) {
	prefixes := make([]netip.Prefix, 0, len(trusted))
	for _, entry := range trusted {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(entry); err == nil {
			prefixes = append(prefixes, prefix)
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return IPResolver{}, fmt.Errorf("trusted proxy %q is neither a CIDR nor an address", entry)
		}
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return IPResolver{trusted: prefixes}, nil
}

func (p IPResolver) Resolve(r *http.Request) string {
	peer, ok := parseHostAddr(r.RemoteAddr)
	if !ok {
		return ""
	}

	// An untrusted peer's forwarded headers are ignored.
	if !p.isTrusted(peer) {
		return peer.String()
	}

	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return peer.String()
	}

	hops := strings.Split(forwarded, ",")
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			// A malformed hop poisons everything left of it.
			return peer.String()
		}
		if !p.isTrusted(addr) {
			return addr.String()
		}
	}

	return peer.String()
}

// Middleware resolves the client address once and stores it on the context,
// with whether the TCP peer is a configured proxy (see forwardedTrusted).
func (p IPResolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), clientIPKey{}, p.Resolve(r))
		ctx = context.WithValue(ctx, forwardedTrustKey{}, p.trustsPeer(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// trustsPeer reports whether the request's TCP peer is a configured proxy.
func (p IPResolver) trustsPeer(r *http.Request) bool {
	peer, ok := parseHostAddr(r.RemoteAddr)
	return ok && p.isTrusted(peer)
}

// forwardedTrustKey marks a request whose peer this resolver vouched for.
type forwardedTrustKey struct{}

// forwardedTrusted reports whether this request's forwarded headers may be
// believed at all. isTLS asks it too, so X-Forwarded-Proto passes the same
// gate as X-Forwarded-For (CLAUDE.md rule 9). It is false when the middleware
// never ran, which is the safe answer.
func forwardedTrusted(r *http.Request) bool {
	trusted, ok := r.Context().Value(forwardedTrustKey{}).(bool)
	return ok && trusted
}

func (p IPResolver) isTrusted(addr netip.Addr) bool {
	for _, prefix := range p.trusted {
		if prefix.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}

type clientIPKey struct{}

func parseHostAddr(remoteAddr string) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(remoteAddr)
	if err == nil {
		return ap.Addr(), true
	}
	addr, err := netip.ParseAddr(remoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr, true
}
