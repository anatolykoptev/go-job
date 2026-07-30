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
	// backendFTSFallback labels the case where the vector path succeeded but
	// returned zero rows and the FTS path was used instead — a third state
	// distinguishable from a plain vector answer (had rows) and a plain FTS
	// answer (no embedder / embed failed). Feeds resumeMemoryOpsTotal.
	backendFTSFallback = "fts_fallback"
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

	rows, backend, err := embedOrFTS(ctx, db, query, "resume_memory",
		func(qvec []float32) ([]VectorRow, error) { return db.SearchByVector(ctx, qvec, topK) },
		func() ([]VectorRow, error) { return db.SearchByText(ctx, query, topK) },
	)
	if err != nil {
		return nil, err
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

	if _, err := db.UpsertVector(ctx, content, memType, embedding); err != nil {
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
// This is an atomic UPDATE preserving the row id —
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

// --- Scoped search (for consumers that need type-filtered results) ---

// searchVectorsScoped searches resume_vectors for rows whose mem_type is in
// memTypes, using vector search when the embedder is available and falling back
// to FTS otherwise.  minScore is the cosine-similarity floor applied to the
// vector path only; the FTS fallback intentionally ignores it because ts_rank
// is not numerically comparable to cosine similarity.
func searchVectorsScoped(ctx context.Context, db *ResumeDB, query string, topK int, minScore float64, memTypes []string) ([]VectorRow, error) {
	rows, _, err := embedOrFTS(ctx, db, query, "resume_vectors_scoped",
		func(qvec []float32) ([]VectorRow, error) {
			return db.SearchByVectorScoped(ctx, qvec, topK, minScore, memTypes)
		},
		func() ([]VectorRow, error) {
			// minScore not applied on the FTS path: ts_rank and cosine similarity
			// are incomparable scales, so a numeric floor here would be misleading.
			return db.SearchByTextScoped(ctx, query, topK, memTypes)
		},
	)
	return rows, err
}

// --- Private helpers ---

// embedOrFTS embeds query and dispatches to vecFn (vector path) or txtFn (FTS fallback).
// It centralises the embed guard (absent embedder, HasEmbedding false, dim mismatch,
// non-finite vector) and bumps resumeEmbedFailuresTotal on every failure before falling
// back to FTS.  op is the caller name used in warning log messages.
// Returns the chosen backend label (backendVector or backendFTS) so callers can
// record per-backend metrics when needed.
func embedOrFTS(
	ctx context.Context,
	db *ResumeDB,
	query, op string,
	vecFn func(qvec []float32) ([]VectorRow, error),
	txtFn func() ([]VectorRow, error),
) ([]VectorRow, string, error) {
	ec := GetEmbedClient()
	if ec != nil && db.HasEmbedding() {
		qvec, err := ec.EmbedQuery(ctx, "query: "+query)
		switch {
		case err != nil:
			slog.Warn(op+": embed query failed, using FTS", slog.Any("error", err))
			resumeEmbedFailuresTotal.Inc()
		case len(qvec) != expectedEmbedDim:
			slog.Warn(op+": embed dim mismatch, using FTS",
				slog.Int("got", len(qvec)), slog.Int("want", expectedEmbedDim))
			resumeEmbedFailuresTotal.Inc()
		case containsNonFinite(qvec):
			slog.Warn(op + ": embed returned non-finite vector, using FTS")
			resumeEmbedFailuresTotal.Inc()
		default:
			rows, err := vecFn(qvec)
			if err != nil {
				return nil, backendVector, err
			}
			if len(rows) > 0 {
				// Vector path has results — return them verbatim. No re-ranking,
				// no merge with FTS: vector wins whenever it has anything.
				return rows, backendVector, nil
			}
			// Vector path succeeded but returned zero rows. Fall back to FTS so
			// an empty vector index (e.g. all embeddings NULL right after
			// migration 005) does not silently zero out results that
			// plainto_tsquery would match. Labelled fts_fallback to keep the
			// signal distinguishable from a plain vector or plain FTS answer.
			ftsRows, ftsErr := txtFn()
			return ftsRows, backendFTSFallback, ftsErr
		}
	}
	rows, err := txtFn()
	return rows, backendFTS, err
}

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
		slog.Warn(op + ": embed returned non-finite vector, storing FTS-only")
		resumeEmbedFailuresTotal.Inc()
		return nil, backendFTS
	default:
		return vecs[0], backendVector
	}
}
