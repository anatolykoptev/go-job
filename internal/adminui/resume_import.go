package adminui

// resume_import.go — self-serve master-resume import (paste text OR upload
// txt/md/docx/pdf) for bcrypt sessions. Bespoke mux routes, NOT MountAction:
// csrfProtect there hard-caps the whole body at 1 MB and oversize gets a bare
// 400, both fatal for real resume files — so this file does headers →
// MaxBytesReader → multipart parse → session-bound CSRF itself (the register.go
// sequence).
//
// The destructive consent is two-layered on purpose: this page renders a
// confirm state (existing person id + entity counts, checkbox + hidden id),
// and jobs.BuildMasterResume re-validates the id inside the write tx under an
// advisory lock. A blind resubmit of a stale form fails closed at the seam.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go-panel/resource"
	"github.com/anatolykoptev/go-panel/shell"
	"github.com/anatolykoptev/go_job/internal/accounts"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/anatolykoptev/go_job/internal/resumeimport"
)

const (
	// resumeImportRateLimit bounds plan+apply calls per account: a preview
	// costs 2 LLM calls, an apply ~40 sequential embed calls — 6/10min covers
	// two full merge cycles plus slack, while still bounding abuse.
	resumeImportRateLimit  = 6
	resumeImportRateWindow = 10 * time.Minute
	// resumeImportMaxBody covers real .docx/.pdf resumes; the extraction
	// result feeds a 12k-rune LLM prompt either way.
	resumeImportMaxBody = 4 << 20
	// resumeImportBuildTTL self-bounds the sync build — the admin listener
	// sets no WriteTimeout, so the handler owns its own deadline.
	resumeImportBuildTTL = 5 * time.Minute
)

// buildMasterResume is a test seam — same contract as var callLLM in
// master_resume.go: capture args + fake results without an LLM.
var (
	buildMasterResume = jobs.BuildMasterResume
	planResumeBuild   = jobs.PlanResumeBuild
	applyResumePlan   = jobs.ApplyResumePlan
	diffResumePlan    = jobs.DiffResumePlan
)

type importView struct {
	CSRF       string
	ErrMsg     string
	Text       string // textarea echo on error
	PersonID   int    // >0 → confirm block rendered
	ExpCount   int
	SkillCount int
	ProjCount  int
	AchvCount  int
}

// resumeImportPage serves GET /admin/resume/import.
func resumeImportPage(p *resource.Panel, acctOf accountResolver, csrfKey []byte, cookieName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		shell.SecurityHeaders(w)
		rdb, ok := resumeScopedDB(r.Context(), acctOf)
		if !ok {
			renderSelfServeUnavailable(w, r, p, "Resume Import", "resume", "resume import")
			return
		}
		v := importView{}
		if pid := existingPersonID(r.Context(), rdb); pid > 0 {
			v = confirmView(r.Context(), rdb, pid, "")
		}
		renderResumeImport(w, r, p, csrf.Issue(csrfKey, sessionValue(r, cookieName), csrf.DefaultTTL), v)
	}
}

