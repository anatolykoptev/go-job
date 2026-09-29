package jobs

// master_resume_diff_test.go — pure-function tests for the merge preview diff.
// The diff is the user's only window into what merge will do: a silent-miss
// class here (entity dropped from the diff while still being written) is the
// highest-risk surface, so both directions are asserted — removed rows must
// appear as removed, additions as added, and field drift as changed.

import (
	"encoding/json"
	"reflect"
	"testing"
)

func snapForTest(t *testing.T, s mergeSnapshot) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func diffForTest(t *testing.T, base, cand mergeSnapshot) *ResumeDiff {
	t.Helper()
	return diffResumeForTest(t, snapForTest(t, base), cand)
}

func diffResumeForTest(t *testing.T, baseJSON string, cand mergeSnapshot) *ResumeDiff {
	t.Helper()
	// Marshal the candidate through parsedResume→snapshotFromParsed? That path
	// is covered by the handler flow; here we exercise DiffResumePlan's JSON
	// boundary by building a plan whose Parsed we can marshal directly.
	candJSON, err := json.Marshal(cand)
	if err != nil {
		t.Fatalf("marshal cand: %v", err)
	}
	var plan ResumePlan
	if err := json.Unmarshal(candJSON, &plan.Parsed); err != nil {
		t.Fatalf("unmarshal cand into parsedResume: %v", err)
	}
	d, err := DiffResumePlan(baseJSON, &plan)
	if err != nil {
		t.Fatalf("DiffResumePlan: %v", err)
	}
	return d
}

