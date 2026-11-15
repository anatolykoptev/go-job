package httputil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/idna"
)

// ErrSSRFBlocked wraps every error CheckURL, GuardedDialContext, or a
// NewSSRFGuardedClient-wrapped transport returns when a target address is
// loopback, private (RFC1918 / RFC4193 ULA), link-local (including the cloud
// metadata address 169.254.169.254), unspecified, multicast, carrier-grade
// NAT (RFC 6598), or one of the IPv6 transition ranges that embed or route
// to an IPv4 address (NAT64, 6to4, the deprecated IPv4-compatible form) —
// the address classes an SSRF payload targets to reach internal
// infrastructure that must never be dialed from a caller-supplied target
// (e.g. an advertiser-provided website URL, a redirect Location header, an
// out-of-process render delegate's fetch target).
var ErrSSRFBlocked = errors.New("httputil: SSRF-blocked address")

const (
	guardedDialTimeout   = 10 * time.Second
	guardedDialKeepAlive = 30 * time.Second
)

// blockedPrefixes are the ranges the netip.Addr predicates (IsLoopback /
// IsPrivate / IsLinkLocalUnicast / IsLinkLocalMulticast / IsMulticast /
// IsUnspecified) do not cover. Sources: the IANA IPv4 and IPv6 Special-Purpose
// Address Registries (RFC 6890 and updates) and the transition mechanisms that
// embed an IPv4 address. Whole prefixes are blocked rather than unpacking the
// embedded address: a carried public IPv4 inside a translation or tunnel prefix
// is never a legitimate fetch target for this guard, and a prefix match cannot
// be looser than a per-address judgement.
//
// IPv4:
//   - 0.0.0.0/8        "this network" (RFC 1122); Linux connects to 0.0.0.0/8
//     as loopback.
//   - 100.64.0.0/10    CGNAT (RFC 6598).
//   - 192.0.0.0/24     IETF protocol assignments (RFC 6890): DS-Lite, NAT64
//     discovery, PCP/TURN anycast.
//   - 192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24   documentation (RFC 5737).
//   - 192.88.99.0/24   6to4 relay anycast (RFC 3068, deprecated by RFC 7526).
//   - 198.18.0.0/15    benchmarking (RFC 2544).
//   - 240.0.0.0/4      reserved (RFC 1112), includes 255.255.255.255 broadcast.
//
// IPv6 (an IPv4-mapped ::ffff:a.b.c.d is unmapped and judged as IPv4 before
// any of these apply):
//   - ::/96            IPv4-compatible (deprecated, RFC 4291); also ::, ::1.
//   - ::ffff:0:0:0/96  IPv4-translated (SIIT, RFC 2765).
//   - 64:ff9b::/96     NAT64 well-known prefix (RFC 6052).
//   - 64:ff9b:1::/48   local-use NAT64 (RFC 8215).
//   - 100::/64         discard-only (RFC 6666).
//   - 2001::/23        IETF protocol assignments (RFC 2928), includes Teredo
//     2001::/32 (RFC 4380), which embeds an obfuscated IPv4.
//   - 2001:db8::/32    documentation (RFC 3849).
//   - 2002::/16        6to4 (RFC 3056).
//   - 3fff::/20        documentation (RFC 9637).
//   - 5f00::/16        SRv6 SIDs (RFC 9602).
//   - fec0::/10        deprecated site-local (RFC 3879).
var blockedPrefixes = mustParsePrefixes(
	"0.0.0.0/8",
	"100.64.0.0/10",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"240.0.0.0/4",
	"::/96",
	"::ffff:0:0:0/96",
	"64:ff9b::/96",
	"64:ff9b:1::/48",
	"100::/64",
	"2001::/23",
	"2001:db8::/32",
	"2002::/16",
	"3fff::/20",
	"5f00::/16",
	"fec0::/10",
)

func mustParsePrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			panic(fmt.Sprintf("httputil: invalid CIDR %q: %v", c, err))
		}
		out = append(out, p)
	}
	return out
}

// allowedSchemes is the scheme allowlist CheckURL enforces. A caller that
// hands a raw URL to an out-of-process render delegate (a headless browser,
// a fetch microservice) must reject file:// and gopher:// etc. before
// dispatch — those schemes can read local files or speak arbitrary
// protocols to internal services, and no dial-time guard in THIS package
// can intervene once the delegate's own client executes them.
var allowedSchemes = map[string]bool{
	"http":  true,
	"https": true,
}

