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

	if got := db.GetMasterPersonID(ctx, nil); got != 0 {
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
	if got := db.GetMasterPersonID(ctx, nil); got != masterID {
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

	if got := db.GetMasterPersonID(ctx, nil); got != newMaster {
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

	if err := db.ClearMasterPerson(ctx, nil); err != nil {
		t.Fatalf("ClearMasterPerson: %v", err)
	}

	// Master should be gone.
	if got := db.GetMasterPersonID(ctx, nil); got != 0 {
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
	exists, id, err := db.GetMasterPersonIDChecked(ctx, nil)
	if err != nil || exists || id != 0 {
		t.Fatalf("no master: expected (false,0,nil), got (%v,%d,%v)", exists, id, err)
	}

	// Master exists.
	masterID := insertTestMaster(t, db, "Checked Master")
	t.Cleanup(func() { _ = db.ClearPerson(ctx, masterID) })

	exists, id, err = db.GetMasterPersonIDChecked(ctx, nil)
	if err != nil || !exists || id != masterID {
		t.Fatalf("master exists: expected (true,%d,nil), got (%v,%d,%v)", masterID, exists, id, err)
	}
}

// F1 — re-parent is covered through the build path. BuildMasterResume must
// re-parent the old master's variant children to the new master through the
// REAL code path (not a re-implementation of the SQL via db.pool.Exec).
// Falsification: delete the re-parent statement in BuildMasterResume → the
// variant's parent_id stays NULL after the build → RED.
func TestBuildMasterResume_F1_ReparentsOrphansThroughBuildPath(t *testing.T) {
	db := testResumeDBClean(t)
	ctx := context.Background()

	// Seed a master + a variant child.
	masterID := insertTestMaster(t, db, "Old Master")
	variantID, err := db.CreateVariant(ctx, masterID, PersonRecord{Name: "Orphaned Variant"})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}

	withStubbedLLM(t)

	// Build with consent to replace the old master.
	result, err := BuildMasterResume(ctx, "dummy resume text", masterID)
	if err != nil {
		t.Fatalf("BuildMasterResume: %v", err)
	}
	newMasterID := result.PersonID

	// The variant must be re-parented to the new master through the build path.
	var parentID *int
	if err := db.pool.QueryRow(ctx, `SELECT parent_id FROM resume_persons WHERE id = $1`, variantID).Scan(&parentID); err != nil {
		t.Fatalf("F1: query variant: %v", err)
	}
	if parentID == nil || *parentID != newMasterID {
		t.Fatalf("F1: variant parent_id: expected new master %d, got %v — the re-parent statement did not run through BuildMasterResume", newMasterID, parentID)
	}
}

// F2 — re-parent is bounded to the deleted master's children. The re-parent
// must adopt ONLY the old master's children (captured before the delete), not
// any unrelated parentless non-master row. Falsification: restore the unbounded
// predicate (WHERE parent_id IS NULL AND is_master = false) → the unrelated
// row is adopted → its parent_id becomes the new master → RED.
func TestBuildMasterResume_F2_ReparentBoundedToDeletedMaster(t *testing.T) {
	db := testResumeDBClean(t)
	ctx := context.Background()

	masterID := insertTestMaster(t, db, "Old Master")
	variantID, err := db.CreateVariant(ctx, masterID, PersonRecord{Name: "Real Child"})
	if err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}
	// Unrelated parentless non-master row — NOT a child of the old master.
	unrelatedID, err := db.InsertPerson(ctx, PersonRecord{Name: "Unrelated Orphan"})
	if err != nil {
		t.Fatalf("InsertPerson unrelated: %v", err)
	}

	withStubbedLLM(t)

	result, err := BuildMasterResume(ctx, "dummy resume text", masterID)
	if err != nil {
		t.Fatalf("BuildMasterResume: %v", err)
	}
	newMasterID := result.PersonID

	// F1 (bounded): real child re-parented.
	var childParent *int
	if err := db.pool.QueryRow(ctx, `SELECT parent_id FROM resume_persons WHERE id = $1`, variantID).Scan(&childParent); err != nil {
		t.Fatalf("F2: query child: %v", err)
	}
	if childParent == nil || *childParent != newMasterID {
		t.Fatalf("F2: real child parent_id: expected %d, got %v", newMasterID, childParent)
	}

	// F2 (bounded): unrelated orphan NOT adopted.
	var unrelatedParent *int
	if err := db.pool.QueryRow(ctx, `SELECT parent_id FROM resume_persons WHERE id = $1`, unrelatedID).Scan(&unrelatedParent); err != nil {
		t.Fatalf("F2: query unrelated: %v", err)
	}
	if unrelatedParent != nil {
		t.Fatalf("F2: unrelated orphan was adopted (parent_id=%v) — the re-parent predicate is unbounded and adopts rows it was never given", unrelatedParent)
	}
}

// F3 — account parameter is load-bearing. ClearMasterPerson with a non-nil
// accountID must delete only that account's master, not cross-account.
// Falsification: ignore the accountID argument inside ClearMasterPerson → the
// other account's master is also deleted → RED.
func TestClearMasterPerson_F3_AccountScoped(t *testing.T) {
	db := testResumeDBClean(t)
	ctx := context.Background()

	// Two masters in different accounts.
	accA := "11111111-1111-1111-1111-111111111111"
	accB := "22222222-2222-2222-2222-222222222222"
	masterA, err := db.InsertPerson(ctx, PersonRecord{Name: "Master A", IsMaster: true, AccountID: &accA})
	if err != nil {
		t.Fatalf("insert A: %v", err)
	}
	masterB, err := db.InsertPerson(ctx, PersonRecord{Name: "Master B", IsMaster: true, AccountID: &accB})
	if err != nil {
		t.Fatalf("insert B: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.pool.Exec(ctx, `DELETE FROM resume_persons WHERE id IN ($1, $2)`, masterA, masterB)
	})

	// Clear account A's master only.
	if err := db.ClearMasterPerson(ctx, &accA); err != nil {
		t.Fatalf("ClearMasterPerson: %v", err)
	}

	// A's master gone.
	if got := db.GetMasterPersonID(ctx, &accA); got != 0 {
		t.Fatalf("account A master: expected 0 (deleted), got %d", got)
	}
	// B's master survives.
	if got := db.GetMasterPersonID(ctx, &accB); got != masterB {
		t.Fatalf("F3: account B master: expected %d (must survive), got %d — ClearMasterPerson ignored the accountID and deleted across accounts", masterB, got)
	}
}

// F5 — multiple masters refuse. GetMasterPersonIDChecked must return an error
// when more than one master exists in scope, rather than silently picking one
// (this value feeds the destructive-consent decision). Falsification: restore
// LIMIT 1 → one of the two masters is silently picked, no error → RED.
func TestGetMasterPersonIDChecked_F5_RefusesMultipleMasters(t *testing.T) {
	db := testResumeDBClean(t)
	ctx := context.Background()

	m1 := insertTestMaster(t, db, "Master One")
	m2 := insertTestMaster(t, db, "Master Two")
	t.Cleanup(func() {
		_, _ = db.pool.Exec(ctx, `DELETE FROM resume_persons WHERE id IN ($1, $2)`, m1, m2)
	})

	exists, id, err := db.GetMasterPersonIDChecked(ctx, nil)
	if err == nil {
		t.Fatalf("F5: expected an error with two masters, got nil (exists=%v, id=%d) — the guard silently picked one of several masters for a destructive decision", exists, id)
	}
}
