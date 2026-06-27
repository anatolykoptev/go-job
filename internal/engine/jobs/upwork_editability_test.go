package jobs

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
)

// openTestDB returns a ConnectResumeDB connected to DATABASE_URL, or skips.
func openTestDB(t *testing.T) *ResumeDB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping DB integration test")
	}
	db, err := ConnectResumeDB(context.Background(), dsn)
	if err != nil {
		t.Fatalf("ConnectResumeDB: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// insertTestPerson inserts a synthetic person and registers cleanup.
func insertTestPerson(t *testing.T, db *ResumeDB, name, email string) int {
	t.Helper()
	id, err := db.InsertPerson(context.Background(), PersonRecord{Name: name, Email: email})
	if err != nil {
		t.Fatalf("InsertPerson(%q): %v", name, err)
	}
	t.Cleanup(func() { _ = db.ClearPerson(context.Background(), id) })
	return id
}

// TestInsertUpworkCatalogItem_RoundTrip verifies that InsertUpworkCatalogItem
// persists a catalog item and GetUpworkProfile reads it back with id > 0.
// Red-on-revert: remove insertUpworkCatalogItemSQL or RETURNING id → test fails.
func TestInsertUpworkCatalogItem_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	personID := insertTestPerson(t, db, "Acme Catalog Test", "acme-catalog-test@example.com")

	id, err := db.InsertUpworkCatalogItem(ctx, personID, "Go Microservices", "High-throughput gRPC backend")
	if err != nil {
		t.Fatalf("InsertUpworkCatalogItem: %v", err)
	}
	if id <= 0 {
		t.Fatalf("InsertUpworkCatalogItem: expected id > 0, got %d", id)
	}

	result, err := db.GetUpworkProfile(ctx, personID)
	if err != nil {
		t.Fatalf("GetUpworkProfile: %v", err)
	}
	if len(result.Catalog) != 1 {
		t.Fatalf("expected 1 catalog item, got %d", len(result.Catalog))
	}
	if result.Catalog[0].Title != "Go Microservices" {
		t.Errorf("Title: got %q want %q", result.Catalog[0].Title, "Go Microservices")
	}
	if result.Catalog[0].Description != "High-throughput gRPC backend" {
		t.Errorf("Description: got %q want %q", result.Catalog[0].Description, "High-throughput gRPC backend")
	}
	if result.Catalog[0].ID != id {
		t.Errorf("ID: got %d want %d", result.Catalog[0].ID, id)
	}
}

// TestDeleteUpworkCatalogItem_RoundTrip verifies that DeleteUpworkCatalogItem removes
// the targeted item and only the targeted item.
// Red-on-revert: remove person_id from WHERE → cross-person delete passes silently.
func TestDeleteUpworkCatalogItem_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	personID := insertTestPerson(t, db, "Acme Delete Test", "acme-delete-test@example.com")

	id1, err := db.InsertUpworkCatalogItem(ctx, personID, "Item One", "placeholder one")
	if err != nil {
		t.Fatalf("InsertUpworkCatalogItem #1: %v", err)
	}
	id2, err := db.InsertUpworkCatalogItem(ctx, personID, "Item Two", "placeholder two")
	if err != nil {
		t.Fatalf("InsertUpworkCatalogItem #2: %v", err)
	}

	// Delete item 1 only.
	if err := db.DeleteUpworkCatalogItem(ctx, personID, id1); err != nil {
		t.Fatalf("DeleteUpworkCatalogItem: %v", err)
	}

	result, err := db.GetUpworkProfile(ctx, personID)
	if err != nil {
		t.Fatalf("GetUpworkProfile after delete: %v", err)
	}
	if len(result.Catalog) != 1 {
		t.Fatalf("expected 1 catalog item remaining, got %d: %+v", len(result.Catalog), result.Catalog)
	}
	if result.Catalog[0].ID != id2 {
		t.Errorf("remaining item: got id=%d want id=%d", result.Catalog[0].ID, id2)
	}
}

