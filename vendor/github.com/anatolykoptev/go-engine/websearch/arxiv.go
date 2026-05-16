package websearch

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const arxivDefaultBase = "http://export.arxiv.org"

// Arxiv searches the arXiv Atom API. No authentication required.
// Rate limit: 1 request per 3 seconds (enforced externally if needed).
type Arxiv struct {
	baseURL    string
	httpClient *http.Client
}

// ArxivOption configures Arxiv.
type ArxivOption func(*Arxiv)

// WithArxivBaseURL overrides the default arXiv API base URL.
func WithArxivBaseURL(u string) ArxivOption {
	return func(a *Arxiv) { a.baseURL = u }
}

// WithArxivHTTPClient sets a custom HTTP client.
func WithArxivHTTPClient(c *http.Client) ArxivOption {
	return func(a *Arxiv) { a.httpClient = c }
}

// NewArxiv creates an arXiv API client.
func NewArxiv(opts ...ArxivOption) *Arxiv {
	a := &Arxiv{baseURL: arxivDefaultBase, httpClient: &http.Client{}}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Search implements Provider. Queries arXiv Atom API.
func (a *Arxiv) Search(ctx context.Context, query string, opts SearchOpts) ([]Result, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	u := fmt.Sprintf(
		"%s/api/query?search_query=all:%s&max_results=%d&sortBy=relevance",
		a.baseURL, url.QueryEscape(query), limit,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("arxiv: build request: %w", err)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("arxiv: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("arxiv: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("arxiv: read body: %w", err)
	}

	results, err := ParseArxivAtom(body)
	if err != nil {
		return nil, fmt.Errorf("arxiv: %w", err)
	}

	return applyLimit(results, limit), nil
}

// Atom XML types for arXiv API response.
type arxivFeed struct {
	XMLName xml.Name     `xml:"feed"`
	Entries []arxivEntry `xml:"entry"`
}

type arxivEntry struct {
	Title     string        `xml:"title"`
	ID        string        `xml:"id"`
	Summary   string        `xml:"summary"`
	Authors   []arxivAuthor `xml:"author"`
	Published string        `xml:"published"`
}

type arxivAuthor struct {
	Name string `xml:"name"`
}

// ParseArxivAtom parses arXiv Atom XML into search results.
func ParseArxivAtom(data []byte) ([]Result, error) {
	var feed arxivFeed
	if err := xml.Unmarshal(data, &feed); err != nil {
		return nil, fmt.Errorf("parse atom: %w", err)
	}

	results := make([]Result, 0, len(feed.Entries))
	for _, e := range feed.Entries {
		title := strings.TrimSpace(e.Title)
		if title == "" || e.ID == "" {
			continue
		}

		authors := make([]string, 0, len(e.Authors))
		for _, a := range e.Authors {
			if name := strings.TrimSpace(a.Name); name != "" {
				authors = append(authors, name)
			}
		}

		content := strings.TrimSpace(e.Summary)
		if len(authors) > 0 {
			content = "Authors: " + strings.Join(authors, ", ") + "\n" + content
		}

		results = append(results, Result{
			Title:    title,
			URL:      strings.TrimSpace(e.ID),
			Content:  content,
			Score:    directResultScore,
			Metadata: map[string]string{"engine": "arxiv"},
		})
	}

	return results, nil
}
