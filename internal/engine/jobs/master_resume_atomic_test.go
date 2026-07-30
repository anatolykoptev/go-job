package jobs

// master_resume_atomic_test.go — falsification tests for the atomic-rebuild /
// replace-guard / deadline-guard changes to BuildMasterResume.
//
// Invariant under test: a master_resume_build call that fails, times out, or is
// cancelled must leave the existing profile byte-identical to what it was
// before the call, and a call that would destroy an existing profile must not
// happen by accident.
//
// Falsification (each test MUST go RED when the production change it guards is
// reverted — the RED step IS this check):
//
//   F1 — atomic rebuild — mutation: remove the transaction (run the write phase
//     on the pool) → the hook-induced failure happens AFTER ClearAllPersons and
//     InsertPerson have committed on the pool → the seeded profile is gone and
//     a partial new profile exists → assertProfileIntact fails → RED.
//
//   F2 — replace guard — mutation: drop the replace guard so a build proceeds
//     against an existing profile with replace=false → the stubbed build runs
//     to completion and replaces the profile → the err!=nil assertion fails AND
//     assertProfileIntact fails → RED.
//
//   F3 — deadline — mutation: remove the ctx.Err() check before the write phase
//     → under a pre-cancelled context the build reaches BeginTx(cancelled) which
//     fails with "begin tx: context canceled" instead of the guard's
//     "caller deadline exceeded before write phase" → the guard-firing
//     assertion fails → RED. (The transaction itself also prevents writes under
//     a cancelled context, so the nothing-written assertion still holds without
//     the check; the check's unique observable is the early abort with the
//     deadline cause, which is what this test asserts.)
//
// All tests run against a real Postgres (gojob_test); they skip when
// DATABASE_URL is unset, using the same dbtest.RequireTestDB guard as the rest
// of this package. The AGE graph extension is NOT required: graph writes are
// best-effort and ClearGraph failure is non-fatal, so the build runs without
// AGE (the local test cluster has pgvector but not AGE).

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubMasterResumeParseLLM is a callLLM replacement that returns a deterministic
// parsed resume (a NEW person distinct from any seeded profile) for the parse
// prompt and an empty enrichment object for the enrichment prompt, so the build
// proceeds through its write phase without a live LLM.
func stubMasterResumeParseLLM(_ context.Context, prompt string) (string, error) {
	if strings.Contains(prompt, "expert resume parser") {
		return `{
		  "person": {"name":"Rebuilt Person","email":"","phone":"","location":"","links":{},"summary":""},
		  "experiences": [{"title":"Engineer","company":"NewCo","location":"","start_date":"2020","end_date":"2021","description":"did stuff","highlights":[],"skills":["Go"],"domain":"","team_size":null,"budget_usd":null,"is_volunteer":false,"sub_projects":[]}],
		  "educations": [],
		  "skills": [{"name":"Go","category":"programming_language","level":"expert","is_implicit":false,"source":"resume"}],
		  "projects": [],
		  "achievements": [],
		  "certifications": [],
		  "domains": [],
		  "methodologies": []
		}`, nil
	}
	return `{}`, nil // enrichment: empty object → no enrichment applied
}

// testResumeDBClean returns a ResumeDB on a clean gojob_test slate: every
// resume_persons row (and its ON DELETE CASCADE children) and every
// resume_vectors row for the test user is removed. It publishes the DB as the
// package-level resumeDB so BuildMasterResume (which reads GetResumeDB()) sees
// it. Uses the same dbtest.RequireTestDB guard as testResumeDB.
func testResumeDBClean(t *testing.T) *ResumeDB {
	t.Helper()
	db := testResumeDB(t)
	SetResumeDB(db)
	t.Cleanup(func() { SetResumeDB(nil) })
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `DELETE FROM resume_vectors WHERE user_name = $1`, resumeVectorUser); err != nil {
		t.Fatalf("purge resume_vectors: %v", err)
	}
	if err := db.ClearAllPersons(ctx); err != nil {
		t.Fatalf("purge resume_persons: %v", err)
	}
	// Best-effort graph clear; AGE may be absent on the test cluster.
	_ = db.ClearGraph(ctx)
	return db
}

