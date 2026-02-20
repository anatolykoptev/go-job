package sources

import (
	"github.com/anatolykoptev/go_job/internal/engine"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	hfAPIModels   = "https://huggingface.co/api/models"
	hfAPIDatasets = "https://huggingface.co/api/datasets"
)

// hfAPIModel is the raw HuggingFace API model response.
type hfAPIModel struct {
	ID           string    `json:"id"`
	Author       string    `json:"author"`
	Likes        int       `json:"likes"`
	Downloads    int       `json:"downloads"`
	PipelineTag  string    `json:"pipeline_tag"`
	Tags         []string  `json:"tags"`
	LibraryName  string    `json:"library_name"`
	Gated        any       `json:"gated"` // bool or string ("auto")
	LastModified time.Time `json:"lastModified"`
}

func (m *hfAPIModel) isGated() bool {
	switch v := m.Gated.(type) {
	case bool:
		return v
	case string:
		return v != "" && v != "false"
	}
	return false
}

// hfAPIDataset is the raw HuggingFace API dataset response.
type hfAPIDataset struct {
	ID           string    `json:"id"`
	Author       string    `json:"author"`
	Likes        int       `json:"likes"`
	Downloads    int       `json:"downloads"`
	Tags         []string  `json:"tags"`
	LastModified time.Time `json:"lastModified"`
}

// hfSortParam converts user-facing sort name to HF API sort parameter.
func hfSortParam(s string) string {
	switch strings.ToLower(s) {
	case "likes":
		return "likes"
	case "trending", "trendingScore":
		return "trendingScore"
	case "updated", "lastmodified":
		return "lastModified"
	default:
		return "downloads"
	}
}

// hfTaskKeywords maps query keywords to HuggingFace pipeline tags.
var hfTaskKeywords = map[string]string{
	"text-to-speech":      "text-to-speech",
	"text to speech":      "text-to-speech",
	"tts":                 "text-to-speech",
	"speech synthesis":    "text-to-speech",
	"voice synthesis":     "text-to-speech",
	"asr":                 "automatic-speech-recognition",
	"speech recognition":  "automatic-speech-recognition",
	"speech to text":      "automatic-speech-recognition",
	"whisper":             "automatic-speech-recognition",
	"transcription":       "automatic-speech-recognition",
	"image generation":    "text-to-image",
	"text to image":       "text-to-image",
	"text-to-image":       "text-to-image",
	"image synthesis":     "text-to-image",
	"stable diffusion":    "text-to-image",
	"diffusion model":     "text-to-image",
	"image classification": "image-classification",
	"object detection":    "object-detection",
	"sentiment analysis":  "text-classification",
	"text classification": "text-classification",
	"named entity":        "token-classification",
	"ner":                 "token-classification",
	"translation":         "translation",
	"summarization":       "summarization",
	"summarize":           "summarization",
	"question answering":  "question-answering",
	"qa model":            "question-answering",
	"fill mask":           "fill-mask",
	"masked language":     "fill-mask",
	"embedding":           "feature-extraction",
	"sentence embedding":  "feature-extraction",
	"text embedding":      "feature-extraction",
	"semantic search":     "feature-extraction",
	"image segmentation":  "image-segmentation",
	"depth estimation":    "depth-estimation",
	"image to text":       "image-to-text",
	"image captioning":    "image-to-text",
	"visual question":     "visual-question-answering",
	"vqa":                 "visual-question-answering",
	"zero-shot":           "zero-shot-classification",
	"zero shot":           "zero-shot-classification",
	"reinforcement learning": "reinforcement-learning",
	"rl model":            "reinforcement-learning",
	"video classification": "video-classification",
	"audio classification": "audio-classification",
	"music generation":    "text-to-audio",
	"audio generation":    "text-to-audio",
	"sound generation":    "text-to-audio",
}