// resumeImportPost serves POST /admin/resume/import.
func resumeImportPost(p *resource.Panel, acctOf accountResolver, csrfKey []byte, cookieName string, limiter *accounts.LoginLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		shell.SecurityHeaders(w)
		ctx := r.Context()
		render := func(v importView) {
			renderResumeImport(w, r, p, csrf.Issue(csrfKey, sessionValue(r, cookieName), csrf.DefaultTTL), v)
		}

		r.Body = http.MaxBytesReader(w, r.Body, resumeImportMaxBody)
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				render(importView{ErrMsg: "Upload too large or unreadable (max 4 MB) — paste the text instead."})
				return
			}
			// File parts over maxMemory spill to os.TempDir and nothing
			// auto-cleans them — remove on every path (incl. CSRF/extract fails).
			defer func() {
				if r.MultipartForm != nil {
					_ = r.MultipartForm.RemoveAll()
				}
			}()
		} else if err := r.ParseForm(); err != nil {
			render(importView{ErrMsg: "Upload too large or unreadable (max 4 MB) — paste the text instead."})
			return
		}
		if csrf.Verify(csrfKey, sessionValue(r, cookieName), r.FormValue(csrf.FormField)) != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		rdb, ok := resumeScopedDB(ctx, acctOf)
		if !ok {
			renderSelfServeUnavailable(w, r, p, "Resume Import", "resume", "resume import")
			return
		}
		aid, _ := acctOf(ctx)

		// Whitelist the mode before anything dispatches on it — apply carries
		// the plan in a signed payload, so the text/file gate below is for
		// merge|rebuild only.
		mode := r.FormValue("mode")
		switch mode {
		case modeMerge, modeApply, "", modeRebuild:
		default:
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}

		// Input: pasted text XOR an uploaded file — merge/rebuild only.
		// mode=apply carries the plan in merge_payload; running the file
		// extraction would just burn IO on bytes that are never read.
		resumeText := ""
		fileName := ""
		text := ""
		if mode != modeApply {
			text = strings.TrimSpace(r.FormValue("resume_text"))
			file, hdr, ferr := r.FormFile("resume_file")
			hasFile := ferr == nil
			if ferr != nil && !errors.Is(ferr, http.ErrMissingFile) && !errors.Is(ferr, http.ErrNotMultipart) {
				render(importView{ErrMsg: "Could not read the uploaded file.", Text: text})
				return
			}
			if text != "" && hasFile {
				render(importView{ErrMsg: "Provide either pasted text or a file, not both.", Text: text})
				return
			}
			if text == "" && !hasFile {
				render(importView{ErrMsg: "Paste your resume text or choose a file (.txt, .md, .docx, .pdf)."})
				return
			}
			resumeText = text
			if hasFile {
				defer file.Close()
				data, err := io.ReadAll(file)
				if err != nil {
					render(importView{ErrMsg: "Could not read the uploaded file."})
					return
				}
				resumeText, err = resumeimport.Extract(data)
				if err != nil {
					render(importView{ErrMsg: extractErrMsg(err)})
					return
				}
				fileName = hdr.Filename
			}
		}

		// Destructive consent — fail-closed probe of the existing profile.
		// merge and apply skip the checkbox: merge only plans (nothing is
		// destroyed), and apply's consent lives inside the signed payload —
		// the preview page IS the informed consent.
		// The person probe feeds only the rebuild/"" consent gate — merge plans
		// get their own probe inside PlanResumeBuild, and apply's consent rides
		// inside the signed payload.
		replaceID, _ := strconv.Atoi(r.FormValue("replace_person_id"))
		if mode != modeMerge && mode != modeApply {
			exists, existingID, gerr := rdb.GetLatestPersonIDChecked(ctx)
			if gerr != nil {
				slog.Error("adminui: resume import person probe", "err", gerr)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if exists &&
				(existingID != replaceID || r.FormValue("confirm_replace") == "") {
				render(confirmView(ctx, rdb, existingID, text))
				return
			}
		}

		allowed, err := limiter.Allow(ctx, "resume-import:"+aid.String(), resumeImportRateLimit, resumeImportRateWindow)
		if err != nil {
			slog.Error("adminui: resume import limiter error — denying (fail-closed)", "err", err)
			allowed = false
		}
		if !allowed {
			render(importView{ErrMsg: "Too many imports — wait a few minutes before retrying.", Text: text})
			return
		}

		bctx, cancel := context.WithTimeout(ctx, resumeImportBuildTTL)
		defer cancel()

		if mode == modeMerge || mode == modeApply {
			handleResumeMerge(bctx, w, r, p, csrfKey, cookieName, aid, mode, resumeText, fileName, render)
			return
		}

		slog.Info("adminui: resume import build start",
			"account", aid, "file", fileName, "runes", utf8.RuneCountInString(resumeText), "mode", mode)
		res, err := buildMasterResume(bctx, aid, resumeText, replaceID)
		if err != nil {
			v := importView{ErrMsg: "Build failed: " + truncateErr(err), Text: text}
			// TOCTOU: the profile id may have changed — surface the fresh id so
			// a retry lands on the real consent, never trust the stale form's.
			if ex, id, perr := rdb.GetLatestPersonIDChecked(ctx); perr == nil && ex {
				v.PersonID = id
			}
			render(v)
			return
		}
		slog.Info("adminui: resume import built",
			"account", aid, "person_id", res.PersonID, "vectors", res.VectorsStored,
			"graph_nodes", res.GraphNodes, "truncated", res.Truncated)
		renderResumeImportResult(w, r, p, res)
	}
}

