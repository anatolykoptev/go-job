package httputil

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

const ipv4InIPv6Bits = 96

// TrustedProxyClientIP returns a client-IP extractor that only believes
// X-Forwarded-For when the direct peer is a trusted proxy.
//
//   - Peer (r.RemoteAddr host) not in trusted: the peer is returned and every
//     forwarding header is ignored.
//   - Peer trusted: X-Forwarded-For (all header lines) is walked right to
//     left and the first hop that is not in trusted is returned (the
//     "rightmost untrusted" rule of labstack/echo ExtractIPFromXFFHeader and
//     realclientip-go RightmostTrustedRangeStrategy). If XFF is absent,
//     every hop is trusted, or a hop does not parse before an untrusted one
//     is found, the peer is returned.
//
// Differences from echo: when every hop is trusted echo returns the leftmost
// XFF entry, which is attacker-supplied; this returns the peer instead.
//
// A hop carrying a port or brackets ("1.2.3.4:80", "[2001:db8::1]:443") does
// not parse and falls back to the peer, so an edge that writes ports
// collapses all clients into the peer's key. That is deliberate: returning
// the raw hop as a key would let a trusted writer mint arbitrary buckets.
//
// The peer is returned canonicalised (IPv4-mapped IPv6 unmapped, zone
// stripped) so one client never maps to two keys. A RemoteAddr host that is
// not an IP (for example "" from a unix socket) is returned verbatim, so
// such callers all share one key.
//
// X-Real-IP is never read. Unlike [ClientIP], a client cannot choose the
// result by sending its own headers. An empty trusted list means "trust
// nobody": headers are always ignored.
func TrustedProxyClientIP(trusted []netip.Prefix) func(*http.Request) string {
	return func(r *http.Request) string {
		host := remoteHost(r.RemoteAddr)
		peer, err := netip.ParseAddr(host)
		if err != nil {
			return host
		}
		peerKey := canonical(peer)
		if !containsAddr(trusted, peer) {
			return peerKey
		}
		lines := r.Header.Values("X-Forwarded-For")
		for i := len(lines) - 1; i >= 0; i-- {
			hops := strings.Split(lines[i], ",")
			for j := len(hops) - 1; j >= 0; j-- {
				hop, err := netip.ParseAddr(strings.TrimSpace(hops[j]))
				if err != nil {
					return peerKey
				}
				if !containsAddr(trusted, hop) {
					return canonical(hop)
				}
			}
		}
		return peerKey
	}
}

// ParseTrustedProxies parses a comma-separated list of CIDRs or bare IPs
// (bare IPs become /32 or /128) for [TrustedProxyClientIP]. Empty entries are
// skipped; an invalid entry yields an error naming it. A list with no entries
// at all (empty input, only separators) is an error: an unset variable must
// not silently become "trust nobody".
func ParseTrustedProxies(csv string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, raw := range strings.Split(csv, ",") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		p, err := netip.ParsePrefix(entry)
		if err != nil {
			addr, aerr := netip.ParseAddr(entry)
			if aerr != nil {
				if strings.Contains(entry, "/") {
					return nil, fmt.Errorf("httputil: invalid trusted proxy %q: %w", entry, err)
				}
				return nil, fmt.Errorf("httputil: invalid trusted proxy %q: %w", entry, aerr)
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		if a := p.Addr(); a.Is4In6() && p.Bits() >= ipv4InIPv6Bits {
			p = netip.PrefixFrom(a.Unmap(), p.Bits()-ipv4InIPv6Bits)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("httputil: no trusted proxies in list")
	}
	return out, nil
}

func canonical(a netip.Addr) string {
	return a.Unmap().WithZone("").String()
}

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func containsAddr(prefixes []netip.Prefix, a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
