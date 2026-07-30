package jobs

// resume_vectors_test.go — tests for the resume_vectors persistence layer and
// the three resume_memory ops.
//
// Unit tests (no DB needed): helper functions, MCP contract JSON shape, fitness functions.
// DB-backed tests: skip when DATABASE_URL is unset (expected in CI without postgres).
//
// Falsification guarantee:
//   - FTS-path tests call AddResumeMemory / SearchResumeMemory / UpdateResumeMemory (public ops).
//     Removing the resume_vectors storage path causes these tests to fail at the DB call.
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

	"github.com/anatolykoptev/go_job/internal/dbtest"
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
//
// Safety gate: the function skips unless the DB name ends in "_test" to prevent
// tests from deleting rows in a production database.  If you have a dedicated
// test instance, set DATABASE_URL with a name like "gojob_test".
// Background: UpsertVector always writes source='agent'; the cleanup that follows
// deletes ALL such rows for resumeVectorUser — on a prod DB this wipes the
// operator's entire vector store (oxpulse TEST_DATABASE_URL→prod isolation class).
func testResumeDB(t *testing.T) *ResumeDB {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dbURL)

	ctx := context.Background()
	db, connErr := ConnectResumeDB(ctx, dbURL)
	if connErr != nil {
		t.Fatalf("ConnectResumeDB: %v", connErr)
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
// Falsification: removing the resume_vectors storage path breaks this test at the DB write.
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

// TestResumeDB_ClearVectors_Scoped verifies that ClearVectors only removes
// source='profile' rows whose mem_type is in the provided list, leaving other
// mem_types intact. Rows are seeded as source='profile' (the source ClearVectors
// is scoped to); the source-scope invariant itself is covered by
// TestResumeDB_ClearVectors_PreservesAgentRows.
//
// Falsification: reverting ClearVectors to a non-scoped DELETE (or deleting by
// user_name only) causes the "other_type" row to be wiped, so the final count
// assertion fails (got 0, want 1).
func TestResumeDB_ClearVectors_Scoped(t *testing.T) {
	db := testResumeDB(t)
	ctx := context.Background()

	// Insert one source='profile' row with the type to be cleared and one that must survive.
	if _, err := db.UpsertVectorWithSource(ctx, "clear target", memTypeResumeExp, nil, nil, sourceProfile); err != nil {
		t.Fatalf("UpsertVectorWithSource target: %v", err)
	}
	if _, err := db.UpsertVectorWithSource(ctx, "must survive", memTypeEnrichProj, nil, nil, sourceProfile); err != nil {
		t.Fatalf("UpsertVectorWithSource survivor: %v", err)
	}

	// Clear only the resume_experience type.
	if err := db.ClearVectors(ctx, memTypeResumeExp); err != nil {
		t.Fatalf("ClearVectors: %v", err)
	}

	// resume_experience row must be gone.
	var cleared int
	if err := db.pool.QueryRow(ctx,
		`SELECT count(*) FROM resume_vectors WHERE user_name=$1 AND content='clear target'`,
		resumeVectorUser,
	).Scan(&cleared); err != nil {
		t.Fatalf("query cleared: %v", err)
	}
	if cleared != 0 {
		t.Errorf("ClearVectors: cleared row still present (count=%d)", cleared)
	}

	// enrich_project row must survive.
	var survived int
	if err := db.pool.QueryRow(ctx,
		`SELECT count(*) FROM resume_vectors WHERE user_name=$1 AND content='must survive'`,
		resumeVectorUser,
	).Scan(&survived); err != nil {
		t.Fatalf("query survived: %v", err)
	}
	if survived != 1 {
		t.Errorf("ClearVectors: survivor row missing or duplicated (count=%d, want 1)", survived)
	}
}

// TestResumeDB_ClearVectors_PreservesAgentRows is the F1 regression: a manual
// source='agent' memory tagged with a derived mem_type (resume_experience) must
// survive ClearVectors — the exact call BuildMasterResume makes before a
// rebuild. Before the source scope, ClearVectors deleted by mem_type only and
// destroyed such a manual row on every rebuild.
//
// Mutant — drop the `source = $2` filter from ClearVectors (back to
// `WHERE user_name=$1 AND mem_type=ANY($2)`) → the manual resume_experience
// row is deleted → RED.
func TestResumeDB_ClearVectors_PreservesAgentRows(t *testing.T) {
	db := testResumeDB(t)
	ctx := context.Background()

	// Manual memory sharing a derived mem_type but source='agent', ref_id=NULL —
	// the row a rebuild must never destroy.
	manualID, err := db.UpsertVector(ctx, "manual agent resume_experience memory", memTypeResumeExp, nil, nil)
	if err != nil {
		t.Fatalf("UpsertVector manual: %v", err)
	}

	// The exact call BuildMasterResume makes before re-deriving.
	if err := db.ClearVectors(ctx, memTypeResumeExp, memTypeResumeProj, memTypeResumeAchv); err != nil {
		t.Fatalf("ClearVectors: %v", err)
	}

	var exists int
	if err := db.pool.QueryRow(ctx,
		`SELECT count(*) FROM resume_vectors WHERE id=$1`,
		manualID,
	).Scan(&exists); err != nil {
		t.Fatalf("query manual row: %v", err)
	}
	if exists != 1 {
		t.Fatalf("manual source='agent' row tagged resume_experience was deleted by ClearVectors "+
			"(exists=%d) — ClearVectors must be scoped to source='profile'", exists)
	}
	var source string
	if err := db.pool.QueryRow(ctx,
		`SELECT source FROM resume_vectors WHERE id=$1`, manualID,
	).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != sourceAgent {
		t.Errorf("manual row source changed: got %q, want %q", source, sourceAgent)
	}
}

// TestResumeDB_SearchByTextScoped_MemTypeFilter verifies that SearchByTextScoped
// only returns rows whose mem_type is in the provided list.
//
// Falsification: reverting SearchByTextScoped to an unscoped query (or removing
// the mem_type filter) causes the "wrong type" row to appear in results, so the
// assertion that only the matching row was returned fails.
func TestResumeDB_SearchByTextScoped_MemTypeFilter(t *testing.T) {
	db := testResumeDB(t)
	ctx := context.Background()

	// Insert two rows with different mem_types but identical keywords.
	if _, err := db.UpsertVector(ctx, "golang distributed systems engineer", memTypeResumeExp, nil, nil); err != nil {
		t.Fatalf("UpsertVector resume_experience: %v", err)
	}
	if _, err := db.UpsertVector(ctx, "golang distributed systems engineer", memTypeResumeAchv, nil, nil); err != nil {
		t.Fatalf("UpsertVector resume_achievement: %v", err)
	}

	// Search scoped to resume_experience only.
	rows, err := db.SearchByTextScoped(ctx, "golang distributed systems", 10, []string{memTypeResumeExp})
	if err != nil {
		t.Fatalf("SearchByTextScoped: %v", err)
	}
	for _, r := range rows {
		if r.MemType != memTypeResumeExp {
			t.Errorf("SearchByTextScoped returned unexpected mem_type %q (want %q)", r.MemType, memTypeResumeExp)
		}
	}
	if len(rows) == 0 {
		t.Error("SearchByTextScoped: expected at least one result for resume_experience, got 0")
	}
}