// existingPersonID returns the current profile's person id (0 = none). A probe
// error renders as "no consent block" — the seam's own guard stays the last
// line of defence and fails closed anyway.
func existingPersonID(ctx context.Context, rdb *jobs.ResumeAccount) int {
	exists, id, err := rdb.GetLatestPersonIDChecked(ctx)
	if err != nil || !exists {
		return 0
	}
	return id
}

// confirmView renders the destructive-consent state with fresh entity counts.
func confirmView(ctx context.Context, rdb *jobs.ResumeAccount, personID int, text string) importView {
	v := importView{PersonID: personID, Text: text}
	if xs, err := rdb.GetAllExperiences(ctx, personID); err == nil {
		v.ExpCount = len(xs)
	}
	if xs, err := rdb.GetAllSkills(ctx, personID); err == nil {
		v.SkillCount = len(xs)
	}
	if xs, err := rdb.GetAllProjects(ctx, personID); err == nil {
		v.ProjCount = len(xs)
	}
	if xs, err := rdb.GetAllAchievements(ctx, personID); err == nil {
		v.AchvCount = len(xs)
	}
	return v
}

func extractErrMsg(err error) string {
	switch {
	case errors.Is(err, resumeimport.ErrScannedPDF):
		return "This PDF has no text layer (scanned/image-only) — paste the text instead."
	case errors.Is(err, resumeimport.ErrEmptyText):
		return "No text could be extracted from that file."
	case errors.Is(err, resumeimport.ErrUnsupportedType):
		return "Unsupported file type — use .txt, .md, .docx or .pdf."
	default:
		return "Could not extract text: " + truncateErr(err)
	}
}

func truncateErr(err error) string {
	const max = 300
	msg := err.Error()
	if utf8.RuneCountInString(msg) > max {
		return string([]rune(msg)[:max]) + "…"
	}
	return msg
}

const resumeImportTmplSrc = `<style>
  .kj-section{background:var(--bg-surface,#1e293b);border:1px solid var(--border,#334155);border-radius:var(--radius-lg,.75rem);padding:1.25rem 1.5rem;margin-bottom:1.25rem}
  .kj-muted{color:var(--text-secondary,#94a3b8);font-size:.875rem;line-height:1.55}
  .kj-error{margin-bottom:1rem;padding:.5rem .75rem;border-radius:.375rem;background:rgba(239,68,68,.12);border:1px solid rgba(239,68,68,.35);color:#fca5a5;font-size:.8125rem}
  .kj-warn{margin-bottom:1rem;padding:.75rem;border-radius:.375rem;background:rgba(234,179,8,.10);border:1px solid rgba(234,179,8,.35);color:#fde68a;font-size:.8125rem}
  .kj-field{margin-bottom:.75rem}
  .kj-field label{display:block;font-size:.8rem;color:var(--text-secondary,#94a3b8);margin-bottom:.25rem}
  .kj-input{width:100%;background:var(--bg-deep,#0f172a);border:1px solid var(--border,#334155);border-radius:.375rem;padding:.4rem .6rem;color:var(--text-primary,#f1f5f9);font-size:.875rem}
  textarea.kj-input{min-height:12rem;font-family:ui-monospace,monospace}
  .kj-btn{padding:.4rem .9rem;border-radius:.375rem;font-size:.8125rem;cursor:pointer;border:none;background:var(--accent,#3b82f6);color:#0f172a;font-weight:600}
  .kj-check{display:flex;gap:.5rem;align-items:center;font-size:.85rem;margin:.5rem 0}
{{template "sharedCSS" .}}
</style>

<div class="page-header">
  <h2>&#x1F4E5; Resume Import</h2>
  <p class="kj-muted">Paste your resume or upload a file — the builder parses it into skills, experience, projects and achievements, then generates embeddings and the knowledge graph. Building takes a minute; do not close the tab.</p>
</div>

{{if .ErrMsg}}<div class="kj-error" role="alert">{{.ErrMsg}}</div>{{end}}

{{if .PersonID}}
<div class="kj-warn" role="alert">
  A profile already exists (person #{{.PersonID}}): {{.ExpCount}} experiences, {{.SkillCount}} skills, {{.ProjCount}} projects, {{.AchvCount}} achievements.
  <strong>Preview merge</strong> folds the new document in and shows the exact changes first — manual edits survive.
  <strong>Rebuild</strong> destroys it and rebuilds from the new text only (requires the checkbox).
</div>
{{end}}

<div class="kj-section">
  <form method="POST" action="{{.BasePath}}/resume/import" enctype="multipart/form-data">
    <input type="hidden" name="_csrf" value="{{.CSRF}}"/>
    {{if .PersonID}}<input type="hidden" name="replace_person_id" value="{{.PersonID}}"/>{{end}}
    <div class="kj-field">
      <label for="resume_text">Resume text</label>
      <textarea id="resume_text" name="resume_text" class="kj-input" placeholder="Paste the full resume text…">{{.Text}}</textarea>
    </div>
    <div class="kj-field">
      <label for="resume_file">…or upload a file (.txt, .md, .docx, .pdf — max 4 MB)</label>
      <input id="resume_file" name="resume_file" type="file" accept=".txt,.md,.docx,.pdf" class="kj-input"/>
    </div>
    {{if .PersonID}}
    <div class="kj-check">
      <input type="checkbox" id="confirm_replace" name="confirm_replace" value="1"/>
      <label for="confirm_replace">Apply to the existing profile (#{{.PersonID}})</label>
    </div>
    {{end}}
    {{if .PersonID}}
    <button type="submit" name="mode" value="merge" class="kj-btn">Preview merge</button>
    <button type="submit" name="mode" value="rebuild" class="kj-btn" style="background:#7f1d1d;color:#fecaca">Rebuild from scratch</button>
    {{else}}
    <button type="submit" class="kj-btn">Build master resume</button>
    {{end}}
  </form>
</div>
`

