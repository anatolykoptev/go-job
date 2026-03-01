package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anatolykoptev/go-kit/llm"
)

// currentDate returns today's date in ISO 8601 format (UTC).
func currentDate() string {
	return time.Now().UTC().Format("2006-01-02")
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

// stripFences removes markdown code fences from LLM output.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// CallLLM sends a prompt using the configured temperature and max_tokens.
func CallLLM(ctx context.Context, prompt string) (string, error) {
	reg.Incr(MetricLLMCalls)
	resp, err := cfg.LLMClient.Complete(ctx, "", prompt)
	if err != nil {
		reg.Incr(MetricLLMErrors)
		return "", err
	}
	return stripFences(resp), nil
}

// CallLLMRaw sends a prompt to the LLM API and returns the raw response text.
// Public wrapper for tool handlers that build their own prompts.
func CallLLMRaw(ctx context.Context, prompt string) (string, error) {
	return CallLLM(ctx, prompt)
}

// RewriteQuery uses the LLM to convert a conversational query into a search-optimized form.
func RewriteQuery(ctx context.Context, query string) string {
	prompt := fmt.Sprintf(rewriteQueryPrompt, query)
	reg.Incr(MetricLLMCalls)
	raw, err := cfg.LLMClient.Complete(ctx, "", prompt,
		llm.WithChatTemperature(0.3),
		llm.WithChatMaxTokens(100),
	)
	if err != nil {
		reg.Incr(MetricLLMErrors)
		return query
	}
	rewritten := strings.TrimSpace(stripFences(raw))
	if rewritten == "" || len(rewritten) > 200 || strings.Contains(rewritten, "\n") {
		return query
	}
	return rewritten
}

// ExpandWebSearchQueries generates semantically diverse web search query variants.
func ExpandWebSearchQueries(ctx context.Context, query string, n int) ([]string, error) {
	prompt := fmt.Sprintf(expandWebQueryPrompt, n, query, n)
	reg.Incr(MetricLLMCalls)
	raw, err := cfg.LLMClient.Complete(ctx, "", prompt,
		llm.WithChatTemperature(0.7),
		llm.WithChatMaxTokens(250),
	)
	if err != nil {
		reg.Incr(MetricLLMErrors)
		return nil, err
	}
	raw = stripFences(raw)
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
func ExpandSearchQueries(ctx context.Context, query string, n int) ([]string, error) {
	prompt := fmt.Sprintf(expandQueryPrompt, n, query, n)
	reg.Incr(MetricLLMCalls)
	raw, err := cfg.LLMClient.Complete(ctx, "", prompt,
		llm.WithChatTemperature(0.7),
		llm.WithChatMaxTokens(250),
	)
	if err != nil {
		reg.Incr(MetricLLMErrors)
		return nil, err
	}
	raw = stripFences(raw)
	var variants []string
	if err := json.Unmarshal([]byte(raw), &variants); err != nil {
		return nil, fmt.Errorf("expand: parse failed on %q: %w", raw, err)
	}
	if len(variants) > n {
		variants = variants[:n]
	}
	return variants, nil
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

// SummarizeToJSON builds an LLM prompt from search results and parses the response as JSON into T.
func SummarizeToJSON[T any](ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*T, string, error) {
	sources := BuildSourcesText(results, contents, contentLimit)
	prompt := fmt.Sprintf("%s\n\nQuery: %s\n\nSources:\n%s", instruction, query, sources)

	raw, err := CallLLM(ctx, prompt)
	if err != nil {
		return nil, "", err
	}

	var out T
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, raw, nil
	}
	return &out, "", nil
}

// SummarizeFreelanceResults calls the LLM with freelance-specific prompt and parses structured projects.
func SummarizeFreelanceResults(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*FreelanceSearchOutput, error) {
	parsed, raw, err := SummarizeToJSON[llmFreelanceOutput](ctx, query, instruction, contentLimit, results, contents)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return &FreelanceSearchOutput{Query: query, Summary: raw}, nil
	}

	for i := range parsed.Projects {
		if parsed.Projects[i].URL == "" && i < len(results) {
			parsed.Projects[i].URL = results[i].URL
		}
	}
	return &FreelanceSearchOutput{Query: query, Projects: parsed.Projects, Summary: parsed.Summary}, nil
}

// SummarizeJobResults calls the LLM with job-specific prompt and parses structured job listings.
func SummarizeJobResults(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*JobSearchOutput, error) {
	parsed, raw, err := SummarizeToJSON[llmJobOutput](ctx, query, instruction, contentLimit, results, contents)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return &JobSearchOutput{Query: query, Summary: raw}, nil
	}

	for i := range parsed.Jobs {
		if parsed.Jobs[i].URL == "" && i < len(results) {
			parsed.Jobs[i].URL = results[i].URL
		}
	}
	return &JobSearchOutput{Query: query, Jobs: parsed.Jobs, Summary: parsed.Summary}, nil
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
