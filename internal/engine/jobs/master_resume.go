package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/jackc/pgx/v5"
)

// callLLM is the seam BuildMasterResume uses for its two LLM calls. It defaults
// to engine.CallLLM; tests swap it to inject deterministic parse/enrichment
// output without a live LLM. Scoped to this file's build path — the other
// engine.CallLLM call sites are untouched.
var callLLM = engine.CallLLM

// masterResumeWriteHook, when non-nil, is invoked once immediately BEFORE
// tx.Commit — after every relational/vector write in the build phase has
// executed inside the transaction. If it returns a non-nil error the build
// aborts, rolling back the transaction. Firing at the latest possible point
// (not right after InsertPerson) makes it an exhaustiveness oracle: a
// contributor who routes any write to db.pool instead of conn(ctx) leaves a
// row that survives the hook-induced rollback, which the total-row-count
// assertion in the atomic test catches. It is nil in production.
var masterResumeWriteHook func() error

// masterResumeGuardHook, when non-nil, is invoked at the destructive-consent
// guard to inject a query error in tests (F4). nil in production.
var masterResumeGuardHook func() error

// masterResumeGraphOpRecorder, when non-nil, records each post-commit graph
// operation ("clear", "node", "edge") so the F5 test can assert that NO graph
// statement runs during a rolled-back build (the buffer is replayed only after
// commit). nil in production.
var masterResumeGraphOpRecorder func(op string)

// MasterResumeBuildResult is the structured output of master_resume_build.
type MasterResumeBuildResult struct {
	PersonID           int    `json:"person_id"`
	Experiences        int    `json:"experiences"`
	Skills             int    `json:"skills"`
	Projects           int    `json:"projects"`
	Achievements       int    `json:"achievements"`
	Educations         int    `json:"educations"`
	Certifications     int    `json:"certifications"`
	Domains            int    `json:"domains"`
	Methodologies      int    `json:"methodologies"`
	ImplicitSkills     int    `json:"implicit_skills"`
	SubProjects        int    `json:"sub_projects"`
	GraphNodes         int    `json:"graph_nodes"`
	GraphEdges         int    `json:"graph_edges"`
	VectorsStored      int    `json:"vectors_stored"`
	Truncated          bool   `json:"truncated,omitempty"`
	TruncatedFromRunes int    `json:"truncated_from_runes,omitempty"`
	Summary            string `json:"summary"`
}

type parsedResume struct {
	Person struct {
		Name     string            `json:"name"`
		Email    string            `json:"email"`
		Phone    string            `json:"phone"`
		Location string            `json:"location"`
		Links    map[string]string `json:"links"`
		Summary  string            `json:"summary"`
	} `json:"person"`
	Experiences []struct {
		Title       string   `json:"title"`
		Company     string   `json:"company"`
		Location    string   `json:"location"`
		StartDate   string   `json:"start_date"`
		EndDate     string   `json:"end_date"`
		Description string   `json:"description"`
		Highlights  []string `json:"highlights"`
		Skills      []string `json:"skills"`
		Domain      string   `json:"domain,omitempty"`
		TeamSize    *int     `json:"team_size,omitempty"`
		BudgetUSD   *int     `json:"budget_usd,omitempty"`
		IsVolunteer bool     `json:"is_volunteer,omitempty"`
		SubProjects []struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Tech        []string `json:"tech"`
			Highlights  []string `json:"highlights"`
		} `json:"sub_projects,omitempty"`
	} `json:"experiences"`
	Educations []struct {
		School     string   `json:"school"`
		Degree     string   `json:"degree"`
		Field      string   `json:"field"`
		StartDate  string   `json:"start_date"`
		EndDate    string   `json:"end_date"`
		GPA        string   `json:"gpa"`
		Highlights []string `json:"highlights"`
	} `json:"educations"`
	Skills []struct {
		Name       string `json:"name"`
		Category   string `json:"category"`
		Level      string `json:"level"`
		IsImplicit bool   `json:"is_implicit,omitempty"`
		Source     string `json:"source,omitempty"`
	} `json:"skills"`
	Projects []struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		URL         string   `json:"url"`
		Tech        []string `json:"tech"`
		Highlights  []string `json:"highlights"`
	} `json:"projects"`
	Achievements []struct {
		Text          string   `json:"text"`
		Metric        string   `json:"metric"`
		Value         string   `json:"value"`
		Context       string   `json:"context"`
		MetricNumeric *float64 `json:"metric_numeric,omitempty"`
		MetricUnit    string   `json:"metric_unit,omitempty"`
	} `json:"achievements"`
	Certifications []struct {
		Name   string `json:"name"`
		Issuer string `json:"issuer"`
		Year   string `json:"year"`
	} `json:"certifications"`
	Domains       []string `json:"domains,omitempty"`
	Methodologies []struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	} `json:"methodologies,omitempty"`
}

type enrichmentResult struct {
	ImplicitSkills []struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Level    string `json:"level"`
		Source   string `json:"source"` // which experience/achievement it was inferred from
	} `json:"implicit_skills"`
	SubProjects []struct {
		ParentExperience string   `json:"parent_experience"` // company or title to match
		Name             string   `json:"name"`
		Description      string   `json:"description"`
		Tech             []string `json:"tech"`
		Highlights       []string `json:"highlights"`
	} `json:"sub_projects"`
	SkillAdjacencies []struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"skill_adjacencies"`
	CareerTrajectory []struct {
		From string `json:"from"` // earlier role (company or title)
		To   string `json:"to"`   // later role (company or title)
	} `json:"career_trajectory"`
	Methodologies []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"methodologies"`
	Domains []string `json:"domains"`
}

