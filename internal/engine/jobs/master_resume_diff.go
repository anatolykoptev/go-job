package jobs

// master_resume_diff.go — merge preview: converts a ResumePlan's parsed
// candidate into the same snapshot shape as the baseline and produces a
// human-readable per-section diff (added / removed / changed, field-level).
// Pure functions — no DB, no LLM.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// FieldChange is one field's old → new value on a changed entity.
type FieldChange struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// DiffEntry is one entity row in a section diff.
type DiffEntry struct {
	Label   string        `json:"label"`             // human key, e.g. "Go" or "Engineer @ NewCo"
	Changes []FieldChange `json:"changes,omitempty"` // set when Kind == "changed"
}

// SectionDiff holds added/removed/changed entries for one entity section.
type SectionDiff struct {
	Name    string      `json:"name"`
	Added   []DiffEntry `json:"added,omitempty"`
	Removed []DiffEntry `json:"removed,omitempty"`
	Changed []DiffEntry `json:"changed,omitempty"`
}

// ResumeDiff is the whole preview: person field changes + per-section diffs.
type ResumeDiff struct {
	Person   []FieldChange `json:"person,omitempty"`
	Sections []SectionDiff `json:"sections"`
	Empty    bool          `json:"empty"` // true when merge produces no visible change
}

// DiffResumePlan compares a plan's parsed candidate against the baseline
// snapshot the merge was seeded with. baselineJSON is "" for a fresh profile
// (everything counts as added).
func DiffResumePlan(baselineJSON string, plan *ResumePlan) (*ResumeDiff, error) {
	var base mergeSnapshot
	if baselineJSON != "" {
		if err := json.Unmarshal([]byte(baselineJSON), &base); err != nil {
			return nil, fmt.Errorf("resume diff: bad baseline: %w", err)
		}
	}
	cand := candidateFromPlan(plan)
	d := &ResumeDiff{Person: diffPerson(base.Person, cand.Person)}
	d.Sections = append(d.Sections,
		diffSection("Experiences", base.Experiences, cand.Experiences, expKey, expLabel),
		diffSection("Skills", base.Skills, cand.Skills, skillKey, skillLabel),
		diffSection("Projects", flatProjects(base), flatProjects(cand), projKey, projLabel),
		diffSection("Achievements", base.Achievements, cand.Achievements, achKey, achLabel),
		diffSection("Education", base.Educations, cand.Educations, eduKey, eduLabel),
		diffSection("Certifications", base.Certifications, cand.Certifications, certKey, certLabel),
		diffSection("Domains", base.Domains, cand.Domains, domKey, domLabel),
		diffSection("Methodologies", base.Methodologies, cand.Methodologies, methKey, methLabel),
	)
	d.Empty = len(d.Person) == 0
	for _, s := range d.Sections {
		if len(s.Added)+len(s.Removed)+len(s.Changed) > 0 {
			d.Empty = false
		}
	}
	return d, nil
}

