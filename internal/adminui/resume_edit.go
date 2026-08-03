package adminui

import (
	"bytes"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/anatolykoptev/go-panel/auth"
	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
)

// cookieNamer is the optional capability interface for authenticators that bind
// CSRF tokens to a session cookie. Mirrors resource.sessionCookier defined at
// resource/resource.go:174. Both HMACAuth and BcryptTOTPAuth satisfy it;
// enforcement is fail-closed at adminui.New() via checkAuthCapabilities.
type cookieNamer interface {
	SessionCookieName() string
}

// resumeEditData is the template context for the resume editor page.
// All string fields come from the DB and are auto-escaped by html/template.
type resumeEditData struct {
	CSRFToken      string
	Person         *jobs.PersonRecord
	HourlyRateStr  string // pre-formatted for template: "175.00" or ""
	Experiences    []jobs.ExperienceRecord
	Skills         []jobs.SkillRecord
	Achievements   []jobs.AchievementRecord
	Domains        []jobs.DomainRecord
	Methodologies  []jobs.MethodologyRecord
	Projects       []jobs.ProjectRecord
	Educations     []jobs.EducationRecord
	Certifications []jobs.CertificationRecord
}

// resumeEditHandler renders the full resume editor page (GET /admin/resume/edit).
func resumeEditHandler(p *resource.Panel, a auth.Authenticator, csrfKey []byte) http.HandlerFunc {
	tmpl := template.Must(template.New("resume_edit").Parse(resumeEditTmplSrc))

	return func(w http.ResponseWriter, r *http.Request) {
		db := jobs.GetResumeDB()
		if db == nil {
			if err := p.RenderPageHTML(w, r, "Edit Resume", "resume", resumeEmptyHTML("Resume database not configured (set DATABASE_URL).")); err != nil {
				slog.Error("adminui: render resume_edit", "err", err)
			}
			return
		}

		ctx := r.Context()
		personID := db.GetLatestPersonID(ctx)
		if personID == 0 {
			if err := p.RenderPageHTML(w, r, "Edit Resume", "resume", resumeEmptyHTML("No resume data yet — run master_resume_build first.")); err != nil {
				slog.Error("adminui: render resume_edit", "err", err)
			}
			return
		}

		person, err := db.GetPerson(ctx, personID)
		if err != nil {
			slog.Warn("resumeEditHandler: GetPerson", "err", err)
			if err2 := p.RenderPageHTML(w, r, "Edit Resume", "resume", resumeEmptyHTML("Could not load person: "+err.Error())); err2 != nil {
				slog.Error("adminui: render resume_edit", "err", err2)
			}
			return
		}

		exps, _ := db.GetAllExperiences(ctx, personID)
		skills, _ := db.GetAllSkills(ctx, personID)
		achs, _ := db.GetAllAchievements(ctx, personID)
		domains, _ := db.GetAllDomains(ctx, personID)
		meths, _ := db.GetAllMethodologies(ctx, personID)
		projs, _ := db.GetAllProjects(ctx, personID)
		edus, _ := db.GetAllEducations(ctx, personID)
		certs, _ := db.GetAllCertifications(ctx, personID)

		sessVal := sessionValue(r, a.(cookieNamer).SessionCookieName())
		hourlyRateStr := ""
		if person.HourlyRateCents > 0 {
			hourlyRateStr = fmt.Sprintf("%.2f", float64(person.HourlyRateCents)/100)
		}
		d := resumeEditData{
			CSRFToken:      csrf.Issue(csrfKey, sessVal, csrf.DefaultTTL),
			Person:         person,
			HourlyRateStr:  hourlyRateStr,
			Experiences:    exps,
			Skills:         skills,
			Achievements:   achs,
			Domains:        domains,
			Methodologies:  meths,
			Projects:       projs,
			Educations:     edus,
			Certifications: certs,
		}

		var buf bytes.Buffer
		if execErr := tmpl.Execute(&buf, d); execErr != nil {
			slog.Error("resumeEditHandler: template execute", "err", execErr)
			http.Error(w, "render error", http.StatusInternalServerError)
			return
		}
		if err := p.RenderPageHTML(w, r, "Edit Resume", "resume", buf.String()); err != nil {
			slog.Error("adminui: render resume_edit", "err", err)
		}
	}
}

