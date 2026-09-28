package jobs

// master_resume_merge_test.go — falsification tests for BuildMergedResume.
//
// Invariants under test: the merge prompt carries the FULL current profile in
// the output schema shape (all sections, sub-projects nested); manually-edited
// state that cannot round-trip through the LLM (headline/hourly_rate,
// upwork_*) is restored mechanically; an edit landing between the prompt and
// the write tx refuses the merge; a snapshot failure or oversized baseline
// refuses rather than silently dropping entities. Atomicity and consent are
// inherited from the shared write path and re-proven here.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// captureMergeLLM records every prompt sent through the callLLM seam while
// delegating to the deterministic stub, so the build completes its write
// phase without a live LLM.
func captureMergeLLM(t *testing.T) *[]string {
	t.Helper()
	prompts := new([]string)
	prev := callLLM
	callLLM = func(ctx context.Context, prompt string) (string, error) {
		*prompts = append(*prompts, prompt)
		return stubMasterResumeParseLLM(ctx, prompt)
	}
	t.Cleanup(func() { callLLM = prev })
	return prompts
}

// seedFullProfile inserts a profile covering EVERY snapshot section plus the
// non-schema manual state (headline/hourly_rate on the person, a full
// upwork_* set, a sub-project). Returns the person id and the parent
// experience id.
func seedFullProfile(t *testing.T, rdb *ResumeAccount) (personID, expID int) {
	t.Helper()
	ctx := context.Background()
	pid, err := rdb.InsertPerson(ctx, PersonRecord{Name: "Full Person"})
	if err != nil {
		t.Fatalf("seed InsertPerson: %v", err)
	}
	if _, err := rdb.db.pool.Exec(ctx,
		`UPDATE resume_persons SET headline = 'Seeded Headline', hourly_rate = 15000 WHERE id = $1`, pid); err != nil {
		t.Fatalf("seed person fields: %v", err)
	}
	expID, err = rdb.InsertExperience(ctx, pid, ExperienceRecord{
		Title: "Seeded Role", Company: "SeededCo", Domain: "infra",
		TeamSize: intp(4), IsVolunteer: true,
	})
	if err != nil {
		t.Fatalf("seed InsertExperience: %v", err)
	}
	if _, err := rdb.InsertProjectWithParent(ctx, pid, &expID, ProjectRecord{Name: "Seeded SubProject", URL: "https://sub.example"}); err != nil {
		t.Fatalf("seed sub-project: %v", err)
	}
	if _, err := rdb.InsertSkillExtended(ctx, pid, SkillRecord{Name: "Seeded Skill", Category: "tool", Level: "expert", IsImplicit: true, Source: "inferred"}); err != nil {
		t.Fatalf("seed InsertSkillExtended: %v", err)
	}
	if _, err := rdb.InsertProject(ctx, pid, ProjectRecord{Name: "Seeded Project"}); err != nil {
		t.Fatalf("seed InsertProject: %v", err)
	}
	if _, err := rdb.InsertAchievementExtended(ctx, pid, AchievementRecord{Text: "Seeded achievement"}); err != nil {
		t.Fatalf("seed achievement: %v", err)
	}
	if _, err := rdb.InsertEducation(ctx, pid, EducationRecord{School: "Seeded School", Degree: "BS"}); err != nil {
		t.Fatalf("seed education: %v", err)
	}
	if _, err := rdb.InsertCertification(ctx, pid, CertificationRecord{Name: "Seeded Cert", Issuer: "SeededIssuer", URL: "https://cert.example/x"}); err != nil {
		t.Fatalf("seed certification: %v", err)
	}
	if _, err := rdb.InsertDomain(ctx, pid, "SeededDomain"); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	if _, err := rdb.InsertMethodology(ctx, pid, "SeededMeth", "Seeded meth desc"); err != nil {
		t.Fatalf("seed methodology: %v", err)
	}
	if err := rdb.UpsertUpworkProfile(ctx, pid, "Up Title", "Up overview", 9000, []string{"backend"}, "full-time"); err != nil {
		t.Fatalf("seed upwork profile: %v", err)
	}
	if _, err := rdb.InsertUpworkSkill(ctx, pid, "UpworkSkill"); err != nil {
		t.Fatalf("seed upwork skill: %v", err)
	}
	if _, err := rdb.InsertUpworkCatalogItem(ctx, pid, "UpworkItem", "Upwork item desc"); err != nil {
		t.Fatalf("seed upwork catalog: %v", err)
	}
	return pid, expID
}