// snapshotFromParsed converts the parse output into the baseline snapshot
// shape so both sides compare field-for-field.
func snapshotFromParsed(p parsedResume) mergeSnapshot {
	var cand mergeSnapshot
	cand.Person = mergeSnapshotPerson{
		Name: p.Person.Name, Email: p.Person.Email, Phone: p.Person.Phone,
		Location: p.Person.Location, Links: p.Person.Links, Summary: p.Person.Summary,
	}
	for _, e := range p.Experiences {
		se := mergeSnapshotExperience{
			Title: e.Title, Company: e.Company, Location: e.Location,
			StartDate: e.StartDate, EndDate: e.EndDate, Description: e.Description,
			Highlights: e.Highlights, Domain: e.Domain,
			TeamSize: e.TeamSize, BudgetUSD: e.BudgetUSD, IsVolunteer: e.IsVolunteer,
		}
		for _, sp := range e.SubProjects {
			se.SubProjects = append(se.SubProjects, mergeSnapshotProject{
				Name: sp.Name, Description: sp.Description, URL: sp.URL,
				Tech: sp.Tech, Highlights: sp.Highlights,
			})
		}
		cand.Experiences = append(cand.Experiences, se)
	}
	for _, s := range p.Skills {
		cand.Skills = append(cand.Skills, mergeSnapshotSkill{
			Name: s.Name, Category: s.Category, Level: s.Level,
			IsImplicit: s.IsImplicit, Source: s.Source,
		})
	}
	for _, pr := range p.Projects {
		cand.Projects = append(cand.Projects, mergeSnapshotProject{
			Name: pr.Name, Description: pr.Description, URL: pr.URL,
			Tech: pr.Tech, Highlights: pr.Highlights,
		})
	}
	for _, a := range p.Achievements {
		cand.Achievements = append(cand.Achievements, mergeSnapshotAchievement{
			Text: a.Text, Metric: a.Metric, Value: a.Value, Context: a.Context,
			MetricNumeric: a.MetricNumeric, MetricUnit: a.MetricUnit,
		})
	}
	for _, e := range p.Educations {
		cand.Educations = append(cand.Educations, mergeSnapshotEducation{
			School: e.School, Degree: e.Degree, Field: e.Field,
			StartDate: e.StartDate, EndDate: e.EndDate, GPA: e.GPA, Highlights: e.Highlights,
		})
	}
	for _, c := range p.Certifications {
		cand.Certifications = append(cand.Certifications, mergeSnapshotCertification{
			Name: c.Name, Issuer: c.Issuer, Year: c.Year, URL: c.URL,
		})
	}
	for _, d := range p.Domains {
		if strings.TrimSpace(d) != "" {
			cand.Domains = append(cand.Domains, d)
		}
	}
	for _, m := range p.Methodologies {
		if strings.TrimSpace(m.Name) == "" {
			continue
		}
		cand.Methodologies = append(cand.Methodologies, mergeSnapshotMethodology{
			Name: m.Name, Description: m.Description,
		})
	}
	return cand
}

// candidateFromPlan builds the snapshot of what ApplyResumePlan will actually
// write — the parsed entities PLUS the enrichment unions and the per-entity
// skill/tech extractions, in the same first-writer-wins order the write phase
// uses (ensureSkill dedups by lower(name); domains/methodologies dedup by
// exact name). The preview is the consent surface: it must enumerate the real
// write-set, not just the parse output — keep this in lockstep with the insert
// phase in master_resume.go.
func candidateFromPlan(plan *ResumePlan) mergeSnapshot {
	cand := snapshotFromParsed(plan.Parsed)

	haveSkill := make(map[string]bool, len(cand.Skills)+16)
	for _, s := range cand.Skills {
		haveSkill[lower(s.Name)] = true
	}
	addSkill := func(name, cat, level string, implicit bool, src string) {
		if lower(name) == "" || haveSkill[lower(name)] {
			return
		}
		haveSkill[lower(name)] = true
		cand.Skills = append(cand.Skills, mergeSnapshotSkill{
			Name: name, Category: cat, Level: level, IsImplicit: implicit, Source: src,
		})
	}
	for _, e := range plan.Parsed.Experiences {
		for _, s := range e.Skills {
			addSkill(s, "other", "intermediate", false, "resume")
		}
		for _, sp := range e.SubProjects {
			for _, t := range sp.Tech {
				addSkill(t, "other", "intermediate", false, "resume")
			}
		}
	}
	for _, pr := range plan.Parsed.Projects {
		for _, t := range pr.Tech {
			addSkill(t, "other", "intermediate", false, "resume")
		}
	}
	for _, is := range plan.Enrichment.ImplicitSkills {
		addSkill(is.Name, is.Category, is.Level, true, "inferred")
	}

	// Enrichment sub-projects: parent hint resolves against parsed experience
	// companies (exact lower match, else substring — mirroring
	// findExperienceByHint); unmatched ones land as standalone projects.
	// Their tech strings become skill rows in apply REGARDLESS of the parent
	// resolution — and before adjacency expansion — so they enter haveSkill
	// here in the same order.
	for _, sp := range plan.Enrichment.SubProjects {
		proj := mergeSnapshotProject{
			Name: sp.Name, Description: sp.Description, Tech: sp.Tech, Highlights: sp.Highlights,
		}
		hint := lower(sp.ParentExperience)
		attached := false
		if hint != "" {
			for i, e := range cand.Experiences {
				if lower(e.Company) == hint {
					cand.Experiences[i].SubProjects = append(cand.Experiences[i].SubProjects, proj)
					attached = true
					break
				}
			}
			for i, e := range cand.Experiences {
				if attached {
					break
				}
				c := lower(e.Company)
				if c != "" && (strings.Contains(c, hint) || strings.Contains(hint, c)) {
					cand.Experiences[i].SubProjects = append(cand.Experiences[i].SubProjects, proj)
					attached = true
				}
			}
		}
		if !attached {
			cand.Projects = append(cand.Projects, proj)
		}
		for _, t := range sp.Tech {
			addSkill(t, "other", "intermediate", false, "resume")
		}
	}

	for _, adj := range plan.Enrichment.SkillAdjacencies {
		if haveSkill[lower(adj.From)] {
			addSkill(adj.To, "other", "intermediate", true, "inferred")
		}
	}

	haveDom := make(map[string]bool, len(cand.Domains)+4)
	for _, d := range cand.Domains {
		haveDom[d] = true
	}
	addDom := func(d string) {
		if d != "" && !haveDom[d] {
			haveDom[d] = true
			cand.Domains = append(cand.Domains, d)
		}
	}
	for _, e := range plan.Parsed.Experiences {
		addDom(e.Domain)
	}
	for _, d := range plan.Enrichment.Domains {
		addDom(d)
	}

	haveMeth := make(map[string]bool, len(cand.Methodologies)+4)
	for _, m := range cand.Methodologies {
		haveMeth[m.Name] = true
	}
	for _, m := range plan.Enrichment.Methodologies {
		if m.Name != "" && !haveMeth[m.Name] {
			haveMeth[m.Name] = true
			cand.Methodologies = append(cand.Methodologies, mergeSnapshotMethodology{
				Name: m.Name, Description: m.Description,
			})
		}
	}
	return cand
}