// detectHFPipelineTag tries to infer a HuggingFace pipeline_tag from a free-text query.
func detectHFPipelineTag(query string) string {
	q := strings.ToLower(query)
	for kw, tag := range hfTaskKeywords {
		if strings.Contains(q, kw) {
			return tag
		}
	}
	return ""
}

// buildHFModelsURL constructs a HF models API URL from parameters.
func buildHFModelsURL(search, task, library, sort string, limit int) string {
	u, _ := url.Parse(hfAPIModels)
	q := u.Query()
	if search != "" {
		q.Set("search", search)
	}
	q.Set("sort", sort)
	q.Set("limit", fmt.Sprintf("%d", limit))
	q.Set("full", "false")
	if task != "" {
		q.Set("pipeline_tag", task)
	}
	if library != "" {
		q.Set("library", library)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// hfLimit normalises the requested limit.
func hfLimit(n int) int {
	if n <= 0 {
		return 20
	}
	if n > 50 {
		return 50
	}
	return n
}

// hfAuthor extracts the author part from a model/dataset ID.
func hfAuthor(id, author string) string {
	if author != "" {
		return author
	}
	if parts := strings.SplitN(id, "/", 2); len(parts) == 2 {
		return parts[0]
	}
	return ""
}

// fetchHFJSON performs a GET request to the HF API and decodes JSON into dst.
func fetchHFJSON(ctx context.Context, rawURL string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", engine.UserAgentBot)
	if engine.Cfg.HuggingFaceToken != "" {
		req.Header.Set("Authorization", "Bearer "+engine.Cfg.HuggingFaceToken)
	}

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req)
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HuggingFace API status %d for %s", resp.StatusCode, rawURL)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// FetchHFModelCard fetches the README (model card) for a HuggingFace model.
func FetchHFModelCard(ctx context.Context, modelID string) (string, error) {
	u := fmt.Sprintf("https://huggingface.co/%s/resolve/main/README.md", modelID)
	return engine.FetchRawContent(ctx, u)
}

// rawToHFModel converts a hfAPIModel to engine.HFModel.
func rawToHFModel(m hfAPIModel) engine.HFModel {
	updatedAt := ""
	if !m.LastModified.IsZero() {
		updatedAt = m.LastModified.UTC().Format("2006-01-02")
	}
	return engine.HFModel{
		ID:        m.ID,
		Author:    hfAuthor(m.ID, m.Author),
		Task:      m.PipelineTag,
		URL:       "https://huggingface.co/" + m.ID,
		Likes:     m.Likes,
		Downloads: m.Downloads,
		Tags:      filterHFTags(m.Tags),
		Library:   m.LibraryName,
		Gated:     m.isGated(),
		UpdatedAt: updatedAt,
	}
}

// SearchHuggingFace queries the HuggingFace models API and returns structured results.
// Strategy: detect pipeline_tag from query, run parallel requests (by task + by text search),
// merge and deduplicate results sorted by downloads.
func SearchHuggingFace(ctx context.Context, input engine.HFModelSearchInput) ([]engine.HFModel, error) {
	sort := hfSortParam(input.Sort)
	limit := hfLimit(input.Limit)

	// Resolve task: explicit > detected from query
	task := input.Task
	if task == "" {
		task = detectHFPipelineTag(input.Query)
	}

	type result struct {
		models []hfAPIModel
		err    error
	}

	fetch := func(url string) result {
		var raw []hfAPIModel
		err := fetchHFJSON(ctx, url, &raw)
		return result{raw, err}
	}

	var wg sync.WaitGroup
	results := make([]result, 0, 2)
	var mu sync.Mutex

	if task != "" {
		// Request 1: by pipeline_tag (sorted by downloads — gives popular models)
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := fetch(buildHFModelsURL("", task, input.Library, sort, limit))
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}()
	}

	if input.Query != "" {
		// Request 2: text search (finds models matching query by name/description)
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := fetch(buildHFModelsURL(input.Query, task, input.Library, sort, limit))
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}()
	}

	wg.Wait()

	// Merge and deduplicate
	seen := make(map[string]struct{})
	var merged []engine.HFModel
	for _, res := range results {
		if res.err != nil {
			slog.Warn("huggingface: API request failed", slog.Any("err", res.err))
			continue
		}
		for _, m := range res.models {
			if _, dup := seen[m.ID]; dup {
				continue
			}
			seen[m.ID] = struct{}{}
			merged = append(merged, rawToHFModel(m))
		}
	}

	// Sort merged by downloads descending
	for i := 1; i < len(merged); i++ {
		for j := i; j > 0 && merged[j].Downloads > merged[j-1].Downloads; j-- {
			merged[j], merged[j-1] = merged[j-1], merged[j]
		}
	}
	if len(merged) > limit {
		merged = merged[:limit]
	}

	if len(merged) == 0 {
		return nil, fmt.Errorf("huggingface: no models found for query %q", input.Query)
	}

	slog.Debug("huggingface: model search complete",
		slog.String("query", input.Query),
		slog.String("task", task),
		slog.Int("results", len(merged)))
	return merged, nil
}

