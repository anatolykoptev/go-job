// Package pipeline orchestrates the full search pipeline:
// sources → fetch → extract → chunk → filter → llm.
package pipeline

import (
	"context"
	"net/url"
	"strings"

	"github.com/anatolykoptev/go-engine/extract"
	"github.com/anatolykoptev/go-engine/llm"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/text"
)

const (
	defaultMaxConcurrency = 10
	defaultMaxTokens      = 4096
)

// Pipeline orchestrates: sources → fetch → extract → chunk → filter → llm.
type Pipeline struct {
	sources       []sources.Source
	fetchFn       func(ctx context.Context, url string) ([]byte, error)
	extractor     extract.Strategy
	chunker       text.Chunker
	filter        text.Filter
	llm           *llm.Client
	maxConc       int
	maxTokens     int
	charsPerToken float64
}

// Option configures a Pipeline.
type Option func(*Pipeline)

// WithSources adds search sources to the pipeline.
func WithSources(srcs ...sources.Source) Option {
	return func(p *Pipeline) { p.sources = append(p.sources, srcs...) }
}

// WithFetchFunc sets the HTTP fetch function used to retrieve page content.
func WithFetchFunc(fn func(ctx context.Context, url string) ([]byte, error)) Option {
	return func(p *Pipeline) { p.fetchFn = fn }
}

// WithExtractor sets the HTML extraction strategy.
func WithExtractor(s extract.Strategy) Option {
	return func(p *Pipeline) { p.extractor = s }
}

// WithChunker sets the text chunker for splitting extracted content.
func WithChunker(c text.Chunker) Option {
	return func(p *Pipeline) { p.chunker = c }
}

// WithFilter sets the chunk filter for relevance ranking.
func WithFilter(f text.Filter) Option {
	return func(p *Pipeline) { p.filter = f }
}

// WithLLMClient sets the LLM client for answer generation.
func WithLLMClient(c *llm.Client) Option {
	return func(p *Pipeline) { p.llm = c }
}

// WithPipelineConcurrency sets the maximum number of concurrent fetches.
func WithPipelineConcurrency(n int) Option {
	return func(p *Pipeline) { p.maxConc = n }
}

// WithMaxTokenBudget sets the per-source token budget for content truncation.
func WithMaxTokenBudget(n int) Option {
	return func(p *Pipeline) { p.maxTokens = n }
}

// WithCharsPerToken sets the characters-per-token ratio for token estimation.
func WithCharsPerToken(f float64) Option {
	return func(p *Pipeline) { p.charsPerToken = f }
}

// NewPipeline creates a Pipeline with the given options.
// Default max concurrency is 10.
func NewPipeline(opts ...Option) *Pipeline {
	p := &Pipeline{
		maxConc:       defaultMaxConcurrency,
		maxTokens:     defaultMaxTokens,
		charsPerToken: text.DefaultCharsPerToken,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Run executes the full pipeline for a query and returns structured output.
func (p *Pipeline) Run(ctx context.Context, query string) (*SearchOutput, error) {
	// 1. Search all sources in parallel.
	srcResults := p.searchSources(ctx, query)
	if len(srcResults) == 0 {
		return &SearchOutput{Query: query}, nil
	}

	// 2. Collect unique URLs.
	urls := uniqueURLs(srcResults)

	// 3. Fetch + extract + chunk + filter content.
	contents := make(map[string]string, len(urls))
	if p.fetchFn != nil && p.extractor != nil && len(urls) > 0 {
		fetchResults := ParallelFetch(ctx, urls, p.buildFetchFn(query), WithMaxConcurrency(p.maxConc))
		for _, r := range fetchResults {
			if r.Err == nil && r.Content != "" {
				contents[r.URL] = r.Content
			}
		}
	}

	// 4. Summarize with LLM.
	if p.llm != nil {
		out, err := p.llm.Summarize(ctx, query, p.maxTokens, p.charsPerToken, srcResults, contents)
		if err != nil {
			return nil, err
		}
		return p.buildOutput(query, out, srcResults), nil
	}

	// No LLM — return sources without answer.
	return p.buildOutput(query, &llm.StructuredOutput{}, srcResults), nil
}

// buildFetchFn wraps p.fetchFn + p.extractor + optional chunk/filter into the
// string-returning signature expected by ParallelFetch. Each goroutine performs
// fetch → extract → chunk+filter → join, so no separate chunkAndFilter step is needed.
func (p *Pipeline) buildFetchFn(query string) func(ctx context.Context, rawURL string) (string, error) {
	return func(ctx context.Context, rawURL string) (string, error) {
		body, err := p.fetchFn(ctx, rawURL)
		if err != nil {
			return "", err
		}
		u, _ := url.Parse(rawURL)
		result, err := p.extractor.Extract(ctx, body, u)
		if err != nil {
			return "", err
		}
		content := result.Content

		// Chunk + filter inline (if configured).
		if p.chunker != nil {
			chunks := p.chunker.Chunk(content)
			if len(chunks) == 0 {
				return "", nil
			}
			if p.filter != nil {
				chunks = p.filter.Filter(chunks, query)
			}
			content = strings.Join(chunks, "\n\n")
		}

		return content, nil
	}
}
