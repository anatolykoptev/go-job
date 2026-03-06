package engine

import (
	"context"
	"fmt"
)

// SearchQuery defines one parallel search call.
type SearchQuery struct {
	Query   string
	Engines string
}

// PipelineOpts configures the reusable search pipeline.
type PipelineOpts struct {
	Queries      []SearchQuery       // parallel SearXNG queries
	Language     string              // language code ("all" default)
	TimeRange    string              // time filter
	Instruction  string              // LLM system instruction (ignored in raw mode)
	Mode         string              // "summary" (default) or "raw"
	Depth        string              // "fast" (snippets only), "" default, "deep" (more sources + rich prompt)
	MaxPerDomain int                 // dedupByDomain limit (0 = no limit)
	ContentLimit int                 // max chars per page (0 = maxContentChars)
	MinScore     float64             // filterByScore threshold (0 = no filter)
	MinKeep      int                 // filterByScore min keep
	URLRewriter  func(string) string // optional URL rewriter (e.g. GitHub blob→raw)
	ExtraResults []SearxngResult     // pre-fetched results merged before dedup (e.g. direct API calls)
}

// RunSearchPipeline executes the full search→merge→dedup→fetch→summarize pipeline.
// Returns SmartSearchOutput for summary mode, or formats raw content for raw mode.
func RunSearchPipeline(ctx context.Context, query string, opts PipelineOpts) (out SmartSearchOutput, err error) {
	_ = TrackOperation(ctx, "pipeline:"+query, func(ctx context.Context) error {
		out, err = runSearchPipeline(ctx, query, opts)
		return err
	})
	return
}

func runSearchPipeline(ctx context.Context, query string, opts PipelineOpts) (SmartSearchOutput, error) {
	lang := opts.Language
	if lang == "" {
		lang = LangAll
	}
	contentLimit := opts.ContentLimit
	if contentLimit == 0 {
		contentLimit = cfg.MaxContentChars
	}
	maxDomain := opts.MaxPerDomain
	if maxDomain == 0 {
		maxDomain = 2
	}
	// deep mode: allow more URLs per domain and more total URLs
	maxFetchURLs := cfg.MaxFetchURLs
	if opts.Depth == "deep" {
		maxDomain = max(maxDomain, 3)
		maxFetchURLs = maxFetchURLs * 3 / 2 // ×1.5
	}

	// --- Parallel search (SearXNG, skipped when not configured) ---
	type searchResult struct {
		results []SearxngResult
		err     error
	}
	var channels []chan searchResult
	if cfg.SearxngURL != "" {
		channels = make([]chan searchResult, len(opts.Queries))
		for i, sq := range opts.Queries {
			ch := make(chan searchResult, 1)
			channels[i] = ch
			go func(sq SearchQuery, ch chan searchResult) {
				r, err := SearchSearXNG(ctx, sq.Query, lang, opts.TimeRange, sq.Engines)
				ch <- searchResult{r, err}
			}(sq, ch)
		}
	}

	// --- Merge ---
	var merged []SearxngResult
	var lastErr error
collectLoop:
	for _, ch := range channels {
		select {
		case res := <-ch:
			if res.err != nil {
				lastErr = res.err
			} else {
				merged = append(merged, res.results...)
			}
		case <-ctx.Done():
			if lastErr == nil {
				lastErr = ctx.Err()
			}
			break collectLoop
		}
	}
	// --- Merge extra results (direct API calls) ---
	merged = append(merged, opts.ExtraResults...)

	// --- Merge direct scraper results (DDG, Startpage, Brave, Reddit) ---
	directQuery := query
	if len(opts.Queries) > 0 {
		directQuery = opts.Queries[0].Query
	}
	if directResults := SearchDirect(ctx, directQuery, lang); len(directResults) > 0 {
		merged = append(merged, directResults...)
	}

	if len(merged) == 0 {
		if lastErr != nil {
			return SmartSearchOutput{}, fmt.Errorf("search failed: %w", lastErr)
		}
		return SmartSearchOutput{Query: query, Answer: "No search results found."}, nil
	}

	// --- Filter by score (optional) ---
	if opts.MinScore > 0 {
		minKeep := opts.MinKeep
		if minKeep == 0 {
			minKeep = 3
		}
		merged = FilterByScore(merged, opts.MinScore, minKeep)
	}

	// --- Dedup by URL ---
	seen := make(map[string]bool)
	var deduped []SearxngResult
	for _, r := range merged {
		if !seen[r.URL] {
			seen[r.URL] = true
			deduped = append(deduped, r)
		}
	}

	// --- Dedup by domain ---
	top := DedupByDomain(deduped, maxDomain)
	if len(top) > maxFetchURLs {
		top = top[:maxFetchURLs]
	}

	// --- Depth: fast — skip URL fetching, use snippets only ---
	var contents map[string]string
	if opts.Depth == "fast" {
		contents = make(map[string]string) // empty: LLM uses snippets from buildSourcesText
	} else {
		contents = fetchContentsWithRewriter(ctx, top, opts.URLRewriter)
	}

	// --- Mode: raw ---
	if opts.Mode == "raw" {
		return buildRawOutput(query, top, contents, contentLimit), nil
	}

	// --- Mode: summary (default) ---
	var llmOut *LLMStructuredOutput
	var err error
	switch {
	case opts.Depth == "deep":
		llmOut, err = SummarizeDeep(ctx, query, opts.Instruction, contentLimit, top, contents)
	case opts.Instruction != "":
		llmOut, err = SummarizeWithInstruction(ctx, query, opts.Instruction, contentLimit, top, contents)
	default:
		llmOut, err = summarizeWithLLM(ctx, query, top, contents)
	}
	if err != nil {
		return SmartSearchOutput{}, fmt.Errorf("LLM summarization failed: %w", err)
	}

	return BuildSearchOutput(query, llmOut, top), nil
}
