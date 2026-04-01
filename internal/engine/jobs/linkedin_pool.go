package jobs

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	linkedin "github.com/anatolykoptev/go-linkedin"
	"github.com/anatolykoptev/go-twitter/social"
	"github.com/anatolykoptev/go_job/internal/engine"
)

const linkedinClientTTL = 10 * time.Minute

var errLinkedInNotConfigured = errors.New("linkedin not configured")

// linkedinPool manages a lazy-initialized LinkedIn client with auto-refresh.
// On first call or after TTL expiry, acquires fresh credentials from go-social.
var linkedinPool = &liPool{}

type liPool struct {
	mu        sync.Mutex
	client    *linkedin.Client
	accountID string
	expiresAt time.Time
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

	if p.client != nil && time.Now().Before(p.expiresAt) {
		return p.client, nil
	}

	client, accountID, err := acquireLinkedIn(ctx, sc)
	if err != nil {
		// If we have a stale client, return it rather than failing.
		if p.client != nil {
			slog.Warn("linkedin refresh failed, using stale client", slog.Any("error", err))
			return p.client, nil
		}
		return nil, err
	}

	p.client = client
	p.accountID = accountID
	p.expiresAt = time.Now().Add(linkedinClientTTL)
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

func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "302") || strings.Contains(s, "403") || strings.Contains(s, "401")
}

func acquireLinkedIn(ctx context.Context, sc *social.Client) (*linkedin.Client, string, error) {
	creds, err := sc.AcquireAccount(ctx, "linkedin")
	if err != nil {
		return nil, "", err
	}

	client, err := linkedin.New(linkedin.ClientConfig{
		Cookies: creds.Credentials,
		Proxy:   creds.Proxy,
	})
	if err != nil {
		return nil, "", err
	}
	return client, creds.ID, nil
}
