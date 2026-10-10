// Package svcauth attaches the fleet's service-to-service credential to
// outbound requests, and only to the internal services it is meant for.
//
// Callers of internal services (ox-browser, go-wowa, memdb-go) each set
// X-Internal-Secret by hand, and shared libraries that also fetch arbitrary
// public URLs cannot set it at all without leaking it to third parties.
// Transport scopes the header by destination instead: wrap the client once,
// list the internal base URLs, and every request to one of them carries the
// secret while every other request carries none — a caller-set
// X-Internal-Secret is stripped from it too.
//
// Redirects: a hop into a routed origin carries the secret only when the hop
// it came from was itself routed. A public page answering 302 to an internal
// service therefore cannot borrow the credential.
//
// Proxies: with a nil base the transport is http.DefaultTransport, which
// honours HTTP_PROXY. A plain-http internal route that is not in NO_PROXY is
// then sent, header included, through that proxy in cleartext. Pass a base
// with Proxy set to nil, or list the internal hosts in NO_PROXY.
package svcauth

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// HeaderInternalSecret is the header internal services check.
const HeaderInternalSecret = "X-Internal-Secret"

// EnvInternalSecret names the env var holding the shared secret.
const EnvInternalSecret = "INTERNAL_SERVICE_SECRET"

// Route binds one internal service, by base URL, to the secret it accepts.
type Route struct {
	// BaseURL is the service origin, e.g. "http://ox-browser:8901". Only
	// scheme, host and port are used; an empty BaseURL is skipped, so a
	// route can be built straight from an optional env var.
	BaseURL string
	// Secret is sent as X-Internal-Secret. An empty Secret sends nothing.
	Secret string
}

// Transport is an http.RoundTripper that adds X-Internal-Secret to requests
// whose scheme, host and port match a Route.
type Transport struct {
	base   http.RoundTripper
	routes map[string]string // origin key -> secret
}

// New wraps base (http.DefaultTransport when nil) with the given routes.
// It returns an error for a BaseURL that does not parse to scheme://host,
// so a typo fails at startup instead of silently sending no credential.
func New(base http.RoundTripper, routes ...Route) (*Transport, error) {
	if base == nil {
		base = http.DefaultTransport
	}
	t := &Transport{base: base, routes: make(map[string]string, len(routes))}
	for _, r := range routes {
		if strings.TrimSpace(r.BaseURL) == "" || r.Secret == "" {
			continue
		}
		u, err := url.Parse(r.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("svcauth: invalid base URL %q", r.BaseURL)
		}
		key := originKey(u)
		if prev, dup := t.routes[key]; dup && prev != r.Secret {
			return nil, fmt.Errorf("svcauth: origin %s listed twice with different secrets", key)
		}
		t.routes[key] = r.Secret
	}
	return t, nil
}

// FromEnv wraps base with one route per non-empty base URL, all using
// INTERNAL_SERVICE_SECRET.
func FromEnv(base http.RoundTripper, baseURLs ...string) (*Transport, error) {
	secret := os.Getenv(EnvInternalSecret)
	routes := make([]Route, 0, len(baseURLs))
	for _, b := range baseURLs {
		routes = append(routes, Route{BaseURL: b, Secret: secret})
	}
	return New(base, routes...)
}

// WrapClient returns a shallow copy of c (a new client when nil) whose
// Transport is wrapped with routes. c itself is not modified.
func WrapClient(c *http.Client, routes ...Route) (*http.Client, error) {
	var cc http.Client
	if c != nil {
		cc = *c
	}
	t, err := New(cc.Transport, routes...)
	if err != nil {
		return nil, err
	}
	cc.Transport = t
	return &cc, nil
}

// RoundTrip implements http.RoundTripper. The request is cloned before the
// header changes, as the RoundTripper contract requires. http.Client calls
// RoundTrip again for every redirect hop, with req.Response set to the
// response that redirected.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	secret, routed := t.secretFor(req)
	if routed || hasSecretHeader(req.Header) {
		req = req.Clone(req.Context())
		for k := range req.Header {
			if strings.EqualFold(k, HeaderInternalSecret) {
				delete(req.Header, k)
			}
		}
		if routed {
			req.Header.Set(HeaderInternalSecret, secret)
		}
	}
	return t.base.RoundTrip(req)
}

// hasSecretHeader matches the header case-insensitively, so a key written
// into the map by hand ("x-internal-secret") is stripped too.
func hasSecretHeader(h http.Header) bool {
	for k := range h {
		if strings.EqualFold(k, HeaderInternalSecret) {
			return true
		}
	}
	return false
}

// secretFor returns the secret for req's origin, provided every hop that led
// to req (when it is a redirect) was routed as well.
func (t *Transport) secretFor(req *http.Request) (string, bool) {
	if req.URL == nil {
		return "", false
	}
	secret, ok := t.routes[originKey(req.URL)]
	if !ok {
		return "", false
	}
	for prev := req.Response; prev != nil; {
		if prev.Request == nil || prev.Request.URL == nil {
			return "", false
		}
		if _, ok := t.routes[originKey(prev.Request.URL)]; !ok {
			return "", false
		}
		prev = prev.Request.Response
	}
	return secret, true
}

// CloseIdleConnections forwards to the base transport, so
// http.Client.CloseIdleConnections keeps working through WrapClient.
func (t *Transport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// originKey normalises scheme://host:port so "http://Ox-Browser:80" and
// "http://ox-browser" compare equal.
func originKey(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}