// --- keying + field comparison ---

// jsonMap renders an entity as a generic map for field-wise comparison.
func jsonMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func renderField(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, renderField(e))
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(t))
		for _, k := range keys {
			parts = append(parts, k+"="+renderField(t[k]))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(t)
	}
}

// diffEntries walks two entity lists keyed by keyFn, emitting added / removed
// / changed (with field-level detail) entries, sorted by label.
func diffEntries[T any](base, cand []T, keyFn func(T) string, labelFn func(T) string) (added, removed, changed []DiffEntry) {
	baseBy := make(map[string]T, len(base))
	for i, e := range base {
		k := keyFn(e)
		if _, dup := baseBy[k]; dup {
			// Two rows with the same key must not silently merge — give each
			// its own bucket so the dup surfaces as removed+added instead of
			// last-writer-wins dropping one.
			k = fmt.Sprintf("%s\x00%d", k, i)
		}
		baseBy[k] = e
	}
	candBy := make(map[string]T, len(cand))
	for i, e := range cand {
		k := keyFn(e)
		if _, dup := candBy[k]; dup {
			k = fmt.Sprintf("%s\x00%d", k, i)
		}
		candBy[k] = e
	}
	for k, c := range candBy {
		b, ok := baseBy[k]
		if !ok {
			added = append(added, DiffEntry{Label: labelFn(c)})
			continue
		}
		var ch []FieldChange
		bm, cm := jsonMap(b), jsonMap(c)
		for f, cv := range cm {
			bv := bm[f]
			if canon(bv) != canon(cv) {
				ch = append(ch, FieldChange{Field: f, Old: renderField(bv), New: renderField(cv)})
			}
		}
		for f, bv := range bm {
			if _, ok := cm[f]; !ok && canon(bv) != "null" && renderField(bv) != "" {
				ch = append(ch, FieldChange{Field: f, Old: renderField(bv), New: ""})
			}
		}
		if len(ch) > 0 {
			sort.Slice(ch, func(i, j int) bool { return ch[i].Field < ch[j].Field })
			changed = append(changed, DiffEntry{Label: labelFn(c), Changes: ch})
		}
	}
	for k, b := range baseBy {
		if _, ok := candBy[k]; !ok {
			removed = append(removed, DiffEntry{Label: labelFn(b)})
		}
	}
	byLabel := func(s []DiffEntry) { sort.Slice(s, func(i, j int) bool { return s[i].Label < s[j].Label }) }
	byLabel(added)
	byLabel(removed)
	byLabel(changed)
	return added, removed, changed
}