const masterResumeParsePrompt = `You are an expert resume parser. Parse the following resume into structured JSON.
Extract EVERYTHING — every role, skill, project, achievement, certification, and education entry.

For each experience:
- List ALL skills/technologies used in that role in the "skills" field
- Identify the domain (e.g., "Event Production", "Digital Marketing", "Software Engineering", "Media")
- If team_size or budget are mentioned, extract them
- If the role was volunteer/unpaid, set is_volunteer to true
- CRITICALLY: If an experience contains sub-events, sub-products, or sub-projects (e.g., a festival company that ran multiple events), extract them in "sub_projects"

For achievements:
- Extract the metric and numeric value when available
- Set metric_numeric as a float (e.g., 16000 for "16K tickets") and metric_unit (e.g., "tickets", "percent", "USD")

For skills:
- Mark explicitly listed skills with is_implicit: false, source: "resume"
- If you can clearly infer skills from context (e.g., "sold 16K tickets with zero budget" implies Guerrilla Marketing), add them with is_implicit: true, source: "inferred"

Also extract:
- "domains": array of professional domains the person operates in (e.g., ["Event Production", "Digital Marketing", "Media"])
- "methodologies": array of approaches/frameworks the person uses (e.g., [{"name": "Zero-Budget Growth", "description": "..."}])

Return a JSON object with this exact structure:
{
  "person": {
    "name": "...",
    "email": "...",
    "phone": "...",
    "location": "...",
    "links": {"linkedin": "url", "github": "url", ...},
    "summary": "professional summary if present"
  },
  "experiences": [
    {
      "title": "...",
      "company": "...",
      "location": "...",
      "start_date": "YYYY-MM or YYYY",
      "end_date": "YYYY-MM or Present",
      "description": "brief role description",
      "highlights": ["bullet point 1", "bullet point 2"],
      "skills": ["Go", "PostgreSQL", "Docker"],
      "domain": "Software Engineering",
      "team_size": null,
      "budget_usd": null,
      "is_volunteer": false,
      "sub_projects": [
        {"name": "...", "description": "...", "tech": [], "highlights": []}
      ]
    }
  ],
  "educations": [
    {"school": "...", "degree": "...", "field": "...", "start_date": "...", "end_date": "...", "gpa": "...", "highlights": []}
  ],
  "skills": [
    {"name": "Go", "category": "programming_language", "level": "expert", "is_implicit": false, "source": "resume"}
  ],
  "projects": [
    {"name": "...", "description": "...", "url": "...", "tech": ["Go", "Redis"], "highlights": ["..."]}
  ],
  "achievements": [
    {"text": "Sold 16K tickets with zero marketing budget", "metric": "tickets sold", "value": "16000", "context": "Festival Empire", "metric_numeric": 16000, "metric_unit": "tickets"}
  ],
  "certifications": [
    {"name": "...", "issuer": "...", "year": "..."}
  ],
  "domains": ["Event Production", "Digital Marketing"],
  "methodologies": [
    {"name": "Zero-Budget Growth", "description": "Driving massive outcomes without paid advertising through viral mechanics and psychological triggers"}
  ]
}

Skill categories: programming_language, framework, database, cloud, devops, tool, methodology, soft_skill, other.
Skill levels: expert, advanced, intermediate, beginner (infer from context — primary stack = expert, mentioned once = intermediate).

IMPORTANT: Do NOT skip any section. The "educations" array MUST be populated if the resume contains education info (look at the bottom of the resume). Same for certifications.
Be aggressive about extracting sub_projects from experiences — if an experience mentions multiple distinct initiatives, events, products, or campaigns, each one is a sub_project.
Be aggressive about inferring implicit skills — read between the lines of achievements and responsibilities.

RESUME:
%s

Return ONLY the JSON object, no markdown, no explanation.`

const enrichmentPrompt = `You are an expert career analyst. Given this parsed resume data and the original resume text, enrich it with deeper insights.

PARSED DATA:
%s

ORIGINAL RESUME:
%s

Analyze and return a JSON object with:

1. "implicit_skills": Skills that can be inferred but were not explicitly stated. For each:
   - "name": skill name
   - "category": one of programming_language, framework, database, cloud, devops, tool, methodology, soft_skill, other
   - "level": expert/advanced/intermediate/beginner
   - "source": which experience or achievement this was inferred from

2. "sub_projects": Hidden sub-projects within experiences. For each:
   - "parent_experience": the company name to match to parent experience
   - "name": project name
   - "description": what it was
   - "tech": technologies used
   - "highlights": key results

3. "skill_adjacencies": Pairs of skills where knowing one implies the other. For each:
   - "from": skill name
   - "to": implied skill name

4. "career_trajectory": Pairs showing career evolution (same domain, higher role). For each:
   - "from": earlier company name
   - "to": later company name

5. "methodologies": Unique approaches or frameworks this person developed or heavily used. For each:
   - "name": methodology name
   - "description": brief description

6. "domains": Professional domains (e.g., "Event Production", "Digital Marketing", "Media", "Software Engineering")

Focus on HIGH-VALUE enrichments that would improve ATS matching and showcase hidden strengths.
Do NOT duplicate items already in the parsed data.

Return ONLY the JSON object, no markdown, no explanation.`