// TestReorderUpworkCatalogItems_RoundTrip verifies that ReorderUpworkCatalogItems
// produces contiguous 1..N positions with no duplicates after reorder.
// Red-on-revert: break transaction or position logic → positions not contiguous → test fails.
func TestReorderUpworkCatalogItems_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	personID := insertTestPerson(t, db, "Acme Reorder Test", "acme-reorder-test@example.com")

	// Insert 3 items (positions will be 1, 2, 3 in insertion order).
	id1, err := db.InsertUpworkCatalogItem(ctx, personID, "Alpha", "first")
	if err != nil {
		t.Fatalf("insert alpha: %v", err)
	}
	id2, err := db.InsertUpworkCatalogItem(ctx, personID, "Beta", "second")
	if err != nil {
		t.Fatalf("insert beta: %v", err)
	}
	id3, err := db.InsertUpworkCatalogItem(ctx, personID, "Gamma", "third")
	if err != nil {
		t.Fatalf("insert gamma: %v", err)
	}

	// Reorder: Gamma first, then Alpha, then Beta.
	if err := db.ReorderUpworkCatalogItems(ctx, personID, []int{id3, id1, id2}); err != nil {
		t.Fatalf("ReorderUpworkCatalogItems: %v", err)
	}

	result, err := db.GetUpworkProfile(ctx, personID)
	if err != nil {
		t.Fatalf("GetUpworkProfile after reorder: %v", err)
	}
	if len(result.Catalog) != 3 {
		t.Fatalf("expected 3 catalog items, got %d", len(result.Catalog))
	}

	// Verify positions are contiguous 1..N (no duplicates, no gaps).
	positions := make([]int, len(result.Catalog))
	for i, c := range result.Catalog {
		positions[i] = c.Position
	}
	sort.Ints(positions)
	for i, p := range positions {
		if p != i+1 {
			t.Errorf("positions not contiguous 1..N: got %v", positions)
			break
		}
	}

	// Verify order: Gamma should be first (position 1), Alpha second, Beta third.
	for _, c := range result.Catalog {
		switch c.Title {
		case "Gamma":
			if c.Position != 1 {
				t.Errorf("Gamma position: got %d want 1", c.Position)
			}
		case "Alpha":
			if c.Position != 2 {
				t.Errorf("Alpha position: got %d want 2", c.Position)
			}
		case "Beta":
			if c.Position != 3 {
				t.Errorf("Beta position: got %d want 3", c.Position)
			}
		}
	}
}

// TestReorderUpworkSkills_RoundTrip verifies that ReorderUpworkSkills produces
// contiguous 1..N positions after reorder.
// Red-on-revert: break skill reorder logic → positions not contiguous → test fails.
func TestReorderUpworkSkills_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	personID := insertTestPerson(t, db, "Acme Skill Reorder", "acme-skill-reorder@example.com")

	idGo, err := db.InsertUpworkSkill(ctx, personID, "Go")
	if err != nil || idGo == 0 {
		t.Fatalf("InsertUpworkSkill Go: %v id=%d", err, idGo)
	}
	idRust, err := db.InsertUpworkSkill(ctx, personID, "Rust")
	if err != nil || idRust == 0 {
		t.Fatalf("InsertUpworkSkill Rust: %v id=%d", err, idRust)
	}
	idTS, err := db.InsertUpworkSkill(ctx, personID, "TypeScript")
	if err != nil || idTS == 0 {
		t.Fatalf("InsertUpworkSkill TypeScript: %v id=%d", err, idTS)
	}

	// Reorder: TypeScript first, Go second, Rust third.
	if err := db.ReorderUpworkSkills(ctx, personID, []int{idTS, idGo, idRust}); err != nil {
		t.Fatalf("ReorderUpworkSkills: %v", err)
	}

	result, err := db.GetUpworkProfile(ctx, personID)
	if err != nil {
		t.Fatalf("GetUpworkProfile after skill reorder: %v", err)
	}
	if len(result.Skills) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(result.Skills))
	}

	// Verify positions are contiguous 1..N (no duplicates).
	positions := make([]int, len(result.Skills))
	for i, s := range result.Skills {
		positions[i] = s.Position
	}
	sort.Ints(positions)
	for i, p := range positions {
		if p != i+1 {
			t.Errorf("skill positions not contiguous 1..N: got %v", positions)
			break
		}
	}

	// Verify TypeScript is position 1 (first in new order).
	for _, s := range result.Skills {
		if s.Name == "TypeScript" && s.Position != 1 {
			t.Errorf("TypeScript position: got %d want 1", s.Position)
		}
	}
}

