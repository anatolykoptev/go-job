package jobs

// resumedb_master_variant_test.go — tests for the master/variant resume model
// (issue #388). Verifies:
//   - InsertPerson with IsMaster=true creates a master row
//   - GetMasterPersonID returns the master, not a variant with a higher id
//   - SetMasterPerson atomically demotes old master + promotes new
//   - CreateVariant inserts a variant with parent_id and is_master=false
//   - ClearMasterPerson deletes only the master, preserving variants
//   - GetMasterPersonIDChecked distinguishes no-master / master-exists / error
//
// All tests run against a real Postgres (gojob_test); they skip when
// DATABASE_URL is unset, using the same dbtest.RequireTestDB guard.

import (
	"context"
	"testing"
)

// insertTestMaster inserts a person as master and returns its id.
func insertTestMaster(t *testing.T, db *ResumeDB, name string) int {
	t.Helper()
	ctx := context.Background()
	id, err := db.InsertPerson(ctx, PersonRecord{Name: name, IsMaster: true})
	if err != nil {
		t.Fatalf("InsertPerson(master): %v", err)
	}
	return id
}

func TestGetMasterPersonID_NoMaster(t *testing.T) {
	db := testResumeDBClean(t)
	ctx := context.Background()

	// Insert a non-master person (variant-style, no is_master flag).
	pid, err := db.InsertPerson(ctx, PersonRecord{Name: "NonMaster"})
	if err != nil {
		t.Fatalf("InsertPerson: %v", err)
	}
	t.Cleanup(func() { _ = db.ClearPerson(ctx, pid) })

	if got := db.GetMasterPersonID(ctx); got != 0 {
		t.Fatalf("GetMasterPersonID: expected 0 (no master), got %d", got)
	}
}

func TestGetMasterPersonID_ReturnsMasterNotVariant(t *testing.T) {
	db := testResumeDBClean(t)
	ctx := context.Background()

	masterID := insertTestMaster(t, db, "Master Person")

	// Insert a variant with a higher id.
	variantID, err := db.CreateVariant(ctx, masterID, PersonRecord{Name: "Variant Person"})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}
	t.Cleanup(func() {
		_ = db.ClearPerson(ctx, variantID)
		_ = db.ClearPerson(ctx, masterID)
	})

	// GetMasterPersonID must return the master, not the variant (which has a higher id).
	if got := db.GetMasterPersonID(ctx); got != masterID {
		t.Fatalf("GetMasterPersonID: expected master %d, got %d", masterID, got)
	}
}

func TestSetMasterPerson_DemotesOldPromotesNew(t *testing.T) {
	db := testResumeDBClean(t)
	ctx := context.Background()

	oldMaster := insertTestMaster(t, db, "Old Master")
	newMaster, err := db.InsertPerson(ctx, PersonRecord{Name: "New Master"})
	if err != nil {
		t.Fatalf("InsertPerson: %v", err)
	}
	t.Cleanup(func() {
		_ = db.ClearPerson(ctx, newMaster)
		_ = db.ClearPerson(ctx, oldMaster)
	})

	if err := db.SetMasterPerson(ctx, newMaster); err != nil {
		t.Fatalf("SetMasterPerson: %v", err)
	}

	if got := db.GetMasterPersonID(ctx); got != newMaster {
		t.Fatalf("after SetMasterPerson: expected %d, got %d", newMaster, got)
	}

	// Old master must be demoted.
	var oldIsMaster bool
	err = db.pool.QueryRow(ctx, `SELECT is_master FROM resume_persons WHERE id = $1`, oldMaster).Scan(&oldIsMaster)
	if err != nil {
		t.Fatalf("query old master: %v", err)
	}
	if oldIsMaster {
		t.Fatal("old master should have is_master=false after SetMasterPerson")
	}
}

func TestSetMasterPerson_NotFound(t *testing.T) {
	db := testResumeDBClean(t)
	ctx := context.Background()

	err := db.SetMasterPerson(ctx, 999999)
	if err == nil {
		t.Fatal("SetMasterPerson with non-existent id should error")
	}
}

func TestCreateVariant_SetsParentAndIsMasterFalse(t *testing.T) {
	db := testResumeDBClean(t)
	ctx := context.Background()

	masterID := insertTestMaster(t, db, "Master For Variant")
	variantID, err := db.CreateVariant(ctx, masterID, PersonRecord{Name: "Variant", Email: "var@test.com"})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}
	t.Cleanup(func() {
		_ = db.ClearPerson(ctx, variantID)
		_ = db.ClearPerson(ctx, masterID)
	})

	var (
		isMaster bool
		parentID *int
	)
	err = db.pool.QueryRow(ctx,
		`SELECT is_master, parent_id FROM resume_persons WHERE id = $1`, variantID,
	).Scan(&isMaster, &parentID)
	if err != nil {
		t.Fatalf("query variant: %v", err)
	}
	if isMaster {
		t.Fatal("variant should have is_master=false")
	}
	if parentID == nil || *parentID != masterID {
		t.Fatalf("variant parent_id: expected %d, got %v", masterID, parentID)
	}
}

func TestClearMasterPerson_PreservesVariants(t *testing.T) {
	db := testResumeDBClean(t)
	ctx := context.Background()

	masterID := insertTestMaster(t, db, "Master To Clear")
	variantID, err := db.CreateVariant(ctx, masterID, PersonRecord{Name: "Variant To Preserve"})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}
	t.Cleanup(func() { _ = db.ClearPerson(ctx, variantID) })

	if err := db.ClearMasterPerson(ctx); err != nil {
		t.Fatalf("ClearMasterPerson: %v", err)
	}

	// Master should be gone.
	if got := db.GetMasterPersonID(ctx); got != 0 {
		t.Fatalf("after ClearMasterPerson: expected no master (0), got %d", got)
	}

	// Variant should still exist.
	var name string
	err = db.pool.QueryRow(ctx, `SELECT name FROM resume_persons WHERE id = $1`, variantID).Scan(&name)
	if err != nil {
		t.Fatalf("variant should still exist after ClearMasterPerson: %v", err)
	}
	if name != "Variant To Preserve" {
		t.Fatalf("variant name: expected 'Variant To Preserve', got %q", name)
	}
}

func TestGetMasterPersonIDChecked_States(t *testing.T) {
	db := testResumeDBClean(t)
	ctx := context.Background()

	// No master exists.
	exists, id, err := db.GetMasterPersonIDChecked(ctx)
	if err != nil || exists || id != 0 {
		t.Fatalf("no master: expected (false,0,nil), got (%v,%d,%v)", exists, id, err)
	}

	// Master exists.
	masterID := insertTestMaster(t, db, "Checked Master")
	t.Cleanup(func() { _ = db.ClearPerson(ctx, masterID) })

	exists, id, err = db.GetMasterPersonIDChecked(ctx)
	if err != nil || !exists || id != masterID {
		t.Fatalf("master exists: expected (true,%d,nil), got (%v,%d,%v)", masterID, exists, id, err)
	}
}