// BuildMasterResume parses resume text into SQL tables, AGE graph, and resume_vectors.
//
// Atomicity invariant: a call that fails, times out, or is cancelled must leave
// the existing profile byte-identical to what it was before the call. The
// relational writes (resume_persons and all ON DELETE CASCADE children) and the
// resume_vectors writes share ONE transaction and commit only when the whole
// build succeeds; any error after the transaction begins rolls it back.
//
// The AGE graph writes are rebuild-then-swap: during the build NO graph
// statement executes. UpsertGraphNode/UpsertGraphEdge calls are buffered in
// memory in call order; only after tx.Commit succeeds does ClearGraph run and
// the buffer replay. A rolled-back build therefore never touches the graph —
// the old graph survives intact alongside the old profile. (Previously the
// graph was cleared before the transaction, so a rollback left a live profile
// with an empty graph and resume_generate silently returned a degraded resume.)
// If the post-commit replay fails, the profile is committed and correct but
// the graph is stale: a WARN is logged naming that state; the call does not
// report success as though the graph were rebuilt, and a real cypher error
// (as opposed to AGE simply being absent) is surfaced, not tolerated.
//
// replacePersonID is the non-replayable destructive consent. When a profile
// already exists, the call refuses unless replacePersonID equals the id of the
// profile actually present — so a blind retry of the same arguments fails as
// soon as the id has changed (e.g. after a successful rebuild created a new
// profile). What protects the data on a retry is the transaction (a failed
// build rolls back and the old profile survives); the consent gate exists to
// stop an accidental/agent-replayed second run from running at all, and to
// make that stop hold against a retry that carries the same arguments.
//
// TOCTOU: the consent is checked twice — once before opening the transaction
// (an early refuse that avoids opening one when consent is clearly missing)
// and again inside the transaction after acquiring a transaction-scoped
// advisory lock (pg_advisory_xact_lock). The in-tx re-read under the lock is
// authoritative: two concurrent builds serialize on the lock, so the second
// sees the first's committed id and its stale consent no longer matches.
func BuildMasterResume(ctx context.Context, resumeText string, replacePersonID int) (*MasterResumeBuildResult, error) { //nolint:funlen
	db := GetResumeDB()
	if db == nil {
		return nil, errors.New("resume database not configured (set DATABASE_URL)")
	}

	// 1. Parse resume via LLM (call #1)
	isTruncated, origLen := checkResumeTruncation(resumeText, 12000)
	resumeTrunc := engine.TruncateRunes(resumeText, 12000, "")
	if isTruncated {
		slog.Warn("resume truncated before LLM parse", slog.Int("original_runes", origLen), slog.Int("limit", 12000))
	}
	prompt := fmt.Sprintf(masterResumeParsePrompt, resumeTrunc)

	raw, err := callLLM(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("master_resume_build LLM: %w", err)
	}

	raw = StripMarkdownFences(raw)

	var parsed parsedResume
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("master_resume_build parse: %w (raw: %s)", err, engine.TruncateRunes(raw, 200, "..."))
	}

	// 2. Enrichment pass (LLM call #2)
	parsedJSON, _ := json.Marshal(parsed)
	enrichPrompt := fmt.Sprintf(enrichmentPrompt,
		engine.TruncateRunes(string(parsedJSON), 8000, ""),
		engine.TruncateRunes(resumeText, 6000, ""),
	)

	enrichRaw, err := callLLM(ctx, enrichPrompt)
	if err != nil {
		slog.Warn("enrichment LLM call failed, continuing without enrichment", slog.Any("error", err))
	}

	var enrichment enrichmentResult
	if enrichRaw != "" {
		enrichRaw = StripMarkdownFences(enrichRaw)
		if err := json.Unmarshal([]byte(enrichRaw), &enrichment); err != nil {
			slog.Warn("enrichment parse failed, continuing without enrichment", slog.Any("error", err))
		}
	}

	// 3. Respect the caller's deadline BEFORE the destructive consent guard.
	// Both LLM calls above ran against ctx (the stub ignores ctx in tests); if
	// the caller is already gone by the time we reach the write phase, abort
	// WITHOUT touching the database — including the guard query — rather than
	// beginning a clear that a retry could race. Checking ctx.Err() before the
	// guard also keeps the guard's fail-closed error path from masking a
	// deadline with a "guard query failed" message.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("master_resume_build: caller deadline exceeded before write phase: %w", err)
	}

	// 4. Explicit, non-replayable destructive consent (pre-tx early refuse).
	// A profile already exists and the caller did not name its id in
	// replacePersonID → refuse and name what would be destroyed. The guard is
	// fail-closed: a guard-query error REFUSES the build rather than reading as
	// "no profile" (the old GetLatestPersonID collapsed both into 0, so a
	// transient pool error turned a guarded destroy into an unguarded one).
	// The consent is re-checked inside the transaction under an advisory lock
	// (step 5) to close the TOCTOU; this pre-tx check only avoids opening a
	// transaction when consent is clearly missing.
	exists, existingID, err := db.guardLatestPersonID(ctx)
	if err != nil {
		return nil, fmt.Errorf("master_resume_build: destructive-consent guard failed (refusing to touch the profile): %w", err)
	}
	if exists {
		if existingID != replacePersonID {
			return nil, fmt.Errorf("master_resume_build: a profile already exists (person_id=%d)%s — "+
				"rebuilding destroys it and all of its skills/projects/experiences/achievements/educations/"+
				"certifications/domains/methodologies plus upwork_profile data (ON DELETE CASCADE). "+
				"To consent to the replacement, name that profile's id in replace_person_id",
				existingID, describeExistingProfile(ctx, db, existingID))
		}
		slog.Warn("master_resume_build: replacing existing profile", slog.Int("person_id", existingID))
	}

	// 5. Atomic write phase. NO graph statement executes here: graph node/edge
	// upserts are buffered and replayed only after tx.Commit (rebuild-then-swap,
	// see the function doc). The relational clear + every insert + the vector
	// clear/upsert share one transaction.
	tx, err := db.Pool().BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("master_resume_build: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, context.Canceled) && !errors.Is(rbErr, context.DeadlineExceeded) {
				slog.Warn("master_resume_build: rollback failed", slog.Any("error", rbErr))
			}
		}
	}()

	// Transaction-scoped advisory lock as the FIRST statement in the tx. Two
	// concurrent rebuilds serialize here; the lock auto-releases on commit/
	// rollback. Taken before any clear so the in-tx re-read below sees a stable
	// id, closing the TOCTOU where both builds read the same id pre-tx.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, masterResumeRebuildLockKey); err != nil {
		return nil, fmt.Errorf("master_resume_build: acquire rebuild lock: %w", err)
	}

	// Rebind ctx to carry the transaction: every relational/vector write and
	// read below runs through db.conn(ctx), which now returns the tx instead of
	// the pool, so the whole write phase shares this transaction.
	ctx = withTx(ctx, tx)

	// Re-read the person id INSIDE the transaction under the lock and re-check
	// consent. A concurrent build that committed between the pre-tx read and
	// here changed the id; the caller's consent named the pre-tx id, which no
	// longer matches → refuse (rollback releases the lock). This is the
	// authoritative check; the pre-tx check was only an early refuse.
	{
		inTxExists, inTxID, err := db.guardLatestPersonID(ctx)
		if err != nil {
			return nil, fmt.Errorf("master_resume_build: in-tx consent re-check failed (refusing): %w", err)
		}
		if inTxExists && inTxID != replacePersonID {
			return nil, fmt.Errorf("master_resume_build: profile id changed under the rebuild lock (consented id=%d, present id=%d) — "+
				"a concurrent rebuild committed first; refusing to destroy a profile the caller did not name",
				replacePersonID, inTxID)
		}
	}

	if err := db.ClearAllPersons(ctx); err != nil {
		return nil, fmt.Errorf("clear persons failed before rebuild: %w", err)
	}

	// Clear source='profile' derived resume_vectors rows for the mem_types master_resume
	// re-derives (resume_experience/project/achievement). Scoped to source='profile' so
	// manual source='agent' memories and enrich_project rows are preserved.
	if err := db.ClearVectors(ctx, memTypeResumeExp, memTypeResumeProj, memTypeResumeAchv); err != nil {
		return nil, fmt.Errorf("clear resume vectors failed before rebuild: %w", err)
	}

	// 6. Insert person
	personID, err := db.InsertPerson(ctx, PersonRecord{
		Name:     parsed.Person.Name,
		Email:    parsed.Person.Email,
		Phone:    parsed.Person.Phone,
		Location: parsed.Person.Location,
		Links:    parsed.Person.Links,
		Summary:  parsed.Person.Summary,
	})
	if err != nil {
		return nil, fmt.Errorf("insert person: %w", err)
	}

	result := &MasterResumeBuildResult{PersonID: personID}
	if isTruncated {
		result.Truncated = true
		result.TruncatedFromRunes = origLen
	}
	var vectorTexts []vectorEntry

	// Graph buffer: collects every UpsertGraphNode/UpsertGraphEdge the build
	// would issue, in call order. Nothing is written to AGE during the build;
	// the buffer is replayed only after tx.Commit (rebuild-then-swap). On
	// rollback the buffer is discarded and the old graph is untouched.
	graphBuf := newGraphBuffer()

	// Track skill name → skill ID for graph edges
	skillIDs := make(map[string]int)

	// Track experience company → expID for enrichment linking
	expByCompany := make(map[string]int)

	// 5. Insert standalone skills (both explicit and implicit from parse)
	for _, s := range parsed.Skills {
		source := s.Source
		if source == "" {
			source = "resume"
		}
		sid, err := db.InsertSkillExtended(ctx, personID, SkillRecord{
			Name:       s.Name,
			Category:   s.Category,
			Level:      s.Level,
			IsImplicit: s.IsImplicit,
			Source:     source,
		})
		if err != nil {
			slog.Debug("insert skill failed", slog.String("name", s.Name), slog.Any("error", err))
			continue
		}
		skillIDs[strings.ToLower(s.Name)] = sid
		result.Skills++
		if s.IsImplicit {
			result.ImplicitSkills++
		}
	}

	// 6. Insert experiences + graph nodes/edges
	for _, exp := range parsed.Experiences {
		expID, err := db.InsertExperience(ctx, personID, ExperienceRecord{
			Title:       exp.Title,
			Company:     exp.Company,
			Location:    exp.Location,
			StartDate:   exp.StartDate,
			EndDate:     exp.EndDate,
			Description: exp.Description,
			Highlights:  exp.Highlights,
		})
		if err != nil {
			slog.Debug("insert experience failed", slog.String("title", exp.Title), slog.Any("error", err))
			continue
		}
		result.Experiences++
		expByCompany[strings.ToLower(exp.Company)] = expID

		// Update extended metadata
		if exp.Domain != "" || exp.TeamSize != nil || exp.BudgetUSD != nil || exp.IsVolunteer {
			if err := db.UpdateExperienceMeta(ctx, expID, exp.TeamSize, exp.BudgetUSD, exp.Domain, exp.IsVolunteer); err != nil {
				slog.Debug("update experience meta failed", slog.Int("exp_id", expID), slog.Any("error", err))
			}
		}

		// Graph: Exp node (buffered — replayed after commit)
		graphBuf.addNode("Exp", expID, map[string]string{
			"title":   exp.Title, //nolint:goconst
			"company": exp.Company,
		})

		// Graph: skill edges
		for _, skillName := range exp.Skills {
			sid := ensureSkill(ctx, db, personID, skillName, "other", "intermediate", false, "resume", skillIDs, result)
			if sid > 0 {
				graphBuf.addNode("Skill", sid, map[string]string{graphPropName: skillName})
				graphBuf.addEdge("Exp", expID, "USED_SKILL", "Skill", sid)
			}
		}

		// Graph: domain edge
		if exp.Domain != "" {
			domID, err := db.InsertDomain(ctx, personID, exp.Domain)
			if err != nil {
				slog.Warn("insert exp domain failed", slog.String("domain", exp.Domain), slog.Any("error", err))
			} else {
				graphBuf.addNode("Domain", domID, map[string]string{graphPropName: exp.Domain})
				graphBuf.addEdge("Exp", expID, "IN_DOMAIN", "Domain", domID)
			}
		}

		// Insert sub-projects from parse
		for _, sp := range exp.SubProjects {
			spID, err := db.InsertProjectWithParent(ctx, personID, &expID, ProjectRecord{
				Name:        sp.Name,
				Description: sp.Description,
				Tech:        sp.Tech,
				Highlights:  sp.Highlights,
			})
			if err != nil {
				slog.Debug("insert sub-project failed", slog.String("name", sp.Name), slog.Any("error", err))
				continue
			}
			result.Projects++
			result.SubProjects++

			graphBuf.addNode("Proj", spID, map[string]string{graphPropName: sp.Name})
			graphBuf.addEdge("Proj", spID, "PART_OF", "Exp", expID)

			for _, techName := range sp.Tech {
				sid := ensureSkill(ctx, db, personID, techName, "other", "intermediate", false, "resume", skillIDs, result)
				if sid > 0 {
					graphBuf.addNode("Skill", sid, map[string]string{graphPropName: techName})
					graphBuf.addEdge("Proj", spID, "USED_SKILL", "Skill", sid)
				}
			}

			text := formatProjectText(sp.Name, sp.Description, sp.Tech, sp.Highlights)
			spIDi64 := int64(spID)
			vectorTexts = append(vectorTexts, vectorEntry{
				content: text,
				memType: memTypeResumeProj,
				refID:   &spIDi64,
			})
		}

		// Vector: experience text (with domain context)
		text := formatExperienceTextExtended(exp.Title, exp.Company, exp.StartDate, exp.EndDate, exp.Description, exp.Highlights, exp.Domain)
		expIDi64 := int64(expID)
		vectorTexts = append(vectorTexts, vectorEntry{
			content: text,
			memType: memTypeResumeExp,
			refID:   &expIDi64,
		})
	}

	// 7. Insert standalone projects + graph
	for _, proj := range parsed.Projects {
		projID, err := db.InsertProject(ctx, personID, ProjectRecord{
			Name:        proj.Name,
			Description: proj.Description,
			URL:         proj.URL,
			Tech:        proj.Tech,
			Highlights:  proj.Highlights,
		})
		if err != nil {
			slog.Debug("insert project failed", slog.String("name", proj.Name), slog.Any("error", err))
			continue
		}
		result.Projects++

		graphBuf.addNode("Proj", projID, map[string]string{graphPropName: proj.Name})

		for _, techName := range proj.Tech {
			sid := ensureSkill(ctx, db, personID, techName, "other", "intermediate", false, "resume", skillIDs, result)
			if sid > 0 {
				graphBuf.addNode("Skill", sid, map[string]string{graphPropName: techName})
				graphBuf.addEdge("Proj", projID, "USED_SKILL", "Skill", sid)
			}
		}

		text := formatProjectText(proj.Name, proj.Description, proj.Tech, proj.Highlights)
		projIDi64 := int64(projID)
		vectorTexts = append(vectorTexts, vectorEntry{
			content: text,
			memType: memTypeResumeProj,
			refID:   &projIDi64,
		})
	}

	// 8. Insert achievements + graph
	for i, achv := range parsed.Achievements {
		achvID, err := db.InsertAchievementExtended(ctx, personID, AchievementRecord{
			Text:          achv.Text,
			Metric:        achv.Metric,
			Value:         achv.Value,
			Context:       achv.Context,
			MetricNumeric: achv.MetricNumeric,
			MetricUnit:    achv.MetricUnit,
		})
		if err != nil {
			slog.Debug("insert achievement failed", slog.Int("index", i), slog.Any("error", err))
			continue
		}
		result.Achievements++

		graphBuf.addNode("Achv", achvID, map[string]string{"text": achv.Text})

		// Link to parent experience/project by context match
		if achv.Context != "" {
			linkAchievementToParent(ctx, db, graphBuf, achv.Context, achvID, personID)
		}

		achvIDi64 := int64(achvID)
		vectorTexts = append(vectorTexts, vectorEntry{
			content: achv.Text,
			memType: memTypeResumeAchv,
			refID:   &achvIDi64,
		})
	}

	// 9. Insert educations
	for _, edu := range parsed.Educations {
		_, err := db.InsertEducation(ctx, personID, EducationRecord{
			School:     edu.School,
			Degree:     edu.Degree,
			Field:      edu.Field,
			StartDate:  edu.StartDate,
			EndDate:    edu.EndDate,
			GPA:        edu.GPA,
			Highlights: edu.Highlights,
		})
		if err != nil {
			slog.Debug("insert education failed", slog.String("school", edu.School), slog.Any("error", err))
			continue
		}
		result.Educations++
	}

	// 10. Insert certifications
	for _, cert := range parsed.Certifications {
		_, err := db.InsertCertification(ctx, personID, CertificationRecord{
			Name:   cert.Name,
			Issuer: cert.Issuer,
			Year:   cert.Year,
		})
		if err != nil {
			slog.Debug("insert certification failed", slog.String("name", cert.Name), slog.Any("error", err))
			continue
		}
		result.Certifications++
	}

	// 11. Insert domains (from parse + enrichment)
	allDomains := make(map[string]bool)
	for _, d := range parsed.Domains {
		allDomains[d] = true
	}
	for _, d := range enrichment.Domains {
		allDomains[d] = true
	}
	for d := range allDomains {
		domID, err := db.InsertDomain(ctx, personID, d)
		if err != nil {
			slog.Warn("insert domain failed", slog.String("name", d), slog.Any("error", err))
			continue
		}
		graphBuf.addNode("Domain", domID, map[string]string{graphPropName: d})
		result.Domains++
	}

	// 12. Insert methodologies (from parse + enrichment)
	allMethods := make(map[string]string) // name → description
	for _, m := range parsed.Methodologies {
		allMethods[m.Name] = m.Description
	}
	for _, m := range enrichment.Methodologies {
		if _, exists := allMethods[m.Name]; !exists {
			allMethods[m.Name] = m.Description
		}
	}
	for name, desc := range allMethods {
		methID, err := db.InsertMethodology(ctx, personID, name, desc)
		if err != nil {
			slog.Warn("insert methodology failed", slog.String("name", name), slog.Any("error", err))
			continue
		}
		graphBuf.addNode("Method", methID, map[string]string{graphPropName: name})
		result.Methodologies++
	}

	// 13. Apply enrichment: implicit skills
	for _, is := range enrichment.ImplicitSkills {
		if _, exists := skillIDs[strings.ToLower(is.Name)]; exists {
			continue // already have this skill
		}
		sid := ensureSkill(ctx, db, personID, is.Name, is.Category, is.Level, true, "inferred", skillIDs, result)
		if sid > 0 {
			result.ImplicitSkills++
			graphBuf.addNode("Skill", sid, map[string]string{graphPropName: is.Name})

			// DERIVED_SKILL: link from achievement context if possible
			if is.Source != "" {
				linkImplicitSkillToSource(ctx, db, graphBuf, is.Source, sid, personID)
			}
		}
	}

	// 14. Apply enrichment: sub-projects
	for _, sp := range enrichment.SubProjects {
		parentExpID := findExperienceByHint(expByCompany, sp.ParentExperience)
		var parentPtr *int
		if parentExpID > 0 {
			parentPtr = &parentExpID
		}

		spID, err := db.InsertProjectWithParent(ctx, personID, parentPtr, ProjectRecord{
			Name:        sp.Name,
			Description: sp.Description,
			Tech:        sp.Tech,
			Highlights:  sp.Highlights,
		})
		if err != nil {
			continue
		}
		result.Projects++
		result.SubProjects++

		graphBuf.addNode("Proj", spID, map[string]string{graphPropName: sp.Name})
		if parentExpID > 0 {
			graphBuf.addEdge("Proj", spID, "PART_OF", "Exp", parentExpID)
		}

		for _, techName := range sp.Tech {
			sid := ensureSkill(ctx, db, personID, techName, "other", "intermediate", false, "resume", skillIDs, result)
			if sid > 0 {
				graphBuf.addNode("Skill", sid, map[string]string{graphPropName: techName})
				graphBuf.addEdge("Proj", spID, "USED_SKILL", "Skill", sid)
			}
		}

		text := formatProjectText(sp.Name, sp.Description, sp.Tech, sp.Highlights)
		spIDi64e := int64(spID)
		vectorTexts = append(vectorTexts, vectorEntry{
			content: text,
			memType: memTypeResumeProj,
			refID:   &spIDi64e,
		})
	}

	// 15. Apply enrichment: skill adjacencies (IMPLIES_SKILL edges)
	for _, adj := range enrichment.SkillAdjacencies {
		fromID, ok := skillIDs[strings.ToLower(adj.From)]
		if !ok {
			continue
		}
		toID := ensureSkill(ctx, db, personID, adj.To, "other", "intermediate", true, "inferred", skillIDs, result)
		if toID > 0 {
			graphBuf.addNode("Skill", toID, map[string]string{graphPropName: adj.To})
			graphBuf.addEdge("Skill", fromID, "IMPLIES_SKILL", "Skill", toID)
		}
	}

	// 16. Apply enrichment: career trajectory (EVOLVED_TO edges)
	for _, ct := range enrichment.CareerTrajectory {
		fromExpID := findExperienceByHint(expByCompany, ct.From)
		toExpID := findExperienceByHint(expByCompany, ct.To)
		if fromExpID > 0 && toExpID > 0 {
			graphBuf.addEdge("Exp", fromExpID, "EVOLVED_TO", "Exp", toExpID)
		}
	}

	// 17. Link methodologies to experiences via USED_METHOD
	exps, _ := db.GetAllExperiences(ctx, personID)
	methods, _ := db.GetAllMethodologies(ctx, personID)
	for _, exp := range exps {
		expText := strings.ToLower(exp.Description + " " + strings.Join(exp.Highlights, " "))
		for _, m := range methods {
			if strings.Contains(expText, strings.ToLower(m.Name)) {
				graphBuf.addEdge("Exp", exp.ID, "USED_METHOD", "Method", m.ID)
			}
		}
	}

	// 18. Graph counts — derived from the buffer (the number of node/edge ops
	// the build would issue), not a live AGE count: no graph statement has run
	// yet, and AGE may be absent. The replay after commit issues exactly these.
	result.GraphNodes = graphBuf.nodeCount()
	result.GraphEdges = graphBuf.edgeCount()

	// 19. Sync to resume_vectors
	if rdb := GetResumeDB(); rdb != nil {
		for _, ve := range vectorTexts {
			embedding, _ := embedPassage(ctx, rdb, ve.content, "master_resume add")
			if _, err := rdb.UpsertVectorWithSource(ctx, ve.content, ve.memType, ve.refID, embedding, sourceProfile); err != nil {
				slog.Debug("resume_vectors add failed", slog.Any("error", err))
				continue
			}
			result.VectorsStored++
		}
	}

	// 20. Mark person as enriched
	if err := db.MarkPersonEnriched(ctx, personID); err != nil {
		slog.Debug("mark person enriched failed", slog.Int("person_id", personID), slog.Any("error", err))
	}

	summary := fmt.Sprintf("Master resume built for %s: %d experiences, %d skills (%d implicit), %d projects (%d sub-projects), %d achievements, %d educations, %d certifications, %d domains, %d methodologies. Graph: %d nodes, %d edges. Vectors: %d stored.",
		parsed.Person.Name,
		result.Experiences, result.Skills, result.ImplicitSkills,
		result.Projects, result.SubProjects,
		result.Achievements, result.Educations, result.Certifications,
		result.Domains, result.Methodologies,
		result.GraphNodes, result.GraphEdges, result.VectorsStored,
	)
	if result.Truncated {
		summary += fmt.Sprintf(" (WARNING: resume was truncated from %d runes to 12000 before parsing; tail may be missing)", result.TruncatedFromRunes)
	}
	result.Summary = summary

	slog.Info("master resume built",
		slog.Int("person_id", personID),
		slog.Int("experiences", result.Experiences),
		slog.Int("skills", result.Skills),
		slog.Int("implicit_skills", result.ImplicitSkills),
		slog.Int("sub_projects", result.SubProjects),
		slog.Int("domains", result.Domains),
		slog.Int("methodologies", result.Methodologies),
		slog.Int("graph_nodes", result.GraphNodes),
		slog.Int("vectors", result.VectorsStored),
	)

	// Test seam (F1/F6): fire the write hook immediately BEFORE tx.Commit, so
	// every relational/vector write in the phase (InsertPerson, InsertExperience,
	// InsertProject, InsertSkillExtended, UpsertVectorWithSource,
	// MarkPersonEnriched, …) has executed inside the transaction. A hook-induced
	// failure rolls back all of them; a contributor who routes any one write to
	// db.pool instead of conn(ctx) leaves a row that survives the rollback, which
	// the total-row-count assertion in the atomic test catches. nil in production.
	if hook := masterResumeWriteHook; hook != nil {
		if err := hook(); err != nil {
			return nil, fmt.Errorf("master_resume_build: write hook aborted rebuild: %w", err)
		}
	}

	// Commit the atomic write phase. Until this point every relational and
	// vector write above was uncommitted inside the transaction; a failure
	// anywhere in the build returned early and the deferred rollback discarded
	// it all, leaving the pre-call profile intact. Only on a fully successful
	// build do the new rows become visible. The graph was never touched during
	// the build (all node/edge ops are in graphBuf); it is swapped only now.
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("master_resume_build: commit: %w", err)
	}
	committed = true

	// Rebuild-then-swap: the graph is cleared and the buffer replayed ONLY after
	// the profile committed. A rolled-back build never reaches here, so the old
	// graph survives alongside the old profile. The clear distinguishes "AGE is
	// absent" (tolerated — nothing to swap onto) from "a real cypher error"
	// (surfaced, not silently tolerated, so resume_generate cannot mix live and
	// dead ids). Replay failures leave the profile committed and correct with a
	// stale graph: a WARN names that state; the call does not report success as
	// though the graph were rebuilt.
	replayGraphAfterCommit(ctx, db, graphBuf)

	return result, nil
}

