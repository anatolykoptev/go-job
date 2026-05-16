package websearch

import "io"

// BrowserDoer performs HTTP requests with browser-like TLS fingerprint.
// *stealth.Client satisfies this interface via BrowserClient().
type BrowserDoer interface {
	Do(method, url string, headers map[string]string, body io.Reader) ([]byte, map[string]string, int, error)
}

// ProxyPoolProvider returns the next proxy URL for rotation.
type ProxyPoolProvider interface {
	Next() string
}