var resumeImportTmpl = template.Must(template.Must(template.New("resume-import").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc)).Parse(resumeImportTmplSrc))

func renderResumeImport(w http.ResponseWriter, r *http.Request, p *resource.Panel, csrfToken string, v importView) {
	var sb strings.Builder
	v.CSRF = csrfToken
	data := struct {
		BasePath string
		importView
	}{BasePath: adminBasePath, importView: v}
	if err := resumeImportTmpl.Execute(&sb, data); err != nil {
		slog.Error("adminui: render resume import", "err", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	if err := p.RenderPageHTML(w, r, "Resume Import", "resume", sb.String()); err != nil {
		slog.Error("adminui: render resume import", "err", err)
	}
}

type importResultView struct {
	Summary            string
	PersonID           int
	Experiences        int
	Skills             int
	Projects           int
	Achievements       int
	Educations         int
	Certifications     int
	Domains            int
	Methodologies      int
	ImplicitSkills     int
	SubProjects        int
	GraphNodes         int
	GraphEdges         int
	VectorsStored      int
	Truncated          bool
	TruncatedFromRunes int
}

const resumeImportResultTmplSrc = `<style>
  .kj-section{background:var(--bg-surface,#1e293b);border:1px solid var(--border,#334155);border-radius:var(--radius-lg,.75rem);padding:1.25rem 1.5rem;margin-bottom:1.25rem}
  .kj-muted{color:var(--text-secondary,#94a3b8);font-size:.875rem;line-height:1.55}
  .kj-ok{margin-bottom:1rem;padding:.5rem .75rem;border-radius:.375rem;background:rgba(34,197,94,.12);border:1px solid rgba(34,197,94,.35);color:#86efac;font-size:.8125rem}
  .kj-warn{margin-bottom:1rem;padding:.75rem;border-radius:.375rem;background:rgba(234,179,8,.10);border:1px solid rgba(234,179,8,.35);color:#fde68a;font-size:.8125rem}
  .kj-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(9rem,1fr));gap:.5rem;margin:1rem 0}
  .kj-cell{background:var(--bg-deep,#0f172a);border:1px solid var(--border,#334155);border-radius:.375rem;padding:.5rem .75rem;text-align:center}
  .kj-cell b{display:block;font-size:1.25rem;color:var(--accent,#3b82f6)}
  .kj-cell span{font-size:.7rem;color:var(--text-secondary,#94a3b8);text-transform:uppercase;letter-spacing:.03em}
{{template "sharedCSS" .}}
</style>

<div class="page-header">
  <h2>&#x1F4E5; Resume Import</h2>
</div>

<div class="kj-ok" role="status">Master resume built — profile #{{.PersonID}}.</div>
{{if .Truncated}}<div class="kj-warn">The resume was truncated from {{.TruncatedFromRunes}} runes to the 12000-rune parse window — the tail may be missing.</div>{{end}}

<div class="kj-section">
  <div class="kj-grid">
    <div class="kj-cell"><b>{{.Experiences}}</b><span>experiences</span></div>
    <div class="kj-cell"><b>{{.Skills}}</b><span>skills</span></div>
    <div class="kj-cell"><b>{{.Projects}}</b><span>projects</span></div>
    <div class="kj-cell"><b>{{.Achievements}}</b><span>achievements</span></div>
    <div class="kj-cell"><b>{{.Educations}}</b><span>education</span></div>
    <div class="kj-cell"><b>{{.Certifications}}</b><span>certs</span></div>
    <div class="kj-cell"><b>{{.Domains}}</b><span>domains</span></div>
    <div class="kj-cell"><b>{{.Methodologies}}</b><span>methods</span></div>
    <div class="kj-cell"><b>{{.ImplicitSkills}}</b><span>implicit</span></div>
    <div class="kj-cell"><b>{{.SubProjects}}</b><span>sub-projects</span></div>
    <div class="kj-cell"><b>{{.GraphNodes}}</b><span>graph nodes</span></div>
    <div class="kj-cell"><b>{{.VectorsStored}}</b><span>vectors</span></div>
  </div>
  <p class="kj-muted">{{.Summary}}</p>
  <p><a href="{{.BasePath}}/resume">Open the resume →</a></p>
</div>
`

var resumeImportResultTmpl = template.Must(template.Must(template.New("resume-import-result").Funcs(adminuiFuncMap).Parse(sharedPartialsSrc)).Parse(resumeImportResultTmplSrc))

func renderResumeImportResult(w http.ResponseWriter, r *http.Request, p *resource.Panel, res *jobs.MasterResumeBuildResult) {
	var sb strings.Builder
	data := struct {
		BasePath string
		importResultView
	}{BasePath: adminBasePath, importResultView: importResultView{
		Summary:            res.Summary,
		PersonID:           res.PersonID,
		Experiences:        res.Experiences,
		Skills:             res.Skills,
		Projects:           res.Projects,
		Achievements:       res.Achievements,
		Educations:         res.Educations,
		Certifications:     res.Certifications,
		Domains:            res.Domains,
		Methodologies:      res.Methodologies,
		ImplicitSkills:     res.ImplicitSkills,
		SubProjects:        res.SubProjects,
		GraphNodes:         res.GraphNodes,
		GraphEdges:         res.GraphEdges,
		VectorsStored:      res.VectorsStored,
		Truncated:          res.Truncated,
		TruncatedFromRunes: res.TruncatedFromRunes,
	}}
	if err := resumeImportResultTmpl.Execute(&sb, data); err != nil {
		slog.Error("adminui: render resume import result", "err", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	if err := p.RenderPageHTML(w, r, "Resume Import", "resume", sb.String()); err != nil {
		slog.Error("adminui: render resume import result", "err", err)
	}
}

// --- merge preview/apply ---

// mergeApplyPayload is the signed state carried between the merge preview
// render and the apply POST — stateless, tamper-proof, account-bound, expiring.
type mergeApplyPayload struct {
	AccountID  string           `json:"a"`
	ExpiresAt  int64            `json:"e"`
	HasChanges bool             `json:"c"` // empty-diff previews mint payloads that refuse on apply
	Plan       *jobs.ResumePlan `json:"plan"`
}

// Import POST modes — the whitelist also lives in the handler switch.
const (
	modeMerge   = "merge"
	modeApply   = "apply"
	modeRebuild = "rebuild"
)

const mergePayloadTTL = 15 * time.Minute

// mergePayloadKey derives a domain-separated subkey from the CSRF key — merge
// payloads and CSRF tokens then share no HMAC space, so a wire-format change
// on either side can never bridge them.
func mergePayloadKey(key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("merge-apply-payload-v1"))
	return mac.Sum(nil)
}

func signMergePayload(key []byte, p *mergeApplyPayload) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, mergePayloadKey(key))
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// verifyMergePayload checks the signature, the TTL, and that the payload was
// minted for THIS session account — a payload lifted from another user's form
// cannot apply a plan to a foreign profile.
func verifyMergePayload(key []byte, tok string, aid uuid.UUID) (*jobs.ResumePlan, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 2 {
		return nil, errors.New("malformed merge payload")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("malformed merge payload")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("malformed merge payload")
	}
	mac := hmac.New(sha256.New, mergePayloadKey(key))
	mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, errors.New("bad merge payload signature")
	}
	var p mergeApplyPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errors.New("malformed merge payload")
	}
	if p.AccountID != aid.String() {
		return nil, errors.New("merge payload belongs to another account")
	}
	if time.Now().Unix() > p.ExpiresAt {
		return nil, errors.New("merge preview expired — run Preview merge again")
	}
	if p.Plan == nil {
		return nil, errors.New("empty merge plan")
	}
	if !p.HasChanges {
		return nil, errors.New("the merge produced no changes — nothing to apply")
	}
	return p.Plan, nil
}