// IsBlockedIP reports whether ip must never be dialed as a fetch/render
// target. This is the single, framework-owned SSRF block-list — every other
// primitive in this file (GuardedDialContext, NewSSRFGuardedClient,
// CheckURL) is built on top of this one predicate. A nil or malformed IP is
// treated as blocked (fail closed).
//
// An IPv4-mapped IPv6 address (::ffff:a.b.c.d) is judged by the IPv4 address
// it carries. Addresses in a transition prefix that embeds an IPv4 address
// (NAT64, 6to4, Teredo, IPv4-compatible, IPv4-translated) are blocked
// wholesale; see blockedPrefixes for the full list of ranges and sources.
func IsBlockedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	return isBlockedAddr(addr)
}

func isBlockedAddr(addr netip.Addr) bool {
	addr = addr.Unmap().WithZone("")
	if !addr.IsValid() {
		return true
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() {
		return true
	}
	if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() || addr.IsMulticast() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// GuardedDialContext wraps base with a Control hook that refuses to connect
// to a blocked address (see IsBlockedIP). The check runs on the
// ALREADY-RESOLVED address at connect time — after DNS lookup, immediately
// before the connect(2) syscall — which is what defeats DNS-rebinding: a
// hostname that resolves to a public IP when net/http first looks it up but
// resolves to a private IP by the time this fires is still caught, because
// the check inspects the literal address about to be dialed, never the
// hostname string. Any pre-existing Control hook on base still runs first.
//
// base == nil uses a Dialer with sane guarded-fetch defaults (10s connect
// timeout, 30s keepalive).
//
// The guard is installed in the copy's ControlContext (Go ignores Control when
// ControlContext is set), chained after the caller's ControlContext or, if
// that is nil, their Control. A caller hook can refuse a connection but never
// skip the check.
//
// This guards connections made by the returned dial function only. A
// DialTLSContext or DialTLS on an http.Transport built around it bypasses this
// check for HTTPS.
func GuardedDialContext(base *net.Dialer) func(ctx context.Context, network, address string) (net.Conn, error) {
	return guardedDialContext(base, nil)
}

// guardedDialContext is GuardedDialContext with an optional resolver seam.
//
// res == nil: the dialer resolves and connects natively (Happy Eyeballs
// included) and its Control hook, which net.Dialer invokes for EVERY address
// it attempts, enforces the policy on the literal address being connected to.
//
// res != nil: the host is resolved through res, the dial is refused outright if
// ANY returned address is blocked (same rule as CheckURL), then each address is
// dialed in order through the same Control-guarded dialer, so the connected
// address is still the one checked.
func guardedDialContext(base *net.Dialer, res Resolver) func(ctx context.Context, network, address string) (net.Conn, error) {
	var d net.Dialer
	switch {
	case base != nil:
		d = *base // shallow copy: never mutate the caller's *net.Dialer
	default:
		d = net.Dialer{Timeout: guardedDialTimeout, KeepAlive: guardedDialKeepAlive}
	}
	// Go ignores Control when ControlContext is non-nil, so the guard always
	// goes in ControlContext and Control is cleared on the copy. The caller's
	// ControlContext is chained if set, otherwise their Control; if both are
	// set Go would have used ControlContext, and so do we.
	prevControl, prevControlCtx := d.Control, d.ControlContext
	d.Control = nil
	d.ControlContext = func(ctx context.Context, network, address string, c syscall.RawConn) error {
		switch {
		case prevControlCtx != nil:
			if err := prevControlCtx(ctx, network, address, c); err != nil {
				return err
			}
		case prevControl != nil:
			if err := prevControl(network, address, c); err != nil {
				return err
			}
		}
		return DenyBlockedAddress(network, address)
	}
	if res == nil {
		return d.DialContext
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || net.ParseIP(host) != nil || isUnixNetwork(network) {
			return d.DialContext(ctx, network, address)
		}
		addrs, err := resolveChecked(ctx, res, host)
		if err != nil {
			return nil, err
		}
		return dialEach(ctx, &d, network, port, addrs)
	}
}

// dialEach dials addrs in order and returns the first connection; if all fail,
// the first error.
func dialEach(ctx context.Context, d *net.Dialer, network, port string, addrs []net.IPAddr) (net.Conn, error) {
	var firstErr error
	for _, a := range addrs {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, firstErr
}

func isUnixNetwork(network string) bool {
	switch network {
	case "unix", "unixgram", "unixpacket":
		return true
	}
	return false
}

// Resolver resolves a host name to IP addresses. *net.Resolver satisfies it;
// pass a fake via WithResolver to test DNS-rebind and DNS-error behaviour
// without the network.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// resolveChecked resolves host through res and fails closed: a resolver error
// (wrapped, the original stays in the chain), an empty answer, or ANY blocked
// address in the answer is an ErrSSRFBlocked.
func resolveChecked(ctx context.Context, res Resolver, host string) ([]net.IPAddr, error) {
	addrs, err := res.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve %q: %w", ErrSSRFBlocked, host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w: %q resolved to no addresses", ErrSSRFBlocked, host)
	}
	for _, a := range addrs {
		if IsBlockedIP(a.IP) {
			return nil, fmt.Errorf("%w: %s resolves to %s", ErrSSRFBlocked, host, a.IP)
		}
	}
	return addrs, nil
}

// DenyBlockedAddress is the Control-hook body: it inspects the
// ALREADY-RESOLVED network/address pair — exactly what net.Dialer.Control
// receives, and what GuardedDialContext wires this into — and refuses
// anything IsBlockedIP flags. Exported (split out from GuardedDialContext
// originally so tests could drive it directly with a hardcoded
// post-resolution address, simulating exactly what net/http passes after DNS
// lookup, without needing a real DNS rebind) so a caller wiring a bespoke
// Control-hook-shaped seam on an opaque transport this package does not
// itself dial (e.g. a stealth/fingerprint-masking client's own dialer hook)
// can reuse the identical policy GuardedDialContext uses internally, rather
// than duplicating it. See SSRFGuards for the paired redirect-guard closure.
func DenyBlockedAddress(network, address string) error {
	if isUnixNetwork(network) {
		// A Unix domain socket dials a local filesystem path, not a network
		// address (address is e.g. "/var/run/sidecar.sock", not host:port) —
		// it is not an SSRF-to-internal-IP vector, since the caller already
		// chose a specific local socket path rather than a resolvable
		// hostname. Blocking it here would make this guard unusable for a
		// future framework consumer whose base Transport dials a UDS
		// sidecar (a legitimate, increasingly common pattern).
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address // no port present — shouldn't happen for a tcp/udp dial target
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A resolved dial target that isn't a literal IP is unexpected; fail
		// closed rather than let an unparseable address through.
		return fmt.Errorf("%w: cannot parse dial address %q (%s)", ErrSSRFBlocked, address, network)
	}
	if IsBlockedIP(ip) {
		return fmt.Errorf("%w: %s (%s)", ErrSSRFBlocked, ip, network)
	}
	return nil
}

// guardedTransport returns an *http.Transport cloned from
// http.DefaultTransport (preserving its proxy / idle-conn / HTTP2 defaults —
// NOTE this means Proxy: http.ProxyFromEnvironment is preserved too, which
// is exactly the case wrapTransportTier's outer CheckURL layer exists to
// cover) with DialContext replaced by GuardedDialContext, so every DIRECT
// (non-proxied) connection made through it is SSRF-safe at connect time.
func guardedTransport(cfg ssrfConfig) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // http.DefaultTransport is always *http.Transport per stdlib contract
	t.DialContext = guardedDialContext(cfg.dialer, cfg.resolver)
	return t
}

