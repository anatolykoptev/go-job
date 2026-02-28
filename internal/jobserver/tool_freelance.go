package jobserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/sources"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerFreelanceSearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "freelance_search",
		Description: "Search for freelance projects and gigs on Upwork and Freelancer.com. Returns structured JSON with project details (title, budget, skills, platform, URL). Freelancer.com uses direct API for rich data (budgets, bids, skills). Filter by platform.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.FreelanceSearchInput) (*mcp.CallToolResult, engine.FreelanceSearchOutput, error) {
		if input.Query == "" {
			return nil, engine.FreelanceSearchOutput{}, errors.New("query is required")
		}

		cacheKey := engine.CacheKey("freelance_search", input.Query, input.Platform, input.Language)
		if out, ok := engine.CacheLoadJSON[engine.FreelanceSearchOutput](ctx, cacheKey); ok {
			return nil, out, nil
		}

		platform := strings.ToLower(input.Platform)
		lang := engine.NormLang(input.Language)

		useUpwork := platform == "" || platform == "all" || platform == "upwork"
		useFreelancer := platform == "" || platform == "all" || platform == "freelancer"

		var freelancerAPIResults []engine.SearxngResult
		freelancerAPISuccess := false
		if useFreelancer {
			projects, err := sources.SearchFreelancerAPI(ctx, input.Query, 10)
			if err != nil {
				slog.Warn("freelance_search: freelancer API error", slog.Any("error", err))
			} else if len(projects) > 0 {
				freelancerAPISuccess = true
				freelancerAPIResults = sources.FreelancerProjectsToSearxngResults(projects)
			}
		}

		type searchResult struct {
			results []engine.SearxngResult
			err     error
		}
		var channels []chan searchResult

		addQuery := func(q, eng string) {
			ch := make(chan searchResult, 1)
			channels = append(channels, ch)
			go func() {
				r, err := engine.SearchSearXNG(ctx, q, lang, "", eng)
				ch <- searchResult{r, err}
			}()
		}

		if useUpwork {
			addQuery(input.Query+" site:upwork.com/freelance-jobs/apply", engine.DefaultSearchEngine)
			addQuery(input.Query+" site:upwork.com/freelance-jobs/apply", engine.DefaultSearchEngine)
		}
		if useFreelancer && !freelancerAPISuccess {
			addQuery(input.Query+" site:freelancer.com/projects", engine.DefaultSearchEngine)
			addQuery(input.Query+" site:freelancer.com/projects", engine.DefaultSearchEngine)
		}

		var merged []engine.SearxngResult
		var lastErr error
		for _, ch := range channels {
			res := <-ch
			if res.err != nil {
				lastErr = res.err
				slog.Warn("freelance_search: search error", slog.Any("error", res.err))
				continue
			}
			merged = append(merged, res.results...)
		}

		apiURLs := make(map[string]bool, len(freelancerAPIResults))
		for _, r := range freelancerAPIResults {
			apiURLs[r.URL] = true
		}
		merged = append(freelancerAPIResults, merged...)

		if len(merged) == 0 {
			if lastErr != nil {
				return nil, engine.FreelanceSearchOutput{}, fmt.Errorf("search failed: %w", lastErr)
			}
			return nil, engine.FreelanceSearchOutput{Query: input.Query, Summary: "No results found."}, nil
		}

		seen := make(map[string]bool)
		var deduped []engine.SearxngResult
		for _, r := range merged {
			if !seen[r.URL] {
				seen[r.URL] = true
				deduped = append(deduped, r)
			}
		}

		var filtered []engine.SearxngResult
		for _, r := range deduped {
			u, err := url.Parse(r.URL)
			if err != nil {
				filtered = append(filtered, r)
				continue
			}
			host := u.Hostname()
			if strings.Contains(host, "upwork") {
				p := u.Path
				if !strings.Contains(p, "/apply/") && !strings.Contains(p, "/~") {
					continue
				}
			}
			filtered = append(filtered, r)
		}

		maxPerDomain := 10
		if useUpwork && useFreelancer {
			maxPerDomain = 5
		}
		top := engine.DedupByDomain(filtered, maxPerDomain)
		if len(top) > 10 {
			top = top[:10]
		}

		contents := engine.FetchContentsParallel(ctx, top, apiURLs)

		freelanceOut, err := engine.SummarizeFreelanceResults(ctx, input.Query, engine.FreelanceSearchInstruction, 4000, top, contents)
		if err != nil {
			return nil, engine.FreelanceSearchOutput{}, fmt.Errorf("LLM summarization failed: %w", err)
		}

		for i := range freelanceOut.Projects {
			p := &freelanceOut.Projects[i]
			if p.URL == "" && i < len(top) {
				p.URL = top[i].URL
			}
			if p.Platform == "" && p.URL != "" {
				if u, err := url.Parse(p.URL); err == nil {
					host := u.Hostname()
					if strings.Contains(host, "upwork") {
						p.Platform = "upwork"
					} else if strings.Contains(host, "freelancer") {
						p.Platform = "freelancer"
					}
				}
			}
		}

		engine.CacheStoreJSON(ctx, cacheKey, input.Query, *freelanceOut)
		return nil, *freelanceOut, nil
	})
}
