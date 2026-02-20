package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// currentDate returns today's date in ISO 8601 format (UTC).
func currentDate() string {
	return time.Now().UTC().Format("2006-01-02")
}

// LLM client with its own timeout (longer than fetch/search).
var llmClient = &http.Client{Timeout: 60 * time.Second}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type LLMStructuredOutput struct {
	Answer string     `json:"answer"`
	Facts  []FactItem `json:"facts,omitempty"`
}

// llmJobOutput is the JSON structure expected from the LLM for job search.
type llmJobOutput struct {
	Jobs    []JobListing `json:"jobs"`
	Summary string       `json:"summary"`
}

// llmFreelanceOutput is the JSON structure expected from the LLM for freelance search.
type llmFreelanceOutput struct {
	Projects []FreelanceProject `json:"projects"`
	Summary  string             `json:"summary"`
}

// callLLMParams sends a prompt to the LLM API with explicit temperature and max_tokens.
// On any error, iterates through LLMAPIKeyFallbacks in order until one succeeds.
func callLLMParams(ctx context.Context, prompt string, temperature float64, maxTokens int) (string, error) {
	raw, err := callLLMWithKey(ctx, prompt, temperature, maxTokens, cfg.LLMAPIKey)
	if err != nil {
		for _, key := range cfg.LLMAPIKeyFallbacks {
			if key == "" {
				continue
			}
			raw, err = callLLMWithKey(ctx, prompt, temperature, maxTokens, key)
			if err == nil {
				break
			}
		}
	}
	return raw, err
}

// callLLMWithKey performs a single LLM API call with the given key.
func callLLMWithKey(ctx context.Context, prompt string, temperature float64, maxTokens int, apiKey string) (string, error) {
	metrics.LLMCalls.Add(1)

	body, _ := json.Marshal(chatRequest{
		Model:       cfg.LLMModel,
		Messages:    []chatMessage{{Role: "user", Content: prompt}},
		Temperature: temperature,
		MaxTokens:   maxTokens,
	})

	apiURL := strings.TrimSuffix(cfg.LLMAPIBase, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		metrics.LLMErrors.Add(1)
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := RetryHTTP(ctx, DefaultRetryConfig, func() (*http.Response, error) {
		return llmClient.Do(req)
	})
	if err != nil {
		metrics.LLMErrors.Add(1)
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		metrics.LLMErrors.Add(1)
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LLM API %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", err
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in LLM response")
	}

	raw := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	return strings.TrimSpace(raw), nil
}

// CallLLM sends a prompt using the configured temperature and max_tokens.
func CallLLM(ctx context.Context, prompt string) (string, error) {
	return callLLMParams(ctx, prompt, cfg.LLMTemperature, cfg.LLMMaxTokens)
}

// RewriteQuery uses the LLM to convert a conversational query into a search-optimized form.
// Returns the rewritten query, or the original query if rewriting fails (non-blocking).
// Example: "как задеплоить go в k8s?" → "golang Kubernetes deployment production best practices"
func RewriteQuery(ctx context.Context, query string) string {
	prompt := fmt.Sprintf(rewriteQueryPrompt, query)
	raw, err := callLLMParams(ctx, prompt, 0.3, 100)
	if err != nil || strings.TrimSpace(raw) == "" {
		return query
	}
	rewritten := strings.TrimSpace(raw)
	// Sanity check: reject if LLM returned something suspiciously long or multi-line
	if len(rewritten) > 200 || strings.Contains(rewritten, "\n") {
		return query
	}
	return rewritten
}

// ExpandWebSearchQueries generates semantically diverse web search query variants.
// Uses expandWebQueryPrompt (no GitHub syntax, preserves language names like "golang").
// Returns up to n alternative queries. Fails fast — caller should fall back gracefully.
func ExpandWebSearchQueries(ctx context.Context, query string, n int) ([]string, error) {
	prompt := fmt.Sprintf(expandWebQueryPrompt, n, query, n)
	raw, err := callLLMParams(ctx, prompt, 0.7, 250)
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

// ExpandSearchQueries uses the LLM to generate semantically diverse query variants.
// Returns up to n alternative queries (not including the original).
// Fails fast — caller should handle error gracefully and fall back to original query.
func ExpandSearchQueries(ctx context.Context, query string, n int) ([]string, error) {
	prompt := fmt.Sprintf(expandQueryPrompt, n, query, n)

	// Low max_tokens: 3 short queries fit in ~150 tokens. Slightly higher temp for variety.
	raw, err := callLLMParams(ctx, prompt, 0.7, 250)
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

// CallLLMRaw sends a prompt to the LLM API and returns the raw response text.
// Public wrapper for tool handlers that build their own prompts.
func CallLLMRaw(ctx context.Context, prompt string) (string, error) {
	return CallLLM(ctx, prompt)
}

// BuildSourcesText formats search results and their fetched content for LLM context.
func BuildSourcesText(results []SearxngResult, contents map[string]string, contentLimit int) string {
	var sb strings.Builder
	for i, r := range results {
		fmt.Fprintf(&sb, "\n[%d] %s\nURL: %s\n", i+1, r.Title, r.URL)
		if c, ok := contents[r.URL]; ok && c != "" {
			if len(c) > contentLimit {
				c = c[:contentLimit] + "..."
			}
			fmt.Fprintf(&sb, "Content: %s\n", c)
		}
		if r.Content != "" {
			if _, ok := contents[r.URL]; !ok {
				fmt.Fprintf(&sb, "Snippet: %s\n", r.Content)
			}
		}
	}
	return sb.String()
}

// summarizeWithLLM builds context from search results and calls the LLM API.
func summarizeWithLLM(ctx context.Context, query string, results []SearxngResult, contents map[string]string) (*LLMStructuredOutput, error) {
	qt := DetectQueryType(query)
	instruction := TypeInstructions[qt]

	contentLimit := cfg.MaxContentChars
	if qt == QtFact {
		contentLimit = 2000
	}

	return SummarizeWithInstruction(ctx, query, instruction, contentLimit, results, contents)
}

// SummarizeWithInstruction summarizes search results using a custom LLM instruction.
func SummarizeWithInstruction(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*LLMStructuredOutput, error) {
	sources := BuildSourcesText(results, contents, contentLimit)
	prompt := fmt.Sprintf(promptBase, currentDate(), instruction, query, sources)

	raw, err := CallLLM(ctx, prompt)
	if err != nil {
		return nil, err
	}

	var out LLMStructuredOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		if answer := ExtractJSONAnswer(raw); answer != "" {
			return &LLMStructuredOutput{Answer: answer}, nil
		}
		return &LLMStructuredOutput{Answer: raw}, nil
	}
	return &out, nil
}

// SummarizeDeep summarizes search results using promptDeep — structured output with
// ## headings and mandatory per-sentence citations.
func SummarizeDeep(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*LLMStructuredOutput, error) {
	sources := BuildSourcesText(results, contents, contentLimit)
	// Prepend domain instruction if present
	instructionSection := ""
	if instruction != "" {
		instructionSection = instruction + "\n\n"
	}
	prompt := fmt.Sprintf(promptDeep, currentDate(), instructionSection, query, sources)

	raw, err := CallLLM(ctx, prompt)
	if err != nil {
		return nil, err
	}

	var out LLMStructuredOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		if answer := ExtractJSONAnswer(raw); answer != "" {
			return &LLMStructuredOutput{Answer: answer}, nil
		}
		return &LLMStructuredOutput{Answer: raw}, nil
	}
	return &out, nil
}

// SummarizeFreelanceResults calls the LLM with freelance-specific prompt and parses structured projects.
// Uses its own prompt format (not promptBase) since the output is structured JSON, not {"answer": "..."}.
func SummarizeFreelanceResults(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*FreelanceSearchOutput, error) {
	sources := BuildSourcesText(results, contents, contentLimit)
	prompt := fmt.Sprintf("%s\n\nQuery: %s\n\nSources:\n%s", instruction, query, sources)

	raw, err := CallLLM(ctx, prompt)
	if err != nil {
		return nil, err
	}

	var out llmFreelanceOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		// Fallback: return raw as summary
		return &FreelanceSearchOutput{Query: query, Summary: raw}, nil
	}

	// Fill URLs from search results for projects that don't have them
	for i := range out.Projects {
		if out.Projects[i].URL == "" && i < len(results) {
			out.Projects[i].URL = results[i].URL
		}
	}

	return &FreelanceSearchOutput{
		Query:    query,
		Projects: out.Projects,
		Summary:  out.Summary,
	}, nil
}

