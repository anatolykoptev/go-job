package jobs

import (
	"context"
	"log/slog"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// BountyWithVector holds a bounty and its precomputed embedding vector.
type BountyWithVector struct {
	Bounty engine.BountyListing `json:"bounty"`
	Vector []float32            `json:"vector"`
}

const algoraEmbedCacheKey = "algora_embed"

// SearchAlgoraWithEmbeddings returns bounties with precomputed embedding vectors.
// Vectors are cached alongside the scrape cache. On cache miss, embeds titles via embed-server.
func SearchAlgoraWithEmbeddings(ctx context.Context, limit int) ([]BountyWithVector, error) {
	// Try embedding cache first.
	if cached, ok := engine.CacheLoadJSON[[]BountyWithVector](ctx, algoraEmbedCacheKey); ok {
		slog.Debug("algora: using cached embeddings", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	// Fetch bounties (uses its own scrape cache).
	bounties, err := SearchAlgora(ctx, limit)
	if err != nil {
		return nil, err
	}
	if len(bounties) == 0 {
		return nil, nil
	}

	// Try to embed titles.
	client := GetEmbedClient()
	if client == nil {
		// No embed client — return bounties without vectors.
		result := make([]BountyWithVector, len(bounties))
		for i, b := range bounties {
			result[i] = BountyWithVector{Bounty: b}
		}
		return result, nil
	}

	// Build texts for embedding: org + title.
	texts := make([]string, len(bounties))
	for i, b := range bounties {
		texts[i] = b.Org + ": " + b.Title
	}

	vecs, err := client.EmbedPassages(ctx, texts)
	if err != nil {
		slog.Warn("algora: embedding failed, returning without vectors", slog.Any("error", err))
		result := make([]BountyWithVector, len(bounties))
		for i, b := range bounties {
			result[i] = BountyWithVector{Bounty: b}
		}
		return result, nil
	}

	result := make([]BountyWithVector, len(bounties))
	for i, b := range bounties {
		var vec []float32
		if i < len(vecs) {
			vec = vecs[i]
		}
		result[i] = BountyWithVector{Bounty: b, Vector: vec}
	}

	// Cache embeddings.
	engine.CacheStoreJSON(ctx, algoraEmbedCacheKey, "", result)
	slog.Debug("algora: cached embeddings", slog.Int("count", len(result)))

	return result, nil
}

const algoraEnrichedCacheKey = "algora_enriched"

// SearchAlgoraEnriched returns bounties fully enriched at cache time:
// GitHub status filtered, titles fixed, skills extracted, and embeddings computed.
// Query-time callers only need to embed the query and rank.
func SearchAlgoraEnriched(ctx context.Context, limit int) ([]BountyWithVector, error) {
	// 1. Check cache.
	if cached, ok := engine.CacheLoadJSON[[]BountyWithVector](ctx, algoraEnrichedCacheKey); ok {
		slog.Debug("algora: using cached enriched bounties", slog.Int("results", len(cached)))
		return cached, nil
	}

	// 2. Fetch raw bounties.
	bounties, err := SearchAlgora(ctx, limit)
	if err != nil {
		return nil, err
	}
	if len(bounties) == 0 {
		return nil, nil
	}

	// 3. Fetch GitHub issue info in batch (single API call per bounty, parallel).
	infoMap := fetchIssueInfoBatch(ctx, bounties)

	// 4. Filter closed, fix titles, extract skills.
	var enriched []engine.BountyListing
	for _, b := range bounties {
		info := infoMap[b.URL]
		if info.State == "closed" {
			continue
		}
		// Fix empty/noisy title from GitHub API.
		if b.Title == "" && info.Title != "" {
			b.Title = info.Title
		}
		// Extract skills from title + labels + repo language.
		labelText := strings.Join(info.Labels, " ")
		b.Skills = ExtractSkillsFromText(b.Title + " " + labelText)
		if info.Language != "" {
			b.Skills = MergeSkills(b.Skills, []string{info.Language})
		}
		enriched = append(enriched, b)
	}

	if len(enriched) == 0 {
		return nil, nil
	}

	// 5. Embed with skills included in text.
	client := GetEmbedClient()
	if client == nil {
		// No embed client — return enriched bounties without vectors.
		result := make([]BountyWithVector, len(enriched))
		for i, b := range enriched {
			result[i] = BountyWithVector{Bounty: b}
		}
		engine.CacheStoreJSON(ctx, algoraEnrichedCacheKey, "", result)
		return result, nil
	}

	texts := make([]string, len(enriched))
	for i, b := range enriched {
		t := b.Org + ": " + b.Title
		if len(b.Skills) > 0 {
			t += " [" + strings.Join(b.Skills, ", ") + "]"
		}
		texts[i] = t
	}

	vecs, err := client.EmbedPassages(ctx, texts)
	if err != nil {
		slog.Warn("algora: embedding failed during enrichment, returning without vectors", slog.Any("error", err))
		result := make([]BountyWithVector, len(enriched))
		for i, b := range enriched {
			result[i] = BountyWithVector{Bounty: b}
		}
		engine.CacheStoreJSON(ctx, algoraEnrichedCacheKey, "", result)
		return result, nil
	}

	// 6. Build result and cache.
	result := make([]BountyWithVector, len(enriched))
	for i, b := range enriched {
		var vec []float32
		if i < len(vecs) {
			vec = vecs[i]
		}
		result[i] = BountyWithVector{Bounty: b, Vector: vec}
	}

	engine.CacheStoreJSON(ctx, algoraEnrichedCacheKey, "", result)
	slog.Debug("algora: cached enriched bounties", slog.Int("count", len(result)))
	return result, nil
}
