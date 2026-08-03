package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"

	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/text"
)

// StructuredOutput is the parsed JSON from an LLM summarization response.
type StructuredOutput struct {
	Answer string     `json:"answer"`
	Facts  []FactItem `json:"facts,omitempty"`
}

// FactItem is a single verified fact with explicit source indices.
type FactItem struct {
	Point   string `json:"point"`   // complete sentence, no markdown
	Sources []int  `json:"sources"` // 1-based indices into Sources array
}

// TypeInstructions maps query types to LLM formatting instructions.
var TypeInstructions = map[text.QueryType]string{
	text.QtFact: `FORMAT: One or two sentences with the specific data point requested. Nothing more.`,

	text.QtComparison: `FORMAT: Start with a compact markdown table (5-8 rows max) comparing key criteria. Column headers = the things being compared.
After the table: 1-2 sentences with a practical recommendation (which to choose and when).
IMPORTANT: Keep table cells SHORT (under 15 words each). No paragraphs inside cells.`,

	text.QtList: `FORMAT: Numbered list. Each item: name + one-line description + citation.
Include ALL items found in sources. Order by relevance or popularity.`,

	text.QtHowTo: `FORMAT: Numbered steps. Each step is actionable and specific.
Include commands, code, or URLs where available in sources.`,

	text.QtGeneral: `FORMAT: Direct factual answer. Use bullet points for multiple aspects. Include specific data.
Be practical — if the question implies a choice, give a recommendation.`,
}

// BuildSourcesTextWeighted formats search results with custom weight allocation.
func BuildSourcesTextWeighted(results []sources.Result, contents map[string]string, totalBudget int, charsPerToken float64, weights []float64) string {
	var sb strings.Builder
	for i, r := range results {
		fmt.Fprintf(&sb, "\n[%d] %s\nURL: %s\n", i+1, r.Title, r.URL)
		c, hasContent := contents[r.URL]
		if hasContent && c != "" && i < len(weights) {
			tokens := int(math.Ceil(float64(totalBudget) * weights[i]))
			c = text.TruncateToTokenBudget(c, tokens, charsPerToken)
			fmt.Fprintf(&sb, "Content: %s\n", c)
			continue
		}
		if r.Content != "" {
			fmt.Fprintf(&sb, "Snippet: %s\n", r.Content)
		}
	}
	return sb.String()
}

// rankedWeights defines the percentage of total budget each source gets by rank.
// Sources beyond this list get snippet-only treatment (no fetched content).
var rankedWeights = []float64{0.30, 0.25, 0.20, 0.15, 0.10}

// BuildSourcesText formats search results with ranked token allocation.
// totalBudget is the TOTAL token budget across all sources (not per-source).
// Higher-ranked sources get proportionally more content; low-ranked ones get snippets only.
func BuildSourcesText(results []sources.Result, contents map[string]string, totalBudget int, charsPerToken float64) string {
	var sb strings.Builder
	for i, r := range results {
		fmt.Fprintf(&sb, "\n[%d] %s\nURL: %s\n", i+1, r.Title, r.URL)

		c, hasContent := contents[r.URL]
		if hasContent && c != "" && i < len(rankedWeights) {
			tokens := int(math.Ceil(float64(totalBudget) * rankedWeights[i]))
			c = text.TruncateToTokenBudget(c, tokens, charsPerToken)
			fmt.Fprintf(&sb, "Content: %s\n", c)
			continue
		}

		if r.Content != "" {
			fmt.Fprintf(&sb, "Snippet: %s\n", r.Content)
		}
	}
	return sb.String()
}

// Summarize summarizes search results using auto-detected query type instructions.
func (c *Client) Summarize(ctx context.Context, query string, maxTokens int, charsPerToken float64, results []sources.Result, contents map[string]string) (*StructuredOutput, error) {
	qt := text.DetectQueryType(query)
	instruction := TypeInstructions[qt]
	return c.SummarizeWithInstruction(ctx, query, instruction, maxTokens, charsPerToken, results, contents)
}

// SummarizeWithInstruction summarizes search results using a custom LLM instruction.
func (c *Client) SummarizeWithInstruction(ctx context.Context, query, instruction string, maxTokens int, charsPerToken float64, results []sources.Result, contents map[string]string) (*StructuredOutput, error) {
	sources := BuildSourcesText(results, contents, maxTokens, charsPerToken)
	prompt := fmt.Sprintf(PromptBase, currentDate(), instruction, query, sources)

	raw, err := c.Complete(ctx, prompt)
	if err != nil {
		return nil, err
	}

	var out StructuredOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		if answer := ExtractJSONAnswer(raw); answer != "" {
			return &StructuredOutput{Answer: answer}, nil
		}
		return &StructuredOutput{Answer: raw}, nil
	}
	return &out, nil
}

