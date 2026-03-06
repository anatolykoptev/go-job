// Package extract provides article text extraction from HTML with a
// three-tier fallback chain: trafilatura → goquery → regex.
//
// Create an extractor with [New] and call [Extractor.Extract]:
//
//	ext := extract.New()
//	result, err := ext.Extract(ctx, htmlBody, pageURL)
package extract

import (
	"context"
	"net/url"
)

// DefaultMinExtractChars is the minimum content length to consider extraction
// successful. Below this threshold, the LLM fallback is triggered (if configured).
const DefaultMinExtractChars = 200

// LLMFallbackFunc is called with cleaned HTML when extraction produces thin content.
type LLMFallbackFunc func(ctx context.Context, cleanedHTML string) (string, error)

// Result holds extracted content from an HTML page.
type Result struct {
	Title   string
	Content string // content in the requested format
	Format  Format // format of Content
}

// Extractor extracts article content from HTML bodies.
type Extractor struct {
	maxContentLen   int
	format          Format
	llmFallback     LLMFallbackFunc
	minExtractChars int
}

// Option configures an Extractor.
type Option func(*Extractor)

// WithMaxContentLen sets the maximum content length in bytes.
// Content exceeding this limit is truncated with "...".
// Zero means no limit.
func WithMaxContentLen(n int) Option {
	return func(e *Extractor) { e.maxContentLen = n }
}

// WithFormat sets the output format (default FormatText).
func WithFormat(f Format) Option {
	return func(e *Extractor) { e.format = f }
}

// WithLLMFallback sets the LLM fallback function for thin content.
func WithLLMFallback(fn LLMFallbackFunc) Option {
	return func(e *Extractor) { e.llmFallback = fn }
}

// WithMinExtractChars sets the minimum chars for extraction to be considered
// successful. Below this threshold, the LLM fallback is triggered (if configured).
// Default: 200.
func WithMinExtractChars(n int) Option {
	return func(e *Extractor) { e.minExtractChars = n }
}

// New creates an Extractor with the given options.
func New(opts ...Option) *Extractor {
	e := &Extractor{
		format:          FormatText,
		minExtractChars: DefaultMinExtractChars,
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// largeBodyThreshold is the size above which StripScriptStyle is called
// before DOM parsing to reduce memory.
const largeBodyThreshold = 500 * 1024 // 500KB

// Extract extracts article content from HTML body bytes.
// Uses a three-tier fallback chain: trafilatura → goquery → regex.
// If all tiers produce thin content and an LLM fallback is configured,
// it is called as a last resort.
func (e *Extractor) Extract(ctx context.Context, body []byte, pageURL *url.URL) (*Result, error) {
	// Pre-strip scripts/styles for large bodies.
	if len(body) > largeBodyThreshold {
		body = StripScriptStyle(body)
	}

	// Tier 1: go-trafilatura (best accuracy, 3-tier internal fallback).
	if result, err := e.extractTrafilatura(body, pageURL); err == nil && result.Content != "" {
		if len(result.Content) >= e.minExtractChars || e.llmFallback == nil {
			return result, nil
		}
	}

	// Tier 2: goquery (structured CSS selector parsing).
	if result, err := e.extractGoquery(body); err == nil && result.Content != "" {
		if len(result.Content) >= e.minExtractChars || e.llmFallback == nil {
			return result, nil
		}
	}

	// Tier 3: regex (last resort, strip tags).
	result, err := e.extractRegex(body)
	if err != nil {
		return result, err
	}

	// LLM fallback: if content is still thin and fallback is configured.
	if e.llmFallback != nil && len(result.Content) < e.minExtractChars {
		llmContent, llmErr := e.llmFallback(ctx, string(body))
		if llmErr == nil && llmContent != "" {
			result.Content = e.truncate(llmContent)
		}
	}

	return result, nil
}

// truncate caps content at maxContentLen if set.
func (e *Extractor) truncate(s string) string {
	if e.maxContentLen > 0 && len(s) > e.maxContentLen {
		return s[:e.maxContentLen] + "..."
	}
	return s
}