func intp(v int) *int { return &v }

// The merge prompt must embed the FULL current profile — every section, with
// sub-projects nested under their parent experience — plus the merge rules
// and the new document. Asserted on entity VALUES so a snapshot silently
// dropping a section goes RED.
func TestBuildMergedResume_PromptCarriesExistingProfile(t *testing.T) {
	_, rdb := testResumeDBClean(t)
	seededID, _ := seedFullProfile(t, rdb)
	prompts := captureMergeLLM(t)

	_, err := BuildMergedResume(context.Background(), rdb.AccountID(), "NEW RESUME TEXT MARKER", seededID)
	if err != nil {
		t.Fatalf("BuildMergedResume: %v", err)
	}
	if len(*prompts) == 0 {
		t.Fatal("LLM never called")
	}
	p := (*prompts)[0]
	for _, want := range []string{
		"EXISTING PROFILE", "Preserve every entity",
		"Full Person", "Seeded Role", "Seeded SubProject", "Seeded Skill", "Seeded Project",
		"Seeded achievement", "Seeded School", "Seeded Cert", "cert.example", "sub.example", "SeededDomain", "SeededMeth",
		"NEW RESUME TEXT MARKER",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("merge prompt missing %q", want)
		}
	}
	// Sub-project nesting: must appear inside the experiences section, not as
	// a standalone project — the write phase re-derives parentage from there.
	iExp := strings.Index(p, `"experiences"`)
	iSub := strings.Index(p, "Seeded SubProject")
	iProjs := strings.Index(p, `"projects"`)
	if iExp < 0 || iSub <= iExp || iSub >= iProjs {
		t.Fatalf("sub-project not nested under experiences (exp=%d sub=%d projects=%d)", iExp, iSub, iProjs)
	}
}

// The write phase must mechanically restore the state the LLM cannot emit —
// person headline/hourly_rate and the upwork_* set — on the new person row.
func TestBuildMergedResume_PreservesManualState(t *testing.T) {
	_, rdb := testResumeDBClean(t)
	seededID, _ := seedFullProfile(t, rdb)
	withStubbedLLM(t)

	res, err := BuildMergedResume(context.Background(), rdb.AccountID(), "new resume text", seededID)
	if err != nil {
		t.Fatalf("BuildMergedResume: %v", err)
	}
	if res.PersonID == seededID {
		t.Fatal("expected a fresh person row after the atomic replace")
	}
	p, err := rdb.GetPerson(context.Background(), res.PersonID)
	if err != nil {
		t.Fatalf("GetPerson: %v", err)
	}
	if p.Headline != "Seeded Headline" || p.HourlyRateCents != 15000 {
		t.Fatalf("person manual fields lost: headline=%q rate=%d", p.Headline, p.HourlyRateCents)
	}
	up, err := rdb.GetUpworkProfile(context.Background(), res.PersonID)
	if err != nil {
		t.Fatalf("GetUpworkProfile: %v", err)
	}
	if up.Missing || up.Profile == nil || up.Profile.Title != "Up Title" || up.Profile.HourlyRate != 9000 {
		t.Fatalf("upwork profile lost: %+v", up.Profile)
	}
	if len(up.Skills) != 1 || up.Skills[0].Name != "UpworkSkill" {
		t.Fatalf("upwork skills lost: %+v", up.Skills)
	}
	if len(up.Catalog) != 1 || up.Catalog[0].Title != "UpworkItem" {
		t.Fatalf("upwork catalog lost: %+v", up.Catalog)
	}
}