// resumePersonEditHandler handles POST /admin/resume/person.
func resumePersonEditHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		// Validate form inputs before any DB call.
		if r.FormValue("name") == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		headline := r.FormValue("headline")
		hourlyRateCents, rateErr := parseDollarsToCents(r.FormValue("hourly_rate"))
		if rateErr != nil {
			http.Error(w, rateErr.Error(), http.StatusBadRequest)
			return
		}
		db, personID, ok := requireResumeDB(w, r)
		if !ok {
			return
		}

		person, err := db.GetPerson(r.Context(), personID)
		if err != nil || person == nil {
			http.Error(w, "person not found", http.StatusNotFound)
			return
		}
		updated := jobs.PersonRecord{
			ID:       person.ID,
			Name:     r.FormValue("name"),
			Email:    r.FormValue("email"),
			Phone:    r.FormValue("phone"),
			Location: r.FormValue("location"),
			Links:    person.Links,
			Summary:  r.FormValue("summary"),
		}
		if err := db.UpdateResumePerson(r.Context(), personID, updated); err != nil {
			slog.Error("resumePersonEditHandler: UpdateResumePerson", "err", err)
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		// Update Upwork-specific fields.
		if err := db.UpdatePersonUpworkFields(r.Context(), personID, headline, hourlyRateCents); err != nil {
			slog.Error("resumePersonEditHandler: UpdatePersonUpworkFields", "err", err)
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeSkillCreateHandler handles POST /admin/resume/skill.
func resumeSkillCreateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		db, personID, ok := requireResumeDB(w, r)
		if !ok {
			return
		}
		name := r.FormValue("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		level := r.FormValue("level")
		if level == "" {
			level = "intermediate"
		}
		if !jobs.IsValidSkillLevel(level) {
			http.Error(w, fmt.Sprintf("invalid level %q", level), http.StatusBadRequest)
			return
		}
		s := jobs.SkillRecord{Name: name, Category: r.FormValue("category"), Level: level}
		if _, err := db.InsertSkill(r.Context(), personID, s); err != nil {
			slog.Error("resumeSkillCreateHandler: InsertSkill", "err", err)
			http.Error(w, "insert failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeSkillDeleteHandler handles POST /admin/resume/skill/{id}/delete.
// parseIDParam runs BEFORE requireResumeDB so a bad id returns 400 regardless of DB state.
func resumeSkillDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		id, ok := parseIDParam(w, r)
		if !ok {
			return
		}
		db, _, ok2 := requireResumeDB(w, r)
		if !ok2 {
			return
		}
		if err := db.DeleteSkill(r.Context(), id); err != nil {
			slog.Error("resumeSkillDeleteHandler: DeleteSkill", "id", id, "err", err)
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeSkillLevelHandler handles POST /admin/resume/skill/{id}/level.
// parseIDParam + level validation run BEFORE requireResumeDB for early rejection.
func resumeSkillLevelHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		id, ok := parseIDParam(w, r)
		if !ok {
			return
		}
		level := r.FormValue("level")
		if !jobs.IsValidSkillLevel(level) {
			http.Error(w, fmt.Sprintf("invalid level %q", level), http.StatusBadRequest)
			return
		}
		db, _, ok2 := requireResumeDB(w, r)
		if !ok2 {
			return
		}
		if err := db.UpdateSkillLevel(r.Context(), id, level); err != nil {
			slog.Error("resumeSkillLevelHandler: UpdateSkillLevel", "id", id, "err", err)
			http.Error(w, "update failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeAchievementCreateHandler handles POST /admin/resume/achievement.
//
//nolint:dupl
func resumeAchievementCreateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		db, personID, ok := requireResumeDB(w, r)
		if !ok {
			return
		}
		text := r.FormValue("text")
		if text == "" {
			http.Error(w, "text is required", http.StatusBadRequest)
			return
		}
		ach := jobs.AchievementRecord{
			Text:    text,
			Metric:  r.FormValue("metric"),
			Value:   r.FormValue("value"),
			Context: r.FormValue("context"),
		}
		if _, err := db.InsertAchievement(r.Context(), personID, ach); err != nil {
			slog.Error("resumeAchievementCreateHandler: InsertAchievement", "err", err)
			http.Error(w, "insert failed", http.StatusInternalServerError)
			return
		}
		syncProfileVectorsBestEffort(r, personID)
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeAchievementDeleteHandler handles POST /admin/resume/achievement/{id}/delete.
func resumeAchievementDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		id, ok := parseIDParam(w, r)
		if !ok {
			return
		}
		db, personID, ok2 := requireResumeDB(w, r)
		if !ok2 {
			return
		}
		if err := db.DeleteAchievement(r.Context(), id); err != nil {
			slog.Error("resumeAchievementDeleteHandler: DeleteAchievement", "id", id, "err", err)
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		syncProfileVectorsBestEffort(r, personID)
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeDomainCreateHandler handles POST /admin/resume/domain.
func resumeDomainCreateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		db, personID, ok := requireResumeDB(w, r)
		if !ok {
			return
		}
		name := r.FormValue("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if _, err := db.InsertDomain(r.Context(), personID, name); err != nil {
			slog.Error("resumeDomainCreateHandler: InsertDomain", "err", err)
			http.Error(w, "insert failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeDomainDeleteHandler handles POST /admin/resume/domain/{id}/delete.
func resumeDomainDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		id, ok := parseIDParam(w, r)
		if !ok {
			return
		}
		db, _, ok2 := requireResumeDB(w, r)
		if !ok2 {
			return
		}
		if err := db.DeleteDomain(r.Context(), id); err != nil {
			slog.Error("resumeDomainDeleteHandler: DeleteDomain", "id", id, "err", err)
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeMethodologyCreateHandler handles POST /admin/resume/methodology.
func resumeMethodologyCreateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		db, personID, ok := requireResumeDB(w, r)
		if !ok {
			return
		}
		name := r.FormValue("name")
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if _, err := db.InsertMethodology(r.Context(), personID, name, r.FormValue("description")); err != nil {
			slog.Error("resumeMethodologyCreateHandler: InsertMethodology", "err", err)
			http.Error(w, "insert failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeMethodologyDeleteHandler handles POST /admin/resume/methodology/{id}/delete.
func resumeMethodologyDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		id, ok := parseIDParam(w, r)
		if !ok {
			return
		}
		db, _, ok2 := requireResumeDB(w, r)
		if !ok2 {
			return
		}
		if err := db.DeleteMethodology(r.Context(), id); err != nil {
			slog.Error("resumeMethodologyDeleteHandler: DeleteMethodology", "id", id, "err", err)
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// --- helpers ---

// requireResumeDB returns the package-level ResumeDB and the latest personID.
// Writes a 500/404 and returns ok=false when DB is nil or no person exists.
func requireResumeDB(w http.ResponseWriter, r *http.Request) (*jobs.ResumeDB, int, bool) {
	db := jobs.GetResumeDB()
	if db == nil {
		http.Error(w, "resume db not configured", http.StatusInternalServerError)
		return nil, 0, false
	}
	personID := db.GetLatestPersonID(r.Context())
	if personID == 0 {
		http.Error(w, "no resume person found", http.StatusNotFound)
		return nil, 0, false
	}
	return db, personID, true
}

// parseIDParam extracts and validates the {id} path value (must be positive int).
func parseIDParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.PathValue("id")
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// syncProfileVectorsBestEffort re-derives the structured-profile resume_vectors
// rows after a profile mutation. It is best-effort: the entity mutation has
// already persisted, so an embedder outage or a vector-store error must NOT
// fail the HTTP response — the derived rows degrade to NULL embeddings for a
// later backfill. Manual source='agent' memories are never touched by the sync.
func syncProfileVectorsBestEffort(r *http.Request, personID int) {
	if err := jobs.SyncProfileVectors(r.Context(), personID); err != nil {
		slog.Warn("resume edit: profile vector sync failed (mutation already persisted)",
			slog.Int("person_id", personID), slog.Any("err", err))
	}
}