// SummarizeDeep summarizes search results with exhaustive fact extraction.
func (c *Client) SummarizeDeep(ctx context.Context, query, instruction string, maxTokens int, charsPerToken float64, results []sources.Result, contents map[string]string) (*StructuredOutput, error) {
	sources := BuildSourcesText(results, contents, maxTokens, charsPerToken)
	instructionSection := ""
	if instruction != "" {
		instructionSection = instruction + "\n\n"
	}
	prompt := fmt.Sprintf(PromptDeep, currentDate(), instructionSection, query, sources)

	raw, err := c.Complete(ctx, prompt)
	if err != nil {
		return nil, err
	}

	var out StructuredOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		if answer := ExtractJSONAnswer(raw); answer != "" {
			return &StructuredOutput{Answer: answer}, nil
		}
		return &StructuredOutput{Answer: raw}, nil
	}
	return &out, nil
}

// SummarizeOpts configures a summarization call.
type SummarizeOpts struct {
	Query           string
	Instruction     string
	TotalBudget     int // total token budget for source context
	CharsPerToken   float64
	MaxOutputTokens int // 0 = use client default
	// DisableReasoning, when true, passes WithReasoningEffort("none") to the LLM
	// call, freeing the token budget from chain-of-thought for content output.
	// The go-kit transport layer gates this per-endpoint via LLM_REASONING_EFFORT_MODELS
	// allowlist, so mixed chains (reasoning + non-reasoning endpoints) work correctly.
	DisableReasoning bool
}

// SummarizeWithOpts summarizes search results with full control over budget.
func (c *Client) SummarizeWithOpts(ctx context.Context, opts SummarizeOpts, results []sources.Result, contents map[string]string) (*StructuredOutput, error) {
	srcs := BuildSourcesText(results, contents, opts.TotalBudget, opts.CharsPerToken)

	var prompt string
	if opts.Instruction != "" {
		prompt = fmt.Sprintf(PromptBase, currentDate(), opts.Instruction, opts.Query, srcs)
	} else {
		qt := text.DetectQueryType(opts.Query)
		instruction := TypeInstructions[qt]
		prompt = fmt.Sprintf(PromptBase, currentDate(), instruction, opts.Query, srcs)
	}

	maxOut := opts.MaxOutputTokens
	if maxOut == 0 {
		maxOut = c.maxTokens
	}

	var callOpts []ChatOption
	if opts.DisableReasoning {
		callOpts = append(callOpts, WithReasoningEffort("none"))
	}
	raw, err := c.CompleteParams(ctx, prompt, c.temperature, maxOut, callOpts...)
	if err != nil {
		return nil, err
	}

	var out StructuredOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		if answer := ExtractJSONAnswer(raw); answer != "" {
			return &StructuredOutput{Answer: answer}, nil
		}
		return &StructuredOutput{Answer: raw}, nil
	}
	return &out, nil
}

// SummarizeDeepWithOpts summarizes with exhaustive fact extraction and output cap.
func (c *Client) SummarizeDeepWithOpts(ctx context.Context, opts SummarizeOpts, results []sources.Result, contents map[string]string) (*StructuredOutput, error) {
	srcs := BuildSourcesText(results, contents, opts.TotalBudget, opts.CharsPerToken)
	instructionSection := ""
	if opts.Instruction != "" {
		instructionSection = opts.Instruction + "\n\n"
	}
	prompt := fmt.Sprintf(PromptDeep, currentDate(), instructionSection, opts.Query, srcs)

	maxOut := opts.MaxOutputTokens
	if maxOut == 0 {
		maxOut = c.maxTokens
	}

	var callOpts []ChatOption
	if opts.DisableReasoning {
		callOpts = append(callOpts, WithReasoningEffort("none"))
	}
	raw, err := c.CompleteParams(ctx, prompt, c.temperature, maxOut, callOpts...)
	if err != nil {
		return nil, err
	}

	var out StructuredOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		if answer := ExtractJSONAnswer(raw); answer != "" {
			return &StructuredOutput{Answer: answer}, nil
		}
		return &StructuredOutput{Answer: raw}, nil
	}
	return &out, nil
}

