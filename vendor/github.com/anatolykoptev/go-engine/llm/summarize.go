package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anatolykoptev/go-engine/search"
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

// BuildSourcesText formats search results and their fetched content for LLM context.
func BuildSourcesText(results []search.Result, contents map[string]string, contentLimit int) string {
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

// Summarize summarizes search results using auto-detected query type instructions.
func (c *Client) Summarize(ctx context.Context, query string, contentLimit int, results []search.Result, contents map[string]string) (*StructuredOutput, error) {
	qt := text.DetectQueryType(query)
	instruction := TypeInstructions[qt]
	return c.SummarizeWithInstruction(ctx, query, instruction, contentLimit, results, contents)
}

// SummarizeWithInstruction summarizes search results using a custom LLM instruction.
func (c *Client) SummarizeWithInstruction(ctx context.Context, query, instruction string, contentLimit int, results []search.Result, contents map[string]string) (*StructuredOutput, error) {
	sources := BuildSourcesText(results, contents, contentLimit)
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
func (c *Client) SummarizeDeep(ctx context.Context, query, instruction string, contentLimit int, results []search.Result, contents map[string]string) (*StructuredOutput, error) {
	sources := BuildSourcesText(results, contents, contentLimit)
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

// SummarizeToJSON builds an LLM prompt from search results and parses the response as JSON into T.
// Returns (parsed, "", nil) on success, (nil, raw, nil) on parse failure (caller handles fallback),
// or (nil, "", err) on LLM error.
func SummarizeToJSON[T any](ctx context.Context, c *Client, query, instruction string, contentLimit int, results []search.Result, contents map[string]string) (*T, string, error) {
	sources := BuildSourcesText(results, contents, contentLimit)
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