// SummarizeJobResults calls the LLM with job-specific prompt and parses structured job listings.
func SummarizeJobResults(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*JobSearchOutput, error) {
	sources := BuildSourcesText(results, contents, contentLimit)
	prompt := fmt.Sprintf("%s\n\nQuery: %s\n\nSources:\n%s", instruction, query, sources)

	raw, err := CallLLM(ctx, prompt)
	if err != nil {
		return nil, err
	}

	var out llmJobOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		// Fallback: return raw as summary
		return &JobSearchOutput{Query: query, Summary: raw}, nil
	}

	// Fill URLs from search results for jobs that don't have them
	for i := range out.Jobs {
		if out.Jobs[i].URL == "" && i < len(results) {
			out.Jobs[i].URL = results[i].URL
		}
	}

	return &JobSearchOutput{
		Query:   query,
		Jobs:    out.Jobs,
		Summary: out.Summary,
	}, nil
}

// ExtractJSONAnswer extracts the "answer" field from malformed JSON
// where the value may contain unescaped newlines or special characters.
func ExtractJSONAnswer(raw string) string {
	prefix := `"answer"`
	idx := strings.Index(raw, prefix)
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len(prefix):]
	rest = strings.TrimSpace(rest)
	if len(rest) == 0 || rest[0] != ':' {
		return ""
	}
	rest = strings.TrimSpace(rest[1:])
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:] // skip opening quote

	var sb strings.Builder
	for i := 0; i < len(rest); i++ {
		if rest[i] == '\\' && i+1 < len(rest) {
			if rest[i+1] == '"' {
				sb.WriteByte('"')
				i++
				continue
			}
			if rest[i+1] == 'n' {
				sb.WriteByte('\n')
				i++
				continue
			}
			sb.WriteByte(rest[i])
			continue
		}
		if rest[i] == '"' {
			return sb.String()
		}
		sb.WriteByte(rest[i])
	}
	if sb.Len() > 0 {
		return sb.String()
	}
	return ""
}