func diffPerson(base, cand mergeSnapshotPerson) []FieldChange {
	var ch []FieldChange
	bm, cm := jsonMap(base), jsonMap(cand)
	for f, cv := range cm {
		if canon(bm[f]) != canon(cv) {
			ch = append(ch, FieldChange{Field: f, Old: renderField(bm[f]), New: renderField(cv)})
		}
	}
	for f, bv := range bm {
		if _, ok := cm[f]; !ok && canon(bv) != "null" && renderField(bv) != "" {
			ch = append(ch, FieldChange{Field: f, Old: renderField(bv), New: ""})
		}
	}
	sort.Slice(ch, func(i, j int) bool { return ch[i].Field < ch[j].Field })
	return ch
}

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// canon renders a field value canonically for EQUALITY — json.Marshal sorts
// map keys, so unlike the display renderer this is injective: ["a, b","c"]
// and ["a","b","c"] compare different.
func canon(v any) string {
	b, _ := json.Marshal(v)
	switch string(b) {
	case "null", `""`, "[]", "{}":
		return "" // every empty shape compares equal — no phantom changes
	}
	return string(b)
}

// jkey joins key parts injectively — a length prefix per part keeps
// "a@b"+"c" distinct from "a"+"b@c" regardless of separator bytes inside the
// content.
func jkey(parts ...string) string {
	var sb strings.Builder
	for _, p := range parts {
		fmt.Fprintf(&sb, "%d:%s|", len(p), p)
	}
	return sb.String()
}

// runeCut truncates s to n runes without splitting mid-rune.
func runeCut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Section adapters — the entity lists differ in type, so each section wires
// its own key + label into the generic differ.

func diffSection[T any](name string, base, cand []T, keyFn func(T) string, labelFn func(T) string) SectionDiff {
	added, removed, changed := diffEntries(base, cand, keyFn, labelFn)
	return SectionDiff{Name: name, Added: added, Removed: removed, Changed: changed}
}

func expKey(e mergeSnapshotExperience) string   { return jkey(lower(e.Title), lower(e.Company)) }
func expLabel(e mergeSnapshotExperience) string { return e.Title + " @ " + e.Company }

func skillKey(s mergeSnapshotSkill) string   { return lower(s.Name) }
func skillLabel(s mergeSnapshotSkill) string { return s.Name }

// Projects diff on the FLATTENED view: sub-projects participate under
// "exp — sub" labels so a re-nesting shows as remove+add rather than a loss.
func flatProjects(s mergeSnapshot) []mergeSnapshotProject {
	var out []mergeSnapshotProject
	out = append(out, s.Projects...)
	for _, e := range s.Experiences {
		for _, sp := range e.SubProjects {
			sp.Name = e.Title + " — " + sp.Name
			out = append(out, sp)
		}
	}
	return out
}
func projKey(p mergeSnapshotProject) string   { return lower(p.Name) }
func projLabel(p mergeSnapshotProject) string { return p.Name }

func achKey(a mergeSnapshotAchievement) string { return lower(a.Text) }
func achLabel(a mergeSnapshotAchievement) string {
	if len([]rune(a.Text)) > 80 {
		return runeCut(a.Text, 80) + "…"
	}
	return a.Text
}

func eduKey(e mergeSnapshotEducation) string   { return jkey(lower(e.School), lower(e.Degree)) }
func eduLabel(e mergeSnapshotEducation) string { return e.School + " — " + e.Degree }

func certKey(c mergeSnapshotCertification) string   { return jkey(lower(c.Name), lower(c.Issuer)) }
func certLabel(c mergeSnapshotCertification) string { return c.Name + " (" + c.Issuer + ")" }

func domKey(d string) string   { return lower(d) }
func domLabel(d string) string { return d }

func methKey(m mergeSnapshotMethodology) string   { return lower(m.Name) }
func methLabel(m mergeSnapshotMethodology) string { return m.Name }
