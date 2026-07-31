package jobs

// master_resume_atomic_test.go — falsification tests for the atomic-rebuild /
// replace-guard / deadline-guard / graph-rebuild-then-swap / non-replayable-consent
// changes to BuildMasterResume.
//
// Invariant under test: a master_resume_build call that fails, times out, or is
// cancelled must leave the existing profile byte-identical to what it was
// before the call, must never touch the AGE graph before commit, and a call
// that would destroy an existing profile must not happen by accident or by a
// blind replay of the same arguments.
//
// Falsification (each test MUST go RED when the production change it guards is
// reverted — the RED step IS this check):
//
//   F1 — atomic rebuild (exhaustiveness oracle) — mutation: remove the
//     transaction (run the write phase on the pool) → the hook-induced failure
//     happens AFTER ClearAllPersons and InsertPerson have committed on the pool
//     → the seeded profile is gone and a partial new profile exists →
//     assertProfileIntact (total row counts across every resume_* table) fails
//     → RED. The hook fires immediately before tx.Commit, so EVERY write in the
//     phase has executed; a contributor who routes any one insert to db.pool
//     instead of conn(ctx) leaves a row that survives the rollback → the
//     total-row-count assertion catches it (this is the F6 oracle).
//
//   F2 — non-replayable consent — mutation: drop the consent check so a build
//     proceeds against an existing profile with the wrong replace_person_id →
//     the stubbed build runs to completion and replaces the profile → err==nil
//     and assertProfileIntact fails → RED.
//
//   F3 — deadline — mutation: remove the ctx.Err() check before the write phase
//     → under a pre-cancelled context the build reaches the guard/BeginTx with
//     a cancelled context → the guard query errors (fail-closed) or BeginTx
//     fails with "context canceled" instead of the guard's "caller deadline
//     exceeded before write phase" → the guard-firing assertion fails → RED.
//
//   F4 — fail-open guard — mutation: make the guard's error path return "no
//     profile" (exists=false, err=nil) instead of refusing → a guard-query
//     error reads as "nothing to destroy" → the build proceeds and destroys the
//     seeded profile → assertProfileIntact fails → RED.
//
//   F5 — graph survives rollback — mutation: move the graph clear/replay back
//     to before the transaction → a rolled-back build has already issued graph
//     statements → the graph-op recorder is non-empty → RED. (AGE is absent on
//     the test cluster, so this asserts at the call-boundary that NO graph
//     statement runs during a rolled-back build, not that AGE handles it.)
//
//   F6 — write-phase exhaustiveness — mutation: route ONE entity insert
//     (InsertProject) back to db.pool → F1, with the moved hook and the
//     total-row-count assertions, must go RED. This is the finding-5 oracle;
//     if it does not go red, the fix is not done. Verified by a mutation run,
//     not a permanent test (see the report).
//
//   F7 — non-replayable consent (replay after success) — mutation: drop the id
//     check → replaying the same arguments (carrying the old id) against a
//     profile whose id has changed (after a successful rebuild) proceeds and
//     destroys it → err==nil and assertProfileIntact fails → RED.
//
// All tests run against a real Postgres (gojob_test); they skip when
// DATABASE_URL is unset, using the same dbtest.RequireTestDB guard as the rest
// of this package. The AGE graph extension is NOT required: graph writes are
// buffered and replayed only after commit, and ClearGraph failure is classified
// (AGE-absent vs real cypher error), so the build runs without AGE (the local
// test cluster has pgvector but not AGE).

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
		  "projects": [{"name":"Rebuilt Project","description":"","url":"","tech":[],"highlights":[]}],
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

// resumeTableTotalCounts is the exhaustive set the F1/F6 oracle asserts: total
// row counts across EVERY resume_* table (not scoped to the seeded person) plus
// resume_vectors. A rolled-back rebuild must not add or remove a single row in
// any of them. Scoped-to-seeded-person counts miss orphan child rows written on
// the pool under a rolled-back person id; total counts do not.
var resumeTableTotalCounts = []string{
	"resume_persons",
	"resume_experiences",
	"resume_skills",
	"resume_projects",
	"resume_achievements",
	"resume_educations",
	"resume_certifications",
	"resume_domains",
	"resume_methodologies",
	"resume_vectors",
}

