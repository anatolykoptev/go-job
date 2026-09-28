package jobs

import (
	"context"
	"testing"

	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newResumeTestAccount provisions a fresh panel_accounts row (Bootstrap is
// idempotent and installs the resume_*.account_id FK guards when
// panel_accounts exists) and returns the resume facade bound to it. Scoped
// writes need a real account: a random UUID would violate the FK, and
// uuid.Nil is refused by the facade itself.
func newResumeTestAccount(t *testing.T, db *ResumeDB) *ResumeAccount {
	t.Helper()
	ctx := context.Background()
	_, _, err := accounts.Bootstrap(ctx, db.Pool(), accounts.OperatorSeed{})
	require.NoError(t, err, "accounts.Bootstrap")
	aid, _, err := accounts.CreateAccount(ctx, db.Pool(),
		"jobs-test-"+uuid.NewString()[:12]+"@example.com", "jobs test", nil, "user")
	require.NoError(t, err, "accounts.CreateAccount")
	return db.ForAccount(aid)
}

// secondResumeTestAccount returns a facade bound to a DIFFERENT fresh account —
// the deny-matrix "account B".
func secondResumeTestAccount(t *testing.T, db *ResumeDB) *ResumeAccount {
	t.Helper()
	return newResumeTestAccount(t, db)
}

// TestPersonAccountIsolation covers the person hub: A's person is invisible
// to B across read, list-latest, update and delete paths.
func TestPersonAccountIsolation(t *testing.T) {
	db, rdbA := testResumeDB(t)
	rdbB := secondResumeTestAccount(t, db)
	ctx := context.Background()

	pidA, err := rdbA.InsertPerson(ctx, PersonRecord{Name: "Account A Person", Email: "a@iso.example"})
	require.NoError(t, err)
	pidB, err := rdbB.InsertPerson(ctx, PersonRecord{Name: "Account B Person", Email: "b@iso.example"})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = rdbA.ClearPerson(ctx, pidA)
		_ = rdbB.ClearPerson(ctx, pidB)
	})

	// Latest person is per-account — never global.
	assert.Equal(t, pidA, rdbA.GetLatestPersonID(ctx))
	assert.Equal(t, pidB, rdbB.GetLatestPersonID(ctx))

	// A's person reads as not-found under B.
	_, err = rdbB.GetPerson(ctx, pidA)
	assert.Error(t, err, "B must not read A's person")

	// B cannot mutate A's person (silent no-op).
	err = rdbB.UpdateResumePerson(ctx, pidA, PersonRecord{Name: "hijacked"})
	require.NoError(t, err)
	pA, err := rdbA.GetPerson(ctx, pidA)
	require.NoError(t, err)
	assert.Equal(t, "Account A Person", pA.Name, "B's update must not touch A's person")

	// B cannot delete A's person.
	require.NoError(t, rdbB.ClearPerson(ctx, pidA))
	_, err = rdbA.GetPerson(ctx, pidA)
	require.NoError(t, err, "A's person survives B's ClearPerson")
}

// TestPersonChildrenAccountIsolation covers child-table ownership: B cannot
// insert under A's person, read A's child rows, or delete them.
func TestPersonChildrenAccountIsolation(t *testing.T) {
	db, rdbA := testResumeDB(t)
	rdbB := secondResumeTestAccount(t, db)
	ctx := context.Background()

	pidA, err := rdbA.InsertPerson(ctx, PersonRecord{Name: "A Child Owner"})
	require.NoError(t, err)
	pidB, err := rdbB.InsertPerson(ctx, PersonRecord{Name: "B Child Owner"})
	require.NoError(t, err)

	expA, err := rdbA.InsertExperience(ctx, pidA, ExperienceRecord{Title: "A Staff Eng", Company: "ACorp"})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = rdbA.ClearPerson(ctx, pidA)
		_ = rdbB.ClearPerson(ctx, pidB)
	})

	// B cannot write under A's person — the EXISTS guard yields ErrNoRows.
	_, err = rdbB.InsertExperience(ctx, pidA, ExperienceRecord{Title: "evil", Company: "evil"})
	assert.Error(t, err, "B must not insert under A's person")

	// B cannot read A's child row by id.
	_, err = rdbB.GetExperienceByID(ctx, expA)
	assert.Error(t, err, "B must not read A's experience")
	got, err := rdbB.GetExperiencesByIDs(ctx, []int{expA})
	require.NoError(t, err)
	assert.Empty(t, got, "B's bulk read must not return A's experience")

	// B cannot delete A's child row (silent no-op — row survives).
	require.NoError(t, rdbB.DeleteExperience(ctx, expA))
	_, err = rdbA.GetExperienceByID(ctx, expA)
	require.NoError(t, err, "A's experience survives B's delete")

	// A cannot see B's person in a GetAll read either.
	expsB, err := rdbB.GetAllExperiences(ctx, pidA)
	require.NoError(t, err)
	assert.Empty(t, expsB, "B reading A's person children gets empty")
}

