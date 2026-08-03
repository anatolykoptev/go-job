package adminui

import (
	"log/slog"
	"net/http"

	"github.com/anatolykoptev/go_job/internal/engine/jobs"
)

// resumeProjectCreateHandler handles POST /admin/resume/project.
func resumeProjectCreateHandler() http.HandlerFunc {
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
		p := jobs.ProjectRecord{
			Name:        name,
			Description: r.FormValue("description"),
			URL:         r.FormValue("url"),
		}
		if _, err := db.InsertProject(r.Context(), personID, p); err != nil {
			slog.Error("resumeProjectCreateHandler: InsertProject", "err", err)
			http.Error(w, "insert failed", http.StatusInternalServerError)
			return
		}
		syncProfileVectorsBestEffort(r, personID)
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeProjectDeleteHandler handles POST /admin/resume/project/{id}/delete.
// parseIDParam runs BEFORE requireResumeDB so a bad id returns 400 regardless of DB state.
func resumeProjectDeleteHandler() http.HandlerFunc {
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
		if err := db.DeleteProject(r.Context(), id); err != nil {
			slog.Error("resumeProjectDeleteHandler: DeleteProject", "id", id, "err", err)
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		syncProfileVectorsBestEffort(r, personID)
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeEducationCreateHandler handles POST /admin/resume/education.
func resumeEducationCreateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF already verified by MountAction — no verifyCSRF call needed.
		db, personID, ok := requireResumeDB(w, r)
		if !ok {
			return
		}
		school := r.FormValue("school")
		if school == "" {
			http.Error(w, "school is required", http.StatusBadRequest)
			return
		}
		e := jobs.EducationRecord{
			School:    school,
			Degree:    r.FormValue("degree"),
			Field:     r.FormValue("field"),
			StartDate: r.FormValue("start_date"),
			EndDate:   r.FormValue("end_date"),
			GPA:       r.FormValue("gpa"),
		}
		if _, err := db.InsertEducation(r.Context(), personID, e); err != nil {
			slog.Error("resumeEducationCreateHandler: InsertEducation", "err", err)
			http.Error(w, "insert failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeEducationDeleteHandler handles POST /admin/resume/education/{id}/delete.
// parseIDParam runs BEFORE requireResumeDB so a bad id returns 400 regardless of DB state.
func resumeEducationDeleteHandler() http.HandlerFunc {
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
		if err := db.DeleteEducation(r.Context(), id); err != nil {
			slog.Error("resumeEducationDeleteHandler: DeleteEducation", "id", id, "err", err)
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeCertificationCreateHandler handles POST /admin/resume/certification.
//
//nolint:dupl
func resumeCertificationCreateHandler() http.HandlerFunc {
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
		c := jobs.CertificationRecord{
			Name:   name,
			Issuer: r.FormValue("issuer"),
			Year:   r.FormValue("year"),
			URL:    r.FormValue("url"),
		}
		if _, err := db.InsertCertification(r.Context(), personID, c); err != nil {
			slog.Error("resumeCertificationCreateHandler: InsertCertification", "err", err)
			http.Error(w, "insert failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}

// resumeCertificationDeleteHandler handles POST /admin/resume/certification/{id}/delete.
// parseIDParam runs BEFORE requireResumeDB so a bad id returns 400 regardless of DB state.
func resumeCertificationDeleteHandler() http.HandlerFunc {
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
		if err := db.DeleteCertification(r.Context(), id); err != nil {
			slog.Error("resumeCertificationDeleteHandler: DeleteCertification", "id", id, "err", err)
			http.Error(w, "delete failed", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/admin/resume/edit", http.StatusSeeOther)
	}
}
