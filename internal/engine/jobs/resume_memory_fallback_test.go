package jobs

// resume_memory_fallback_test.go — tests for the FTS fallback when the vector
// path returns zero rows (embedOrFTS).
//
// Production defect this guards: after migration 005 every row still had
// embedding IS NULL, so the vector path succeeded (valid query embedding) but
// returned 0 rows, and embedOrFTS returned that empty result verbatim —
// resume_memory op=search went to zero results while plainto_tsquery matched 4.
//
// Falsification (mutants):
//   - Mutant A: delete the empty-vector fallback branch → the test asserting
//     "empty vector result yields the FTS rows" goes RED (Total == 0).
//   - Mutant B: make the fallback fire unconditionally (fall back even when the
//     vector result is non-empty) → the test asserting a non-empty vector
//     result is returned untouched goes RED (Total != 1 / wrong rows).

import (
	"context"
	"testing"

	kitembed "github.com/anatolykoptev/go-kit/embed"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// fakeEmbedder returns a fixed vector for every call so embedOrFTS selects the
// vector path. The test controls whether the DB actually has rows with
// non-NULL embeddings, so it can simulate the "vector path succeeds but the
// index is empty" condition without a real embedder service.
type fakeEmbedder struct{ vec []float32 }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = f.vec
	}
	return out, nil
}

func (f *fakeEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return f.vec, nil
}

func (f *fakeEmbedder) Dimension() int { return len(f.vec) }
func (f *fakeEmbedder) Close() error   { return nil }