// describeExistingProfile returns a human-readable summary of the profile that
// a rebuild without consent would destroy, for the refuse error. It reads
// committed state (the guard runs before the transaction begins). A count whose
// query errored renders as "?" rather than "0": "0 experiences, 0 skills, …"
// reads as "nothing to lose" — the exact input that makes an agent consent to
// the replacement — and a failing count is not the same as an empty profile.
func describeExistingProfile(ctx context.Context, db *ResumeDB, personID int) string {
	fmtCount := func(v any, err error) string {
		if err != nil {
			return "?"
		}
		return fmt.Sprintf("%d", v)
	}
	expsN, err := db.GetAllExperiences(ctx, personID)
	exps := fmtCount(len(expsN), err)
	skillsN, err := db.GetAllSkills(ctx, personID)
	skills := fmtCount(len(skillsN), err)
	projsN, err := db.GetAllProjects(ctx, personID)
	projs := fmtCount(len(projsN), err)
	achvsN, err := db.GetAllAchievements(ctx, personID)
	achvs := fmtCount(len(achvsN), err)
	return fmt.Sprintf(" with %s experiences, %s skills, %s projects, %s achievements", exps, skills, projs, achvs)
}

// ensureSkill inserts or retrieves a skill, updating the tracking map and result counter.
func ensureSkill(ctx context.Context, db *ResumeDB, personID int, name, category, level string, isImplicit bool, source string, skillIDs map[string]int, result *MasterResumeBuildResult) int {
	key := strings.ToLower(name)
	if sid, ok := skillIDs[key]; ok {
		return sid
	}
	sid, err := db.InsertSkillExtended(ctx, personID, SkillRecord{
		Name:       name,
		Category:   category,
		Level:      level,
		IsImplicit: isImplicit,
		Source:     source,
	})
	if err != nil {
		return 0
	}
	skillIDs[key] = sid
	result.Skills++
	return sid
}