func sectionByName(t *testing.T, d *ResumeDiff, name string) SectionDiff {
	t.Helper()
	for _, s := range d.Sections {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("section %q missing", name)
	return SectionDiff{}
}

func TestDiffResumePlan_AddRemoveChange(t *testing.T) {
	base := mergeSnapshot{
		Skills:      []mergeSnapshotSkill{{Name: "Go", Category: "lang", Level: "expert"}, {Name: "Dropped", Category: "x"}},
		Projects:    []mergeSnapshotProject{{Name: "OldProj"}},
		Experiences: []mergeSnapshotExperience{{Title: "Dev", Company: "Co", StartDate: "2020"}},
	}
	cand := mergeSnapshot{
		Skills:      []mergeSnapshotSkill{{Name: "Go", Category: "lang", Level: "senior"}, {Name: "Rust", Category: "lang"}},
		Projects:    []mergeSnapshotProject{},
		Experiences: []mergeSnapshotExperience{{Title: "Dev", Company: "Co", StartDate: "2020"}, {Title: "Lead", Company: "NewCo"}},
	}
	d := diffForTest(t, base, cand)

	sk := sectionByName(t, d, "Skills")
	if len(sk.Added) != 1 || sk.Added[0].Label != "Rust" {
		t.Fatalf("skills added: %+v", sk.Added)
	}
	if len(sk.Removed) != 1 || sk.Removed[0].Label != "Dropped" {
		t.Fatalf("skills removed: %+v", sk.Removed)
	}
	if len(sk.Changed) != 1 || sk.Changed[0].Label != "Go" {
		t.Fatalf("skills changed: %+v", sk.Changed)
	}
	foundLevel := false
	for _, c := range sk.Changed[0].Changes {
		if c.Field == "level" && c.Old == "expert" && c.New == "senior" {
			foundLevel = true
		}
	}
	if !foundLevel {
		t.Fatalf("level change not surfaced: %+v", sk.Changed[0].Changes)
	}

	pr := sectionByName(t, d, "Projects")
	if len(pr.Removed) != 1 || pr.Removed[0].Label != "OldProj" {
		t.Fatalf("project removal not surfaced — silent-loss class: %+v", pr.Removed)
	}
	ex := sectionByName(t, d, "Experiences")
	if len(ex.Added) != 1 || ex.Added[0].Label != "Lead @ NewCo" {
		t.Fatalf("experience added: %+v", ex.Added)
	}
	if d.Empty {
		t.Fatal("non-empty diff reported empty")
	}
}

func TestDiffResumePlan_PersonChanges(t *testing.T) {
	base := mergeSnapshot{Person: mergeSnapshotPerson{Name: "A", Email: "a@x.io", Summary: "old"}}
	cand := mergeSnapshot{Person: mergeSnapshotPerson{Name: "A", Email: "a@x.io", Summary: "new", Phone: "555"}}
	d := diffForTest(t, base, cand)
	fields := map[string]FieldChange{}
	for _, c := range d.Person {
		fields[c.Field] = c
	}
	if c, ok := fields["summary"]; !ok || c.Old != "old" || c.New != "new" {
		t.Fatalf("summary change: %+v", fields)
	}
	if c, ok := fields["phone"]; !ok || c.New != "555" {
		t.Fatalf("phone add: %+v", fields)
	}
	if _, ok := fields["name"]; ok {
		t.Fatal("unchanged field reported changed")
	}
}

// An empty baseline (first build) renders every candidate entity as added.
func TestDiffResumePlan_EmptyBaseline(t *testing.T) {
	cand := mergeSnapshot{Skills: []mergeSnapshotSkill{{Name: "Go"}}}
	d := diffResumeForTest(t, "", cand)
	sk := sectionByName(t, d, "Skills")
	if len(sk.Added) != 1 || sk.Added[0].Label != "Go" {
		t.Fatalf("empty-baseline add: %+v", sk.Added)
	}
}

// Identical profiles produce an empty diff — the apply button hides.
func TestDiffResumePlan_NoChanges(t *testing.T) {
	s := mergeSnapshot{Skills: []mergeSnapshotSkill{{Name: "Go", Level: "expert"}}}
	d := diffForTest(t, s, s)
	if !d.Empty {
		t.Fatalf("identical profiles produced a diff: %+v", d)
	}
}

// The plan rides inside the signed apply payload — a JSON round-trip must be
// lossless, or apply writes a silently-degraded profile. Guards against anyone
// adding an unexported field to the plan's schema later.
func TestResumePlan_JSONRoundTripLossless(t *testing.T) {
	planJSON := `{"person":{"name":"Jane","email":"j@x.io","links":{"github":"g"}},
		"experiences":[{"title":"Eng","company":"AC","sub_projects":[{"name":"sp","url":"u"}]}],
		"skills":[{"name":"Go","is_implicit":true,"source":"exp"}],
		"achievements":[{"text":"a","metric_numeric":1.5}],
		"certifications":[{"name":"c","url":"cu"}],"domains":["fintech"],
		"methodologies":[{"name":"ag","description":"d"}]}`
	var parsed parsedResume
	if err := json.Unmarshal([]byte(planJSON), &parsed); err != nil {
		t.Fatal(err)
	}
	plan := &ResumePlan{
		Parsed:    parsed,
		Baseline:  `{"person":{"name":"Jane"}}`,
		PersonID:  7,
		Truncated: true, TruncatedFromRunes: 999,
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var back ResumePlan
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Parsed, back.Parsed) {
		t.Fatalf("parsed resume did not survive JSON round-trip:\n%+v\nvs\n%+v", plan.Parsed, back.Parsed)
	}
	if !reflect.DeepEqual(plan.Enrichment, back.Enrichment) ||
		back.Baseline != plan.Baseline || back.PersonID != plan.PersonID ||
		back.Truncated != plan.Truncated || back.TruncatedFromRunes != plan.TruncatedFromRunes {
		t.Fatal("plan metadata did not survive JSON round-trip")
	}
}

// The candidate must enumerate the real write-set: implicit skills, adjacency
// targets, enrichment sub-projects (parented AND standalone), domain and
// methodology unions — everything apply writes must appear in the diff.
func TestDiffResumePlan_EnrichmentCoverage(t *testing.T) {
	plan := &ResumePlan{}
	if err := json.Unmarshal([]byte(`{
		"parsed": {
			"person": {"name":"Jane"},
			"experiences":[{"title":"Eng","company":"AC","skills":["Go"],
				"sub_projects":[{"name":"sp","tech":["Redis"]}]}],
			"skills":[{"name":"K8s"}],
			"domains":["fintech"]
		},
		"enrichment": {
			"implicit_skills":[{"name":"Leadership","category":"soft","level":"senior","source":"exp"}],
			"skill_adjacencies":[{"from":"Go","to":"Concurrency"},{"from":"Missing","to":"Nope"}],
			"sub_projects":[{"parent_experience":"ac","name":"EnrichedSub","tech":["Kafka"]},
			                {"parent_experience":"nowhere","name":"OrphanSub"}],
			"domains":["ml"],"methodologies":[{"name":"tdd"}]
		},
		"person_id": 3
	}`), plan); err != nil {
		t.Fatal(err)
	}
	diff, err := DiffResumePlan("", plan)
	if err != nil {
		t.Fatal(err)
	}
	var skills, projs, doms, meths SectionDiff
	for _, s := range diff.Sections {
		switch s.Name {
		case "Skills":
			skills = s
		case "Projects":
			projs = s
		case "Domains":
			doms = s
		case "Methodologies":
			meths = s
		}
	}
	labels := func(es []DiffEntry) map[string]bool {
		m := map[string]bool{}
		for _, e := range es {
			m[e.Label] = true
		}
		return m
	}
	added := labels(skills.Added)
	for _, want := range []string{"K8s", "Go", "Redis", "Leadership", "Concurrency", "Kafka"} {
		if !added[want] {
			t.Errorf("skill %q missing from diff.Added", want)
		}
	}
	if added["Nope"] {
		t.Error("adjacency target with missing source must not be added")
	}
	padd := labels(projs.Added)
	if !padd["Eng — EnrichedSub"] {
		t.Error("enrichment sub-project under matched company must be added as 'Eng — EnrichedSub'")
	}
	if !padd["OrphanSub"] {
		t.Error("unmatched enrichment sub-project must be added standalone")
	}
	if !labels(doms.Added)["fintech"] || !labels(doms.Added)["ml"] {
		t.Error("parsed + enrichment domains must both appear")
	}
	if !labels(meths.Added)["tdd"] {
		t.Error("enrichment methodology missing")
	}
}

// Non-injective field rendering must not hide real changes: ["a, b","c"] and
// ["a","b","c"] render the same with comma-join — the comparison must see
// through it.
func TestDiffResumePlan_InjectiveFieldCompare(t *testing.T) {
	var base mergeSnapshot
	if err := json.Unmarshal([]byte(`{"experiences":[{"title":"T","company":"C","highlights":["a, b","c"]}]}`), &base); err != nil {
		t.Fatal(err)
	}
	plan := &ResumePlan{}
	// Inject cand via a crafted parse
	if err := json.Unmarshal([]byte(`{"parsed":{"experiences":[{"title":"T","company":"C","highlights":["a","b","c"]}]}}`), plan); err != nil {
		t.Fatal(err)
	}
	baseJSON, _ := json.Marshal(base)
	diff, err := DiffResumePlan(string(baseJSON), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range diff.Sections {
		if s.Name == "Experiences" {
			if len(s.Changed) != 1 {
				t.Fatalf("split/merge in highlights must show as changed, got %+v", s)
			}
			return
		}
	}
	t.Fatal("Experiences section missing")
}

// A baseline row the new parse omits but enrichment re-emits must NOT show as
// removed — apply keeps it, the diff must not lie.
func TestDiffResumePlan_EnrichmentReemittedNotRemoved(t *testing.T) {
	base := `{"domains":["fintech","ml"],"skills":[{"name":"Go"}]}`
	plan := &ResumePlan{}
	if err := json.Unmarshal([]byte(`{
		"parsed": {"domains":["fintech"],"skills":[{"name":"Go"}]},
		"enrichment": {"domains":["ml"]},
		"person_id": 3
	}`), plan); err != nil {
		t.Fatal(err)
	}
	diff, err := DiffResumePlan(base, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range diff.Sections {
		if s.Name == "Domains" && len(s.Removed) != 0 {
			t.Fatalf("enrichment-reemitted domain must not show removed: %+v", s.Removed)
		}
	}
}