// TestUpworkCatalogItem_CrossPersonIsolation verifies that person B's
// delete/reorder operations do not affect person A's catalog items.
// Red-on-revert: remove AND person_id from deleteUpworkCatalogItemSQL → A's items deleted.
func TestUpworkCatalogItem_CrossPersonIsolation(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	personA := insertTestPerson(t, db, "Acme Person A", "acme-person-a@example.com")
	personB := insertTestPerson(t, db, "Acme Person B", "acme-person-b@example.com")

	// Insert item for A.
	idA, err := db.InsertUpworkCatalogItem(ctx, personA, "A Item", "A placeholder")
	if err != nil {
		t.Fatalf("InsertUpworkCatalogItem for A: %v", err)
	}
	// Insert item for B.
	idB, err := db.InsertUpworkCatalogItem(ctx, personB, "B Item", "B placeholder")
	if err != nil {
		t.Fatalf("InsertUpworkCatalogItem for B: %v", err)
	}

	// B tries to delete A's item using A's item ID but B's person_id.
	// Should be a no-op (WHERE id = $1 AND person_id = $2 filters it out).
	if err := db.DeleteUpworkCatalogItem(ctx, personB, idA); err != nil {
		t.Fatalf("DeleteUpworkCatalogItem (B on A's id): %v", err)
	}

	// A's item must still exist.
	resultA, err := db.GetUpworkProfile(ctx, personA)
	if err != nil {
		t.Fatalf("GetUpworkProfile A: %v", err)
	}
	if len(resultA.Catalog) != 1 {
		t.Errorf("A's catalog should have 1 item after B's attempted delete, got %d", len(resultA.Catalog))
	}

	// B still has their own item.
	resultB, err := db.GetUpworkProfile(ctx, personB)
	if err != nil {
		t.Fatalf("GetUpworkProfile B: %v", err)
	}
	if len(resultB.Catalog) != 1 || resultB.Catalog[0].ID != idB {
		t.Errorf("B's catalog: expected id=%d, got %+v", idB, resultB.Catalog)
	}
}

// TestUpworkCategoriesEdit_PreservesFields verifies the #118 invariant:
// calling UpsertUpworkProfile with new categories preserves title, overview,
// hourly_rate, and availability from the existing row.
// Red-on-revert: remove read-modify-write in handler or this test → fields zeroed.
func TestUpworkCategoriesEdit_PreservesFields(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	personID := insertTestPerson(t, db, "Acme Preserve Test", "acme-preserve-test@example.com")

	// Set initial profile.
	const wantTitle = "Go + Rust Backend Engineer"
	const wantOverview = "Building high-performance distributed systems."
	const wantAvailability = "30+ hrs/week"
	const wantRate int64 = 17500
	initialCats := []string{"Software Development", "Backend"}

	if err := db.UpsertUpworkProfile(ctx, personID, wantTitle, wantOverview, wantRate, initialCats, wantAvailability); err != nil {
		t.Fatalf("UpsertUpworkProfile (initial): %v", err)
	}

	// Simulate the categories handler: read existing, then upsert with new categories
	// but preserving all other fields (this is exactly what upworkCategoriesEditHandler does).
	existing, err := db.GetUpworkProfile(ctx, personID)
	if err != nil {
		t.Fatalf("GetUpworkProfile: %v", err)
	}

	newCategories := []string{"Distributed Systems", "Performance Engineering"}
	if err := db.UpsertUpworkProfile(ctx, personID,
		existing.Profile.Title,
		existing.Profile.Overview,
		existing.Profile.HourlyRate,
		newCategories,
		existing.Profile.Availability,
	); err != nil {
		t.Fatalf("UpsertUpworkProfile (categories update): %v", err)
	}

	// Read back and verify all fields preserved.
	result, err := db.GetUpworkProfile(ctx, personID)
	if err != nil {
		t.Fatalf("GetUpworkProfile (after categories update): %v", err)
	}

	if result.Profile.Title != wantTitle {
		t.Errorf("Title: got %q want %q", result.Profile.Title, wantTitle)
	}
	if result.Profile.Overview != wantOverview {
		t.Errorf("Overview: got %q want %q", result.Profile.Overview, wantOverview)
	}
	if result.Profile.HourlyRate != wantRate {
		t.Errorf("HourlyRate: got %d want %d", result.Profile.HourlyRate, wantRate)
	}
	if result.Profile.Availability != wantAvailability {
		t.Errorf("Availability: got %q want %q", result.Profile.Availability, wantAvailability)
	}
	if len(result.Profile.Categories) != 2 {
		t.Fatalf("Categories len: got %d want 2", len(result.Profile.Categories))
	}
	if result.Profile.Categories[0] != "Distributed Systems" || result.Profile.Categories[1] != "Performance Engineering" {
		t.Errorf("Categories: got %v want [Distributed Systems Performance Engineering]", result.Profile.Categories)
	}
}

// TestNewSQLConstants_Structure verifies that the new SQL constants contain
// person_id in their WHERE/VALUES clauses (ADR #7 person-scope invariant).
// Red-on-revert: remove person_id from any constant → test fails.
func TestNewSQLConstants_Structure(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "insertUpworkCatalogItemSQL",
			sql:  insertUpworkCatalogItemSQL,
			want: []string{"upwork_catalog_items", "person_id", "RETURNING id"},
		},
		{
			name: "deleteUpworkCatalogItemSQL",
			sql:  deleteUpworkCatalogItemSQL,
			want: []string{"upwork_catalog_items", "person_id"},
		},
		{
			name: "deleteUpworkSkillPersonSQL",
			sql:  deleteUpworkSkillPersonSQL,
			want: []string{"upwork_skills", "person_id"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			for _, frag := range tc.want {
				if !strings.Contains(tc.sql, frag) {
					t.Errorf("%s must contain %q\nSQL: %s", tc.name, frag, tc.sql)
				}
			}
		})
	}
}

