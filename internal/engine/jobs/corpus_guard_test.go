package jobs

import (
	"context"
	"errors"
	"math"
	"testing"
)

// measuredUnprefixedCosine is what re-embedding a stored row WITHOUT the e5
// "passage: " prefix scored against the prefixed corpus on 2026-08-04, on the
// live resume_vectors table. It is the concrete failure the floor exists to
// reject, so it is pinned here rather than left in a comment.
const measuredUnprefixedCosine = 0.970994

// The floor has to sit strictly above the one wrong configuration we have
// actually measured. Lowering it below that value would make the guard accept
// exactly the drift it was built to catch — the cheapest way to "fix" a firing
// probe, and the reason this test exists.
func TestCorpusConventionFloor_RejectsTheMeasuredPrefixGap(t *testing.T) {
	if corpusConventionMinCosine <= measuredUnprefixedCosine {
		t.Fatalf("floor %.6f admits the measured unprefixed drift %.6f — the guard would pass on a corpus it cannot reproduce",
			corpusConventionMinCosine, measuredUnprefixedCosine)
	}
	if corpusConventionMinCosine > 1.0 {
		t.Fatalf("floor %.6f is unreachable — cosine never exceeds 1", corpusConventionMinCosine)
	}
}

// fixedEmbedder returns one canned vector for any input, so a test controls
// the cosine the probe will observe.
type fixedEmbedder struct{ vec []float32 }

func (f *fixedEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = f.vec
	}
	return out, nil
}

func (f *fixedEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return f.vec, nil
}

func (f *fixedEmbedder) Dimension() int { return len(f.vec) }
func (f *fixedEmbedder) Close() error   { return nil }

// axisVector is the unit vector along the first axis.
func axisVector(dim int) []float32 {
	v := make([]float32, dim)
	v[0] = 1
	return v
}

// vectorAtCosine returns a unit vector whose cosine against axisVector is
// exactly cos.
func vectorAtCosine(dim int, cos float64) []float32 {
	v := make([]float32, dim)
	v[0] = float32(cos)
	v[1] = float32(math.Sqrt(1 - cos*cos))
	return v
}

func TestCheckCorpusConvention_MatchingClientPasses(t *testing.T) {
	db := testResumeDB(t)
	if !db.HasEmbedding() {
		t.Skip("embedding column absent (005 migration not applied on test DB) — convention probe needs the vector path")
	}
	ctx := context.Background()

	stored := axisVector(1024)
	if _, err := db.UpsertVector(ctx, "corpus convention probe row", "resume_project", stored); err != nil {
		t.Fatalf("seed vector: %v", err)
	}

	if err := CheckCorpusConvention(ctx, db, &fixedEmbedder{vec: stored}); err != nil {
		t.Fatalf("a client that reproduces the stored vector must pass, got: %v", err)
	}
}

// The guard's reason for existing: a client at the dimension the corpus expects,
// returning a plausible vector, that is nevertheless not the client that wrote
// the corpus. Nothing else in the stack reports this — pgvector accepts the
// write, the row count goes up, and retrieval quietly degrades.
func TestCheckCorpusConvention_DriftedClientIsCaught(t *testing.T) {
	db := testResumeDB(t)
	if !db.HasEmbedding() {
		t.Skip("embedding column absent (005 migration not applied on test DB) — convention probe needs the vector path")
	}
	ctx := context.Background()

	stored := axisVector(1024)
	if _, err := db.UpsertVector(ctx, "corpus convention probe row", "resume_project", stored); err != nil {
		t.Fatalf("seed vector: %v", err)
	}

	// Same dimension, same shape, 0.97 away — the prefix-drift signature.
	drifted := vectorAtCosine(1024, measuredUnprefixedCosine)

	err := CheckCorpusConvention(ctx, db, &fixedEmbedder{vec: drifted})
	if err == nil {
		t.Fatal("a client that does not reproduce the corpus must be reported, got nil")
	}
	var convErr *ErrCorpusConvention
	if !errors.As(err, &convErr) {
		t.Fatalf("want *ErrCorpusConvention, got %T: %v", err, err)
	}
	if convErr.Cosine >= corpusConventionMinCosine {
		t.Fatalf("reported cosine %.6f is at or above the floor %.6f — the error fired for the wrong reason",
			convErr.Cosine, corpusConventionMinCosine)
	}
	if math.Abs(convErr.Cosine-measuredUnprefixedCosine) > 1e-4 {
		t.Fatalf("probe measured cosine %.6f, want ~%.6f — the comparison is not scoring what the test constructed",
			convErr.Cosine, measuredUnprefixedCosine)
	}
}

func TestStoredDim_ReportsTheCorpusDimension(t *testing.T) {
	db := testResumeDB(t)
	if !db.HasEmbedding() {
		t.Skip("embedding column absent (005 migration not applied on test DB)")
	}
	ctx := context.Background()

	if _, err := db.UpsertVector(ctx, "corpus dim probe row", "resume_project", axisVector(1024)); err != nil {
		t.Fatalf("seed vector: %v", err)
	}
	dim, err := db.StoredDim(ctx)
	if err != nil {
		t.Fatalf("StoredDim: %v", err)
	}
	if dim != 1024 {
		t.Fatalf("StoredDim = %d, want 1024", dim)
	}
}