// findExperienceByHint looks up an experience ID by matching company name (case-insensitive).
func findExperienceByHint(expByCompany map[string]int, hint string) int {
	hint = strings.ToLower(hint)
	if id, ok := expByCompany[hint]; ok {
		return id
	}
	// Partial match
	for company, id := range expByCompany {
		if strings.Contains(company, hint) || strings.Contains(hint, company) {
			return id
		}
	}
	return 0
}

// linkImplicitSkillToSource creates a DERIVED_SKILL edge from the matching achievement to the skill.
func linkImplicitSkillToSource(ctx context.Context, db *ResumeDB, buf *graphBuffer, sourceHint string, skillID int, personID int) {
	hint := strings.ToLower(sourceHint)
	achvs, _ := db.GetAllAchievements(ctx, personID)
	for _, a := range achvs {
		if strings.Contains(strings.ToLower(a.Text), hint) || strings.Contains(strings.ToLower(a.Context), hint) {
			buf.addEdge("Achv", a.ID, "DERIVED_SKILL", "Skill", skillID)
			return
		}
	}
}

type vectorEntry struct {
	content string
	memType string
	refID   *int64
}

func formatExperienceTextExtended(title, company, startDate, endDate, description string, highlights []string, domain string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s at %s (%s–%s)", title, company, startDate, endDate)
	if domain != "" {
		fmt.Fprintf(&b, " [%s]", domain)
	}
	if description != "" {
		fmt.Fprintf(&b, ": %s", description)
	}
	for _, h := range highlights {
		fmt.Fprintf(&b, " | %s", h)
	}
	return b.String()
}