// TestClearAllPersonsAccountIsolation proves ClearAllPersons wipes only the
// bound account's persons — B's data survives A's destructive rebuild.
func TestClearAllPersonsAccountIsolation(t *testing.T) {
	db, rdbA := testResumeDB(t)
	rdbB := secondResumeTestAccount(t, db)
	ctx := context.Background()

	_, err := rdbA.InsertPerson(ctx, PersonRecord{Name: "A Person"})
	require.NoError(t, err)
	pidB, err := rdbB.InsertPerson(ctx, PersonRecord{Name: "B Person"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rdbB.ClearPerson(ctx, pidB) })

	require.NoError(t, rdbA.ClearAllPersons(ctx))
	assert.Zero(t, rdbA.GetLatestPersonID(ctx), "A's namespace is cleared")
	pB, err := rdbB.GetPerson(ctx, pidB)
	require.NoError(t, err, "B's person survives A's ClearAllPersons")
	assert.Equal(t, "B Person", pB.Name)
}

// TestVectorAccountIsolation covers the resume_vectors scope: A's rows are
// invisible to B's searches and counts; dedup is per-account so the same
// content under B is a distinct row.
func TestVectorAccountIsolation(t *testing.T) {
	db, rdbA := testResumeDB(t)
	rdbB := secondResumeTestAccount(t, db)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = db.pool.Exec(ctx, `DELETE FROM resume_vectors WHERE account_id IN ($1, $2)`,
			rdbA.AccountID(), rdbB.AccountID())
	})

	_, err := rdbA.UpsertVector(ctx, "account A secret career note", "note", nil)
	require.NoError(t, err)

	// B's FTS search must not surface A's row.
	rows, err := rdbB.SearchByText(ctx, "career note", 10)
	require.NoError(t, err)
	for _, r := range rows {
		assert.NotContains(t, r.Content, "account A", "B must never see A's vector content")
	}
	cnt, err := rdbB.CountVectors(ctx, "note")
	require.NoError(t, err)
	assert.Zero(t, cnt, "B's count excludes A's rows")

	// Same content under B is a distinct row — per-account dedup.
	_, err = rdbB.UpsertVector(ctx, "account A secret career note", "note", nil)
	require.NoError(t, err, "B may hold identical content independently")
	cntA, err := rdbA.CountVectors(ctx, "note")
	require.NoError(t, err)
	assert.Equal(t, 1, cntA)
	cntB, err := rdbB.CountVectors(ctx, "note")
	require.NoError(t, err)
	assert.Equal(t, 1, cntB)

	// B cannot delete A's derived rows.
	require.NoError(t, rdbB.DeleteDerivedVectorByID(ctx, "note", 0)) // no-op on foreign
}

// TestNilAccountFailsClosed proves the Nil-bound facade denies writes and
// reads back nothing — the fail-closed identity contract.
func TestNilAccountFailsClosed(t *testing.T) {
	db, _ := testResumeDB(t)
	nilRDB := db.ForAccount(uuid.Nil)
	ctx := context.Background()

	_, err := nilRDB.InsertPerson(ctx, PersonRecord{Name: "nobody"})
	assert.ErrorIs(t, err, ErrNoAccountScope)
	_, err = nilRDB.UpsertVector(ctx, "x", "note", nil)
	assert.ErrorIs(t, err, ErrNoAccountScope)
	assert.ErrorIs(t, nilRDB.ClearAllPersons(ctx), ErrNoAccountScope)
	assert.ErrorIs(t, nilRDB.ClearGraph(ctx), ErrNoAccountScope)
	assert.ErrorIs(t, nilRDB.ClearVectors(ctx), ErrNoAccountScope)
	assert.Zero(t, nilRDB.GetLatestPersonID(ctx))
}

// TestGraphAccountIsolation covers the AGE graph scope (plan ADR-8): A's
// nodes/edges are invisible to B's queries and B's ClearGraph cannot touch
// A's subgraph. Skips when Apache AGE is absent (pgtest has no ag_catalog).
func TestGraphAccountIsolation(t *testing.T) {
	db, rdbA := testResumeDB(t)
	rdbB := secondResumeTestAccount(t, db)
	ctx := context.Background()

	var ageNS *string
	require.NoError(t, db.pool.QueryRow(ctx,
		`SELECT to_regnamespace('ag_catalog')::text`).Scan(&ageNS))
	if ageNS == nil {
		t.Skip("Apache AGE absent (ag_catalog missing) — graph isolation not exercisable on this Postgres")
	}

	require.NoError(t, rdbA.UpsertGraphNode(ctx, "Skill", 900001, map[string]string{"name": "go"}))
	require.NoError(t, rdbB.UpsertGraphNode(ctx, "Skill", 900002, map[string]string{"name": "rust"}))
	t.Cleanup(func() {
		_ = rdbA.ClearGraph(ctx)
		_ = rdbB.ClearGraph(ctx)
	})

	// Each account counts only its own nodes.
	nA, err := rdbA.CountGraphNodes(ctx)
	require.NoError(t, err)
	nB, err := rdbB.CountGraphNodes(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, nA, 1)
	assert.GreaterOrEqual(t, nB, 1)

	// B's clear must not delete A's nodes.
	require.NoError(t, rdbB.ClearGraph(ctx))
	nA2, err := rdbA.CountGraphNodes(ctx)
	require.NoError(t, err)
	assert.Equal(t, nA, nA2, "B's ClearGraph must not touch A's subgraph")
	nB2, err := rdbB.CountGraphNodes(ctx)
	require.NoError(t, err)
	assert.Zero(t, nB2, "B's own subgraph is cleared")
}
