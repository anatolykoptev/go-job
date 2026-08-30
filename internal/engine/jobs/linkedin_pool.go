package jobs

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	linkedin "github.com/anatolykoptev/go-linkedin"
	"github.com/anatolykoptev/go-twitter/social"
	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	linkedinClientTTL   = 10 * time.Minute
	linkedinMaxStaleAge = 30 * time.Minute // PF-10: refuse stale client beyond this age
)

var errLinkedInNotConfigured = errors.New("linkedin not configured")
var errLinkedInStaleExpired = errors.New("linkedin: stale client exceeded max age, refresh failed")

// linkedinPool manages a lazy-initialized LinkedIn client with auto-refresh.
// On first call or after TTL expiry, acquires fresh credentials from go-social.
//
// PF-10 fix: stale client fallback has a max age (linkedinMaxStaleAge). If the
// cached client is older than max-stale-age and refresh fails, return an error
// instead of a potentially expired credential.
//
// PF-12 fix: client swap uses atomic.Pointer so concurrent get() callers never
// see a partially-written pointer. The mutex is only used to serialize refresh
// attempts (single-flight), not to protect the client field.
var linkedinPool = &liPool{}

type liPool struct {
	mu          sync.Mutex // serializes refresh attempts (single-flight)
	client      atomic.Pointer[linkedin.Client]
	accountID   string
	expiresAt   time.Time
	refreshedAt time.Time // when the client was last successfully refreshed
}

// getLinkedInClient returns a cached LinkedIn client, refreshing from go-social if expired.
// Falls back to engine.Cfg.LinkedInClient if go-social is unavailable.
func getLinkedInClient(ctx context.Context) (*linkedin.Client, error) {
	// Fast path: static client without go-social.
	sc := engine.Cfg.SocialClient
	if sc == nil {
		client := engine.Cfg.LinkedInClient
		if client == nil {
			return nil, errLinkedInNotConfigured
		}
		return client, nil
	}

	return linkedinPool.get(ctx, sc)
}

func (p *liPool) get(ctx context.Context, sc *social.Client) (*linkedin.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Fast path: client is fresh.
	if ptr := p.client.Load(); ptr != nil && time.Now().Before(p.expiresAt) {
		return ptr, nil
	}

	// Slow path: needs refresh.
	client, accountID, err := acquireLinkedIn(ctx, sc)
	if err != nil {
		// PF-10 fix: stale client fallback with max age check.
		// If we have a stale client and it's not too old, return it.
		if ptr := p.client.Load(); ptr != nil {
			staleAge := time.Since(p.refreshedAt)
			if staleAge < linkedinMaxStaleAge {
				slog.Warn("linkedin refresh failed, using stale client",
					slog.Any("error", err),
					slog.Duration("stale_age", staleAge),
					slog.Duration("max_stale_age", linkedinMaxStaleAge),
				)
				return ptr, nil
			}
			slog.Error("linkedin refresh failed and stale client exceeded max age",
				slog.Any("error", err),
				slog.Duration("stale_age", staleAge),
				slog.Duration("max_stale_age", linkedinMaxStaleAge),
			)
			return nil, errLinkedInStaleExpired
		}
		return nil, err
	}

	// PF-12 fix: atomic store — concurrent readers never see a partial write
	// even if they bypass the mutex via a future fast path.
	p.client.Store(client)
	p.accountID = accountID
	p.expiresAt = time.Now().Add(linkedinClientTTL)
	p.refreshedAt = time.Now()
	slog.Info("linkedin client refreshed from go-social")
	return client, nil
}

// invalidate forces the next call to re-acquire credentials.
func (p *liPool) invalidate() {
	p.mu.Lock()
	p.expiresAt = time.Time{}
	p.mu.Unlock()
}

