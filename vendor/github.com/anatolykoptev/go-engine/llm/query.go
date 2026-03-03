package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	rewriteQueryMaxLen = 200
	rewriteQueryMaxTok = 100
	rewriteQueryTemp   = 0.3
	expandQueryTok     = 250
	expandQueryTemp    = 0.7
)

// RewriteQuery converts a conversational query into a search-optimized form.
// Returns the original query if rewriting fails (non-blocking).
func (c *Client) RewriteQuery(ctx context.Context, query string) string {
	prompt := fmt.Sprintf(RewriteQueryPrompt, query)
	raw, err := c.CompleteParams(ctx, prompt, rewriteQueryTemp, rewriteQueryMaxTok)
	if err != nil || strings.TrimSpace(raw) == "" {
		return query
	}
	rewritten := strings.TrimSpace(raw)
	if len(rewritten) > rewriteQueryMaxLen || strings.Contains(rewritten, "\n") {
		return query
	}
	return rewritten
}

// ExpandWebSearchQueries generates semantically diverse web search query variants.
// Returns up to n alternative queries. Fails fast — caller should fall back gracefully.
func (c *Client) ExpandWebSearchQueries(ctx context.Context, query string, n int) ([]string, error) {
	prompt := fmt.Sprintf(ExpandWebQueryPrompt, n, query, n)
	raw, err := c.CompleteParams(ctx, prompt, expandQueryTemp, expandQueryTok)
	if err != nil {
		return nil, err
	}
	var variants []string
	if err := json.Unmarshal([]byte(raw), &variants); err != nil {
		return nil, fmt.Errorf("expand web: parse failed on %q: %w", raw, err)
	}
	if len(variants) > n {
		variants = variants[:n]
	}
	return variants, nil
}

// ExpandSearchQueries generates semantically diverse GitHub search query variants.
// Returns up to n alternative queries. Fails fast — caller should fall back gracefully.
func (c *Client) ExpandSearchQueries(ctx context.Context, query string, n int) ([]string, error) {
	prompt := fmt.Sprintf(ExpandQueryPrompt, n, query, n)
	raw, err := c.CompleteParams(ctx, prompt, expandQueryTemp, expandQueryTok)
	if err != nil {
		return nil, err
	}
	var variants []string
	if err := json.Unmarshal([]byte(raw), &variants); err != nil {
		return nil, fmt.Errorf("expand: parse failed on %q: %w", raw, err)
	}
	if len(variants) > n {
		variants = variants[:n]
	}
	return variants, nil
}