// SearchHuggingFaceDatasets queries the HuggingFace datasets API.
func SearchHuggingFaceDatasets(ctx context.Context, input engine.HFDatasetSearchInput) ([]engine.HFDataset, error) {
	u, err := url.Parse(hfAPIDatasets)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if input.Query != "" {
		q.Set("search", input.Query)
	}
	q.Set("sort", hfSortParam(input.Sort))
	q.Set("limit", fmt.Sprintf("%d", hfLimit(input.Limit)))
	q.Set("full", "false")
	u.RawQuery = q.Encode()

	var raw []hfAPIDataset
	if err := fetchHFJSON(ctx, u.String(), &raw); err != nil {
		return nil, err
	}

	datasets := make([]engine.HFDataset, 0, len(raw))
	for _, d := range raw {
		updatedAt := ""
		if !d.LastModified.IsZero() {
			updatedAt = d.LastModified.UTC().Format("2006-01-02")
		}
		datasets = append(datasets, engine.HFDataset{
			ID:        d.ID,
			Author:    hfAuthor(d.ID, d.Author),
			URL:       "https://huggingface.co/datasets/" + d.ID,
			Likes:     d.Likes,
			Downloads: d.Downloads,
			Tags:      filterHFTags(d.Tags),
			UpdatedAt: updatedAt,
		})
	}

	slog.Debug("huggingface: dataset search complete", slog.String("query", input.Query), slog.Int("results", len(datasets)))
	return datasets, nil
}

// filterHFTags removes low-signal HF infrastructure tags and returns the most informative ones.
func filterHFTags(tags []string) []string {
	var out []string
	skipPrefixes := []string{
		"arxiv:", "license:", "doi:", "region:",
		"base_model:", "dataset:", "model-index",
	}
	skipExact := map[string]bool{
		"transformers": true, "pytorch": true, "safetensors": true,
		"autotrain_compatible": true, "text-generation-inference": true,
		"endpoints_compatible": true, "has_space": true,
	}
	for _, t := range tags {
		low := strings.ToLower(t)
		if skipExact[low] {
			continue
		}
		skip := false
		for _, p := range skipPrefixes {
			if strings.HasPrefix(low, p) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, t)
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}

// engine.HFModelSnippet builds a short content string from an engine.HFModel for use as a engine.SearxngResult snippet.
func HFModelSnippet(m engine.HFModel) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Task: %s | Likes: %d | Downloads: %d | Library: %s", m.Task, m.Likes, m.Downloads, m.Library)
	if len(m.Tags) > 0 {
		fmt.Fprintf(&sb, " | Tags: %s", strings.Join(m.Tags, ", "))
	}
	if m.Gated {
		sb.WriteString(" | gated")
	}
	return sb.String()
}

