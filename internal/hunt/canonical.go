package hunt

import (
	"net/url"
	"sort"
	"strings"
)

// CanonicalURL normalizes a raw URL for deduplication purposes.
// Steps:
//  1. Parse URL — return raw on parse failure (preserves dedup invariance for malformed input).
//  2. Lowercase scheme + host.
//  3. Strip default port (http:80, https:443).
//  4. Trim trailing slash from path (root path becomes empty string).
//  5. Drop tracking query params (utm_*, gclid, fbclid, ref, ref_*, trk, src, source, mc_cid, mc_eid).
//  6. Sort remaining query params alphabetically (stable order).
//  7. Strip fragment (#section).
func CanonicalURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		// Not a recognisable URL — return as-is to preserve dedup invariance.
		return raw
	}

	// Lowercase scheme and host.
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)

	// Strip default ports.
	host := u.Hostname()
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		u.Host = host
	}

	// Trim trailing slash from path; root "/" becomes "".
	u.Path = strings.TrimRight(u.Path, "/")

	// Strip fragment.
	u.Fragment = ""

	// Filter tracking query params and sort the rest.
	q := u.Query()
	for key := range q {
		if isTrackingParam(key) {
			delete(q, key)
		}
	}
	if len(q) == 0 {
		u.RawQuery = ""
	} else {
		keys := make([]string, 0, len(q))
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var pairs []string
		for _, k := range keys {
			vals := q[k]
			for _, v := range vals {
				pairs = append(pairs, url.QueryEscape(k)+"="+url.QueryEscape(v))
			}
		}
		u.RawQuery = strings.Join(pairs, "&")
	}

	return u.String()
}

// CanonicalURLForSource applies source-aware canonicalization on top of CanonicalURL.
// Currently handled sources: "algora", "opire", "bountyhub" — these may wrap a GitHub
// issue URL in a query parameter. If such a param is found and decodes as a valid
// github.com/.../issues/N path, that canonical github URL is returned.
// Otherwise falls back to CanonicalURL(raw).
//
// No HTTP fetching is performed — only URL-level parsing.
func CanonicalURLForSource(raw, source string) string {
	switch strings.ToLower(source) {
	case "algora", "opire", "bountyhub":
		if gh := extractGitHubFromWrapper(raw); gh != "" {
			return gh
		}
	}
	return CanonicalURL(raw)
}

// extractGitHubFromWrapper looks for a github.com/owner/repo/issues/N URL embedded
// in the query params of a wrapper URL. Checks "id", "github_url", and "url" params.
// Returns the normalised https://github.com/... form, or "" if not found.
func extractGitHubFromWrapper(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	q := u.Query()
	for _, key := range []string{"id", "github_url", "url"} {
		val := q.Get(key)
		if val == "" {
			continue
		}
		// val may be a bare path like "github.com/foo/bar/issues/42" or a full URL.
		if gh := normalizeGitHubIssueParam(val); gh != "" {
			return gh
		}
	}
	return ""
}

// normalizeGitHubIssueParam accepts either a full https://github.com URL or a bare
// "github.com/owner/repo/issues/N" path and returns the canonical https:// form.
// Returns "" if the value does not match the expected github issue pattern.
func normalizeGitHubIssueParam(val string) string {
	// Ensure it has a scheme for url.Parse to work correctly.
	candidate := val
	if !strings.HasPrefix(candidate, "http://") && !strings.HasPrefix(candidate, "https://") {
		candidate = "https://" + candidate
	}
	u, err := url.Parse(candidate)
	if err != nil {
		return ""
	}
	if !strings.EqualFold(u.Host, "github.com") {
		return ""
	}
	// Expect path: /owner/repo/issues/N
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || !strings.EqualFold(parts[2], "issues") {
		return ""
	}
	// Rebuild clean canonical URL.
	return "https://github.com/" + parts[0] + "/" + parts[1] + "/issues/" + parts[3]
}

// trackingParams is the set of exact query keys stripped during canonicalization.
var trackingParams = map[string]struct{}{
	"utm_source":   {},
	"utm_medium":   {},
	"utm_campaign": {},
	"utm_term":     {},
	"utm_content":  {},
	"gclid":        {},
	"fbclid":       {},
	"ref":          {},
	"trk":          {},
	"src":          {},
	"source":       {},
	"mc_cid":       {},
	"mc_eid":       {},
}

// isTrackingParam reports whether a query key should be stripped during canonicalization.
func isTrackingParam(key string) bool {
	k := strings.ToLower(key)
	if _, ok := trackingParams[k]; ok {
		return true
	}
	if strings.HasPrefix(k, "utm_") {
		return true
	}
	if strings.HasPrefix(k, "ref_") {
		return true
	}
	return false
}
