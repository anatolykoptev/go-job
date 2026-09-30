package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const algoraBountiesURL = "https://algora.io/bounties"

// algora HTML patterns — the page renders bounties in a table with:
//
//	org name, issue #, amount, title, and GitHub issue links.
var (
	// Match GitHub issue links in bounty cards.
	reAlgoraBountyBlock = regexp.MustCompile(
		`(?s)<a\s+href="(https://github\.com/[^"]+/issues/\d+)"[^>]*>.*?</a>`)
	reAlgoraAmount = regexp.MustCompile(`\$[\d,]+`)
	reAlgoraIssue  = regexp.MustCompile(`#(\d+)`) //nolint:unused // reserved for issue number extraction
	reHTMLTag      = regexp.MustCompile(`<[^>]+>`)
	reWhitespace   = regexp.MustCompile(`\s+`)
	reTitleNoise   = regexp.MustCompile(`(?i)^\d+\s+(seconds?|minutes?|hours?|days?|weeks?|months?)\s+ago$`)
)

// AlgoraBounty is a raw bounty scraped from algora.io.
type AlgoraBounty struct {
	Title     string
	Org       string
	Amount    string
	IssueNum  string
	GitHubURL string
}

const algoraScrapeCacheKey = "algora_scrape"

// algoraOxFetchFetch is the HTML transport for Algora: ox-browser POST /fetch.
var algoraOxFetchFetch = func(ctx context.Context, pageURL string, headers map[string]string) (status int, body []byte, err error) {
	fetchURL := strings.TrimRight(engine.Cfg.OxBrowserURL, "/") + "/fetch"
	payload, err := json.Marshal(map[string]any{
		"url":     pageURL,
		"headers": headers,
		"timeout": int(engine.Cfg.FetchTimeout.Seconds()),
	})
	if err != nil {
		return 0, nil, fmt.Errorf("algora ox-browser /fetch marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fetchURL, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("algora ox-browser /fetch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if engine.Cfg.HTTPClient == nil {
		return 0, nil, errors.New("algora ox-browser /fetch: HTTPClient not configured")
	}
	resp, err := engine.Cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("algora ox-browser /fetch: %w", err)
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return 0, nil, fmt.Errorf("algora ox-browser /fetch body: %w", readErr)
	}

	var oxResp struct {
		Status     int               `json:"status"`
		Headers    map[string]string `json:"headers"`
		Body       string            `json:"body"`
		CfDetected bool              `json:"cf_detected"`
		Error      string            `json:"error,omitempty"`
	}
	if jsonErr := json.Unmarshal(respBody, &oxResp); jsonErr != nil {
		return 0, nil, fmt.Errorf("algora ox-browser /fetch decode: %w", jsonErr)
	}

	if resp.StatusCode == http.StatusOK {
		if oxResp.CfDetected {
			return 0, nil, errors.New("algora ox-browser: CF detected")
		}
		return oxResp.Status, []byte(oxResp.Body), nil
	}

	return 0, nil, fmt.Errorf("algora ox-browser /fetch: wrapper %d: %s", resp.StatusCode, oxResp.Error)
}
// SearchAlgora fetches bounties from Algora. Tries REST API first (if token configured),
// falls back to HTML scraping. Results are cached for 15 min.
func SearchAlgora(ctx context.Context, limit int) ([]engine.BountyListing, error) {
	if limit <= 0 || limit > 50 {
		limit = 30
	}

	// Try scrape cache first (shared by both API and scraping paths).
	if cached, ok := engine.CacheLoadJSON[[]engine.BountyListing](ctx, algoraScrapeCacheKey); ok {
		slog.Debug("algora: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	// Try API first.
	bounties, err := searchAlgoraAPI(ctx, limit)
	if err != nil {
		slog.Warn("algora: API failed, falling back to scraping", slog.Any("error", err))
	}
	if len(bounties) > 0 {
		engine.CacheStoreJSON(ctx, algoraScrapeCacheKey, "", bounties)
		slog.Debug("algora: API fetch complete", slog.Int("results", len(bounties)))
		return bounties, nil
	}

	// Fallback: HTML scraping.
	bounties, err = scrapeAlgoraBounties(ctx, limit)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, algoraScrapeCacheKey, "", bounties)
	if len(bounties) > limit {
		bounties = bounties[:limit]
	}

	slog.Debug("algora: scrape complete", slog.Int("results", len(bounties)))
	return bounties, nil
}

// scrapeAlgoraBounties fetches bounties by scraping the algora.io HTML page.
func scrapeAlgoraBounties(ctx context.Context, limit int) ([]engine.BountyListing, error) {
	engine.IncrAlgoraRequests()

	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	headers := map[string]string{
		"User-Agent": engine.UserAgentChrome,
		"Accept":     "text/html,application/xhtml+xml",
	}

	body, err := engine.RetryDo(fetchCtx, engine.DefaultRetryConfig, func() ([]byte, error) {
		status, b, fetchErr := algoraOxFetchFetch(fetchCtx, algoraBountiesURL, headers)
		if fetchErr != nil {
			return nil, fetchErr
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("algora.io returned status %d", status)
		}
		return b, nil
	})
	if err != nil {
		return nil, err
	}

	bounties := parseAlgoraBounties(string(body))
	_ = limit // limit applied by caller
	return bounties, nil
}

// parseAlgoraBounties extracts bounty data from algora.io HTML.
// The page structure has bounty cards with: org, issue#, amount, title, github link.
func parseAlgoraBounties(html string) []engine.BountyListing {
	// Split by github issue links to find bounty blocks.
	githubLinks := reAlgoraBountyBlock.FindAllStringSubmatchIndex(html, -1)
	if len(githubLinks) == 0 {
		return nil
	}

	// Deduplicate github URLs (page shows each bounty twice: table + feed).
	seen := make(map[string]bool)
	var bounties []engine.BountyListing

	for _, match := range githubLinks {
		ghURL := html[match[2]:match[3]]
		if seen[ghURL] {
			continue
		}
		seen[ghURL] = true

		// Look backwards from the github link for the bounty card context.
		blockStart := match[0] - 800
		if blockStart < 0 {
			blockStart = 0
		}
		block := html[blockStart:match[1]]

		// Find the closest (last) amount before the github link.
		amount := ""
		if matches := reAlgoraAmount.FindAllString(block, -1); len(matches) > 0 {
			amount = matches[len(matches)-1]
		}
		if amount == "" {
			continue // Not a bounty entry.
		}

		// Extract org name.
		org := extractOrgFromGitHubURL(ghURL)

		// Extract issue number from GitHub URL.
		issueNum := ""
		if _, _, num, ok := ParseGitHubIssueURL(ghURL); ok {
			issueNum = "#" + strconv.Itoa(num)
		}

		// Extract title: find text between the dollar amount and the github link.
		// Strip HTML tags and take the last meaningful text segment.
		title := extractTitleFromBlock(block, amount)

		// Filter out noisy titles like "tip 14 hours ago".
		if title != "" && isTitleNoise(title) {
			title = ""
		}

		bounties = append(bounties, engine.BountyListing{
			Title:    title,
			Org:      org,
			URL:      ghURL,
			Amount:   amount,
			Currency: currencyUSD,
			Source:   sourceAlgora,
			IssueNum: issueNum,
		})
	}

	return bounties
}