// Rebuild preserves the same manual state — the consent consents to the
// resume profile, not to wiping unrelated upwork configuration.
func TestBuildMasterResume_PreservesManualState(t *testing.T) {
	_, rdb := testResumeDBClean(t)
	seededID, _ := seedFullProfile(t, rdb)
	withStubbedLLM(t)

	res, err := BuildMasterResume(context.Background(), rdb.AccountID(), "new resume text", seededID)
	if err != nil {
		t.Fatalf("BuildMasterResume: %v", err)
	}
	p, err := rdb.GetPerson(context.Background(), res.PersonID)
	if err != nil {
		t.Fatalf("GetPerson: %v", err)
	}
	if p.Headline != "Seeded Headline" {
		t.Fatalf("rebuild lost headline: %q", p.Headline)
	}
	up, err := rdb.GetUpworkProfile(context.Background(), res.PersonID)
	if err != nil || up.Missing || up.Profile.Title != "Up Title" {
		t.Fatalf("rebuild lost upwork profile: %+v err=%v", up, err)
	}
}

// An edit landing between the merge prompt (LLM window) and the write tx
// must refuse the merge — the baseline no longer matches the profile. The
// stub mutates the DB on the POOL (outside the tx) during the parse call.
func TestBuildMergedResume_LostUpdateRefuses(t *testing.T) {
	_, rdb := testResumeDBClean(t)
	seededID, _ := seedFullProfile(t, rdb)
	want := snapshotProfile(t, rdb, seededID)

	prev := callLLM
	callLLM = func(ctx context.Context, prompt string) (string, error) {
		if strings.Contains(prompt, "EXISTING PROFILE") {
			// Simulate a concurrent manual edit during the LLM window.
			if _, err := rdb.InsertSkill(ctx, seededID, SkillRecord{Name: "LateSkill"}); err != nil {
				return "", err
			}
		}
		return stubMasterResumeParseLLM(ctx, prompt)
	}
	t.Cleanup(func() { callLLM = prev })

	_, err := BuildMergedResume(context.Background(), rdb.AccountID(), "new text", seededID)
	if err == nil || !strings.Contains(err.Error(), "changed since the merge started") {
		t.Fatalf("expected lost-update refusal, got %v", err)
	}
	// Profile intact except the one skill the "concurrent edit" added.
	got := snapshotProfile(t, rdb, seededID)
	if got.personCount != want.personCount || got.seededName != want.seededName || got.skills != want.skills+1 {
		t.Fatalf("profile mangled by refused merge: %+v vs %+v", got, want)
	}
}

// A snapshot exceeding the cap refuses — a truncated baseline would silently
// destroy tail entities.
func TestBuildMergedResume_SnapshotTooLargeRefuses(t *testing.T) {
	_, rdb := testResumeDBClean(t)
	seededID := seedProfile(t, rdb)
	want := snapshotProfile(t, rdb, seededID)
	withStubbedLLM(t)

	prev := mergeSnapshotMaxRunes
	mergeSnapshotMaxRunes = 10
	t.Cleanup(func() { mergeSnapshotMaxRunes = prev })

	_, err := BuildMergedResume(context.Background(), rdb.AccountID(), "new text", seededID)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected too-large refusal, got %v", err)
	}
	assertProfileIntact(t, rdb, seededID, want)
}

// With no existing profile the merge degrades to a plain build — the prompt
// must not carry a baseline it doesn't have.
func TestBuildMergedResume_NoProfile_PlainBuild(t *testing.T) {
	_, rdb := testResumeDBClean(t)
	prompts := captureMergeLLM(t)

	_, err := BuildMergedResume(context.Background(), rdb.AccountID(), "first resume text", 0)
	if err != nil {
		t.Fatalf("BuildMergedResume: %v", err)
	}
	if strings.Contains((*prompts)[0], "EXISTING PROFILE") {
		t.Fatal("merge prompt carried a baseline on an empty profile")
	}
}

