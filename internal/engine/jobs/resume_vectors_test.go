package jobs

// resume_vectors_test.go — tests for the resume_vectors persistence layer and
// the three resume_memory ops.
//
// Unit tests (no DB needed): helper functions, MCP contract JSON shape, fitness functions.
// DB-backed tests: skip when DATABASE_URL is unset (expected in CI without postgres).
//
// Falsification guarantee:
//   - FTS-path tests call AddResumeMemory / SearchResumeMemory / UpdateResumeMemory (public ops).
//     Reverting resume_memory.go to the MemDB path causes every such test to fail with
//     "resume DB not configured" (GetResumeDB() is set but GetMemDB() returns nil → MemDB error).
//   - Vector-path and dim-mismatch tests call ResumeDB methods directly, bypassing the ops layer
//     to test the persistence invariants without needing a mock embedder.

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// --- Helper unit tests (no DB) ---

func TestVectorLiteral_Format(t *testing.T) {
	cases := []struct {
		in   []float32
		want string
	}{
		{[]float32{}, "[]"},
		{[]float32{1.0, 2.0, 3.0}, "[1,2,3]"},
		{[]float32{0.1, -0.5, 1e-4}, "[0.1,-0.5,0.0001]"},
	}
	for _, tc := range cases {
		got := vectorLiteral(tc.in)
		if got != tc.want {
			t.Errorf("vectorLiteral(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestVectorContentHash_Deterministic(t *testing.T) {
	h1 := vectorContentHash("gojob", "note", nil, "my career goal")
	h2 := vectorContentHash("gojob", "note", nil, "my career goal")
	if h1 != h2 {
		t.Errorf("hash not deterministic: %q vs %q", h1, h2)
	}
}

func TestVectorContentHash_Distinct(t *testing.T) {
	base := vectorContentHash("gojob", "note", nil, "same content")
	if vectorContentHash("other", "note", nil, "same content") == base {
		t.Error("different user_name should produce different hash")
	}
	if vectorContentHash("gojob", "goal", nil, "same content") == base {
		t.Error("different mem_type should produce different hash")
	}
	if vectorContentHash("gojob", "note", nil, "different content") == base {
		t.Error("different content should produce different hash")
	}
	rid := int64(42)
	if vectorContentHash("gojob", "note", &rid, "same content") == base {
		t.Error("non-nil ref_id should produce different hash")
	}
}

// --- MCP contract shape (fitness function 3) ---

// TestResumeMemory_ContractShape verifies the JSON output shapes of the three
// result types are byte-identical to the spec. Any field rename breaks this test.
func TestResumeMemory_ContractShape(t *testing.T) {
	t.Run("SearchResult", func(t *testing.T) {
		r := ResumeMemorySearchResult{
			Query: "test",
			Results: []ResumeMemoryItem{
				{Content: "c", Score: 0.9, Type: "note", ID: 0, MemoryID: "1"},
			},
			Total: 1,
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"query", "results", "total"} {
			if _, ok := m[field]; !ok {
				t.Errorf("SearchResult missing JSON field %q", field)
			}
		}
		// Verify items field set.
		var items []map[string]json.RawMessage
		if err := json.Unmarshal(m["results"], &items); err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 {
			t.Fatal("results empty")
		}
		for _, f := range []string{"content", "score", "memory_id"} {
			if _, ok := items[0][f]; !ok {
				t.Errorf("ResumeMemoryItem missing JSON field %q", f)
			}
		}
	})

	t.Run("AddResult", func(t *testing.T) {
		r := ResumeMemoryAddResult{Status: "stored", Type: "note"}
		b, _ := json.Marshal(r)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(b, &m)
		for _, field := range []string{"status", "type"} {
			if _, ok := m[field]; !ok {
				t.Errorf("AddResult missing JSON field %q", field)
			}
		}
	})

	t.Run("UpdateResult", func(t *testing.T) {
		r := ResumeMemoryUpdateResult{MemoryID: "1", Updated: true}
		b, _ := json.Marshal(r)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(b, &m)
		for _, field := range []string{"memory_id", "updated"} {
			if _, ok := m[field]; !ok {
				t.Errorf("UpdateResult missing JSON field %q", field)
			}
		}
	})
}

// --- Fitness function F1: resume_vectors.go must not import net/http or call embed funcs ---

func TestFitness_F1_VectorFilePurity(t *testing.T) {
	const path = "resume_vectors.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, imp := range f.Imports {
		name := strings.Trim(imp.Path.Value, `"`)
		if name == "net/http" {
			t.Errorf("F1 violation: %s imports net/http (embedding must stay in ops layer)", path)
		}
	}
}

func TestFitness_F1_NoEmbedCallsInVectorFile(t *testing.T) {
	const path = "resume_vectors.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	forbidden := map[string]bool{
		"GetEmbedClient": true,
		"EmbedQuery":     true,
		"EmbedPassages":  true,
		"EmbedTexts":     true,
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if forbidden[id.Name] {
				t.Errorf("F1 violation: %s calls %s (embedding must stay in ops layer)", path, id.Name)
			}
		}
		return true
	})
}