// wrapTransportTier composes the pre-request CheckURL guard (guardedRoundTripper)
// on top of an already dial-guarded *http.Transport t.
//
// Why both layers are needed: when t.Proxy is non-nil (either explicitly set
// by a caller, or inherited from http.DefaultTransport's
// Proxy: http.ProxyFromEnvironment via guardedTransport()/Clone()), net/http
// calls DialContext with the PROXY's host:port, not the real destination —
// the real target lives in the request line (plain HTTP) or the CONNECT
// tunnel target (HTTPS), and t.DialContext (GuardedDialContext) never sees
// it. A proxied request to an internal target would sail straight through
// the connect-time guard, which only ever inspects the (public, allowed)
// proxy address. guardedRoundTripper's CheckURL(req.URL) runs BEFORE t is
// even reached and always evaluates the real destination URL regardless of
// whether the request ends up proxied, closing that hole.
//
// Net effect per request:
//   - direct (no proxy in play): BOTH tiers fire — the pre-request CheckURL
//     here, plus GuardedDialContext's connect-time, DNS-rebind-proof check
//     already wired into t.
//   - proxied: only the pre-request CheckURL fires (the same pre-resolve
//     tier CheckURL's own GoDoc describes — weaker than connect-time against
//     DNS-rebind, but real: it refuses the request outright for a target
//     that resolves blocked at check time). http.Client re-invokes
//     RoundTrip on every redirect hop, so each hop's real destination is
//     re-checked too.
func wrapTransportTier(t *http.Transport, cfg ssrfConfig) http.RoundTripper {
	return &guardedRoundTripper{next: t, resolver: cfg.resolver}
}