// handleResumeMerge runs the merge sub-flow: "merge" plans and renders the
// diff preview; "apply" verifies the signed payload and writes atomically.
func handleResumeMerge(bctx context.Context, w http.ResponseWriter, r *http.Request, p *resource.Panel,
	csrfKey []byte, cookieName string, aid uuid.UUID, mode, resumeText, fileName string,
	render func(importView)) {

	if mode == modeMerge {
		slog.Info("adminui: resume merge plan start",
			"account", aid, "file", fileName, "runes", utf8.RuneCountInString(resumeText))
		plan, err := planResumeBuild(bctx, aid, resumeText, true)
		if err != nil {
			render(importView{ErrMsg: "Merge failed: " + truncateErr(err), Text: resumeText})
			return
		}
		diff, err := diffResumePlan(plan.Baseline, plan)
		if err != nil {
			render(importView{ErrMsg: "Diff failed: " + truncateErr(err), Text: resumeText})
			return
		}
		tok, err := signMergePayload(csrfKey, &mergeApplyPayload{
			AccountID:  aid.String(),
			ExpiresAt:  time.Now().Add(mergePayloadTTL).Unix(),
			HasChanges: !diff.Empty,
			Plan:       plan,
		})
		if err != nil {
			render(importView{ErrMsg: "Could not sign the preview — internal error.", Text: resumeText})
			return
		}
		renderMergePreview(w, r, p, csrf.Issue(csrfKey, sessionValue(r, cookieName), csrf.DefaultTTL), diff, tok, plan.Truncated)
		return
	}

	// mode == modeApply
	plan, err := verifyMergePayload(csrfKey, r.FormValue("merge_payload"), aid)
	if err != nil {
		render(importView{ErrMsg: "Merge apply refused: " + truncateErr(err)})
		return
	}
	slog.Info("adminui: resume merge apply start", "account", aid, "person_id", plan.PersonID)
	res, err := applyResumePlan(bctx, aid, plan, plan.PersonID)
	if err != nil {
		msg := "Apply failed: " + truncateErr(err)
		if strings.Contains(err.Error(), "already exists") ||
			strings.Contains(err.Error(), "changed since the merge started") ||
			strings.Contains(err.Error(), "id changed under the rebuild lock") {
			msg += " — the profile changed after the preview; run Preview merge again."
		}
		render(importView{ErrMsg: msg})
		return
	}
	slog.Info("adminui: resume merge applied",
		"account", aid, "person_id", res.PersonID, "vectors", res.VectorsStored,
		"graph_nodes", res.GraphNodes)
	renderResumeImportResult(w, r, p, res)
}

