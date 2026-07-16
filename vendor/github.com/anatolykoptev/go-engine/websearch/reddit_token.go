package websearch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	redditTokenURL = "https://www.reddit.com/api/v1/access_token" //nolint:gosec // URL, not a credential
	refreshMargin  = 60 * time.Second
)

// RedditCredentials is the immutable credential + UA bundle. Constructed once from env (by a later phase).
type RedditCredentials struct {
	ClientID     string
	ClientSecret string
	UserAgent    string // descriptive: "platform:appname:vX.Y (by /u/<acct>)"
}

// RedditTokenManager exchanges client_credentials for a bearer token, caches it with a
// refresh-before-expiry margin, and is safe for concurrent use.
type RedditTokenManager interface {
	// Token returns a valid bearer token, refreshing via the passed BrowserDoer if the cache
	// is empty or within the refresh margin. The BrowserDoer is a PARAMETER (not held) so the
	// manager has no transport of its own — trivially mockable.
	Token(ctx context.Context, doer BrowserDoer) (string, error)
	// Invalidate drops the cached token (called on a 401 from the API GET).
	Invalidate()
}

type redditTokenManager struct {
	creds     RedditCredentials
	now       func() time.Time // injected clock — deterministic margin tests
	mu        sync.Mutex       // guards cached+expiry; single-flight via mutex held across refresh
	cached    string
	expiresAt time.Time
}

// NewRedditTokenManager creates a token manager for Reddit's client_credentials OAuth flow.
// If now is nil, time.Now is used.
func NewRedditTokenManager(creds RedditCredentials, now func() time.Time) RedditTokenManager {
	if now == nil {
		now = time.Now
	}
	return &redditTokenManager{
		creds: creds,
		now:   now,
	}
}

// Token returns a valid bearer token. If the cached token is still within its validity
// window, it is returned immediately. Otherwise a new token is fetched via doer.
// The mutex is held across the refresh to provide single-flight semantics.
// NOTE: the mutex is held across the doer.Do call; callers serialize behind a refresh and ctx cannot cancel mid-fetch.
func (m *redditTokenManager) Token(ctx context.Context, doer BrowserDoer) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cached != "" && m.now().Before(m.expiresAt) {
		return m.cached, nil
	}

	tok, expiry, err := m.fetchToken(ctx, doer)
	if err != nil {
		return "", err
	}

	m.cached = tok
	m.expiresAt = expiry
	return tok, nil
}

// Invalidate drops the cached token. Called when a GET to the OAuth API returns 401,
// indicating the token has expired or been revoked.
func (m *redditTokenManager) Invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cached = ""
	m.expiresAt = time.Time{}
}

// tokenResponse is the JSON shape of POST /api/v1/access_token.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// fetchToken performs the HTTP POST to Reddit's token endpoint and returns the token
// and its computed expiry time. Caller must hold m.mu.
func (m *redditTokenManager) fetchToken(ctx context.Context, doer BrowserDoer) (string, time.Time, error) {
	_ = ctx // retained for interface; BrowserDoer.Do does not accept context

	auth := base64.StdEncoding.EncodeToString(
		[]byte(m.creds.ClientID + ":" + m.creds.ClientSecret),
	)

	headers := map[string]string{
		"Authorization": "Basic " + auth,
		"Content-Type":  acceptFormURLEncoded,
		"User-Agent":    m.creds.UserAgent,
		"Accept":        acceptJSON,
	}

	body := strings.NewReader("grant_type=client_credentials")

	data, _, status, err := doer.Do(http.MethodPost, redditTokenURL, headers, body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("reddit token request: %w", err)
	}

	if status == http.StatusUnauthorized {
		return "", time.Time{}, ErrCredentialInvalid
	}
	if status >= http.StatusInternalServerError {
		return "", time.Time{}, fmt.Errorf("reddit token: status %d: %w", status, ErrTransient)
	}
	if status != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("reddit token endpoint returned status %d", status)
	}

	var resp tokenResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", time.Time{}, fmt.Errorf("reddit token response decode: %w", err)
	}

	if resp.AccessToken == "" {
		return "", time.Time{}, errors.New("reddit token endpoint returned empty access_token")
	}

	// Compute expiry: cache until (expires_in - refreshMargin), floored at 1s to ensure
	// the window is always positive even if expires_in is very small.
	window := time.Duration(resp.ExpiresIn)*time.Second - refreshMargin
	if window < time.Second {
		window = time.Second
	}
	expiry := m.now().Add(window)

	return resp.AccessToken, expiry, nil
}

// compile-time check that redditTokenManager implements RedditTokenManager.
var _ RedditTokenManager = (*redditTokenManager)(nil)