// ssrfConfig is the resolved set of NewSSRFGuardedClient options.
type ssrfConfig struct {
	dialer   *net.Dialer
	resolver Resolver
}

// SSRFOption customises NewSSRFGuardedClient.
type SSRFOption func(*ssrfConfig)

// WithDialer sets the base net.Dialer the guarded transport connects with.
// Its ControlContext hook (or Control, when ControlContext is nil, as in
// net.Dialer itself) is kept and runs first; the SSRF check runs after it on the
// address actually being connected to, so a caller can add its own
// connect-time restrictions (extra ranges, socket options) without losing the
// guard. The dialer is copied, never mutated. Without this option the
// transport uses a fresh dialer (10s connect timeout, 30s keepalive); a
// DialContext already set on a caller's *http.Transport is replaced and cannot
// be composed, so use this option to carry a Control hook.
func WithDialer(d *net.Dialer) SSRFOption {
	return func(c *ssrfConfig) { c.dialer = d }
}

// WithResolver replaces net.DefaultResolver for BOTH the pre-request CheckURL
// tier and the connect-time dial, so tests can simulate DNS errors and
// DNS-rebinding (a name that resolves public at check time and internal at
// dial time) without the network. With a resolver set, the dial refuses a host
// if any of its resolved addresses is blocked and dials the addresses
// sequentially instead of racing them. A nil r keeps the default.
func WithResolver(r Resolver) SSRFOption {
	return func(c *ssrfConfig) { c.resolver = r }
}

// NewSSRFGuardedClient returns an SSRF-guarded *http.Client built on top of
// base. base == nil returns a fresh client with a guardedTransport().
//
// Two composition tiers, chosen by what base.Transport actually is:
//
//   - nil or *http.Transport: cloned (Clone() preserves TLSClientConfig,
//     proxy, HTTP2 settings, MaxIdleConns, etc. — every field a caller may
//     have set) with DialContext replaced by GuardedDialContext, THEN
//     wrapped with the pre-request CheckURL layer (wrapTransportTier) — see
//     wrapTransportTier's doc for why a proxy-configured Transport needs
//     both layers, not just the dial-time one.
//   - any other http.RoundTripper (e.g. a stealth/fingerprint-masking
//     client whose Transport performs its own dial via a bespoke backend,
//     with no DialContext/net.Dialer hook exposed at all): wrapped with
//     ONLY the pre-request CheckURL layer — the necessarily WEAKER
//     pre-resolve tier (a DNS-rebind can still occur between this check and
//     the delegate's own, separate resolution), but the best guarantee
//     available without reaching into a dial mechanism this package does
//     not own.
//
// A DialTLSContext or DialTLS set on a supplied *http.Transport is kept by
// Clone and bypasses the connect-time check for HTTPS; only the pre-request
// CheckURL tier covers such connections.
//
// Options (WithDialer, WithResolver) extend the guard without weakening it. The
// client's CheckRedirect is kept, and every redirect hop re-enters the guarded
// transport, so a redirect to a blocked address is refused like a direct one.
//
// The returned *http.Client is a shallow copy of base; base itself (and its
// Transport) is never mutated.
func NewSSRFGuardedClient(base *http.Client, opts ...SSRFOption) *http.Client {
	var cfg ssrfConfig
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if base == nil {
		return &http.Client{Transport: wrapTransportTier(guardedTransport(cfg), cfg)}
	}
	cc := *base
	switch t := base.Transport.(type) {
	case nil:
		cc.Transport = wrapTransportTier(guardedTransport(cfg), cfg)
	case *http.Transport:
		tc := t.Clone()
		tc.DialContext = guardedDialContext(cfg.dialer, cfg.resolver)
		cc.Transport = wrapTransportTier(tc, cfg)
	default:
		cc.Transport = &guardedRoundTripper{next: t, resolver: cfg.resolver}
	}
	return &cc
}

