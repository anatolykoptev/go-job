package jobs

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strconv"
)

const (
	defaultTopK       = 10
	maxTopK           = 30
	defaultMemoryType = "note"

	// expectedEmbedDim is the expected dimension for resume_vectors.embedding.
	// Vectors from a model with a different dimension are stored as NULL (FTS fallback).
	expectedEmbedDim = 1024

	// backendFTS / backendVector are metric label values for the backend dimension.
	backendFTS    = "fts"
	backendVector = "vector"
)

// --- Search ---

// ResumeMemoryItem is a single result from a resume_memory search.
type ResumeMemoryItem struct {
	Content  string  `json:"content"`
	Score    float64 `json:"score"`
	Type     string  `json:"type,omitempty"`
	ID       int     `json:"id,omitempty"`
	MemoryID string  `json:"memory_id"`
}

// ResumeMemorySearchResult is the output of resume_memory op=search.
type ResumeMemorySearchResult struct {
	Query   string             `json:"query"`
	Results []ResumeMemoryItem `json:"results"`
	Total   int                `json:"total"`
}

// SearchResumeMemory queries resume_vectors for resume-related memories.
// Uses pgvector cosine search when an embedder is configured and the embedding
// column exists; falls back to tsvector FTS when either is absent.
func SearchResumeMemory(ctx context.Context, query string, topK int) (*ResumeMemorySearchResult, error) {
	db := GetResumeDB()
	if db == nil {
		return nil, errors.New("resume DB not configured (set DATABASE_URL)")
	}

	if topK <= 0 {
		topK = defaultTopK
	}
	if topK > maxTopK {
		topK = maxTopK
	}

	var rows []VectorRow
	backend := backendFTS

	ec := GetEmbedClient()
	if ec != nil && db.HasEmbedding() {
		qvec, err := ec.EmbedQuery(ctx, "query: "+query)
		switch {
		case err != nil:
			slog.Warn("resume_memory: embed query failed, using FTS", slog.Any("error", err))
			resumeEmbedFailuresTotal.Inc()
		case len(qvec) != expectedEmbedDim:
			slog.Warn("resume_memory: embed query dim mismatch, using FTS",
				slog.Int("got", len(qvec)), slog.Int("want", expectedEmbedDim))
			resumeEmbedFailuresTotal.Inc()
		case containsNonFinite(qvec):
			slog.Warn("resume_memory: embed returned non-finite query vector, using FTS")
			resumeEmbedFailuresTotal.Inc()
		default:
			rows, err = db.SearchByVector(ctx, qvec, topK)
			if err != nil {
				return nil, err
			}
			backend = backendVector
		}
	}

	if backend == backendFTS {
		var err error
		rows, err = db.SearchByText(ctx, query, topK)
		if err != nil {
			return nil, err
		}
	}

	resumeMemoryOpsTotal.WithLabelValues("search", backend).Inc()

	items := make([]ResumeMemoryItem, len(rows))
	for i, r := range rows {
		refID := 0
		if r.RefID != nil {
			refID = int(*r.RefID)
		}
		items[i] = ResumeMemoryItem{
			Content:  r.Content,
			Score:    r.Score,
			Type:     r.MemType,
			ID:       refID,
			MemoryID: strconv.FormatInt(r.ID, 10),
		}
	}

	return &ResumeMemorySearchResult{
		Query:   query,
		Results: items,
		Total:   len(items),
	}, nil
}

// --- Add ---

// ResumeMemoryAddResult is the output of resume_memory op=add.
type ResumeMemoryAddResult struct {
	Status string `json:"status"`
	Type   string `json:"type"`
}

