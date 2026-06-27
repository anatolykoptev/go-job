package jobs

// resume_vectors.go — ResumeDB persistence methods for the resume_vectors table.
//
// Layering invariant (fitness function F1):
//   - These methods are pure SQL: they accept precomputed embeddings as []float32 parameters.
//   - No net/http import, no GetEmbedClient call, no EmbedQuery call lives here.
//   - Embedding generation happens in the engine ops layer (resume_memory.go).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// hasEmbeddingCol caches whether resume_vectors.embedding column exists.
// Set once at startup via DetectEmbeddingColumn; immutable after that.
var hasEmbeddingCol bool

// HasEmbedding reports whether the embedding column is present (pgvector migration 005 succeeded).
func (db *ResumeDB) HasEmbedding() bool { return hasEmbeddingCol }

// DetectEmbeddingColumn queries information_schema.columns to determine whether
// migration 005 created the embedding column. Must be called after ConnectResumeDB.
func (db *ResumeDB) DetectEmbeddingColumn(ctx context.Context) error {
	err := db.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM information_schema.columns
			WHERE table_schema='public'
			  AND table_name='resume_vectors'
			  AND column_name='embedding'
		)
	`).Scan(&hasEmbeddingCol)
	return err
}

// vectorContentHash computes sha256(user_name|mem_type|coalesce(ref_id,0)|content)
// used as the dedup key in the UNIQUE(user_name, content_hash) constraint.
func vectorContentHash(userName, memType string, refID *int64, content string) string {
	rid := int64(0)
	if refID != nil {
		rid = *refID
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s", userName, memType, rid, content)))
	return hex.EncodeToString(sum[:])
}

// vectorLiteral converts a float32 slice into the pgvector literal format: "[f1,f2,...]".
func vectorLiteral(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	b := make([]string, len(v))
	for i, f := range v {
		b[i] = strconv.FormatFloat(float64(f), 'f', -1, 32)
	}
	return "[" + strings.Join(b, ",") + "]"
}

// VectorRow is a result row from a vector or text search.
type VectorRow struct {
	ID      int64
	Content string
	MemType string
	RefID   *int64
	Score   float64
}

// UpsertVector inserts or updates a resume memory row.
// embedding may be nil or empty — in that case the row is stored without a vector (FTS-only).
// embedding dimension must be 1024 when provided; mismatched dims are silently ignored (FTS fallback).
func (db *ResumeDB) UpsertVector(
	ctx context.Context,
	content, memType string,
	refID *int64,
	embedding []float32,
) (int64, error) {
	hash := vectorContentHash(resumeVectorUser, memType, refID, content)

	useVec := len(embedding) == 1024 && hasEmbeddingCol
	var id int64
	if useVec {
		vec := vectorLiteral(embedding)
		err := db.pool.QueryRow(ctx, `
			INSERT INTO resume_vectors (user_name, content, mem_type, source, ref_id, content_hash, embedding)
			VALUES ($1, $2, $3, 'agent', $4, $5, $6::vector)
			ON CONFLICT (user_name, content_hash) DO UPDATE
			  SET content    = EXCLUDED.content,
			      embedding  = EXCLUDED.embedding,
			      updated_at = now()
			RETURNING id
		`, resumeVectorUser, content, memType, refID, hash, vec).Scan(&id)
		return id, err
	}

	err := db.pool.QueryRow(ctx, `
		INSERT INTO resume_vectors (user_name, content, mem_type, source, ref_id, content_hash)
		VALUES ($1, $2, $3, 'agent', $4, $5)
		ON CONFLICT (user_name, content_hash) DO UPDATE
		  SET content    = EXCLUDED.content,
		      updated_at = now()
		RETURNING id
	`, resumeVectorUser, content, memType, refID, hash).Scan(&id)
	return id, err
}

// minVectorSimilarity is the default cosine similarity floor for unscoped vector search.
// Results below this threshold are not meaningfully related and would inflate the FTS comparison.
// Scoped searches use a caller-supplied floor via SearchByVectorScoped.
const minVectorSimilarity = 0.5

// SearchByVectorScoped performs exact cosine-distance search via pgvector (<=>).
// Only rows with non-NULL embeddings are scanned.  Results whose similarity is
// below minScore are excluded.  When memTypes is nil the filter is skipped and
// all mem_types are returned.  Pass minVectorSimilarity and nil to get the same
// behaviour as the old unscoped SearchByVector.
func (db *ResumeDB) SearchByVectorScoped(ctx context.Context, qvec []float32, topK int, minScore float64, memTypes []string) ([]VectorRow, error) {
	vec := vectorLiteral(qvec)
	rows, err := db.pool.Query(ctx, `
		SELECT id, content, mem_type, ref_id,
		       1.0 - (embedding <=> $1::vector) AS score
		FROM resume_vectors
		WHERE user_name = $2
		  AND embedding IS NOT NULL
		  AND ($5::text[] IS NULL OR mem_type = ANY($5::text[]))
		  AND 1.0 - (embedding <=> $1::vector) >= $4
		ORDER BY embedding <=> $1::vector
		LIMIT $3
	`, vec, resumeVectorUser, topK, minScore, memTypes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVectorRows(rows)
}

// SearchByTextScoped performs GIN tsvector full-text search.
// When memTypes is nil the filter is skipped and all mem_types are returned.
// Pass nil to get the same behaviour as the old unscoped SearchByText.
func (db *ResumeDB) SearchByTextScoped(ctx context.Context, query string, topK int, memTypes []string) ([]VectorRow, error) {
	rows, err := db.pool.Query(ctx, `
		SELECT id, content, mem_type, ref_id,
		       ts_rank(tsv, plainto_tsquery('english', $1)) AS score
		FROM resume_vectors
		WHERE user_name = $2
		  AND ($4::text[] IS NULL OR mem_type = ANY($4::text[]))
		  AND tsv @@ plainto_tsquery('english', $1)
		ORDER BY score DESC
		LIMIT $3
	`, query, resumeVectorUser, topK, memTypes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVectorRows(rows)
}

// SearchByVector is an unscoped convenience wrapper (no mem_type filter, default
// minVectorSimilarity floor) that delegates to SearchByVectorScoped.
func (db *ResumeDB) SearchByVector(ctx context.Context, qvec []float32, topK int) ([]VectorRow, error) {
	return db.SearchByVectorScoped(ctx, qvec, topK, minVectorSimilarity, nil)
}

// SearchByText is an unscoped convenience wrapper (no mem_type filter) that
// delegates to SearchByTextScoped.
func (db *ResumeDB) SearchByText(ctx context.Context, query string, topK int) ([]VectorRow, error) {
	return db.SearchByTextScoped(ctx, query, topK, nil)
}

func scanVectorRows(rows pgx.Rows) ([]VectorRow, error) {
	var out []VectorRow
	for rows.Next() {
		var r VectorRow
		if err := rows.Scan(&r.ID, &r.Content, &r.MemType, &r.RefID, &r.Score); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FetchVectorMeta returns the mem_type and ref_id of an existing row by id.
// Used by UpdateResumeMemory to recompute the content_hash with the correct mem_type.
func (db *ResumeDB) FetchVectorMeta(ctx context.Context, id int64) (memType string, refID *int64, err error) {
	err = db.pool.QueryRow(ctx, `
		SELECT mem_type, ref_id
		FROM resume_vectors
		WHERE id = $1 AND user_name = $2
	`, id, resumeVectorUser).Scan(&memType, &refID)
	return
}

// ClearVectors deletes all resume_vectors rows for the current user whose
// mem_type matches any of the provided values. Only the caller's own mem_types
// are affected; other consumers' rows (including resume_memory "note" rows) are untouched.
func (db *ResumeDB) ClearVectors(ctx context.Context, memTypes ...string) error {
	_, err := db.pool.Exec(ctx, `
		DELETE FROM resume_vectors
		WHERE user_name = $1 AND mem_type = ANY($2::text[])
	`, resumeVectorUser, memTypes)
	return err
}

// CountVectors returns the number of resume_vectors rows for the current user
// whose mem_type matches any of the provided values.
func (db *ResumeDB) CountVectors(ctx context.Context, memTypes ...string) (int, error) {
	var n int
	err := db.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM resume_vectors
		WHERE user_name = $1 AND mem_type = ANY($2::text[])
	`, resumeVectorUser, memTypes).Scan(&n)
	return n, err
}

// UpdateVector atomically updates content, content_hash, and embedding for a row.
// embedding may be nil (FTS-only update).
func (db *ResumeDB) UpdateVector(
	ctx context.Context,
	id int64,
	content, contentHash string,
	embedding []float32,
) error {
	useVec := len(embedding) == 1024 && hasEmbeddingCol
	if useVec {
		vec := vectorLiteral(embedding)
		tag, err := db.pool.Exec(ctx, `
			UPDATE resume_vectors
			   SET content      = $2,
			       content_hash = $3,
			       embedding    = $4::vector,
			       updated_at   = now()
			WHERE id = $1 AND user_name = $5
		`, id, content, contentHash, vec, resumeVectorUser)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("resume_vectors: row %d not found", id)
		}
		return nil
	}

	tag, err := db.pool.Exec(ctx, `
		UPDATE resume_vectors
		   SET content      = $2,
		       content_hash = $3,
		       updated_at   = now()
		WHERE id = $1 AND user_name = $4
	`, id, content, contentHash, resumeVectorUser)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("resume_vectors: row %d not found", id)
	}
	return nil
}
