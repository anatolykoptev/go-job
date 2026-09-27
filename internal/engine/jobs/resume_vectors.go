package jobs

// resume_vectors.go — account-scoped persistence for the resume_vectors table.
//
// Layering invariant (fitness function F1):
//   - These methods are pure SQL: they accept precomputed embeddings as []float32 parameters.
//   - No net/http import, no GetEmbedClient call, no EmbedQuery call lives here.
//   - Embedding generation happens in the engine ops layer (resume_memory.go).
//
// Account scope (plan ADR-9, P4): rows are keyed by account_id — the bound
// ResumeAccount's uuid — not by user_name. The user_name column remains until
// the P5 drop (ADR-13 expand/constrain) but is never read or written here.
// Dedup uniqueness is UNIQUE(account_id, content_hash) with content_hash =
// sha256(account_id|mem_type|ref_id|content); Bootstrap.BackfillResumeVectors
// re-keys legacy hashes to the same formula so pre-existing rows still dedupe.

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

// vectorContentHash computes sha256(account|mem_type|coalesce(ref_id,0)|content)
// — the dedup key in the UNIQUE(account_id, content_hash) constraint. The
// account string is the canonical uuid (uuid.String()); the SQL backfill
// recomputes the identical digest via encode(sha256(...),'hex').
func vectorContentHash(accountKey, memType string, refID *int64, content string) string {
	rid := int64(0)
	if refID != nil {
		rid = *refID
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s", accountKey, memType, rid, content)))
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

// UpsertVector inserts or updates a resume memory row with source='agent'
// (manual free-text memories). It delegates to UpsertVectorWithSource with a
// nil ref_id — manual memories never carry a ref_id, and this method makes
// that pairing impossible to express at compile time (no refID parameter).
// embedding may be nil or empty — in that case the row is stored without a vector (FTS-only).
// embedding dimension must be 1024 when provided; mismatched dims are silently ignored (FTS fallback).
func (a *ResumeAccount) UpsertVector(
	ctx context.Context,
	content, memType string,
	embedding []float32,
) (int64, error) {
	return a.UpsertVectorWithSource(ctx, content, memType, nil, embedding, sourceAgent)
}

// UpsertVectorWithSource is the single write path for resume_vectors, with the
// row source parameterized so derived rows (source='profile') and manual rows
// (source='agent') share the same SQL and account-scoped content-hash dedup.
// On conflict the source label is refreshed to EXCLUDED.source so a re-sync
// corrects the label on pre-existing rows.
func (a *ResumeAccount) UpsertVectorWithSource(
	ctx context.Context,
	content, memType string,
	refID *int64,
	embedding []float32,
	source string,
) (int64, error) {
	if !a.writable() {
		return 0, ErrNoAccountScope
	}
	// Mechanical invariant: source='agent' rows must always have ref_id IS NULL.
	// The 007 backfill predicate (ref_id IS NOT NULL AND source='agent') and the
	// ON CONFLICT re-label (SET source = EXCLUDED.source) both depend on this.
	// A source='agent' row with a non-nil ref_id would be silently re-labelled
	// 'profile' by the backfill and again by any derived upsert with matching
	// content — corrupting a real user memory. The UpsertVector wrapper makes
	// the common agent path impossible to express at compile time (no refID
	// parameter); this guard catches any direct UpsertVectorWithSource caller.
	if source == sourceAgent && refID != nil {
		return 0, fmt.Errorf("resume_vectors: source='agent' rows must have nil ref_id (got ref_id=%d) — use source='profile' for derived rows", *refID)
	}
	hash := vectorContentHash(a.aid.String(), memType, refID, content)
	if source == "" {
		source = sourceAgent
	}

	useVec := len(embedding) == 1024 && hasEmbeddingCol
	var id int64
	if useVec {
		vec := vectorLiteral(embedding)
		err := a.conn(ctx).QueryRow(ctx, `
			INSERT INTO resume_vectors (account_id, content, mem_type, source, ref_id, content_hash, embedding)
			VALUES ($1, $2, $3, $4, $5, $6, $7::vector)
			ON CONFLICT (account_id, content_hash) DO UPDATE
			  SET content   = EXCLUDED.content,
			      embedding = EXCLUDED.embedding,
			      source     = EXCLUDED.source,
			      updated_at = now()
			RETURNING id
		`, a.aid, content, memType, source, refID, hash, vec).Scan(&id)
		return id, err
	}

	err := a.conn(ctx).QueryRow(ctx, `
		INSERT INTO resume_vectors (account_id, content, mem_type, source, ref_id, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (account_id, content_hash) DO UPDATE
		  SET content    = EXCLUDED.content,
		      source     = EXCLUDED.source,
		      updated_at = now()
		RETURNING id
	`, a.aid, content, memType, source, refID, hash).Scan(&id)
	return id, err
}

// minVectorSimilarity is the default cosine similarity floor for vector search.
// Results below this threshold are not meaningfully related and would inflate the FTS comparison.
// Scoped searches use a caller-supplied floor via SearchByVectorScoped.
const minVectorSimilarity = 0.5

// SearchByVectorScoped performs exact cosine-distance search via pgvector (<=>)
// over the bound account's rows only. Only rows with non-NULL embeddings are
// scanned. Results whose similarity is below minScore are excluded. When
// memTypes is nil the filter is skipped and all of the account's mem_types are
// returned.
func (a *ResumeAccount) SearchByVectorScoped(ctx context.Context, qvec []float32, topK int, minScore float64, memTypes []string) ([]VectorRow, error) {
	vec := vectorLiteral(qvec)
	rows, err := a.db.pool.Query(ctx, `
		SELECT id, content, mem_type, ref_id,
		       1.0 - (embedding <=> $1::vector) AS score
		FROM resume_vectors
		WHERE account_id = $2
		  AND embedding IS NOT NULL
		  AND ($5::text[] IS NULL OR mem_type = ANY($5::text[]))
		  AND 1.0 - (embedding <=> $1::vector) >= $4
		ORDER BY embedding <=> $1::vector
		LIMIT $3
	`, vec, a.aid, topK, minScore, memTypes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVectorRows(rows)
}

// SearchByTextScoped performs GIN tsvector full-text search over the bound
// account's rows only. When memTypes is nil the filter is skipped and all of
// the account's mem_types are returned.
func (a *ResumeAccount) SearchByTextScoped(ctx context.Context, query string, topK int, memTypes []string) ([]VectorRow, error) {
	rows, err := a.db.pool.Query(ctx, `
		SELECT id, content, mem_type, ref_id,
		       ts_rank(tsv, plainto_tsquery('english', $1)) AS score
		FROM resume_vectors
		WHERE account_id = $2
		  AND ($4::text[] IS NULL OR mem_type = ANY($4::text[]))
		  AND tsv @@ plainto_tsquery('english', $1)
		ORDER BY score DESC
		LIMIT $3
	`, query, a.aid, topK, memTypes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVectorRows(rows)
}

// SearchByVector is a convenience wrapper (no mem_type filter, default
// minVectorSimilarity floor) that delegates to SearchByVectorScoped — still
// account-scoped via the bound facade.
func (a *ResumeAccount) SearchByVector(ctx context.Context, qvec []float32, topK int) ([]VectorRow, error) {
	return a.SearchByVectorScoped(ctx, qvec, topK, minVectorSimilarity, nil)
}

// SearchByText is a convenience wrapper (no mem_type filter) that delegates to
// SearchByTextScoped — still account-scoped via the bound facade.
func (a *ResumeAccount) SearchByText(ctx context.Context, query string, topK int) ([]VectorRow, error) {
	return a.SearchByTextScoped(ctx, query, topK, nil)
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

// FetchVectorMeta returns the mem_type and ref_id of an existing row by id —
// empty result (ErrNoRows) when the row belongs to another account.
// Used by UpdateResumeMemory to recompute the content_hash with the correct mem_type.
func (a *ResumeAccount) FetchVectorMeta(ctx context.Context, id int64) (memType string, refID *int64, err error) {
	err = a.db.pool.QueryRow(ctx, `
		SELECT mem_type, ref_id
		FROM resume_vectors
		WHERE id = $1 AND account_id = $2
	`, id, a.aid).Scan(&memType, &refID)
	return
}

// ClearVectors deletes the source='profile' derived resume_vectors rows owned
// by the bound account whose mem_type matches any of the provided values. The
// delete is scoped to source='profile' so manual source='agent' memories
// (including a manual row tagged with a derived mem_type like
// resume_experience) are never destroyed by a rebuild. Other consumers'
// mem_types (e.g. enrich_project) are also untouched. The single caller is
// BuildMasterResume, which clears the account's derived rows it is about to
// re-derive from the structured profile.
func (a *ResumeAccount) ClearVectors(ctx context.Context, memTypes ...string) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.conn(ctx).Exec(ctx, `
		DELETE FROM resume_vectors
		WHERE account_id = $1 AND source = $2 AND mem_type = ANY($3::text[])
	`, a.aid, sourceProfile, memTypes)
	return err
}

// SourceProfile returns the source label for derived rows re-derived from the
// structured profile entities. Exported for cross-package tests.
func SourceProfile() string { return sourceProfile }

// SourceAgent returns the source label for manual free-text memories.
// Exported for cross-package tests.
func SourceAgent() string { return sourceAgent }

// CountVectors returns the number of resume_vectors rows owned by the bound
// account whose mem_type matches any of the provided values.
func (a *ResumeAccount) CountVectors(ctx context.Context, memTypes ...string) (int, error) {
	var n int
	err := a.db.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM resume_vectors
		WHERE account_id = $1 AND mem_type = ANY($2::text[])
	`, a.aid, memTypes).Scan(&n)
	return n, err
}

// ListDerivedVectors returns the bound account's source='profile' derived
// vector rows whose mem_type is in memTypes. Used by SyncProfileVectors to
// detect unchanged rows (no-op) vs changed/new rows (upsert). Only
// source='profile' rows are returned — manual source='agent' rows are never
// listed and therefore never compared or touched by the sync.
func (a *ResumeAccount) ListDerivedVectors(ctx context.Context, memTypes []string) ([]VectorRow, error) {
	rows, err := a.db.pool.Query(ctx, `
		SELECT id, content, mem_type, ref_id, 0::float8
		FROM resume_vectors
		WHERE account_id = $1
		  AND source = $2
		  AND mem_type = ANY($3::text[])
	`, a.aid, sourceProfile, memTypes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVectorRows(rows)
}

// DeleteDerivedVectorsNotIn removes the bound account's source='profile'
// derived rows of memType whose ref_id is not in keepIDs (or whose ref_id is
// NULL). Scoped to source='profile' AND the single mem_type so manual
// source='agent' rows and other consumers' mem_types are never deleted. When
// keepIDs is empty, all of the account's source='profile' rows of memType are
// removed (the entity set is empty).
func (a *ResumeAccount) DeleteDerivedVectorsNotIn(ctx context.Context, memType string, keepIDs []int64) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	if len(keepIDs) == 0 {
		_, err := a.db.pool.Exec(ctx, `
			DELETE FROM resume_vectors
			WHERE account_id = $1 AND source = $2 AND mem_type = $3
		`, a.aid, sourceProfile, memType)
		return err
	}
	_, err := a.db.pool.Exec(ctx, `
		DELETE FROM resume_vectors
		WHERE account_id = $1 AND source = $2 AND mem_type = $3
		  AND (ref_id IS NULL OR NOT (ref_id = ANY($4::bigint[])))
	`, a.aid, sourceProfile, memType, keepIDs)
	return err
}

// DeleteDerivedVectorByID removes the bound account's source='profile' derived
// rows for a single (mem_type, ref_id). Used by SyncProfileVectors to clear
// stale rows before re-inserting with new content, so editing an entity does
// not leave a duplicate row carrying the old content_hash. Scoped to
// source='profile' so manual source='agent' rows are never deleted.
func (a *ResumeAccount) DeleteDerivedVectorByID(ctx context.Context, memType string, refID int64) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	_, err := a.db.pool.Exec(ctx, `
		DELETE FROM resume_vectors
		WHERE account_id = $1 AND source = $2 AND mem_type = $3 AND ref_id = $4
	`, a.aid, sourceProfile, memType, refID)
	return err
}

// UpdateVector atomically updates content, content_hash, and embedding for a
// row owned by the bound account; a foreign or missing row id errors.
// embedding may be nil (FTS-only update).
func (a *ResumeAccount) UpdateVector(
	ctx context.Context,
	id int64,
	content, contentHash string,
	embedding []float32,
) error {
	if !a.writable() {
		return ErrNoAccountScope
	}
	useVec := len(embedding) == 1024 && hasEmbeddingCol
	if useVec {
		vec := vectorLiteral(embedding)
		tag, err := a.db.pool.Exec(ctx, `
			UPDATE resume_vectors
			   SET content      = $2,
			       content_hash = $3,
			       embedding    = $4::vector,
			       updated_at   = now()
			WHERE id = $1 AND account_id = $5
		`, id, content, contentHash, vec, a.aid)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("resume_vectors: row %d not found", id)
		}
		return nil
	}

	tag, err := a.db.pool.Exec(ctx, `
		UPDATE resume_vectors
		   SET content      = $2,
		       content_hash = $3,
		       updated_at   = now()
		WHERE id = $1 AND account_id = $4
	`, id, content, contentHash, a.aid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("resume_vectors: row %d not found", id)
	}
	return nil
}