// Merge is still a replace under the hood — a stale/mismatched person id must
// refuse before touching anything, exactly like the rebuild path.
func TestBuildMergedResume_ConsentGuard(t *testing.T) {
	_, rdb := testResumeDBClean(t)
	seededID := seedProfile(t, rdb)
	want := snapshotProfile(t, rdb, seededID)
	withStubbedLLM(t)

	_, err := BuildMergedResume(context.Background(), rdb.AccountID(), "new text", seededID+9999)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected refusal, got %v", err)
	}
	assertProfileIntact(t, rdb, seededID, want)
}

// profileSnapshotJSON is fail-closed: a load error on ANY section must
// propagate (a missing section would let the model drop those entities).
// Exercised directly — a cancelled context fails the first query.
func TestProfileSnapshotJSON_FailClosed(t *testing.T) {
	_, rdb := testResumeDBClean(t)
	seededID := seedProfile(t, rdb)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := profileSnapshotJSON(ctx, rdb, seededID); err == nil {
		t.Fatal("snapshot succeeded on a dead context — a section failure would go silent")
	}
}

// capturePreservedState must see the upwork set even when run inside the
// transaction context (conn-bound, not pool).
func TestCapturePreservedState_InTx(t *testing.T) {
	db, rdb := testResumeDBClean(t)
	seededID, _ := seedFullProfile(t, rdb)

	ctx := context.Background()
	tx, err := db.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ctx = withTx(ctx, tx)

	st, err := capturePreservedState(ctx, rdb, seededID)
	if err != nil {
		t.Fatalf("capturePreservedState: %v", err)
	}
	if st.Headline != "Seeded Headline" || st.HourlyRateCents != 15000 {
		t.Fatalf("person fields not captured: %+v", st)
	}
	if st.Upwork == nil || st.Upwork.Title != "Up Title" {
		t.Fatalf("upwork profile not captured: %+v", st.Upwork)
	}
	if len(st.UpworkSkills) != 1 || len(st.UpworkCatalog) != 1 {
		t.Fatalf("upwork rows not captured: %+v", st)
	}
}

// Orphan sub-projects (parent_experience_id pointing outside this profile —
// possible via a foreign person's experience row, since the FK is unscoped)
// must serialize DETERMINISTICALLY: the in-tx drift check byte-compares two
// snapshots of identical state, and a random map order would flake refusals.
func TestProfileSnapshotJSON_OrphanDeterministic(t *testing.T) {
	_, rdb := testResumeDBClean(t)
	pid, _ := seedFullProfile(t, rdb)
	ctx := context.Background()
	// Foreign parent: another person's experience — satisfies the FK, lands in
	// no experience's subByParent bucket for THIS profile.
	otherPID, err := rdb.InsertPerson(ctx, PersonRecord{Name: "Other"})
	if err != nil {
		t.Fatalf("other person: %v", err)
	}
	foreignExp, err := rdb.InsertExperience(ctx, otherPID, ExperienceRecord{Title: "Foreign", Company: "X"})
	if err != nil {
		t.Fatalf("foreign exp: %v", err)
	}
	for _, name := range []string{"Orphan A", "Orphan B"} {
		if _, err := rdb.InsertProjectWithParent(ctx, pid, &foreignExp, ProjectRecord{Name: name}); err != nil {
			t.Fatalf("orphan project %s: %v", name, err)
		}
	}
	first, err := profileSnapshotJSON(ctx, rdb, pid)
	if err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := profileSnapshotJSON(ctx, rdb, pid)
		if err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
		if again != first {
			t.Fatal("snapshot serialization is nondeterministic — drift check would flake")
		}
	}
	if !strings.Contains(first, "Orphan A") || !strings.Contains(first, "Orphan B") {
		t.Fatal("orphan sub-projects vanished from the snapshot")
	}
}