// AddResumeMemory stores a new free-text memory in resume_vectors.
// Embeds the content when an embedder is configured and the embedding column exists;
// stores FTS-only (embedding=NULL) otherwise.
func AddResumeMemory(ctx context.Context, content, memType string) (*ResumeMemoryAddResult, error) {
	db := GetResumeDB()
	if db == nil {
		return nil, errors.New("resume DB not configured (set DATABASE_URL)")
	}

	if memType == "" {
		memType = defaultMemoryType
	}

	embedding, backend := embedPassage(ctx, db, content, "resume_memory add")

	if _, err := db.UpsertVector(ctx, content, memType, nil, embedding); err != nil {
		return nil, err
	}

	resumeMemoryOpsTotal.WithLabelValues("add", backend).Inc()

	return &ResumeMemoryAddResult{
		Status: "stored",
		Type:   memType,
	}, nil
}

// --- Update ---

// ResumeMemoryUpdateResult is the output of resume_memory op=update.
type ResumeMemoryUpdateResult struct {
	MemoryID string `json:"memory_id"`
	Updated  bool   `json:"updated"`
}

// UpdateResumeMemory replaces the content (and re-embeds) an existing memory by its row id.
// Unlike the old MemDB delete+re-add, this is an atomic UPDATE preserving the row id —
// so a cached memory_id stays valid after the update.
func UpdateResumeMemory(ctx context.Context, memoryID, content string) (*ResumeMemoryUpdateResult, error) {
	db := GetResumeDB()
	if db == nil {
		return nil, errors.New("resume DB not configured (set DATABASE_URL)")
	}

	id, err := strconv.ParseInt(memoryID, 10, 64)
	if err != nil {
		return nil, errors.New("invalid memory_id: must be a numeric string")
	}

	// Fetch existing row's mem_type and ref_id to correctly recompute the content_hash.
	memType, refID, err := db.FetchVectorMeta(ctx, id)
	if err != nil {
		return nil, err
	}

	newHash := vectorContentHash(resumeVectorUser, memType, refID, content)

	embedding, backend := embedPassage(ctx, db, content, "resume_memory update")

	if err := db.UpdateVector(ctx, id, content, newHash, embedding); err != nil {
		return nil, err
	}

	resumeMemoryOpsTotal.WithLabelValues("update", backend).Inc()

	return &ResumeMemoryUpdateResult{
		MemoryID: memoryID,
		Updated:  true,
	}, nil
}

// --- Private helpers ---

// containsNonFinite reports whether any component of v is NaN or infinite.
// pgvector rejects such vectors at INSERT/UPDATE time; we detect them early and
// fall back to FTS storage instead of propagating a DB error.
func containsNonFinite(v []float32) bool {
	for _, f := range v {
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return true
		}
	}
	return false
}

// embedPassage embeds a single passage text and returns the vector + backend label.
// Returns (nil, backendFTS) when the embedder is absent, encounters an error, or returns
// a vector of the wrong dimension; bumps resumeEmbedFailuresTotal on error/mismatch.
// op is a short label used in warning log messages.
func embedPassage(ctx context.Context, db *ResumeDB, content, op string) ([]float32, string) {
	ec := GetEmbedClient()
	if ec == nil || !db.HasEmbedding() {
		return nil, backendFTS
	}

	vecs, err := ec.Embed(ctx, []string{"passage: " + content})
	switch {
	case err != nil:
		slog.Warn(op+": embed failed, storing FTS-only", slog.Any("error", err))
		resumeEmbedFailuresTotal.Inc()
		return nil, backendFTS
	case len(vecs) == 0 || len(vecs[0]) == 0:
		return nil, backendFTS
	case len(vecs[0]) != expectedEmbedDim:
		slog.Warn(op+": embed dim mismatch, storing FTS-only",
			slog.Int("got", len(vecs[0])),
			slog.Int("want", expectedEmbedDim))
		resumeEmbedFailuresTotal.Inc()
		return nil, backendFTS
	case containsNonFinite(vecs[0]):
		slog.Warn(op+": embed returned non-finite vector, storing FTS-only")
		resumeEmbedFailuresTotal.Inc()
		return nil, backendFTS
	default:
		return vecs[0], backendVector
	}
}