// seedProfile inserts a small, recognizable profile (1 person + 1 skill + 1
// project + 1 experience + 1 achievement) and returns the person id. The
// person name is unique so tests can detect whether the seeded person survived.
func seedProfile(t *testing.T, db *ResumeDB) int {
	t.Helper()
	ctx := context.Background()
	pid, err := db.InsertPerson(ctx, PersonRecord{Name: "Seeded Person"})
	if err != nil {
		t.Fatalf("seed InsertPerson: %v", err)
	}
	if _, err := db.InsertSkillExtended(ctx, pid, SkillRecord{Name: "Seeded Skill", Category: "tool", Level: "expert", Source: "resume"}); err != nil {
		t.Fatalf("seed InsertSkillExtended: %v", err)
	}
	if _, err := db.InsertProject(ctx, pid, ProjectRecord{Name: "Seeded Project"}); err != nil {
		t.Fatalf("seed InsertProject: %v", err)
	}
	if _, err := db.InsertExperience(ctx, pid, ExperienceRecord{Title: "Seeded Role", Company: "SeededCo"}); err != nil {
		t.Fatalf("seed InsertExperience: %v", err)
	}
	if _, err := db.InsertAchievementExtended(ctx, pid, AchievementRecord{Text: "Seeded achievement"}); err != nil {
		t.Fatalf("seed InsertAchievementExtended: %v", err)
	}
	return pid
}

// profileSnapshot captures the committed profile state the invariant cares
// about: total person count, the seeded person's name (looked up by id), and
// per-entity counts for the seeded person.
type profileSnapshot struct {
	personCount  int
	seededName   string
	skills       int
	projects     int
	experiences  int
	achievements int
}

func snapshotProfile(t *testing.T, db *ResumeDB, seededID int) profileSnapshot {
	t.Helper()
	ctx := context.Background()
	var snap profileSnapshot
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM resume_persons`).Scan(&snap.personCount); err != nil {
		t.Fatalf("snapshot person count: %v", err)
	}
	var name string
	if err := db.pool.QueryRow(ctx, `SELECT name FROM resume_persons WHERE id = $1`, seededID).Scan(&name); err != nil {
		t.Fatalf("snapshot seeded person name: %v", err)
	}
	snap.seededName = name
	if v, err := db.GetAllSkills(ctx, seededID); err == nil {
		snap.skills = len(v)
	}
	if v, err := db.GetAllProjects(ctx, seededID); err == nil {
		snap.projects = len(v)
	}
	if v, err := db.GetAllExperiences(ctx, seededID); err == nil {
		snap.experiences = len(v)
	}
	if v, err := db.GetAllAchievements(ctx, seededID); err == nil {
		snap.achievements = len(v)
	}
	return snap
}

// assertProfileIntact fails the test if the committed profile differs from the
// snapshot: the seeded person must still exist with the same name, the total
// person count must be unchanged (no new person from a rolled-back rebuild),
// and the seeded person's entity counts must match.
func assertProfileIntact(t *testing.T, db *ResumeDB, seededID int, want profileSnapshot) {
	t.Helper()
	ctx := context.Background()
	var gotCount int
	if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM resume_persons`).Scan(&gotCount); err != nil {
		t.Fatalf("verify person count: %v", err)
	}
	if gotCount != want.personCount {
		t.Errorf("profile damaged: person count = %d, want %d (a rolled-back rebuild must not add or remove persons)",
			gotCount, want.personCount)
	}
	var name string
	if err := db.pool.QueryRow(ctx, `SELECT name FROM resume_persons WHERE id = $1`, seededID).Scan(&name); err != nil {
		t.Fatalf("profile damaged: seeded person %d no longer exists: %v", seededID, err)
	}
	if name != want.seededName {
		t.Errorf("profile damaged: seeded person name = %q, want %q", name, want.seededName)
	}
	if v, _ := db.GetAllSkills(ctx, seededID); len(v) != want.skills {
		t.Errorf("profile damaged: skills = %d, want %d", len(v), want.skills)
	}
	if v, _ := db.GetAllProjects(ctx, seededID); len(v) != want.projects {
		t.Errorf("profile damaged: projects = %d, want %d", len(v), want.projects)
	}
	if v, _ := db.GetAllExperiences(ctx, seededID); len(v) != want.experiences {
		t.Errorf("profile damaged: experiences = %d, want %d", len(v), want.experiences)
	}
	if v, _ := db.GetAllAchievements(ctx, seededID); len(v) != want.achievements {
		t.Errorf("profile damaged: achievements = %d, want %d", len(v), want.achievements)
	}
}