// guardedRoundTripper wraps an arbitrary http.RoundTripper with a
// pre-request SSRF check (see CheckURL) on the outbound request's URL. This
// composes with ANY RoundTripper implementation — including a dial-guarded
// *http.Transport (see wrapTransportTier, which layers this on top of the
// *http.Transport tier too, to cover the proxy case) — it never touches the
// wrapped one's internal dial mechanics, so a stealth/fingerprint-masking
// implementation is untouched on the allow path.
type guardedRoundTripper struct {
	next     http.RoundTripper
	resolver Resolver // nil = net.DefaultResolver
}

func (g *guardedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := checkURL(req.Context(), req.URL, g.resolver); err != nil {
		return nil, err
	}
	return g.next.RoundTrip(req)
}

// CheckURL is a pre-handoff safety check for a URL this package does not
// itself dial — e.g. before handing an "any place" URL to an out-of-process
// render delegate (a headless browser, a fetch microservice) whose own
// outbound dial GuardedDialContext / NewSSRFGuardedClient cannot reach.
// Call it as close as possible to the point of dispatch, and again after
// each redirect hop the delegate reports, to minimize the DNS-rebind
// window — CheckURL is necessarily weaker against rebinding than
// GuardedDialContext, since DNS can change between this resolution and the
// delegate's own, separate one.
//
// Enforces two things:
//  1. Scheme allowlist — only "http" and "https" are permitted; a headless
//     browser coerced into "file://" or "gopher://" bypasses every IP-based
//     check below, since those schemes never dial the checked host at all.
//  2. Every resolved address for the URL's host passes IsBlockedIP.
//
// A host that is a literal IP is checked directly. A host that LOOKS like a
// non-standard numeral encoding of an IP (decimal, octal, or hex — e.g.
// "2130706433", "0x7f000001", or "012.0.0.1") but fails net.ParseIP is
// refused outright rather than handed to DNS resolution: some resolvers
// (notably glibc's getaddrinfo via cgo) still parse these forms as literal
// IPs, which would silently defeat this check if it fell through to a
// same-string-but-different DNS lookup.
//
// A DNS failure is an ErrSSRFBlocked too (the resolver error stays in the
// chain), so a caller that only tests errors.Is(err, ErrSSRFBlocked) fails
// closed. A resolver answering with zero addresses is blocked as well.
func CheckURL(ctx context.Context, u *url.URL) error {
	return checkURL(ctx, u, nil)
}

func checkURL(ctx context.Context, u *url.URL, res Resolver) error {
	if res == nil {
		res = net.DefaultResolver
	}
	if u == nil {
		return fmt.Errorf("%w: nil URL", ErrSSRFBlocked)
	}
	if !allowedSchemes[strings.ToLower(u.Scheme)] {
		return fmt.Errorf("%w: scheme %q not allowed (http/https only)", ErrSSRFBlocked, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrSSRFBlocked)
	}
	if ip := net.ParseIP(host); ip != nil {
		if IsBlockedIP(ip) {
			return fmt.Errorf("%w: %s", ErrSSRFBlocked, ip)
		}
		return nil
	}
	if looksLikeAltEncodedIP(host) {
		return fmt.Errorf("%w: host %q looks like a non-standard IP encoding", ErrSSRFBlocked, host)
	}
	_, err := resolveChecked(ctx, res, host)
	return err
}

// CheckRawURL parses rawURL and delegates to CheckURL. Convenience for
// callers holding a string (a redirect Location header, an MCP tool
// argument) rather than an already-parsed *url.URL.
func CheckRawURL(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: parse %q: %w", ErrSSRFBlocked, rawURL, err)
	}
	return CheckURL(ctx, u)
}