// SummarizeToJSON builds an LLM prompt from search results and parses the response as JSON into T.
// Returns (parsed, "", nil) on success, (nil, raw, nil) on parse failure (caller handles fallback),
// or (nil, "", err) on LLM error.
// Truncation-aware: uses CompleteDetailed so finish_reason="length" is surfaced instead of
// being indistinguishable from an honest "nothing found" (issue #413/#428). A truncated response
// increments the bounded llm_truncations counter and logs raw_len + served model so the first
// recurrence root-causes itself (raw_len/4 ≈ max_tokens → output cap; far below → upstream cut).
func SummarizeToJSON[T any](ctx context.Context, c *Client, query, instruction string, maxTokens int, charsPerToken float64, results []sources.Result, contents map[string]string) (*T, string, error) {
	sources := BuildSourcesText(results, contents, maxTokens, charsPerToken)
	prompt := fmt.Sprintf("%s\n\nQuery: %s\n\nSources:\n%s", instruction, query, sources)

	maxOut := c.maxTokens
	if maxTokens > 0 {
		maxOut = maxTokens
	}
	resp, err := c.CompleteDetailed(ctx, prompt, WithChatMaxTokens(maxOut))
	if err != nil {
		return nil, "", err
	}
	raw := resp.Content
	rawLen := len(raw)

	if resp.FinishReason == "length" {
		IncTruncated("length", resp.ServedBy, rawLen)
		slog.Warn("llm: response truncated by length limit",
			slog.Int("raw_len", rawLen),
			slog.Int("max_tokens", maxOut),
			slog.String("model", resp.ServedBy),
			slog.String("query", truncateForLog(query, 80)),
		)
		return nil, raw, nil // caller sees raw, but truncation is now visible
	}

	var out T
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		IncTruncated("unparseable", resp.ServedBy, rawLen)
		slog.Warn("llm: response unparseable as JSON (possibly truncated)",
			slog.Int("raw_len", rawLen),
			slog.String("model", resp.ServedBy),
			slog.String("finish_reason", resp.FinishReason),
		)
		return nil, raw, nil //nolint:nilerr // by design: parse failure returns raw for caller handling
	}
	return &out, "", nil
}

// truncationCounters is a bounded counter (ok/truncated/unparseable) for LLM
// response classification. Pre-touched at zero so dashboards see a real 0→N
// transition on first occurrence, not just the second.
// ponytail: process-local mutex-protected counters; promote to metrics registry
// if per-model labels are ever needed.
var (
	truncationMu     sync.Mutex
	truncationCounts = map[string]int{"ok": 0, "truncated": 0, "unparseable": 0}
)

// IncTruncated increments the bounded truncation counter for a class.
func IncTruncated(class, model string, rawLen int) {
	truncationMu.Lock()
	defer truncationMu.Unlock()
	truncationCounts[class]++
	if model != "" {
		// Keep the last routed model per class (bounded at 9 weight-routed models).
		lastModel[class] = model
	}
}

// lastModel records the most recent routed model per class (bounded at 9).
var lastModel = map[string]string{}

// TruncationStats returns a copy of the truncation counters.
func TruncationStats() map[string]int {
	truncationMu.Lock()
	defer truncationMu.Unlock()
	out := make(map[string]int, len(truncationCounts))
	for k, v := range truncationCounts {
		out[k] = v
	}
	return out
}

// truncateForLog shortens query strings for log lines.
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// SummarizeWithTier summarizes with tier-specific weights and prompt selection.
// Variadic chatOpts pass through to kit (e.g. WithChatModel для per-call
// model override в chain loops с per-attempt timeout).
func (c *Client) SummarizeWithTier(ctx context.Context, opts SummarizeOpts, results []sources.Result, contents map[string]string, weights []float64, useDeepPrompt bool, chatOpts ...ChatOption) (*StructuredOutput, error) {
	srcs := BuildSourcesTextWeighted(results, contents, opts.TotalBudget, opts.CharsPerToken, weights)
	var prompt string
	if useDeepPrompt {
		instructionSection := ""
		if opts.Instruction != "" {
			instructionSection = opts.Instruction + "\n\n"
		}
		prompt = fmt.Sprintf(PromptDeep, currentDate(), instructionSection, opts.Query, srcs)
	} else {
		instruction := opts.Instruction
		if instruction == "" {
			qt := text.DetectQueryType(opts.Query)
			instruction = TypeInstructions[qt]
		}
		prompt = fmt.Sprintf(PromptBase, currentDate(), instruction, opts.Query, srcs)
	}
	maxOut := opts.MaxOutputTokens
	if maxOut == 0 {
		maxOut = c.maxTokens
	}
	if opts.DisableReasoning {
		chatOpts = append([]ChatOption{WithReasoningEffort("none")}, chatOpts...)
	}
	raw, err := c.CompleteParams(ctx, prompt, c.temperature, maxOut, chatOpts...)
	if err != nil {
		return nil, err
	}
	var out StructuredOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		if answer := ExtractJSONAnswer(raw); answer != "" {
			return &StructuredOutput{Answer: answer}, nil
		}
		return &StructuredOutput{Answer: raw}, nil
	}
	return &out, nil
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
			switch rest[i+1] {
			case '"':
				sb.WriteByte('"')
				i++
				continue
			case 'n':
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