// withRetry wraps a LinkedIn API call: on 302/403 errors, invalidates the pool and retries once.
func withRetry[T any](ctx context.Context, fn func(*linkedin.Client) (T, error)) (T, error) {
	client, err := getLinkedInClient(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	result, err := fn(client)
	if err != nil && isAuthError(err) {
		slog.Warn("linkedin auth error, refreshing client", slog.Any("error", err))
		linkedinPool.invalidate()
		client, err = getLinkedInClient(ctx)
		if err != nil {
			var zero T
			return zero, err
		}
		result, err = fn(client)
		if err != nil && isAuthError(err) {
			reportLinkedInAuthError(ctx)
		}
	}
	return result, err
}

// reportLinkedInAuthError notifies go-social that LinkedIn credentials are failing.
// Best-effort: logs warning on failure, does not block the error return.
func reportLinkedInAuthError(ctx context.Context) {
	sc := engine.Cfg.SocialClient
	if sc == nil {
		return
	}
	linkedinPool.mu.Lock()
	accountID := linkedinPool.accountID
	linkedinPool.mu.Unlock()
	if accountID == "" {
		return
	}
	if err := sc.ReportUsage(ctx, "linkedin", accountID, "auth_error"); err != nil {
		slog.Warn("failed to report linkedin auth_error to go-social", slog.Any("error", err))
	} else {
		slog.Info("reported linkedin auth_error to go-social", slog.String("account_id", accountID))
	}
}

// isAuthError reports whether err indicates a LinkedIn auth/block signal that
// should trigger client rotation. Uses classifyLinkedInError so the Voyager
// path rotates on 999 and 200-with-challenge-body — not just 302/401/403
// (issue #290).
//
// A 429 rate limit (liRateLimited) is NOT an auth error: rotating on a
// transient throttle would invalidate a healthy pooled client, report
// auth_error to go-social (poisoning the account's health signal), and retry
// immediately against a rate-limited endpoint with no backoff. The Voyager
// path has no breaker, so 429 must be surfaced to the caller for backoff — not
// rotated (issue #291).
func isAuthError(err error) bool {
	switch classifyLinkedInError(err) {
	case liHardBlock, liChallenge:
		return true
	default:
		return false
	}
}

func acquireLinkedIn(ctx context.Context, sc *social.Client) (*linkedin.Client, string, error) {
	creds, err := sc.AcquireAccount(ctx, "linkedin")
	if err != nil {
		return nil, "", err
	}

	client, err := linkedin.New(buildLinkedInClientConfig(creds.Credentials))
	if err != nil {
		return nil, "", err
	}
	return client, creds.ID, nil
}

// buildLinkedInClientConfig constructs the go-linkedin ClientConfig from the
// acquired credential cookies, gating the CDP Voyager transport behind the
// LINKEDIN_TRANSPORT env flag.
//
// Two-way door:
//   - LINKEDIN_TRANSPORT unset or "stealth" (default): WowaURL stays empty →
//     go-linkedin uses its existing go-stealth HTTP transport (direct HTTP w/
//     go-stealth JA3 fingerprint). This is the byte-for-byte current behavior.
//   - LINKEDIN_TRANSPORT="cdp": WowaURL is set → go-linkedin runs Voyager
//     calls as an in-page fetch() inside the cloakbrowser via go-wowa's
//     /api/v1/chrome/interact evaluate seam. The real Chrome on our
//     datacenter IP passes Cloudflare where standalone HTTP loops forever
//     (the 302-to-self loop); no proxy needed.
//
// GO_WOWA_BASE_URL is the go-wowa BASE url (no path); defaults to the docker
// backend network name. Do NOT reuse GOWOWA_URL — that carries the /api/v1/render
// path used by gowowa_render.go.
func buildLinkedInClientConfig(creds map[string]string) linkedin.ClientConfig {
	cfg := linkedin.ClientConfig{Cookies: creds}
	if os.Getenv("LINKEDIN_TRANSPORT") != "cdp" {
		return cfg
	}
	wowaBase := os.Getenv("GO_WOWA_BASE_URL")
	if wowaBase == "" {
		wowaBase = "http://go-wowa:8906"
	}
	cfg.WowaURL = wowaBase
	cfg.Session = linkedin.SessionNameLinkedIn
	cfg.InternalSecret = os.Getenv("INTERNAL_SERVICE_SECRET")
	return cfg
}
