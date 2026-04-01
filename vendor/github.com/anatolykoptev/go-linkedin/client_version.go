package linkedin

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	stealth "github.com/anatolykoptev/go-stealth"
)

var clientVersionRe = regexp.MustCompile(`"clientVersion":"([^"]+)"`)

type versionCache struct {
	mu      sync.RWMutex
	version string
	expires time.Time
}

func (vc *versionCache) get() (string, bool) {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	if vc.version != "" && time.Now().Before(vc.expires) {
		return vc.version, true
	}
	return "", false
}

func (vc *versionCache) set(version string, ttl time.Duration) {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	vc.version = version
	vc.expires = time.Now().Add(ttl)
}

func scrapeClientVersion(ctx context.Context, bc *stealth.BrowserClient, headers map[string]string) (string, error) {
	body, _, statusCode, err := bc.DoCtx(ctx, "GET", "https://www.linkedin.com/feed/", headers, nil)
	if err != nil {
		return "", fmt.Errorf("scrape client version: %w", err)
	}
	if statusCode != 200 {
		return "", fmt.Errorf("scrape client version: status %d", statusCode)
	}
	matches := clientVersionRe.FindSubmatch(body)
	if len(matches) < 2 {
		return "", fmt.Errorf("clientVersion not found in LinkedIn page")
	}
	return string(matches[1]), nil
}
