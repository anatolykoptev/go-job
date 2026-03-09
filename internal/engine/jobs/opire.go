package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const (
	opireHomeURL       = "https://app.opire.dev/home"
	opireScrapeCacheKey = "opire_scrape"
)

// opireReward matches the reward objects inside "initialRewards" from the Opire RSC response.
type opireReward struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
	PendingPrice struct {
		Value int    `json:"value"`
		Unit  string `json:"unit"`
	} `json:"pendingPrice"`
	ProgrammingLanguages []string `json:"programmingLanguages"`
	Organization         struct {
		Name string `json:"name"`
	} `json:"organization"`
	CreatedAt    int64           `json:"createdAt"`
	ClaimerUsers json.RawMessage `json:"claimerUsers"`
	TryingUsers  json.RawMessage `json:"tryingUsers"`
}

// SearchOpire fetches bounties from Opire. Results are cached.
func SearchOpire(ctx context.Context, limit int) ([]engine.BountyListing, error) {
	if limit <= 0 || limit > 50 {
		limit = 30
	}

	// Check cache first.
	if cached, ok := engine.CacheLoadJSON[[]engine.BountyListing](ctx, opireScrapeCacheKey); ok {
		slog.Debug("opire: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	bounties, err := scrapeOpireBounties(ctx)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, opireScrapeCacheKey, "", bounties)
	if len(bounties) > limit {
		bounties = bounties[:limit]
	}

	slog.Debug("opire: scrape complete", slog.Int("results", len(bounties)))
	return bounties, nil
}

func scrapeOpireBounties(ctx context.Context) ([]engine.BountyListing, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, engine.Cfg.FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, opireHomeURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/x-component")
	req.Header.Set("RSC", "1")
	req.Header.Set("User-Agent", engine.UserAgentChrome)

	resp, err := engine.Cfg.HTTPClient.Do(req) //nolint:gosec // intentional outbound HTTP request
	if err != nil {
		return nil, fmt.Errorf("opire request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("opire returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	return parseOpireResponse(string(body))
}

// parseOpireResponse extracts the initialRewards JSON array from the RSC response body.
func parseOpireResponse(body string) ([]engine.BountyListing, error) {
	const marker = `"initialRewards":[`
	idx := strings.Index(body, marker)
	if idx < 0 {
		return nil, fmt.Errorf("opire: initialRewards not found in response")
	}

	// Start right after "initialRewards":
	arrStart := idx + len(marker) - 1 // include the opening '['
	depth := 0
	arrEnd := -1
	for i := arrStart; i < len(body); i++ {
		switch body[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				arrEnd = i + 1
				break
			}
		}
		if arrEnd > 0 {
			break
		}
	}
	if arrEnd < 0 {
		return nil, fmt.Errorf("opire: could not find closing bracket for initialRewards")
	}

	var rewards []opireReward
	if err := json.Unmarshal([]byte(body[arrStart:arrEnd]), &rewards); err != nil {
		return nil, fmt.Errorf("opire: JSON parse failed: %w", err)
	}

	bounties := make([]engine.BountyListing, 0, len(rewards))
	for _, r := range rewards {
		if r.URL == "" {
			continue
		}

		amount := formatCentsUSD(r.PendingPrice.Value)

		issueNum := ""
		if _, _, num, ok := ParseGitHubIssueURL(r.URL); ok {
			issueNum = "#" + strconv.Itoa(num)
		}

		posted := ""
		if r.CreatedAt > 0 {
			posted = time.UnixMilli(r.CreatedAt).UTC().Format(time.RFC3339)
		}

		bounties = append(bounties, engine.BountyListing{
			Title:    r.Title,
			Org:      r.Organization.Name,
			URL:      r.URL,
			Amount:   amount,
			Currency: "USD",
			Skills:   r.ProgrammingLanguages,
			Source:   "opire",
			IssueNum: issueNum,
			Posted:   posted,
		})
	}

	return bounties, nil
}

// formatCentsUSD converts USD cents (e.g. 150000) to "$1,500".
func formatCentsUSD(cents int) string {
	dollars := cents / 100
	if dollars <= 0 {
		return "$0"
	}
	s := strconv.Itoa(dollars)
	// Insert commas from right to left.
	n := len(s)
	if n <= 3 {
		return "$" + s
	}
	var b strings.Builder
	b.WriteByte('$')
	rem := n % 3
	if rem > 0 {
		b.WriteString(s[:rem])
		if rem < n {
			b.WriteByte(',')
		}
	}
	for i := rem; i < n; i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < n {
			b.WriteByte(',')
		}
	}
	return b.String()
}