// renderMergePreview renders the diff page between the current profile and
// the merge candidate.
func renderMergePreview(w http.ResponseWriter, r *http.Request, p *resource.Panel, csrfToken string, diff *jobs.ResumeDiff, payload string, truncated bool) {
	var sb strings.Builder
	sb.WriteString(`<div class="page-header"><h2>&#x1F50D; Merge Preview</h2>
<p class="kj-muted">The proposed merge — nothing is written until you apply.</p></div>`)
	if truncated {
		sb.WriteString(`<div class="kj-warn" role="alert">The resume text was truncated before parsing — review carefully.</div>`)
	}
	if diff.Empty {
		sb.WriteString(`<div class="kj-section"><p class="kj-muted">No changes — the document adds nothing new to the current profile.</p></div>`)
	} else {
		if len(diff.Person) > 0 {
			sb.WriteString(`<div class="kj-section"><h3>Profile</h3>`)
			writeFieldChanges(&sb, diff.Person)
			sb.WriteString(`</div>`)
		}
		for _, s := range diff.Sections {
			if len(s.Added)+len(s.Removed)+len(s.Changed) == 0 {
				continue
			}
			sb.WriteString(`<div class="kj-section"><h3>` + htmlEsc(s.Name) + `</h3>`)
			for _, e := range s.Added {
				sb.WriteString(`<div class="kj-diff kj-add">+ ` + htmlEsc(e.Label) + `</div>`)
			}
			for _, e := range s.Removed {
				sb.WriteString(`<div class="kj-diff kj-del">&minus; ` + htmlEsc(e.Label) + `</div>`)
			}
			for _, e := range s.Changed {
				sb.WriteString(`<div class="kj-diff kj-chg">~ ` + htmlEsc(e.Label) + `</div>`)
				writeFieldChanges(&sb, e.Changes)
			}
			sb.WriteString(`</div>`)
		}
	}
	sb.WriteString(`<div class="kj-section"><form method="POST" action="` + htmlEsc(r.URL.Path) + `">`)
	sb.WriteString(`<input type="hidden" name="_csrf" value="` + htmlEsc(csrfToken) + `"/>`)
	sb.WriteString(`<input type="hidden" name="merge_payload" value="` + htmlEsc(payload) + `"/>`)
	sb.WriteString(`<input type="hidden" name="mode" value="apply"/>`)
	if !diff.Empty {
		sb.WriteString(`<button type="submit" class="kj-btn">Apply merge</button> `)
	}
	sb.WriteString(`<a class="kj-btn" style="background:var(--bg-deep,#0f172a);color:var(--text-secondary,#94a3b8)" href="` + htmlEsc(r.URL.Path) + `">Cancel</a>`)
	sb.WriteString(`</form></div>`)
	body := previewStyles + sb.String()
	if err := p.RenderPageHTML(w, r, "Merge Preview", "resume", body); err != nil {
		slog.Error("adminui: render merge preview", "err", err)
	}
}

