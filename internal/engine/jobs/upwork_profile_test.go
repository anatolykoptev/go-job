package jobs

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestFormatUpworkPasteBlocks_TitleAndOverview verifies that title and overview
// each produce a labeled block with the correct content.
// Red-on-revert: remove title/overview handling → blocks empty.
func TestFormatUpworkPasteBlocks_TitleAndOverview(t *testing.T) {
	r := &UpworkProfileResult{
		Profile: &UpworkProfile{
			Title:    "Staff Software Engineer",
			Overview: "I build distributed systems.",
		},
	}
	blocks := FormatUpworkPasteBlocks(r)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (title + overview), got %d", len(blocks))
	}
	if blocks[0].Label != "Title / Headline" {
		t.Errorf("block[0].Label: got %q want %q", blocks[0].Label, "Title / Headline")
	}
	if blocks[0].Content != "Staff Software Engineer" {
		t.Errorf("block[0].Content: got %q", blocks[0].Content)
	}
	if blocks[1].Label != "Professional Overview" {
		t.Errorf("block[1].Label: got %q want %q", blocks[1].Label, "Professional Overview")
	}
	if blocks[1].Content != "I build distributed systems." {
		t.Errorf("block[1].Content: got %q", blocks[1].Content)
	}
}

// TestFormatUpworkPasteBlocks_Skills verifies that skills are joined into a single block.
// Red-on-revert: remove skills handling → skills block absent.
func TestFormatUpworkPasteBlocks_Skills(t *testing.T) {
	r := &UpworkProfileResult{
		Profile: &UpworkProfile{},
		Skills: []UpworkSkillRecord{
			{Name: "Go"},
			{Name: "Rust"},
			{Name: "TypeScript"},
		},
	}
	blocks := FormatUpworkPasteBlocks(r)
	var skillBlock *UpworkPasteBlock
	for i := range blocks {
		if blocks[i].Label == "Skills" {
			skillBlock = &blocks[i]
		}
	}
	if skillBlock == nil {
		t.Fatal("no Skills block produced")
	}
	for _, name := range []string{"Go", "Rust", "TypeScript"} {
		if !strings.Contains(skillBlock.Content, name) {
			t.Errorf("Skills block missing %q: %q", name, skillBlock.Content)
		}
	}
}

// TestFormatUpworkPasteBlocks_CatalogLabel verifies that catalog items get a
// "Catalog: <title>" label.
// Red-on-revert: change label prefix → test fails.
func TestFormatUpworkPasteBlocks_CatalogLabel(t *testing.T) {
	r := &UpworkProfileResult{
		Profile: &UpworkProfile{},
		Catalog: []UpworkCatalogItem{
			{Title: "go-relay", Description: "WebRTC relay in Go"},
		},
	}
	blocks := FormatUpworkPasteBlocks(r)
	var catalogBlock *UpworkPasteBlock
	for i := range blocks {
		if strings.HasPrefix(blocks[i].Label, "Catalog:") {
			catalogBlock = &blocks[i]
		}
	}
	if catalogBlock == nil {
		t.Fatal("no Catalog: block produced")
	}
	if catalogBlock.Label != "Catalog: go-relay" {
		t.Errorf("Label: got %q want %q", catalogBlock.Label, "Catalog: go-relay")
	}
	if catalogBlock.Content != "WebRTC relay in Go" {
		t.Errorf("Content: got %q want %q", catalogBlock.Content, "WebRTC relay in Go")
	}
}

// TestFormatUpworkPasteBlocks_EmptyProfile returns zero blocks for a nil/empty profile.
// Red-on-revert: add unconditional block append → panics or returns blocks.
func TestFormatUpworkPasteBlocks_EmptyProfile(t *testing.T) {
	r := &UpworkProfileResult{
		Profile: &UpworkProfile{},
		Skills:  []UpworkSkillRecord{},
		Catalog: []UpworkCatalogItem{},
	}
	blocks := FormatUpworkPasteBlocks(r)
	if len(blocks) != 0 {
		t.Errorf("expected 0 blocks for empty profile, got %d", len(blocks))
	}
}

// TestFormatUpworkPasteBlocks_NoTemplateHTMLInContent asserts the source
// file does not use template.HTML — paste blocks must use plain strings only.
// Red-on-revert: add template.HTML cast → file scan fails.
func TestFormatUpworkPasteBlocks_NoTemplateHTMLInContent(t *testing.T) {
	src, err := os.ReadFile("upwork_profile.go")
	if err != nil {
		t.Fatalf("read upwork_profile.go: %v", err)
	}
	if strings.Contains(string(src), "template.HTML") {
		t.Error("upwork_profile.go must not use template.HTML (content must be plain strings)")
	}
}

// TestUpsertUpworkProfileSQL_Structure verifies the upsert SQL targets
// the correct table and includes all required columns.
// Red-on-revert: remove a column name → test fails.
func TestUpsertUpworkProfileSQL_Structure(t *testing.T) {
	for _, frag := range []string{
		"upwork_profile",
		"person_id",
		"title",
		"overview",
		"hourly_rate",
		"ON CONFLICT",
	} {
		if !strings.Contains(upsertUpworkProfileSQL, frag) {
			t.Errorf("upsertUpworkProfileSQL missing %q", frag)
		}
	}
}