// counterValue reads the current value of a prometheus counter (used instead
// of prometheus/testutil, which is not vendored).
func counterValue(c prometheus.Counter) float64 {
	m := &dto.Metric{}
	if err := c.(prometheus.Metric).Write(m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}

// unitQueryVec is a 1024-dim unit vector used both as the fake query embedding
// and as the stored embedding for "vector-matched" rows, so cosine similarity
// is 1.0 (>= minVectorSimilarity) and SearchByVector returns those rows.
func unitQueryVec() []float32 {
	v := make([]float32, expectedEmbedDim)
	v[0] = 1.0
	return v
}

// TestResumeMemory_EmptyVectorFallsBackToFTS reproduces the production defect:
// the embedder is configured and returns a valid query vector, but no row has a
// non-NULL embedding, so the vector path returns 0 rows. The FTS fallback must
// return the rows that plainto_tsquery matches, and the backend label must be
// distinguishable (fts_fallback) from a plain vector or plain FTS answer.
//
// Mutant A — delete the empty-vector fallback branch → Total == 0 → RED.
func TestResumeMemory_EmptyVectorFallsBackToFTS(t *testing.T) {
	db := testResumeDB(t)
	if !db.HasEmbedding() {
		t.Skip("embedding column absent — fallback-after-empty is a vector-path behaviour")
	}
	SetResumeDB(db)
	t.Cleanup(func() { SetResumeDB(nil) })

	prev := GetEmbedClient()
	SetEmbedClient(&fakeEmbedder{vec: unitQueryVec()})
	t.Cleanup(func() { SetEmbedClient(prev) })

	ctx := context.Background()

	// Row with a NULL embedding: the vector path (embedding IS NOT NULL) skips
	// it, but plainto_tsquery matches it — exactly the post-migration-005 state.
	const ftsContent = "kubernetes orchestration fallback probe zeta"
	if _, err := db.UpsertVector(ctx, ftsContent, "note", nil); err != nil {
		t.Fatalf("UpsertVector FTS-only row: %v", err)
	}

	before := counterValue(resumeMemoryOpsTotal.WithLabelValues("search", backendFTSFallback))

	res, err := SearchResumeMemory(ctx, "kubernetes orchestration fallback zeta", 5)
	if err != nil {
		t.Fatalf("SearchResumeMemory: %v", err)
	}
	if res.Total == 0 {
		t.Fatal("expected FTS fallback rows when vector path returned empty, got 0 — " +
			"embedOrFTS returned the empty vector result verbatim (Mutant A)")
	}
	found := false
	for _, r := range res.Results {
		if r.Content == ftsContent {
			found = true
		}
	}
	if !found {
		t.Errorf("FTS fallback did not return the matching row; got %d results, none equal the FTS row", res.Total)
	}

	// The backend label must reach the existing metric as a third state
	// (fts_fallback), not a parallel metric, and not collapse into "fts" or
	// "vector".
	after := counterValue(resumeMemoryOpsTotal.WithLabelValues("search", backendFTSFallback))
	if got := after - before; got != 1 {
		t.Errorf("resumeMemoryOpsTotal{search,fts_fallback} delta = %v, want 1 — "+
			"fallback-after-empty must be distinguishable from plain vector/FTS", got)
	}
}

// TestResumeMemory_NonEmptyVectorReturnedUntouched proves that a non-empty
// vector result is returned verbatim — the empty-result fallback must NOT fire
// when the vector path already has rows (no re-ranking, no merge, no replace).
//
// Mutant B — make the fallback fire unconditionally → FTS rows replace the
// vector rows → Total != 1 (and the FTS-only row appears) → RED.
func TestResumeMemory_NonEmptyVectorReturnedUntouched(t *testing.T) {
	db := testResumeDB(t)
	if !db.HasEmbedding() {
		t.Skip("embedding column absent — vector-wins behaviour needs the embedding column")
	}
	SetResumeDB(db)
	t.Cleanup(func() { SetResumeDB(nil) })

	prev := GetEmbedClient()
	SetEmbedClient(&fakeEmbedder{vec: unitQueryVec()})
	t.Cleanup(func() { SetEmbedClient(prev) })

	ctx := context.Background()
	vec := unitQueryVec()

	// Row V: has a real embedding (== query vector) → vector path returns it.
	const vecContent = "zeta vector wins untouched marker alpha"
	if _, err := db.UpsertVector(ctx, vecContent, "note", vec); err != nil {
		t.Fatalf("UpsertVector vector row: %v", err)
	}
	// Row F: NULL embedding, but its tsv matches the same query terms → FTS
	// would return it (and V) if the fallback fired incorrectly.
	const ftsContent = "zeta fts only marker sigma beta"
	if _, err := db.UpsertVector(ctx, ftsContent, "note", nil); err != nil {
		t.Fatalf("UpsertVector FTS-only row: %v", err)
	}

	res, err := SearchResumeMemory(ctx, "zeta marker", 5)
	if err != nil {
		t.Fatalf("SearchResumeMemory: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("expected exactly 1 vector result (vector wins when non-empty), got %d — "+
			"fallback fired unconditionally (Mutant B)", res.Total)
	}
	if res.Results[0].Content != vecContent {
		t.Errorf("non-empty vector result was altered: got %q, want %q — "+
			"vector path must win verbatim when it has rows",
			res.Results[0].Content, vecContent)
	}
}

// TestEmbedOrFTS_BackendLabels guards the three-state backend label directly:
//   - vector path with rows      → "vector"   (txtFn never called)
//   - vector path empty → FTS    → "fts_fallback"
//   - no embedder / embed failed → "fts"
//
// No DB needed: vecFn/txtFn are injected, HasEmbedding is forced via the
// package-level flag. This is the unit the label logic lives in; the DB-backed
// tests above prove the rows reach the caller through the real ops path.
func TestEmbedOrFTS_BackendLabels(t *testing.T) {
	prevEmbed := GetEmbedClient()
	prevCol := hasEmbeddingCol
	t.Cleanup(func() {
		SetEmbedClient(prevEmbed)
		hasEmbeddingCol = prevCol
	})

	vecRows := []VectorRow{{ID: 1, Content: "v1", MemType: "note", Score: 0.9}}
	ftsRows := []VectorRow{{ID: 2, Content: "f1", MemType: "note", Score: 0.1}}

	t.Run("vector_nonempty", func(t *testing.T) {
		SetEmbedClient(&fakeEmbedder{vec: unitQueryVec()})
		hasEmbeddingCol = true
		txtCalled := false
		rows, backend, err := embedOrFTS(context.Background(), &ResumeDB{}, "q", "test",
			func(_ []float32) ([]VectorRow, error) { return vecRows, nil },
			func() ([]VectorRow, error) { txtCalled = true; return ftsRows, nil },
		)
		if err != nil {
			t.Fatalf("embedOrFTS: %v", err)
		}
		if backend != backendVector {
			t.Errorf("backend = %q, want %q", backend, backendVector)
		}
		if len(rows) != len(vecRows) || rows[0].ID != vecRows[0].ID {
			t.Errorf("vector rows not returned verbatim: %+v", rows)
		}
		if txtCalled {
			t.Error("txtFn called when vector path had rows — vector must win without FTS")
		}
	})

	t.Run("vector_empty_falls_back", func(t *testing.T) {
		SetEmbedClient(&fakeEmbedder{vec: unitQueryVec()})
		hasEmbeddingCol = true
		rows, backend, err := embedOrFTS(context.Background(), &ResumeDB{}, "q", "test",
			func(_ []float32) ([]VectorRow, error) { return nil, nil },
			func() ([]VectorRow, error) { return ftsRows, nil },
		)
		if err != nil {
			t.Fatalf("embedOrFTS: %v", err)
		}
		if backend != backendFTSFallback {
			t.Errorf("backend = %q, want %q (must be distinguishable from plain vector/FTS)",
				backend, backendFTSFallback)
		}
		if len(rows) != len(ftsRows) || rows[0].ID != ftsRows[0].ID {
			t.Errorf("FTS rows not returned after empty vector: %+v", rows)
		}
	})

	t.Run("no_embedder_plain_fts", func(t *testing.T) {
		SetEmbedClient(nil)
		hasEmbeddingCol = true
		rows, backend, err := embedOrFTS(context.Background(), &ResumeDB{}, "q", "test",
			func(_ []float32) ([]VectorRow, error) { return vecRows, nil },
			func() ([]VectorRow, error) { return ftsRows, nil },
		)
		if err != nil {
			t.Fatalf("embedOrFTS: %v", err)
		}
		if backend != backendFTS {
			t.Errorf("backend = %q, want %q", backend, backendFTS)
		}
		if len(rows) != len(ftsRows) {
			t.Errorf("FTS rows not returned: %+v", rows)
		}
	})
}

// compile-time guard: fakeEmbedder satisfies the go-kit Embedder interface.
var _ kitembed.Embedder = (*fakeEmbedder)(nil)
