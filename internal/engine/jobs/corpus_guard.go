package jobs

// corpus_guard.go — startup guards that the active embed client is the one
// that built the corpus it is about to write into.

import (
	"context"
	"errors"
	"fmt"

	kitembed "github.com/anatolykoptev/go-kit/embed"
	"github.com/jackc/pgx/v5"
)

// corpusConventionMinCosine is the floor for the startup convention probe.
//
// It is not a tuning knob. Re-embedding a stored row with the SAME client that
// wrote it reproduces the vector exactly: measured 1.000000 against the live
// corpus, and the embed endpoint is deterministic (embedding one input twice
// gives self-cosine 1.000000). Anything below this floor means the active
// client is not the one that built the corpus.
//
// The number that motivated the floor: embedding the same text WITHOUT the
// e5 "passage: " prefix scores 0.970994 against the prefixed corpus — high
// enough to look healthy in a spot check, low enough to reorder results. A
// threshold above that value and below 1 is the whole point; 0.999 leaves
// room for float noise without admitting a convention change.
const corpusConventionMinCosine = 0.999

// ErrCorpusConvention reports that the active embed client does not reproduce
// a vector already stored in the corpus.
//
// Distinct from kitembed.ErrCorpusDimMismatch, which compares dimensions. The
// dimension can match while the vectors are still incompatible: a changed
// prefix convention, a model version bump, or a different embedding backend
// all keep 1024 dimensions and move the vectors. pgvector's vector(1024)
// column type catches the dimension case loudly on the first write; nothing
// catches this one, which is why this exists.
type ErrCorpusConvention struct {
	VectorID int64
	Cosine   float64
	Min      float64
}

func (e *ErrCorpusConvention) Error() string {
	return fmt.Sprintf(
		"embed: corpus convention drift — re-embedding stored vector %d with the active client scores cosine %.6f against its own stored embedding (floor %.3f); "+
			"the active client is not the one that built this corpus (prefix convention, model version, or backend changed). "+
			"New vectors will not be comparable with existing ones. Re-embed the corpus or restore the previous embedding configuration",
		e.VectorID, e.Cosine, e.Min)
}

// StoredDim implements kitembed.CorpusInspector over resume_vectors.
// Returns 0 when the corpus holds no embeddings — an empty corpus has no
// established dimension and cannot drift.
func (db *ResumeDB) StoredDim(ctx context.Context) (int, error) {
	if db == nil || !db.HasEmbedding() {
		return 0, nil
	}
	var dim int
	err := db.conn(ctx).QueryRow(ctx,
		`SELECT vector_dims(embedding) FROM resume_vectors WHERE embedding IS NOT NULL LIMIT 1`,
	).Scan(&dim)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("resume corpus: stored dim: %w", err)
	}
	return dim, nil
}

// CheckCorpusConvention re-embeds one row that is already in the corpus and
// requires the result to match what is stored.
//
// This is the corpus analogue of go-stealth's fingerprint oracle: rather than
// asserting that the embedding configuration is right, it measures whether the
// active client reproduces the corpus it is about to write into. A failure is
// a true result about the configuration, not a flaky test.
//
// Returns nil when there is nothing to check (no embedder, no embedding column,
// empty corpus). The error is non-fatal by design — the caller decides whether
// to refuse writes or log and continue.
func CheckCorpusConvention(ctx context.Context, db *ResumeDB, ec kitembed.Embedder) error {
	if db == nil || ec == nil || !db.HasEmbedding() {
		return nil
	}

	var (
		id      int64
		content string
	)
	err := db.conn(ctx).QueryRow(ctx,
		`SELECT id, content FROM resume_vectors
		  WHERE embedding IS NOT NULL AND mem_type = 'resume_project'
		  ORDER BY id LIMIT 1`,
	).Scan(&id, &content)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil // empty corpus — nothing established yet
	case err != nil:
		return fmt.Errorf("resume corpus: probe row: %w", err)
	}

	// The same prefix the writers use (embedPassage, the profile rebuild). If
	// this line and the writers ever disagree, the probe is what says so.
	vecs, err := ec.Embed(ctx, []string{kitembed.E5PassagePrefix + content})
	if err != nil {
		return fmt.Errorf("resume corpus: embed probe: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return fmt.Errorf("resume corpus: embed probe returned %d vectors", len(vecs))
	}

	var cos float64
	if err := db.conn(ctx).QueryRow(ctx,
		`SELECT 1 - (embedding <=> $1::vector) FROM resume_vectors WHERE id = $2`,
		vectorLiteral(vecs[0]), id,
	).Scan(&cos); err != nil {
		return fmt.Errorf("resume corpus: compare probe: %w", err)
	}

	if cos < corpusConventionMinCosine {
		return &ErrCorpusConvention{VectorID: id, Cosine: cos, Min: corpusConventionMinCosine}
	}
	return nil
}