// withStubbedLLM swaps the callLLM seam for the deterministic stub and restores
// it on cleanup, so the build's two LLM calls return canned JSON without a live
// LLM and without touching the network.
func withStubbedLLM(t *testing.T) {
	t.Helper()
	prev := callLLM
	callLLM = stubMasterResumeParseLLM
	t.Cleanup(func() { callLLM = prev })
}

// F1 — atomic rebuild: a failure partway through the write phase must leave the
// pre-call profile byte-identical. The write hook forces a failure right after
// the new person is inserted (inside the transaction); with the transaction the
// rollback discards the clear+insert, with the transaction removed the clear
// and insert have already committed on the pool and the seeded profile is gone.
func TestBuildMasterResume_F1_AtomicRebuild_RollbackOnFailure(t *testing.T) {
	db := testResumeDBClean(t)
	seededID := seedProfile(t, db)
	want := snapshotProfile(t, db, seededID)
	if want.personCount != 1 {
		t.Fatalf("test setup: expected 1 person after seed, got %d", want.personCount)
	}

	withStubbedLLM(t)

	prevHook := masterResumeWriteHook
	masterResumeWriteHook = func() error { return errors.New("injected partway failure") }
	t.Cleanup(func() { masterResumeWriteHook = prevHook })

	ctx := context.Background()
	_, err := BuildMasterResume(ctx, "dummy resume text", true)
	if err == nil {
		t.Fatal("F1: expected the build to fail (injected hook error), got nil")
	}
	if !strings.Contains(err.Error(), "injected partway failure") {
		t.Errorf("F1: error did not carry the injected cause: %v", err)
	}

	assertProfileIntact(t, db, seededID, want)
}

// F2 — replace guard: when a profile already exists and replace is false, the
// call must refuse and name what would be destroyed, leaving the profile
// intact. Dropping the guard lets the stubbed build run to completion and
// replace the profile → err==nil and the profile is damaged → RED.
func TestBuildMasterResume_F2_ReplaceGuard_RefusesWithoutConsent(t *testing.T) {
	db := testResumeDBClean(t)
	seededID := seedProfile(t, db)
	want := snapshotProfile(t, db, seededID)

	withStubbedLLM(t)

	ctx := context.Background()
	_, err := BuildMasterResume(ctx, "dummy resume text", false) // replace=false
	if err == nil {
		t.Fatal("F2: expected a refuse error (profile exists, replace=false), got nil — " +
			"the replace guard is missing and a second run would silently destroy the profile")
	}
	if !strings.Contains(err.Error(), "profile already exists") || !strings.Contains(err.Error(), "replace=true") {
		t.Errorf("F2: refuse error must name the existing profile and instruct replace=true, got: %v", err)
	}

	assertProfileIntact(t, db, seededID, want)
}

// F3 — deadline: under a pre-cancelled context the build must abort before the
// write phase with the deadline cause, writing nothing. Removing the ctx.Err()
// check lets the build reach BeginTx(cancelled) which fails with a different
// ("begin tx: context canceled") error → the guard-firing assertion fails →
// RED. (The transaction also prevents writes under a cancelled context, so the
// nothing-written assertion holds either way; the check's unique observable is
// the early abort with the deadline cause.)
func TestBuildMasterResume_F3_Deadline_AbortsBeforeWritePhase(t *testing.T) {
	db := testResumeDBClean(t)
	seededID := seedProfile(t, db)
	want := snapshotProfile(t, db, seededID)

	withStubbedLLM(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // caller already gone before the call

	_, err := BuildMasterResume(ctx, "dummy resume text", true) // replace=true to bypass the guard
	if err == nil {
		t.Fatal("F3: expected an error under a cancelled context, got nil")
	}
	if !strings.Contains(err.Error(), "caller deadline exceeded before write phase") {
		t.Errorf("F3: error must be the deadline guard (\"caller deadline exceeded before write phase\"), "+
			"got: %v — the ctx.Err() check before the write phase is missing", err)
	}

	assertProfileIntact(t, db, seededID, want)
}