func formatProjectText(name, description string, tech []string, highlights []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project %s", name)
	if description != "" {
		fmt.Fprintf(&b, ": %s", description)
	}
	if len(tech) > 0 {
		fmt.Fprintf(&b, " [%s]", strings.Join(tech, ", "))
	}
	for _, h := range highlights {
		fmt.Fprintf(&b, " | %s", h)
	}
	return b.String()
}

// linkAchievementToParent creates a PRODUCED edge from the matching experience/project to the achievement.
func linkAchievementToParent(ctx context.Context, db *ResumeDB, buf *graphBuffer, contextHint string, achvID int, personID int) {
	hint := strings.ToLower(contextHint)

	// Try experiences
	exps, _ := db.GetAllExperiences(ctx, personID)
	for _, exp := range exps {
		if strings.Contains(hint, strings.ToLower(exp.Company)) || strings.Contains(hint, strings.ToLower(exp.Title)) {
			buf.addEdge("Exp", exp.ID, "PRODUCED", "Achv", achvID)
			return
		}
	}

	// Try projects
	projs, _ := db.GetAllProjects(ctx, personID)
	for _, proj := range projs {
		if strings.Contains(hint, strings.ToLower(proj.Name)) {
			buf.addEdge("Proj", proj.ID, "PRODUCED", "Achv", achvID)
			return
		}
	}
}

// StripMarkdownFences removes ```json and ``` wrappers from LLM output.
func StripMarkdownFences(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	return strings.TrimSpace(raw)
}

// checkResumeTruncation returns whether text exceeds limit runes and the actual rune count.
// Extracted as a pure helper to allow unit testing without a database dependency.
func checkResumeTruncation(text string, limit int) (truncated bool, origLen int) {
	n := utf8.RuneCountInString(text)
	return n > limit, n
}