const previewStyles = `<style>
  .kj-section{background:var(--bg-surface,#1e293b);border:1px solid var(--border,#334155);border-radius:var(--radius-lg,.75rem);padding:1.25rem 1.5rem;margin-bottom:1.25rem}
  .kj-muted{color:var(--text-secondary,#94a3b8);font-size:.875rem;line-height:1.55}
  .kj-warn{margin-bottom:1rem;padding:.75rem;border-radius:.375rem;background:rgba(234,179,8,.10);border:1px solid rgba(234,179,8,.35);color:#fde68a;font-size:.8125rem}
  .kj-diff{font-size:.875rem;padding:.2rem .4rem;border-radius:.25rem;margin:.15rem 0}
  .kj-add{color:#86efac}
  .kj-del{color:#fca5a5;text-decoration:line-through}
  .kj-chg{color:#fde68a}
  .kj-field-diff{font-size:.8rem;color:var(--text-secondary,#94a3b8);margin-left:1.5rem}
  .kj-old{color:#fca5a5}
  .kj-new{color:#86efac}
  .kj-btn{display:inline-block;padding:.4rem .9rem;border-radius:.375rem;font-size:.8125rem;cursor:pointer;border:none;background:var(--accent,#3b82f6);color:#0f172a;font-weight:600;text-decoration:none}
</style>`

func writeFieldChanges(sb *strings.Builder, ch []jobs.FieldChange) {
	for _, c := range ch {
		old := c.Old
		if old == "" {
			old = "—"
		}
		nv := c.New
		if nv == "" {
			nv = "—"
		}
		sb.WriteString(`<div class="kj-field-diff">` + htmlEsc(c.Field) + `: <span class="kj-old">` + htmlEsc(old) + `</span> → <span class="kj-new">` + htmlEsc(nv) + `</span></div>`)
	}
}

func htmlEsc(s string) string { return template.HTMLEscapeString(s) }