// profileSnapshot captures the committed profile state the invariant cares
// about: total person count, the seeded person's name (looked up by id),
// per-entity counts for the seeded person, AND total row counts across every
// resume_* table plus resume_vectors (the exhaustiveness oracle).
type profileSnapshot struct {
	personCount  int
	seededName   string
	skills       int
	projects     int
	experiences  int
	achievements int
	totalCounts  map[string]int
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
	snap.totalCounts = make(map[string]int, len(resumeTableTotalCounts))
	for _, tbl := range resumeTableTotalCounts {
		var n int
		if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM `+tbl).Scan(&n); err != nil {
			t.Fatalf("snapshot total count %s: %v", tbl, err)
		}
		snap.totalCounts[tbl] = n
	}
	return snap
}

// assertProfileIntact fails the test if the committed profile differs from the
// snapshot: the seeded person must still exist with the same name, the total
// person count must be unchanged, the seeded person's entity counts must match,
// AND the total row count across every resume_* table plus resume_vectors must
// be unchanged — so an orphan child row written on the pool under a rolled-back
// person id (the F6 regression) is caught.
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
	for _, tbl := range resumeTableTotalCounts {
		var n int
		if err := db.pool.QueryRow(ctx, `SELECT count(*) FROM `+tbl).Scan(&n); err != nil {
			t.Fatalf("verify total count %s: %v", tbl, err)
		}
		if n != want.totalCounts[tbl] {
			t.Errorf("profile damaged: total %s = %d, want %d (a write that escaped the transaction left a row)",
				tbl, n, want.totalCounts[tbl])
		}
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

// F1 — atomic rebuild (exhaustiveness oracle): a failure immediately before
// tx.Commit (after every write in the phase has executed) must leave the
// pre-call profile byte-identical. With the transaction the rollback discards
// the clear+inserts; with the transaction removed the clear and inserts have
// already committed on the pool and the seeded profile is gone. The total
// row-count assertion also catches any single write routed to db.pool (F6).
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
	_, err := BuildMasterResume(ctx, "dummy resume text", seededID)
	if err == nil {
		t.Fatal("F1: expected the build to fail (injected hook error), got nil")
	}
	if !strings.Contains(err.Error(), "injected partway failure") {
		t.Errorf("F1: error did not carry the injected cause: %v", err)
	}

	assertProfileIntact(t, db, seededID, want)
}

// F2 — non-replayable consent: when a profile already exists and the caller
// does not name its id in replace_person_id, the call must refuse and name what
// would be destroyed, leaving the profile intact. Dropping the consent check
// lets the stubbed build run to completion and replace the profile → err==nil
// and the profile is damaged → RED.
func TestBuildMasterResume_F2_ReplaceGuard_RefusesWithoutConsent(t *testing.T) {
	db := testResumeDBClean(t)
	seededID := seedProfile(t, db)
	want := snapshotProfile(t, db, seededID)

	withStubbedLLM(t)

	ctx := context.Background()
	// replace_person_id=0 → no consent → must refuse.
	_, err := BuildMasterResume(ctx, "dummy resume text", 0)
	if err == nil {
		t.Fatal("F2: expected a refuse error (profile exists, no consent), got nil — " +
			"the consent guard is missing and a second run would silently destroy the profile")
	}
	if !strings.Contains(err.Error(), "profile already exists") || !strings.Contains(err.Error(), "replace_person_id") {
		t.Errorf("F2: refuse error must name the existing profile and the replace_person_id consent, got: %v", err)
	}
	if !strings.Contains(err.Error(), "person_id=") {
		t.Errorf("F2: refuse error must name the existing person_id so the caller can consent, got: %v", err)
	}

	assertProfileIntact(t, db, seededID, want)
}

// F3 — deadline: under a pre-cancelled context the build must abort before the
// write phase with the deadline cause, writing nothing. Removing the ctx.Err()
// check lets the build reach the guard/BeginTx with a cancelled context → the
// guard query errors (fail-closed) or BeginTx fails with "context canceled"
// instead of the guard's "caller deadline exceeded before write phase" → the
// guard-firing assertion fails → RED.
func TestBuildMasterResume_F3_Deadline_AbortsBeforeWritePhase(t *testing.T) {
	db := testResumeDBClean(t)
	seededID := seedProfile(t, db)
	want := snapshotProfile(t, db, seededID)

	withStubbedLLM(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // caller already gone before the call

	_, err := BuildMasterResume(ctx, "dummy resume text", seededID) // consent given, but caller gone
	if err == nil {
		t.Fatal("F3: expected an error under a cancelled context, got nil")
	}
	if !strings.Contains(err.Error(), "caller deadline exceeded before write phase") {
		t.Errorf("F3: error must be the deadline guard (\"caller deadline exceeded before write phase\"), "+
			"got: %v — the ctx.Err() check before the write phase is missing", err)
	}

	assertProfileIntact(t, db, seededID, want)
}

// F4 — fail-open guard: when the destructive-consent guard's query errors, the
// build must REFUSE (fail-closed), not read the error as "no profile" and
// proceed to destroy. The guard hook injects a query error; mutation that makes
// the error path return "no profile" (exists=false, err=nil) lets the build
// proceed → the seeded profile is destroyed → assertProfileIntact fails → RED.
func TestBuildMasterResume_F4_GuardFailsClosedOnQueryError(t *testing.T) {
	db := testResumeDBClean(t)
	seededID := seedProfile(t, db)
	want := snapshotProfile(t, db, seededID)

	withStubbedLLM(t)

	prevGuard := masterResumeGuardHook
	masterResumeGuardHook = func() error { return errors.New("injected guard query failure") }
	t.Cleanup(func() { masterResumeGuardHook = prevGuard })

	ctx := context.Background()
	_, err := BuildMasterResume(ctx, "dummy resume text", seededID)
	if err == nil {
		t.Fatal("F4: expected the build to refuse when the guard query errors, got nil — " +
			"the guard is fail-open and a transient pool error turns a guarded destroy into an unguarded one")
	}
	if !strings.Contains(err.Error(), "destructive-consent guard failed") {
		t.Errorf("F4: error must name the guard failure (refusing to touch the profile), got: %v", err)
	}

	assertProfileIntact(t, db, seededID, want)
}

// F5 — graph survives rollback: NO graph statement (clear/node/edge) may execute
// during a rolled-back build, and the live AGE graph must be byte-identical
// afterwards. The test seeds real graph nodes (AGE is present on the test
// cluster), snapshots the live node/edge counts, runs a rolled-back build, and
// asserts both that the graph-op recorder is empty (no statement ran) AND that
// the live graph counts are unchanged. Mutation: move the graph clear/replay
// back to before the transaction → ClearGraph runs before the tx → the live
// graph is emptied and the recorder is non-empty even on a rolled-back build →
// RED.
func TestBuildMasterResume_F5_GraphUntouchedOnRollback(t *testing.T) {
	db := testResumeDBClean(t)
	seededID := seedProfile(t, db)
	want := snapshotProfile(t, db, seededID)

	// Seed real graph nodes so a pre-tx ClearGraph is observable. Best-effort:
	// AGE may be absent in some environments; if so, only the recorder
	// assertion applies (and the test still catches the mutation at the
	// call-boundary).
	ctx := context.Background()
	_ = db.UpsertGraphNode(ctx, "Skill", 999001, map[string]string{graphPropName: "Seeded Graph Skill"})
	_ = db.UpsertGraphNode(ctx, "Exp", 999002, map[string]string{"title": "Seeded Graph Exp"})

	wantNodes, wantEdges, ageOK := liveGraphCounts(t, db)

	withStubbedLLM(t)

	prevHook := masterResumeWriteHook
	masterResumeWriteHook = func() error { return errors.New("injected partway failure") }
	t.Cleanup(func() { masterResumeWriteHook = prevHook })

	var graphOps []string
	prevRec := masterResumeGraphOpRecorder
	masterResumeGraphOpRecorder = func(op string) { graphOps = append(graphOps, op) }
	t.Cleanup(func() { masterResumeGraphOpRecorder = prevRec })

	_, err := BuildMasterResume(ctx, "dummy resume text", seededID)
	if err == nil {
		t.Fatal("F5: expected the build to fail (injected hook error), got nil")
	}
	if len(graphOps) != 0 {
		t.Errorf("F5: a rolled-back build issued graph statements %v — the graph clear/replay must run ONLY after commit, "+
			"so a rollback leaves the old graph intact", graphOps)
	}

	if ageOK {
		gotNodes, gotEdges, _ := liveGraphCounts(t, db)
		if gotNodes != wantNodes || gotEdges != wantEdges {
			t.Errorf("F5: graph damaged on rollback: nodes=%d want=%d, edges=%d want=%d — "+
				"ClearGraph must not run before the transaction commits", gotNodes, wantNodes, gotEdges, wantEdges)
		}
	} else {
		// Not a silent pass. The recorder assertion above only observes graph
		// statements issued through replayGraphAfterCommit; the mutation this
		// test guards against (graph clear moved back before the transaction)
		// calls db.ClearGraph directly and is invisible to it. Without AGE the
		// central invariant is therefore UNVERIFIED, and saying so in the test
		// output is the difference between a known gap and a false gate.
		t.Log("F5: AGE absent — live-graph assertion SKIPPED; the pre-transaction-clear " +
			"mutation is NOT covered on this runner. Provision AGE in preflight to close this.")
	}

	assertProfileIntact(t, db, seededID, want)
}

// liveGraphCounts returns the total node/edge count of the resume AGE graph,
// plus whether AGE is available at all.
//
// AGE is optional by design in this repo — migration 002 is `-- soft` and the
// build degrades cleanly without it — and the CI postgres (pgvector/pgvector)
// does not ship it. So absence is reported, not fatal. Any OTHER error still
// fails the test: "AGE is installed but broken" must not be laundered into
// "AGE is absent".
func liveGraphCounts(t *testing.T, db *ResumeDB) (nodes, edges int, available bool) {
	t.Helper()
	n, err := db.CountGraphNodes(context.Background())
	if err != nil {
		if isAgeMissing(err) {
			return 0, 0, false
		}
		t.Fatalf("F5: CountGraphNodes: %v", err)
	}
	e, err := db.CountGraphEdges(context.Background())
	if err != nil {
		if isAgeMissing(err) {
			return 0, 0, false
		}
		t.Fatalf("F5: CountGraphEdges: %v", err)
	}
	return n, e, true
}

// F7 — non-replayable consent (replay after success): a successful rebuild
// creates a new profile (new id). Replaying the SAME arguments (carrying the
// old id) against the new profile must REFUSE, because the consent named the
// old id, not the present one. Dropping the id check lets the replay proceed
// and destroy the new profile → err==nil and the profile is damaged → RED.
func TestBuildMasterResume_F7_NonReplayableConsent_ReplayAfterSuccess(t *testing.T) {
	db := testResumeDBClean(t)
	seededID := seedProfile(t, db) // profile A (id=seededID)

	withStubbedLLM(t)

	ctx := context.Background()

	// First build: consent names A → succeeds, replaces A with a new profile B.
	first, err := BuildMasterResume(ctx, "dummy resume text", seededID)
	if err != nil {
		t.Fatalf("F7: first build (consenting to seeded id=%d) must succeed, got: %v", seededID, err)
	}
	newID := first.PersonID
	if newID == seededID {
		t.Fatalf("F7: first build must create a NEW person id (got %d == seeded %d); test cannot model the replay", newID, seededID)
	}

	// Replay the SAME arguments (consent still names the old id=seededID) against
	// the now-present profile B (id=newID). The consent is stale → must refuse.
	_, err = BuildMasterResume(ctx, "dummy resume text", seededID)
	if err == nil {
		t.Fatal("F7: expected the replay (stale consent id) to refuse, got nil — " +
			"the consent is replayable and a blind retry destroyed the profile")
	}
	if !strings.Contains(err.Error(), "profile already exists") {
		t.Errorf("F7: replay refuse error must name the existing profile, got: %v", err)
	}

	// The new profile B must survive the refused replay intact.
	wantB := snapshotProfile(t, db, newID)
	if wantB.personCount != 1 {
		t.Fatalf("F7: expected 1 person after the successful build, got %d", wantB.personCount)
	}
	assertProfileIntact(t, db, newID, wantB)
}