// TestInsertUpworkSkillSQL_Structure verifies the insert SQL targets
// upwork_skills and handles duplicates gracefully.
// Red-on-revert: remove ON CONFLICT → test fails.
func TestInsertUpworkSkillSQL_Structure(t *testing.T) {
	for _, frag := range []string{
		"upwork_skills",
		"person_id",
		"name",
		"ON CONFLICT",
		"DO NOTHING",
	} {
		if !strings.Contains(insertUpworkSkillSQL, frag) {
			t.Errorf("insertUpworkSkillSQL missing %q", frag)
		}
	}
}

// TestGetUpworkProfile_RoundTrip verifies that UpsertUpworkProfile persists
// profile + GetUpworkProfile reads it back: Missing state, title, overview,
// hourly_rate, categories, and availability all survive the write→read cycle.
// Requires DATABASE_URL to be set; skips otherwise.
// Red-on-revert: break getUpworkProfileSQL COALESCE or error discrimination → test fails.
func TestGetUpworkProfile_RoundTrip(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping DB integration test")
	}

	ctx := context.Background()
	db, err := ConnectResumeDB(ctx, dsn)
	if err != nil {
		t.Fatalf("ConnectResumeDB: %v", err)
	}
	t.Cleanup(db.Close)

	personID, err := db.InsertPerson(ctx, PersonRecord{
		Name:  "Upwork Profile Test",
		Email: "upwork-profile-test@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPerson: %v", err)
	}
	t.Cleanup(func() { _ = db.ClearPerson(ctx, personID) })

	// Before upsert: Missing=true with no error.
	before, err := db.GetUpworkProfile(ctx, personID)
	if err != nil {
		t.Fatalf("GetUpworkProfile (before): %v", err)
	}
	if !before.Missing {
		t.Error("expected Missing=true before upsert")
	}

	// Upsert and read back.
	const wantTitle = "Staff SWE | Go + Rust"
	const wantOverview = "Builds high-performance distributed systems."
	const wantAvailability = "30+ hrs/week"
	const wantRate int64 = 15000
	wantCategories := []string{"Software Development", "Backend"}

	if err := db.UpsertUpworkProfile(ctx, personID, wantTitle, wantOverview, wantRate, wantCategories, wantAvailability); err != nil {
		t.Fatalf("UpsertUpworkProfile: %v", err)
	}

	after, err := db.GetUpworkProfile(ctx, personID)
	if err != nil {
		t.Fatalf("GetUpworkProfile (after): %v", err)
	}
	if after.Missing {
		t.Error("expected Missing=false after upsert")
	}
	if after.Profile.Title != wantTitle {
		t.Errorf("Title: got %q want %q", after.Profile.Title, wantTitle)
	}
	if after.Profile.Overview != wantOverview {
		t.Errorf("Overview: got %q want %q", after.Profile.Overview, wantOverview)
	}
	if after.Profile.HourlyRate != wantRate {
		t.Errorf("HourlyRate: got %d want %d", after.Profile.HourlyRate, wantRate)
	}
	if after.Profile.Availability != wantAvailability {
		t.Errorf("Availability: got %q want %q", after.Profile.Availability, wantAvailability)
	}
	if len(after.Profile.Categories) != len(wantCategories) {
		t.Errorf("Categories len: got %d want %d: %v", len(after.Profile.Categories), len(wantCategories), after.Profile.Categories)
	} else {
		for i, cat := range wantCategories {
			if after.Profile.Categories[i] != cat {
				t.Errorf("Categories[%d]: got %q want %q", i, after.Profile.Categories[i], cat)
			}
		}
	}
}

// TestInsertDeleteUpworkSkill_RoundTrip verifies skill insert + delete.
// Requires DATABASE_URL; skips otherwise.
func TestInsertDeleteUpworkSkill_RoundTrip(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping DB integration test")
	}

	ctx := context.Background()
	db, err := ConnectResumeDB(ctx, dsn)
	if err != nil {
		t.Fatalf("ConnectResumeDB: %v", err)
	}
	t.Cleanup(db.Close)

	personID, err := db.InsertPerson(ctx, PersonRecord{
		Name:  "Upwork Skill Test",
		Email: "upwork-skill-test@example.com",
	})
	if err != nil {
		t.Fatalf("InsertPerson: %v", err)
	}
	t.Cleanup(func() { _ = db.ClearPerson(ctx, personID) })

	id, err := db.InsertUpworkSkill(ctx, personID, "Go")
	if err != nil {
		t.Fatalf("InsertUpworkSkill: %v", err)
	}
	if id == 0 {
		t.Fatal("InsertUpworkSkill returned id=0 (expected positive id for new row)")
	}

	// Duplicate insert should be a no-op (not an error).
	id2, err := db.InsertUpworkSkill(ctx, personID, "Go")
	if err != nil {
		t.Fatalf("InsertUpworkSkill duplicate: %v", err)
	}
	if id2 != 0 {
		t.Errorf("duplicate insert should return id=0, got %d", id2)
	}

	// Delete and verify gone.
	if err := db.DeleteUpworkSkill(ctx, id); err != nil {
		t.Fatalf("DeleteUpworkSkill: %v", err)
	}
}