// SummarizeHFResults fetches model cards for top models and summarizes with LLM.
func SummarizeHFResults(ctx context.Context, query string, models []engine.HFModel) (engine.HFModelSearchOutput, error) {
	if len(models) == 0 {
		return engine.HFModelSearchOutput{Query: query, Summary: "No models found."}, nil
	}

	// Parallel fetch model cards for top 3 models.
	cardLimit := 3
	if len(models) < cardLimit {
		cardLimit = len(models)
	}

	cards := make([]string, cardLimit)
	var wg sync.WaitGroup
	for i := 0; i < cardLimit; i++ {
		wg.Add(1)
		go func(idx int, modelID string) {
			defer wg.Done()
			card, err := FetchHFModelCard(ctx, modelID)
			if err != nil {
				slog.Debug("hf: model card fetch failed", slog.String("model", modelID), slog.Any("error", err))
				return
			}
			if len(card) > 4000 {
				card = card[:4000] + "..."
			}
			cards[idx] = card
		}(i, models[i].ID)
	}
	wg.Wait()

	// Build engine.SearxngResult list for engine.SummarizeWithInstruction.
	results := make([]engine.SearxngResult, len(models))
	contents := make(map[string]string)

	for i, m := range models {
		var sb strings.Builder
		fmt.Fprintf(&sb, "Task: %s | Likes: %d | Downloads: %d | Library: %s", m.Task, m.Likes, m.Downloads, m.Library)
		if len(m.Tags) > 0 {
			fmt.Fprintf(&sb, "\nTags: %s", strings.Join(m.Tags, ", "))
		}
		if m.Gated {
			sb.WriteString("\nAccess: gated (requires HuggingFace auth)")
		}
		if m.UpdatedAt != "" {
			fmt.Fprintf(&sb, "\nUpdated: %s", m.UpdatedAt)
		}
		results[i] = engine.SearxngResult{
			Title:   m.ID,
			URL:     m.URL,
			Content: sb.String(),
		}
		if i < cardLimit && cards[i] != "" {
			contents[m.URL] = cards[i]
		}
	}

	llmOut, err := engine.SummarizeWithInstruction(ctx, query, engine.HFModelSearchInstruction, engine.Cfg.MaxContentChars, results, contents)
	if err != nil {
		return engine.HFModelSearchOutput{Query: query, Models: models, Summary: "LLM summarization failed: " + err.Error()}, nil
	}

	return engine.HFModelSearchOutput{Query: query, Models: models, Summary: llmOut.Answer}, nil
}

// SummarizeHFDatasets summarizes dataset search results with LLM.
func SummarizeHFDatasets(ctx context.Context, query string, datasets []engine.HFDataset) (engine.HFDatasetSearchOutput, error) {
	if len(datasets) == 0 {
		return engine.HFDatasetSearchOutput{Query: query, Summary: "No datasets found."}, nil
	}

	results := make([]engine.SearxngResult, len(datasets))
	for i, d := range datasets {
		var sb strings.Builder
		fmt.Fprintf(&sb, "Likes: %d | Downloads: %d", d.Likes, d.Downloads)
		if len(d.Tags) > 0 {
			fmt.Fprintf(&sb, "\nTags: %s", strings.Join(d.Tags, ", "))
		}
		if d.UpdatedAt != "" {
			fmt.Fprintf(&sb, "\nUpdated: %s", d.UpdatedAt)
		}
		results[i] = engine.SearxngResult{
			Title:   d.ID,
			URL:     d.URL,
			Content: sb.String(),
		}
	}

	llmOut, err := engine.SummarizeWithInstruction(ctx, query, engine.HFDatasetSearchInstruction, engine.Cfg.MaxContentChars, results, nil)
	if err != nil {
		return engine.HFDatasetSearchOutput{Query: query, Datasets: datasets, Summary: "LLM summarization failed: " + err.Error()}, nil
	}

	return engine.HFDatasetSearchOutput{Query: query, Datasets: datasets, Summary: llmOut.Answer}, nil
}
