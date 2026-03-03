// Package extract provides article text extraction from HTML with a
// three-tier fallback chain: trafilatura → goquery → regex.
//
// Create an extractor with [New] and call [Extractor.Extract]:
//
//	ext := extract.New()
//	result, err := ext.Extract(htmlBody, pageURL)
package extract

import (
	"net/url"
)

// Result holds extracted content from an HTML page.
type Result struct {
	Title    string
	Content  string // clean text
	Markdown string // markdown (may be empty)
}

// Extractor extracts article content from HTML bodies.
type Extractor struct {
	maxContentLen int
}

// Option configures an Extractor.
type Option func(*Extractor)

// WithMaxContentLen sets the maximum content length in bytes.
// Content exceeding this limit is truncated with "...".
// Zero means no limit.
func WithMaxContentLen(n int) Option {
	return func(e *Extractor) { e.maxContentLen = n }
}

// New creates an Extractor with the given options.
func New(opts ...Option) *Extractor {
	e := &Extractor{}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Extract extracts article content from HTML body bytes.
// Uses a three-tier fallback chain: trafilatura → goquery → regex.
// pageURL is used by trafilatura for site-specific extraction rules.
func (e *Extractor) Extract(body []byte, pageURL *url.URL) (*Result, error) {
	// Tier 1: go-trafilatura (best accuracy, 3-tier internal fallback).
	if result, err := e.extractTrafilatura(body, pageURL); err == nil && result.Content != "" {
		return result, nil
	}

	// Tier 2: goquery (structured CSS selector parsing).
	if result, err := e.extractGoquery(body); err == nil && result.Content != "" {
		return result, nil
	}

	// Tier 3: regex (last resort, strip tags).
	return e.extractRegex(body)
}

// truncate caps content at maxContentLen if set.
func (e *Extractor) truncate(s string) string {
	if e.maxContentLen > 0 && len(s) > e.maxContentLen {
		return s[:e.maxContentLen] + "..."
	}
	return s
}
