package fetch

import (
	"net/url"
	"strings"
)

// defaultProxyFirstHosts is the built-in list of domains known to aggressively
// block non-browser / non-residential traffic. Requests to these hosts skip the
// direct attempt and go straight to the proxy tier.
var defaultProxyFirstHosts = []string{
	"linkedin.com",
	"x.com",
	"twitter.com",
	"instagram.com",
	"facebook.com",
	"amazon.com",
	"ebay.com",
	"glassdoor.com",
	"indeed.com",
	"ziprecruiter.com",
	"medium.com",
	"quora.com",
	"pinterest.com",
	"tiktok.com",
	"g2.com",
	"capterra.com",
	"trustpilot.com",
	"yelp.com",
}

// ProxyFirstDomains holds a set of domain suffixes whose requests should bypass
// the direct tier and go straight to proxy. Suffix matching handles subdomains
// (e.g. "www.linkedin.com" matches "linkedin.com").
type ProxyFirstDomains struct {
	set map[string]struct{}
}

// NewProxyFirstDomains creates a ProxyFirstDomains from the default list plus
// any extra entries provided by the caller.
func NewProxyFirstDomains(extra []string) *ProxyFirstDomains {
	combined := make([]string, 0, len(defaultProxyFirstHosts)+len(extra))
	combined = append(combined, defaultProxyFirstHosts...)
	combined = append(combined, extra...)

	set := make(map[string]struct{}, len(combined))
	for _, h := range combined {
		set[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	return &ProxyFirstDomains{set: set}
}

// MatchURL reports whether rawURL's host (or any of its parent domains) is in the
// proxy-first set. Returns false on URL parse error.
func (d *ProxyFirstDomains) MatchURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}

	// Walk from full host toward root, checking each suffix.
	// "sub.linkedin.com" → check "sub.linkedin.com", "linkedin.com", "com".
	for {
		if _, ok := d.set[host]; ok {
			return true
		}
		idx := strings.IndexByte(host, '.')
		if idx < 0 {
			break
		}
		host = host[idx+1:]
	}
	return false
}