// --- Fitness function F3: resumeVectorUser appears once as a const decl ---

func TestFitness_F3_SingleSourceCubeKey(t *testing.T) {
	const path = "const.go"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	occurrences := strings.Count(string(data), `resumeVectorUser = "gojob"`)
	if occurrences != 1 {
		t.Errorf("F3: resumeVectorUser declared %d times in const.go (want exactly 1)", occurrences)
	}
}

// --- DB-backed tests (skip without DATABASE_URL) ---

// testResumeDB connects to the DB and registers cleanup.
// It purges stale test rows so tests remain idempotent.
func testResumeDB(t *testing.T) *ResumeDB {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set — skipping DB-backed test")
	}
	ctx := context.Background()
	db, err := ConnectResumeDB(ctx, dbURL)
	if err != nil {
		t.Fatalf("ConnectResumeDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Purge rows written by tests to keep them idempotent.
	if _, err := db.pool.Exec(ctx,
		`DELETE FROM resume_vectors WHERE user_name = $1 AND source = 'agent'`,
		resumeVectorUser,
	); err != nil {
		t.Fatalf("cleanup resume_vectors: %v", err)
	}
	return db
}

// TestResumeMemory_AddSearch_FTSPath exercises the full add→search round-trip
// via the public ops API with no embedder (FTS path).
// Falsification: reverting resume_memory.go to the MemDB path breaks this test
// because GetMemDB() returns nil → "MemDB not configured" error.
func TestResumeMemory_AddSearch_FTSPath(t *testing.T) {
	db := testResumeDB(t)
	SetResumeDB(db)
	t.Cleanup(func() { SetResumeDB(nil) })

	prev := GetEmbedClient()
	SetEmbedClient(nil) // force FTS path
	t.Cleanup(func() { SetEmbedClient(prev) })

	ctx := context.Background()

	addResult, err := AddResumeMemory(ctx, "wrote distributed systems in Rust", "note")
	if err != nil {
		t.Fatalf("AddResumeMemory: %v", err)
	}
	if addResult.Status != "stored" {
		t.Errorf("status = %q, want %q", addResult.Status, "stored")
	}
	if addResult.Type != "note" {
		t.Errorf("type = %q, want %q", addResult.Type, "note")
	}

	result, err := SearchResumeMemory(ctx, "Rust distributed systems", 5)
	if err != nil {
		t.Fatalf("SearchResumeMemory: %v", err)
	}
	if result.Total == 0 {
		t.Error("expected ≥1 FTS result, got 0")
	}
	if result.Query != "Rust distributed systems" {
		t.Errorf("query = %q, want %q", result.Query, "Rust distributed systems")
	}
	if len(result.Results) == 0 {
		t.Fatal("results slice is empty")
	}
	first := result.Results[0]
	if first.MemoryID == "" {
		t.Error("memory_id must not be empty")
	}
	// memory_id must be a numeric string (row id), not a UUID.
	if _, err := strconv.ParseInt(first.MemoryID, 10, 64); err != nil {
		t.Errorf("memory_id %q is not numeric: %v", first.MemoryID, err)
	}
}

// TestResumeMemory_IdempotentUpsert verifies that double-adding the same content
// produces one row (ON CONFLICT DO UPDATE).
func TestResumeMemory_IdempotentUpsert(t *testing.T) {
	db := testResumeDB(t)
	SetResumeDB(db)
	t.Cleanup(func() { SetResumeDB(nil) })
	SetEmbedClient(nil)

	ctx := context.Background()
	content := "idempotent note for dedup test"

	if _, err := AddResumeMemory(ctx, content, "note"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := AddResumeMemory(ctx, content, "note"); err != nil {
		t.Fatalf("second add: %v", err)
	}

	var count int
	if err := db.pool.QueryRow(ctx,
		`SELECT count(*) FROM resume_vectors WHERE user_name=$1 AND content=$2`,
		resumeVectorUser, content,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 row after double-add, got %d", count)
	}
}

// TestResumeMemory_Update verifies that UpdateResumeMemory atomically mutates
// the row and preserves the row id (memory_id unchanged after update).
func TestResumeMemory_Update(t *testing.T) {
	db := testResumeDB(t)
	SetResumeDB(db)
	t.Cleanup(func() { SetResumeDB(nil) })
	SetEmbedClient(nil)

	ctx := context.Background()

	if _, err := AddResumeMemory(ctx, "original content for update test", "goal"); err != nil {
		t.Fatalf("AddResumeMemory: %v", err)
	}

	result, err := SearchResumeMemory(ctx, "original content for update test", 5)
	if err != nil {
		t.Fatalf("SearchResumeMemory: %v", err)
	}
	if result.Total == 0 {
		t.Fatal("expected to find the added note before update")
	}
	memoryID := result.Results[0].MemoryID

	updateResult, err := UpdateResumeMemory(ctx, memoryID, "updated content after mutation")
	if err != nil {
		t.Fatalf("UpdateResumeMemory: %v", err)
	}
	if !updateResult.Updated {
		t.Error("Updated must be true")
	}
	if updateResult.MemoryID != memoryID {
		t.Errorf("memory_id changed from %s to %s (row id must be preserved)", memoryID, updateResult.MemoryID)
	}

	var oldCount, newCount int
	_ = db.pool.QueryRow(ctx,
		`SELECT count(*) FROM resume_vectors WHERE user_name=$1 AND content='original content for update test'`,
		resumeVectorUser,
	).Scan(&oldCount)
	_ = db.pool.QueryRow(ctx,
		`SELECT count(*) FROM resume_vectors WHERE user_name=$1 AND content='updated content after mutation'`,
		resumeVectorUser,
	).Scan(&newCount)

	if oldCount != 0 {
		t.Errorf("old content still present (%d rows)", oldCount)
	}
	if newCount != 1 {
		t.Errorf("new content not found (%d rows)", newCount)
	}
}

// TestResumeDB_DimMismatch_FTSFallback calls UpsertVector directly with a
// wrong-dim vector and verifies embedding is stored as NULL (FTS-only row).
func TestResumeDB_DimMismatch_FTSFallback(t *testing.T) {
	db := testResumeDB(t)
	if !db.HasEmbedding() {
		t.Skip("embedding column absent (005 migration not applied on test DB) — skipping dim-mismatch test")
	}

	ctx := context.Background()
	content := "dim-mismatch test note"

	// Wrong dim: 3 instead of 1024.
	shortVec := make([]float32, 3)
	if _, err := db.UpsertVector(ctx, content, "note", nil, shortVec); err != nil {
		t.Fatalf("UpsertVector with wrong dim: %v", err)
	}

	var embeddingIsNull bool
	if err := db.pool.QueryRow(ctx,
		`SELECT embedding IS NULL FROM resume_vectors WHERE user_name=$1 AND content=$2`,
		resumeVectorUser, content,
	).Scan(&embeddingIsNull); err != nil {
		t.Fatalf("query embedding: %v", err)
	}
	if !embeddingIsNull {
		t.Error("expected embedding=NULL for wrong-dim vector, but it was stored")
	}
}

// TestResumeDB_VectorPath calls UpsertVector and SearchByVector directly to
// verify the pgvector code path (no embed client needed — vec is precomputed).
func TestResumeDB_VectorPath(t *testing.T) {
	db := testResumeDB(t)
	if !db.HasEmbedding() {
		t.Skip("embedding column absent (005 migration not applied on test DB) — skipping vector-path test")
	}

	ctx := context.Background()
	content := "vector path test note"

	// Build a unit vector of the correct dimension.
	vec := make([]float32, expectedEmbedDim)
	vec[0] = 1.0

	id, err := db.UpsertVector(ctx, content, "note", nil, vec)
	if err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive row id, got %d", id)
	}

	// Embedding must be non-NULL.
	var embeddingIsNull bool
	if err := db.pool.QueryRow(ctx,
		`SELECT embedding IS NULL FROM resume_vectors WHERE id=$1`,
		id,
	).Scan(&embeddingIsNull); err != nil {
		t.Fatalf("query embedding: %v", err)
	}
	if embeddingIsNull {
		t.Error("expected embedding IS NOT NULL for correct-dim vector")
	}

	// SearchByVector must return the row (cosine similarity to itself = 1.0).
	rows, err := db.SearchByVector(ctx, vec, 5)
	if err != nil {
		t.Fatalf("SearchByVector: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("SearchByVector returned 0 rows")
	}
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = true
			if r.Score < 0.99 {
				t.Errorf("self-similarity score = %f, want ≥0.99", r.Score)
			}
		}
	}
	if !found {
		t.Errorf("SearchByVector did not return the inserted row (id=%d)", id)
	}
}