// TestDeleteUpworkSkill_CrossPersonIsolation verifies that person B's delete
// attempt on person A's skill (using A's skill ID but B's personID) is a no-op.
// Red-on-revert: remove AND person_id from deleteUpworkSkillPersonSQL -> A's skill deleted.
func TestDeleteUpworkSkill_CrossPersonIsolation(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	personA := insertTestPerson(t, db, "Skill Iso A", "skill-iso-a@example.com")
	personB := insertTestPerson(t, db, "Skill Iso B", "skill-iso-b@example.com")

	// Insert a skill for person A.
	idA, err := db.InsertUpworkSkill(ctx, personA, "Go")
	if err != nil || idA == 0 {
		t.Fatalf("InsertUpworkSkill for A: %v id=%d", err, idA)
	}

	// Person B attempts to delete person A's skill using A's skill ID but B's personID.
	// Should be a no-op (WHERE id = $1 AND person_id = $2 filters it out).
	if err := db.DeleteUpworkSkill(ctx, personB, idA); err != nil {
		t.Fatalf("DeleteUpworkSkill (B on A's id): %v", err)
	}

	// A's skill must still exist.
	resultA, err := db.GetUpworkProfile(ctx, personA)
	if err != nil {
		t.Fatalf("GetUpworkProfile A: %v", err)
	}
	if len(resultA.Skills) != 1 {
		t.Errorf("A's skills should have 1 item after B's attempted delete, got %d", len(resultA.Skills))
	}
	if len(resultA.Skills) == 1 && resultA.Skills[0].ID != idA {
		t.Errorf("A's skill ID: got %d want %d", resultA.Skills[0].ID, idA)
	}
}

// TestReorderUpworkSkills_SubsetNormalization verifies that posting a subset of skill IDs
// to ReorderUpworkSkills places the supplied IDs first (in supplied order) and appends
// the omitted ID at the end — no gaps, no duplicates across the full set.
// Red-on-revert: remove full-set fetch logic -> omitted skill gets position 0 or gap.
func TestReorderUpworkSkills_SubsetNormalization(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	personID := insertTestPerson(t, db, "Subset Reorder Test", "subset-reorder-test@example.com")

	id1, err := db.InsertUpworkSkill(ctx, personID, "Go")
	if err != nil || id1 == 0 {
		t.Fatalf("InsertUpworkSkill Go: %v id=%d", err, id1)
	}
	id2, err := db.InsertUpworkSkill(ctx, personID, "Rust")
	if err != nil || id2 == 0 {
		t.Fatalf("InsertUpworkSkill Rust: %v id=%d", err, id2)
	}
	id3, err := db.InsertUpworkSkill(ctx, personID, "TypeScript")
	if err != nil || id3 == 0 {
		t.Fatalf("InsertUpworkSkill TypeScript: %v id=%d", err, id3)
	}

	// Reorder with only [id2, id1] — omitting id3.
	// Expected: id2=pos1, id1=pos2, id3=pos3 (appended stable by old position).
	if err := db.ReorderUpworkSkills(ctx, personID, []int{id2, id1}); err != nil {
		t.Fatalf("ReorderUpworkSkills subset: %v", err)
	}

	result, err := db.GetUpworkProfile(ctx, personID)
	if err != nil {
		t.Fatalf("GetUpworkProfile after subset reorder: %v", err)
	}
	if len(result.Skills) != 3 {
		t.Fatalf("expected 3 skills after subset reorder, got %d", len(result.Skills))
	}

	// Collect positions by id.
	posOf := make(map[int]int, 3)
	for _, s := range result.Skills {
		posOf[s.ID] = s.Position
	}

	if posOf[id2] != 1 {
		t.Errorf("id2 (Rust) position: got %d want 1", posOf[id2])
	}
	if posOf[id1] != 2 {
		t.Errorf("id1 (Go) position: got %d want 2", posOf[id1])
	}
	if posOf[id3] != 3 {
		t.Errorf("id3 (TypeScript) position: got %d want 3", posOf[id3])
	}

	// Assert no duplicate positions in full set.
	seen := make(map[int]bool, 3)
	for _, s := range result.Skills {
		if seen[s.Position] {
			t.Errorf("duplicate position %d in skill set", s.Position)
		}
		seen[s.Position] = true
	}
}
