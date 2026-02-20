package jobserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/toolutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerRemoteWorkSearch(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "remote_work_search",
		Description: "Search for remote jobs on RemoteOK, WeWorkRemotely, and the web via SearXNG. Returns structured JSON with job details (title, company, salary, tags, source). Best for remote-first positions worldwide.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.RemoteWorkSearchInput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
		if input.Query == "" {
			return nil, engine.SmartSearchOutput{}, fmt.Errorf("query is required")
		}

		cacheKey := engine.CacheKey("remote_work_search", input.Query, input.Language)
		if cached, ok := engine.CacheGet(ctx, cacheKey); ok {
			return nil, cached, nil
		}

		lang := toolutil.NormLang(input.Language)

		type apiResult struct {
			jobList []engine.RemoteJobListing
			err     error
		}
		rokCh := make(chan apiResult, 1)
		wwrCh := make(chan apiResult, 1)
		remCh := make(chan apiResult, 1)

		go func() {
			j, err := jobs.SearchRemoteOK(ctx, input.Query, 20)
			rokCh <- apiResult{j, err}
		}()
		go func() {
			j, err := jobs.SearchWeWorkRemotely(ctx, input.Query, 20)
			wwrCh <- apiResult{j, err}
		}()
		go func() {
			j, err := jobs.SearchRemotive(ctx, input.Query, 15)
			remCh <- apiResult{j, err}
		}()

		type searchResult struct {
			results []engine.SearxngResult
			err     error
		}
		var searxChannels []chan searchResult

		addQuery := func(q, eng string) {
			ch := make(chan searchResult, 1)
			searxChannels = append(searxChannels, ch)
			go func() {
				r, err := engine.SearchSearXNG(ctx, q, lang, "", eng)
				ch <- searchResult{r, err}
			}()
		}
		addQuery(input.Query+" remote job", "google")
		addQuery(input.Query+" remote job", "bing")

		var rokRes, wwrRes, remRes apiResult
		for i := 0; i < 3; i++ {
			select {
			case r := <-rokCh:
				rokRes = r
			case r := <-wwrCh:
				wwrRes = r
			case r := <-remCh:
				remRes = r
			case <-ctx.Done():
				return nil, engine.SmartSearchOutput{}, ctx.Err()
			}
		}

		if rokRes.err != nil {
			slog.Warn("remote_work_search: RemoteOK error", slog.Any("error", rokRes.err))
		}
		if wwrRes.err != nil {
			slog.Warn("remote_work_search: WWR error", slog.Any("error", wwrRes.err))
		}
		if remRes.err != nil {
			slog.Warn("remote_work_search: Remotive error", slog.Any("error", remRes.err))
		}

		var apiSearxResults []engine.SearxngResult
		apiURLs := make(map[string]bool)

		if len(rokRes.jobList) > 0 {
			converted := jobs.RemoteJobsToSearxngResults(rokRes.jobList)
			for _, r := range converted {
				apiURLs[r.URL] = true
			}
			apiSearxResults = append(apiSearxResults, converted...)
		}
		if len(wwrRes.jobList) > 0 {
			converted := jobs.RemoteJobsToSearxngResults(wwrRes.jobList)
			for _, r := range converted {
				apiURLs[r.URL] = true
			}
			apiSearxResults = append(apiSearxResults, converted...)
		}
		if len(remRes.jobList) > 0 {
			converted := jobs.RemoteJobsToSearxngResults(remRes.jobList)
			for _, r := range converted {
				apiURLs[r.URL] = true
			}
			apiSearxResults = append(apiSearxResults, converted...)
		}

		var webResults []engine.SearxngResult
		for _, ch := range searxChannels {
			select {
			case res := <-ch:
				if res.err != nil {
					slog.Warn("remote_work_search: SearXNG error", slog.Any("error", res.err))
					continue
				}
				webResults = append(webResults, res.results...)
			case <-ctx.Done():
				return nil, engine.SmartSearchOutput{}, ctx.Err()
			}
		}

		var merged []engine.SearxngResult
		merged = append(merged, apiSearxResults...)
		merged = append(merged, webResults...)

		if len(merged) == 0 {
			if rokRes.err != nil && wwrRes.err != nil && remRes.err != nil {
				return nil, engine.SmartSearchOutput{}, fmt.Errorf("all sources failed")
			}
			out := engine.RemoteWorkSearchOutput{Query: input.Query, Summary: "No remote jobs found."}
			return remoteWorkResult(ctx, cacheKey, out)
		}

		seen := make(map[string]bool)
		var deduped []engine.SearxngResult
		for _, r := range merged {
			if !seen[r.URL] {
				seen[r.URL] = true
				deduped = append(deduped, r)
			}
		}
		deduped = engine.DedupByDomain(deduped, 5)
		if len(deduped) > 15 {
			deduped = deduped[:15]
		}

		contents := toolutil.FetchURLsParallel(ctx, deduped, apiURLs)

		remoteOut, err := jobs.SummarizeRemoteWorkResults(ctx, input.Query, engine.RemoteWorkInstruction, 4000, deduped, contents)
		if err != nil {
			return nil, engine.SmartSearchOutput{}, fmt.Errorf("LLM summarization failed: %w", err)
		}

		enrichedJobs := make([]engine.RemoteJobListing, len(remoteOut.Jobs))
		for i, job := range remoteOut.Jobs {
			if job.URL == "" && i < len(deduped) {
				job.URL = deduped[i].URL
			}
			enrichedJobs[i] = job
		}

		return remoteWorkResult(ctx, cacheKey, engine.RemoteWorkSearchOutput{
			Query:   remoteOut.Query,
			Jobs:    enrichedJobs,
			Summary: remoteOut.Summary,
		})
	})
}

func remoteWorkResult(ctx context.Context, cacheKey string, out engine.RemoteWorkSearchOutput) (*mcp.CallToolResult, engine.SmartSearchOutput, error) {
	jsonBytes, err := json.Marshal(out)
	if err != nil {
		return nil, engine.SmartSearchOutput{}, fmt.Errorf("json marshal: %w", err)
	}
	result := engine.SmartSearchOutput{
		Query:   out.Query,
		Answer:  string(jsonBytes),
		Sources: []engine.SourceItem{},
	}
	engine.CacheSet(ctx, cacheKey, result)
	return nil, result, nil
}
