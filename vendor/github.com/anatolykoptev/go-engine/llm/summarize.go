package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

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

	raw, err := c.CompleteParams(ctx, prompt, c.temperature, maxOut)
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

	raw, err := c.CompleteParams(ctx, prompt, c.temperature, maxOut)
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
func SummarizeToJSON[T any](ctx context.Context, c *Client, query, instruction string, maxTokens int, charsPerToken float64, results []sources.Result, contents map[string]string) (*T, string, error) {
	sources := BuildSourcesText(results, contents, maxTokens, charsPerToken)
	prompt := fmt.Sprintf("%s\n\nQuery: %s\n\nSources:\n%s", instruction, query, sources)

	raw, err := c.Complete(ctx, prompt)
	if err != nil {
		return nil, "", err
	}

	var out T
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, raw, nil //nolint:nilerr // by design: parse failure returns raw for caller handling
	}
	return &out, "", nil
}

// SummarizeWithTier summarizes with tier-specific weights and prompt selection.
func (c *Client) SummarizeWithTier(ctx context.Context, opts SummarizeOpts, results []sources.Result, contents map[string]string, weights []float64, useDeepPrompt bool) (*StructuredOutput, error) {
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
	raw, err := c.CompleteParams(ctx, prompt, c.temperature, maxOut)
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
