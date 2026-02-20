package engine

// --- GitHub repo search types ---

type GithubRepoSearchInput struct {
	Query    string `json:"query" jsonschema:"Search query to find GitHub repositories. Plain natural language works, but you can also use GitHub search syntax for precision: 'language:go topic:ai-agent', 'user:owner stars:>100', 'topic:react language:typescript'. Examples: 'golang MCP server', 'language:go topic:ai stars:>500'"`
	Language string `json:"language,omitempty" jsonschema:"Language code (default: all)"`
	Sort     string `json:"sort,omitempty" jsonschema:"Sort repositories by field: stars, forks, updated (default: best match)"`
}

// --- Repo analyze types ---

type RepoAnalyzeInput struct {
	Repo        string   `json:"repo" jsonschema:"GitHub repository (owner/repo or full URL)"`
	Query       string   `json:"query" jsonschema:"What to find (e.g. WebSocket upgrade handler, auth middleware)"`
	Repos       []string `json:"repos,omitempty" jsonschema:"Multiple repos for quick mode (owner/repo). If empty, uses repo field"`
	Mode        string   `json:"mode,omitempty" jsonschema:"Mode: quick (Code Search API, fast), raw (code fragments only, no LLM). Default: deep clone+analyze"`
	Type        string   `json:"type,omitempty" jsonschema:"Search type: code (default), pr (pull requests), issue (issues)"`
	Pattern     string   `json:"pattern,omitempty" jsonschema:"File glob to include (e.g. *.go, *.ts). Default: all files"`
	Language    string   `json:"language,omitempty" jsonschema:"Language code for the answer (default: all)"`
	MaxModules  int      `json:"max_modules,omitempty" jsonschema:"Max modules to return (default: 10)"`
	IncludeTree bool     `json:"include_tree,omitempty" jsonschema:"Include directory tree in response (default: false)"`
}

type CodeModule struct {
	FilePath    string `json:"file_path"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CodeSnippet string `json:"code_snippet,omitempty"`
}

// IssueItem represents a GitHub issue or pull request from search results.
type IssueItem struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	URL       string   `json:"url"`
	State     string   `json:"state"`             // "open", "closed"
	Author    string   `json:"author"`
	Labels    []string `json:"labels,omitempty"`
	Body      string   `json:"body,omitempty"`
	Comments  int      `json:"comments"`
	CreatedAt string   `json:"created_at"`
	MergedAt  string   `json:"merged_at,omitempty"` // non-empty for merged PRs
	Repo      string   `json:"repo"`
}

type RepoAnalyzeOutput struct {
	Repo    string       `json:"repo"`
	Query   string       `json:"query"`
	Modules []CodeModule `json:"modules"`
	Issues  []IssueItem  `json:"issues,omitempty"`
	Summary string       `json:"summary"`
	Tree    string       `json:"tree,omitempty"`
}
