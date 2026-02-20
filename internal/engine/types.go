package engine

// --- Core search types ---

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

// --- Output types (JSON responses) ---

type SourceItem struct {
	Index   int    `json:"index"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// FactItem is a single verified fact with explicit source indices.
type FactItem struct {
	Point   string `json:"point"`   // complete sentence, no markdown
	Sources []int  `json:"sources"` // 1-based indices into Sources array
}

type SmartSearchOutput struct {
	Query   string       `json:"query"`
	Answer  string       `json:"answer"`  // 2-3 sentence plain text summary, no markdown
	Facts   []FactItem   `json:"facts"`   // key facts with explicit source indices
	Sources []SourceItem `json:"sources"`
}

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

// --- Output formatting ---

// OutputOpts controls the size and shape of SmartSearchOutput.
type OutputOpts struct {
	MaxAnswerChars  int  // truncate LLM answer (0 = no limit)
	MaxSources      int  // max sources in output (0 = all)
	IncludeSnippets bool // include snippet text in sources
}

// --- Internal types ---

type SearxngResult struct {
	Title   string  `json:"title"`
	Content string  `json:"content"`
	URL     string  `json:"url"`
	Score   float64 `json:"score"`
}

type searxngResponse struct {
	Results []SearxngResult `json:"results"`
}
