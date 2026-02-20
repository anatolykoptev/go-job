package engine

// --- HackerNews types ---

// HNSearchInput is the input for the hn_search tool.
type HNSearchInput struct {
	Query     string `json:"query" jsonschema:"Search query for HackerNews discussions"`
	Language  string `json:"language,omitempty" jsonschema:"Language code for the answer (default: all)"`
	TimeRange string `json:"time_range,omitempty" jsonschema:"Time filter: day, week, month, year"`
}

// HNResult represents a single HackerNews result.
type HNResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Author      string `json:"author"`
	Points      int    `json:"points"`
	NumComments int    `json:"num_comments"`
	CreatedAt   string `json:"created_at"`
	Snippet     string `json:"snippet,omitempty"`
	ObjectID    string `json:"-"` // internal: HN story ID for comment fetching
}

// HNSearchOutput is the structured output for hn_search.
type HNSearchOutput struct {
	Query   string     `json:"query"`
	Results []HNResult `json:"results"`
	Summary string     `json:"summary"`
}

// --- HuggingFace types ---

// HFModelSearchInput is the input for the hf_model_search tool.
type HFModelSearchInput struct {
	Query    string `json:"query" jsonschema:"Search query (e.g. 'Russian speech recognition', 'small vision language model', 'image segmentation')"`
	Task     string `json:"task,omitempty" jsonschema:"Filter by task: text-generation, automatic-speech-recognition, image-classification, text-to-image, image-to-text, text2text-generation, token-classification, sentence-similarity, etc."`
	Library  string `json:"library,omitempty" jsonschema:"Filter by library: transformers, diffusers, gguf, llama.cpp, timm, sentence-transformers, etc. Use 'gguf' to find quantized models for local inference."`
	Sort     string `json:"sort,omitempty" jsonschema:"Sort by: trending (default), likes, downloads, updated"`
	Limit    int    `json:"limit,omitempty" jsonschema:"Max results (default: 20, max: 50)"`
	Language string `json:"language,omitempty" jsonschema:"Language code for the answer (default: all)"`
}

// HFDatasetSearchInput is the input for the hf_dataset_search tool.
type HFDatasetSearchInput struct {
	Query    string `json:"query" jsonschema:"Search query (e.g. 'Russian speech dataset', 'instruction tuning', 'code generation')"`
	Sort     string `json:"sort,omitempty" jsonschema:"Sort by: trending (default), likes, downloads, updated"`
	Limit    int    `json:"limit,omitempty" jsonschema:"Max results (default: 20, max: 50)"`
	Language string `json:"language,omitempty" jsonschema:"Language code for the answer (default: all)"`
}

// HFDataset is a structured representation of a HuggingFace dataset.
type HFDataset struct {
	ID        string   `json:"id"`
	Author    string   `json:"author"`
	URL       string   `json:"url"`
	Likes     int      `json:"likes"`
	Downloads int      `json:"downloads"`
	Tags      []string `json:"tags,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

// HFDatasetSearchOutput is the structured output for hf_dataset_search.
type HFDatasetSearchOutput struct {
	Query    string      `json:"query"`
	Datasets []HFDataset `json:"datasets"`
	Summary  string      `json:"summary"`
}

// HFModel is a structured representation of a HuggingFace model.
type HFModel struct {
	ID        string   `json:"id"`
	Author    string   `json:"author"`
	Task      string   `json:"task,omitempty"`
	URL       string   `json:"url"`
	Likes     int      `json:"likes"`
	Downloads int      `json:"downloads"`
	Tags      []string `json:"tags,omitempty"`
	Library   string   `json:"library,omitempty"`
	Gated     bool     `json:"gated,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

// HFModelSearchOutput is the structured output for hf_model_search.
type HFModelSearchOutput struct {
	Query   string    `json:"query"`
	Models  []HFModel `json:"models"`
	Summary string    `json:"summary"`
}

// --- YouTube types ---

type YouTubeSearchInput struct {
	Query    string `json:"query" jsonschema:"Search query"`
	Language string `json:"language,omitempty" jsonschema:"Transcript language code (default: en). Also used for search language."`
	Limit    int    `json:"limit,omitempty" jsonschema:"Max videos to fetch transcripts for (default: 3, max: 5)"`
}

type YouTubeVideo struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Snippet    string `json:"snippet,omitempty"`
	Transcript string `json:"transcript,omitempty"`
}

type YouTubeSearchOutput struct {
	Query   string         `json:"query"`
	Videos  []YouTubeVideo `json:"videos"`
	Summary string         `json:"summary"`
}
