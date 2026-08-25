package httpx

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// IPResolver determines the address a request actually came from when the
// service sits behind a reverse proxy.
//
// The problem it solves is not cosmetic: with the proxy as the only ingress,
// RemoteAddr is the proxy for every request, so the per-address login throttle
// collapses into one counter shared by the whole installation and the audit
// trail records the proxy instead of the participant.
//
// X-Forwarded-For cannot simply be believed either — a direct client writes
// whatever it likes into it. The resolver therefore trusts the header only
// when the TCP peer is a configured proxy, and within the header walks from
// the right (the entry the nearest proxy appended) to the left, skipping
// further trusted hops; the first entry that is not a trusted proxy is the
// client. Everything left of it is client-controlled noise.
type IPResolver struct {
	trusted []netip.Prefix
}

// NewIPResolver builds a resolver from CIDR prefixes; a bare address is
// accepted as a single-host prefix. An unparseable entry is an error — a typo
// in the deployment's TRUSTED_PROXIES must fail startup, not silently produce
// a resolver that trusts nobody and reintroduces the shared-counter bug.
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

// Resolve returns the client address for the request.
func (p IPResolver) Resolve(r *http.Request) string {
	peer, ok := parseHostAddr(r.RemoteAddr)
	if !ok {
		return ""
	}

	// A peer that is not a trusted proxy speaks for itself; its headers are
	// its own claims and stay ignored.
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
			// A malformed hop poisons everything left of it; the peer is the
			// last address that is actually known.
			return peer.String()
		}
		if !p.isTrusted(addr) {
			return addr.String()
		}
	}

	// Every hop was one of our proxies: internal traffic.
	return peer.String()
}

// Middleware resolves the client address once and stores it on the context,
// so ClientIP callers all see the same answer.
func (p IPResolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), clientIPKey{}, p.Resolve(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (p IPResolver) isTrusted(addr netip.Addr) bool {
	for _, prefix := range p.trusted {
		if prefix.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}

// clientIPKey is unexported so only the middleware can set the resolved value.
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
