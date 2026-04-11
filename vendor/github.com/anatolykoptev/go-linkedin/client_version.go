package linkedin

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	stealth "github.com/anatolykoptev/go-stealth"
)

// clientVersionPatterns lists regexes tried in order to extract LinkedIn's client version.
// LinkedIn previously embedded "clientVersion":"X.Y.Z" in inline JS, but now the version
// appears as a data-app-version HTML attribute on the <meta id="config"> tag on the homepage.
// The old JSON pattern is kept as a fallback in case LinkedIn reverts or uses it on other pages.
var clientVersionPatterns = []*regexp.Regexp{
	// Current format (2026+): <meta id="config" data-app-version="2.1.2820" ...>
	regexp.MustCompile(`data-app-version="([^"]+)"`),
	// Legacy format: "clientVersion":"2.0.1234" in inline JS
	regexp.MustCompile(`"clientVersion":"([^"]+)"`),
}

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

func scrapeClientVersion(ctx context.Context, bc *stealth.BrowserClient, _ map[string]string) (string, error) {
	// The homepage is publicly accessible and contains data-app-version.
	// Don't send account cookies — invalid/expired cookies cause LinkedIn to
	// serve different content (error page or redirect instead of guest homepage).
	body, _, statusCode, err := bc.DoCtx(ctx, "GET", "https://www.linkedin.com/", nil, nil)
	if err != nil {
		return "", fmt.Errorf("scrape client version: %w", err)
	}
	if statusCode != 200 {
		return "", fmt.Errorf("scrape client version: status %d", statusCode)
	}
	for _, re := range clientVersionPatterns {
		if m := re.FindSubmatch(body); len(m) >= 2 {
			return string(m[1]), nil
		}
	}
	return "", fmt.Errorf("clientVersion not found in LinkedIn page")
}
