package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const goHullyURL = "http://127.0.0.1:8892/mcp"

// PersonProfile is the structured output of person_research.
type PersonProfile struct {
	Name           string   `json:"name"`
	Title          string   `json:"title"`
	Company        string   `json:"company"`
	LinkedInURL    string   `json:"linkedin_url,omitempty"`
	GitHubURL      string   `json:"github_url,omitempty"`
	TwitterURL     string   `json:"twitter_url,omitempty"`
	Location       string   `json:"location,omitempty"`
	Background     string   `json:"background"`
	Skills         []string `json:"skills,omitempty"`
	Interests      string   `json:"interests,omitempty"`
	RecentActivity string   `json:"recent_activity,omitempty"`
	CommonGround   string   `json:"common_ground"`
	InterviewTips  string   `json:"interview_tips"`
}

// callGoHully calls a go-hully MCP tool via JSON-RPC 2.0.
// Returns empty string (not error) if go-hully is unavailable — it's optional.
func callGoHully(ctx context.Context, toolName string, params map[string]any) (string, error) {
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": params,
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	httpCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(httpCtx, http.MethodPost, goHullyURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("go-hully unreachable: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var rpcResp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return "", fmt.Errorf("go-hully parse: %w (body: %s)", err, engine.TruncateRunes(string(respBody), 200, "..."))
	}

	if rpcResp.Error != nil {
		return "", fmt.Errorf("go-hully error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	var parts []string
	for _, c := range rpcResp.Result.Content {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// extractTwitterHandle tries to find a Twitter/X handle from a URL.
func extractTwitterHandle(rawURL string) string {
	parts := strings.Split(rawURL, "/")
	for i, p := range parts {
		if (p == "twitter.com" || p == "x.com") && i+1 < len(parts) {
			h := parts[i+1]
			h = strings.TrimPrefix(h, "@")
			h = strings.SplitN(h, "?", 2)[0]
			h = strings.SplitN(h, "#", 2)[0]
			if h != "" && !strings.Contains(h, ".") && len(h) <= 50 {
				return h
			}
		}
	}
	return ""
}

const personResearchPrompt = `You are an interview preparation expert. Research gathered about a person is shown below.

Person: %s
Context (role/company): %s

Gathered data:
%s

Based on this, create a comprehensive person profile for interview preparation.

Return a JSON object with this exact structure:
{
  "name": "<full name>",
  "title": "<current job title>",
  "company": "<current company>",
  "linkedin_url": "<LinkedIn profile URL if found, otherwise empty string>",
  "github_url": "<GitHub profile URL if found, otherwise empty string>",
  "twitter_url": "<Twitter/X profile URL if found, otherwise empty string>",
  "location": "<city/country>",
  "background": "<2-3 sentence professional background summary>",
  "skills": [<list of technical/professional skills found>],
  "interests": "<professional interests, topics they post about or contribute to>",
  "recent_activity": "<recent projects, blog posts, talks, open source contributions>",
  "common_ground": "<possible shared interests or natural conversation starters based on their public profile>",
  "interview_tips": "<specific actionable tips: topics they care about, their communication style, how to make a good impression>"
}

Return ONLY the JSON object, no markdown, no explanation.`

//nolint:funlen // orchestration function
// ResearchPerson gathers and synthesizes public information about a person for interview prep.
func ResearchPerson(ctx context.Context, name, company, jobTitle string) (*PersonProfile, error) {
	subject := name
	if company != "" {
		subject = name + " " + company
	}
	if jobTitle != "" {
		subject = name + " " + jobTitle
	}

	type sourceData struct {
		name string
		text string
	}

	ch := make(chan sourceData, 5)
	var wg sync.WaitGroup

	// Source 1: LinkedIn
	wg.Add(1)
	go func() {
		defer wg.Done()
		results, err := engine.SearchSearXNG(ctx, subject+" site:linkedin.com/in", "all", "", "google")
		if err != nil {
			ch <- sourceData{name: "linkedin"}
			return
		}
		var snippets []string
		for _, r := range results {
			if strings.Contains(r.URL, "linkedin.com") {
				snippets = append(snippets, fmt.Sprintf("[LinkedIn] %s\n%s\n%s", r.Title, r.URL, engine.TruncateRunes(r.Content, 500, "...")))
			}
			if len(snippets) >= 3 {
				break
			}
		}
		ch <- sourceData{name: "linkedin", text: strings.Join(snippets, "\n\n")}
	}()

	// Source 2: GitHub
	wg.Add(1)
	go func() {
		defer wg.Done()
		results, err := engine.SearchSearXNG(ctx, subject+" site:github.com", "all", "", "google")
		if err != nil {
			ch <- sourceData{name: "github"}
			return
		}
		var snippets []string
		for _, r := range results {
			if strings.Contains(r.URL, "github.com") {
				snippets = append(snippets, fmt.Sprintf("[GitHub] %s\n%s\n%s", r.Title, r.URL, engine.TruncateRunes(r.Content, 400, "...")))
			}
			if len(snippets) >= 3 {
				break
			}
		}
		ch <- sourceData{name: "github", text: strings.Join(snippets, "\n\n")}
	}()

	// Source 3: General web
	wg.Add(1)
	go func() {
		defer wg.Done()
		query := subject + " developer engineer"
		if company != "" {
			query = name + " " + company
		}
		results, err := engine.SearchSearXNG(ctx, query, "all", "", "google")
		if err != nil {
			ch <- sourceData{name: "web"}
			return
		}
		var snippets []string
		for i, r := range results {
			if i >= 5 {
				break
			}
			snippets = append(snippets, fmt.Sprintf("[Web] %s\n%s\n%s", r.Title, r.URL, engine.TruncateRunes(r.Content, 300, "...")))
		}
		ch <- sourceData{name: "web", text: strings.Join(snippets, "\n\n")}
	}()

	// Source 4: Habr (Russian tech community)
	wg.Add(1)
	go func() {
		defer wg.Done()
		results, err := engine.SearchSearXNG(ctx, name+" site:habr.com", "ru", "", "google")
		if err != nil {
			ch <- sourceData{name: "habr"}
			return
		}
		var snippets []string
		for i, r := range results {
			if i >= 3 {
				break
			}
			snippets = append(snippets, fmt.Sprintf("[Habr] %s\n%s\n%s", r.Title, r.URL, engine.TruncateRunes(r.Content, 300, "...")))
		}
		ch <- sourceData{name: "habr", text: strings.Join(snippets, "\n\n")}
	}()

	// Source 5: Twitter via go-hully
	wg.Add(1)
	go func() {
		defer wg.Done()
		// First find their Twitter handle via web search
		twResults, _ := engine.SearchSearXNG(ctx, subject+" twitter OR x.com", "all", "", "google")
		var handle string
		for _, r := range twResults {
			if h := extractTwitterHandle(r.URL); h != "" {
				handle = h
				break
			}
		}
		if handle == "" {
			ch <- sourceData{name: "twitter"}
			return
		}
		text, err := callGoHully(ctx, "analyze_account", map[string]any{"username": handle})
		if err != nil {
			// go-hully optional — just record the handle
			ch <- sourceData{name: "twitter", text: fmt.Sprintf("[Twitter] handle: @%s (analysis failed)", handle)}
			return
		}
		ch <- sourceData{name: "twitter", text: fmt.Sprintf("[Twitter @%s]\n%s", handle, engine.TruncateRunes(text, 1000, "..."))}
	}()

	go func() {
		wg.Wait()
		close(ch)
	}()

	var parts []string
	for sd := range ch {
		if sd.text != "" {
			parts = append(parts, "=== "+strings.ToUpper(sd.name)+" ===\n"+sd.text)
		}
	}

	if len(parts) == 0 {
		return nil, fmt.Errorf("person_research: no data found for %q", name)
	}

	combined := engine.TruncateRunes(strings.Join(parts, "\n\n"), 8000, "")

	context2 := company
	if jobTitle != "" && company != "" {
		context2 = jobTitle + " at " + company
	} else if jobTitle != "" {
		context2 = jobTitle
	}

	prompt := fmt.Sprintf(personResearchPrompt, name, context2, combined)
	raw, err := engine.CallLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("person_research LLM: %w", err)
	}

	raw = engine.ExtractJSONAnswer(raw)

	var profile PersonProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return nil, fmt.Errorf("person_research parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}
	return &profile, nil
}