// maxSSRFRedirectHops caps how many redirect hops the redirect closure
// SSRFGuards returns will follow before refusing. Owned ONCE here, not
// duplicated per consumer: installing a custom redirect-decision hook (a
// stdlib http.Client.CheckRedirect, or an adapter around a non-stdlib
// equivalent) REPLACES net/http's own built-in 10-hop cap, not just its SSRF
// behavior. A consumer that re-implements the SSRF check but forgets this
// trades a bounded redirect chain for an unbounded one — a self-redirecting
// or looping target then hangs the caller until its own request timeout
// instead of failing fast. Keeping the cap here, next to the check it
// replaces, means every consumer gets it for free and identically.
const maxSSRFRedirectHops = 10

// SSRFGuards returns the pair of stdlib-typed closures an opaque-transport
// HTTP client (one whose Transport performs its own dial/redirect via a
// bespoke backend this package cannot reach — e.g. a stealth/
// fingerprint-masking client) installs to close the SAME two gaps
// NewSSRFGuardedClient already closes for a plain *http.Transport-backed
// client:
//
//   - dial is DenyBlockedAddress itself — wire it into whatever
//     Control-hook-shaped seam the opaque transport exposes (directly, if it
//     accepts a net.Dialer.Control-shaped func, or behind a thin adapter
//     otherwise) for the rebind-proof, connect-time check.
//   - redirect enforces the ≤maxSSRFRedirectHops cap (see that const's doc
//     for why re-owning it is required, not optional) and THEN
//     CheckURL(req.Context(), req.URL) on every hop — wire it into whatever
//     per-hop redirect-decision seam the opaque transport exposes. Its
//     signature matches http.Client.CheckRedirect exactly, so a
//     stdlib-backed caller can assign it directly with no adapter.
//
// Named for the CAPABILITY (this package's framework-owned SSRF policy), not
// any one consumer — this package takes no dependency on whatever
// opaque-transport client ends up wiring these in, and the identical pair of
// closures can be handed to more than one backend so their behavior cannot
// drift apart.
func SSRFGuards() (
	redirect func(req *http.Request, via []*http.Request) error,
	dial func(network, address string) error,
) {
	redirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxSSRFRedirectHops {
			return fmt.Errorf("httputil: stopped after %d redirects", maxSSRFRedirectHops)
		}
		return CheckURL(req.Context(), req.URL)
	}
	dial = DenyBlockedAddress
	return redirect, dial
}

// looksLikeAltEncodedIP reports whether the WHOLE host is an alternate
// encoding of an IPv4 address that net.ParseIP rejects but a permissive
// resolver (glibc getaddrinfo / inet_aton) still reads as one — a classic SSRF
// filter bypass.
//
// inet_aton accepts one to four dot-separated parts, each decimal, octal
// (leading 0) or hex (0x/0X prefix; a bare "0x" is zero), with the last part
// filling the remaining bytes: "2130706433", "0x7f000001", "0x7f.0.0.1",
// "0177.0.0.1", "127.1", "127.0.1". The host matches only if EVERY part has
// one of those shapes, so DNS names that merely contain "0x" or a zero-led
// label ("0x.org", "docs.0x.org", "01.example.com") are not matched. Numeric
// range is deliberately not validated: an all-numeric host is not a DNS name
// (a TLD is never numeric), so refusing an out-of-range one costs nothing and
// cannot loosen the check. One trailing dot (an absolute name) is ignored, and
// a non-ASCII host is checked in its IDNA ASCII form so full-width digits
// cannot hide a numeric host.
func looksLikeAltEncodedIP(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return false
	}
	if !isASCII(host) {
		ascii, err := idna.Lookup.ToASCII(host)
		if err != nil {
			return false
		}
		host = strings.TrimSuffix(ascii, ".")
	}
	parts := strings.Split(host, ".")
	if len(parts) > 4 { //nolint:mnd // inet_aton takes at most 4 parts
		return false
	}
	for _, p := range parts {
		if !isNumericIPPart(p) {
			return false
		}
	}
	return true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 { //nolint:mnd // ASCII boundary
			return false
		}
	}
	return true
}

// isNumericIPPart reports whether p is a decimal, octal or 0x-prefixed hex
// number as inet_aton reads one.
func isNumericIPPart(p string) bool {
	if p == "" {
		return false
	}
	if len(p) >= 2 && p[0] == '0' && (p[1] == 'x' || p[1] == 'X') {
		for _, r := range p[2:] {
			if !isHexDigit(r) {
				return false
			}
		}
		return true // bare "0x" parses as zero in glibc
	}
	for _, r := range p {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isHexDigit(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
}
