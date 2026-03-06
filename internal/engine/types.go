package engine

import (
	"github.com/anatolykoptev/go-engine/llm"
	"github.com/anatolykoptev/go-engine/pipeline"
	"github.com/anatolykoptev/go-engine/sources"
	"github.com/anatolykoptev/go-engine/text"
)

// --- Type aliases mapping go-job types to go-engine types ---

type SearxngResult = sources.Result
type LLMStructuredOutput = llm.StructuredOutput
type FactItem = llm.FactItem
type SourceItem = pipeline.SourceItem
type SmartSearchOutput = pipeline.SearchOutput
type OutputOpts = pipeline.OutputOpts
type QueryType = text.QueryType
type QueryDomain = text.QueryDomain

// Constant aliases for query types.
const (
	QtGeneral    = text.QtGeneral
	QtFact       = text.QtFact
	QtComparison = text.QtComparison
	QtList       = text.QtList
	QtHowTo      = text.QtHowTo
)

// Constant aliases for query domains.
const (
	QdGeneral     = text.QdGeneral
	QdWordPress   = text.QdWordPress
	QdClaudeCode  = text.QdClaudeCode
	QdGitHubRepo  = text.QdGitHubRepo
	QdLibDocs     = text.QdLibDocs
	QdHuggingFace = text.QdHuggingFace
)

// --- Domain-specific input types (go-job only) ---

type SmartSearchInput struct {
	Query     string `json:"query" jsonschema:"Search query"`
	Language  string `json:"language,omitempty" jsonschema:"Language code (default: all)"`
	TimeRange string `json:"time_range,omitempty" jsonschema:"Time filter: day, month, year"`
	Depth     string `json:"depth,omitempty" jsonschema:"Search depth: fast (snippets only, ~2s), deep (structured answer with headings + more sources, ~8s). Default: balanced"`
	Source    string `json:"source,omitempty" jsonschema:"Search source: web (default), academic (arXiv + Scholar + PubMed), news (Google News), code (GitHub + StackOverflow + HackerNews), reddit"`
}

type RawSearchInput struct {
	Query     string `json:"query" jsonschema:"Search query"`
	Language  string `json:"language,omitempty" jsonschema:"Language code (default: all)"`
	TimeRange string `json:"time_range,omitempty" jsonschema:"Time filter: day, month, year"`
}

type URLReadInput struct {
	URL       string `json:"url" jsonschema:"URL to fetch"`
	MaxLength int    `json:"max_length,omitempty" jsonschema:"Max characters (default: 10000)"`
}

type WPDevSearchInput struct {
	Query     string `json:"query" jsonschema:"WordPress development search query"`
	Language  string `json:"language,omitempty" jsonschema:"Language code (default: all)"`
	TimeRange string `json:"time_range,omitempty" jsonschema:"Time filter: day, month, year"`
}

// --- Domain-specific output types (go-job only) ---

type RawSearchOutput struct {
	Query   string          `json:"query"`
	Total   int             `json:"total"`
	Results []RawResultItem `json:"results"`
}

type RawResultItem struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	URL         string  `json:"url"`
	Score       float64 `json:"score"`
}

type URLReadOutput struct {
	URL       string `json:"url"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}
