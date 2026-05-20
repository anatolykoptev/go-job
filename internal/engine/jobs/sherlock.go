package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

var (
	sherlockBaseURL = "https://api.github.com/orgs/sherlock-audit/repos"
	sherlockCacheKey = "sherlock_audits"
)

type sherlockRepo struct {
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updated_at"`
	Archived    bool      `json:"archived"`
	Fork        bool      `json:"fork"`
}

// SearchSherlock fetches audit contests from the sherlock-audit GitHub org.
// Returns recently-updated, non-archived, non-fork repos as audit_contest programs.
// Results are cached.
func SearchSherlock(ctx context.Context, limit int) ([]engine.SecurityProgram, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.SecurityProgram](ctx, sherlockCacheKey); ok {
		slog.Debug("sherlock: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	repos, err := fetchSherlockRepos(ctx)
	if err != nil {
		return nil, err
	}

	programs := make([]engine.SecurityProgram, 0, len(repos))
	for _, r := range repos {
		if r.Archived || r.Fork {
			continue
		}
		programs = append(programs, engine.SecurityProgram{
			Name:     prettifySherlockName(r.Name),
			Platform: "sherlock",
			URL:      r.HTMLURL,
			Type:     "audit_contest",
			Managed:  true,
		})
	}

	engine.CacheStoreJSON(ctx, sherlockCacheKey, "", programs)
	if len(programs) > limit {
		programs = programs[:limit]
	}
	slog.Info("sherlock: fetched audits", slog.Int("count", len(programs)))
	return programs, nil
}

func fetchSherlockRepos(ctx context.Context) ([]sherlockRepo, error) {
	var all []sherlockRepo
	// Paginate up to 3 pages (300 repos max — Sherlock has fewer).
	for page := 1; page <= 3; page++ {
		url := fmt.Sprintf("%s?per_page=100&sort=updated&page=%d", sherlockBaseURL, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("sherlock: build request: %w", err)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("sherlock: fetch page %d: %w", page, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("sherlock: status %d on page %d: %s", resp.StatusCode, page, sherlockTruncate(string(body), 200))
		}
		var pageRepos []sherlockRepo
		if err := json.Unmarshal(body, &pageRepos); err != nil {
			return nil, fmt.Errorf("sherlock: decode page %d: %w", page, err)
		}
		all = append(all, pageRepos...)
		if len(pageRepos) < 100 {
			break // last page
		}
	}
	return all, nil
}

// prettifySherlockName turns "2024-12-foo-protocol-judging" → "Foo Protocol (2024-12)".
// Falls back to the raw name if the pattern does not match.
func prettifySherlockName(raw string) string {
	s := strings.TrimSuffix(raw, "-judging")
	parts := strings.SplitN(s, "-", 3)
	if len(parts) < 3 {
		return raw
	}
	year, month, rest := parts[0], parts[1], parts[2]
	if len(year) != 4 {
		return raw
	}
	title := titleCase(strings.ReplaceAll(rest, "-", " "))
	return fmt.Sprintf("%s (%s-%s)", title, year, month)
}

// titleCase uppercases the first letter of each word.
// Uses simple ASCII transform; sufficient for slug-style repo names.
func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) == 0 {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// sherlockTruncate truncates s to at most n bytes, appending "..." when cut.
// Named sherlockTruncate to avoid collision with any truncate func in the package.
func sherlockTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
