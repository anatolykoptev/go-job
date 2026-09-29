package adminui

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/anatolykoptev/go_job/internal/dbtest"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
)

var errConsentRefused = errors.New("master_resume_build: a profile already exists (person_id=1)")

type buildCall struct {
	aid  uuid.UUID
	text string
	pid  int
}

// stubBuild swaps the build seam for a recorder and returns the calls slice.
func stubBuild(t *testing.T, res *jobs.MasterResumeBuildResult, err error) *[]buildCall {
	t.Helper()
	calls := new([]buildCall)
	prev := buildMasterResume
	buildMasterResume = func(ctx context.Context, aid uuid.UUID, text string, pid int) (*jobs.MasterResumeBuildResult, error) {
		*calls = append(*calls, buildCall{aid, text, pid})
		return res, err
	}
	t.Cleanup(func() { buildMasterResume = prev })
	return calls
}

// wireResumeDB points the resume facade at the test DB (the fixture's New()
// does not set the package-level instance).
func wireResumeDB(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	dbtest.RequireTestDB(t, dsn)
	db, err := jobs.ConnectResumeDB(context.Background(), dsn)
	require.NoError(t, err)
	jobs.SetResumeDB(db)
	t.Cleanup(func() { jobs.SetResumeDB(nil); db.Close() })
}

// seedPerson inserts a bare person row so the consent leg has an existing
// profile to defend.
func seedPerson(t *testing.T, aid uuid.UUID) int {
	t.Helper()
	rdb := jobs.GetResumeDB().ForAccount(aid)
	pid, err := rdb.InsertPerson(context.Background(), jobs.PersonRecord{Name: "Existing Profile"})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = jobs.GetResumeDB().Pool().Exec(context.Background(), `DELETE FROM resume_persons WHERE id = $1`, pid)
	})
	return pid
}

// zipSingle builds an in-memory zip with one entry — the docx shape for the
// upload tests.
func zipSingle(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	require.NoError(t, err)
	_, err = w.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// multipartPost issues a multipart POST with session cookies + CSRF — the
// selfServePost twin for file uploads.
func multipartPost(t *testing.T, h http.Handler, cookies []*http.Cookie, csrfKey, path string, fields map[string]string, fileField, fileName string, fileData []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		require.NoError(t, mw.WriteField(k, v))
	}
	if fileField != "" {
		fw, err := mw.CreateFormFile(fileField, fileName)
		require.NoError(t, err)
		_, err = fw.Write(fileData)
		require.NoError(t, err)
	}
	require.NoError(t, mw.WriteField("_csrf", sessionToken(csrfKey, cookies)))
	require.NoError(t, mw.Close())
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestResumeImport_Unauth(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	if w := selfServeGet(fx.handler, nil, adminBasePath+"/resume/import"); w.Code != http.StatusSeeOther {
		t.Fatalf("GET unauth: want 303, got %d", w.Code)
	}
}

func TestResumeImport_PasteText(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	calls := stubBuild(t, &jobs.MasterResumeBuildResult{PersonID: 7, Skills: 3, Summary: "ok"}, nil)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")

	w := selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"resume_text": {"Jane Doe — Go engineer, 10y"}})
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, *calls, 1)
	require.Equal(t, fx.op.ID, (*calls)[0].aid.String())
	require.Equal(t, "Jane Doe — Go engineer, 10y", (*calls)[0].text)
	require.Zero(t, (*calls)[0].pid)
	require.Contains(t, w.Body.String(), "Master resume built")
}

func TestResumeImport_BadCSRF(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	calls := stubBuild(t, &jobs.MasterResumeBuildResult{}, nil)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")

	form := url.Values{"resume_text": {"x"}, "_csrf": {"bogus"}}
	req := httptest.NewRequest(http.MethodPost, adminBasePath+"/resume/import", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	fx.handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Empty(t, *calls)
}

func TestResumeImport_DocxUpload(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	calls := stubBuild(t, &jobs.MasterResumeBuildResult{PersonID: 8}, nil)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")

	docx := zipSingle(t, "word/document.xml",
		`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Docx Jane</w:t></w:r></w:p></w:body></w:document>`)
	w := multipartPost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		nil, "resume_file", "cv.docx", docx)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, *calls, 1)
	require.Contains(t, (*calls)[0].text, "Docx Jane")
}

func TestResumeImport_TxtUpload(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	calls := stubBuild(t, &jobs.MasterResumeBuildResult{PersonID: 9}, nil)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")

	w := multipartPost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		nil, "resume_file", "cv.txt", []byte("Plain text resume body"))
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, *calls, 1)
	require.Equal(t, "Plain text resume body", (*calls)[0].text)
}

// Oversize bodies must produce a friendly rendered page, never a bare 400.
func TestResumeImport_OversizeBody(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	calls := stubBuild(t, &jobs.MasterResumeBuildResult{}, nil)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")

	w := multipartPost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		nil, "resume_file", "big.txt", make([]byte, resumeImportMaxBody+1))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "Upload too large")
	require.Empty(t, *calls)
}

func TestResumeImport_BothInputsRefused(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	calls := stubBuild(t, &jobs.MasterResumeBuildResult{}, nil)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")

	w := multipartPost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		map[string]string{"resume_text": "pasted"}, "resume_file", "cv.txt", []byte("file text"))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "not both")
	require.Empty(t, *calls)
}

// TestResumeImport_ConsentRequired is the silent-destruction gate: an existing
// profile + no/mismatched consent must never reach the build seam.
func TestResumeImport_ConsentRequired(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	calls := stubBuild(t, &jobs.MasterResumeBuildResult{PersonID: 99}, nil)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")

	pid := seedPerson(t, uuid.MustParse(fx.op.ID))

	// no consent → confirm page, seam untouched
	w := selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"resume_text": {"Jane Doe resume"}})
	require.Contains(t, w.Body.String(), "already exists")
	require.Empty(t, *calls)

	// mismatched person id → still refused
	w = selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"resume_text": {"Jane Doe resume"}, "replace_person_id": {"999999"}, "confirm_replace": {"1"}})
	require.Contains(t, w.Body.String(), "already exists")
	require.Empty(t, *calls)

	// real consent → seam receives the pid
	w = selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"resume_text": {"Jane Doe resume"}, "replace_person_id": {strconv.Itoa(pid)}, "confirm_replace": {"1"}})
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, *calls, 1)
	require.Equal(t, pid, (*calls)[0].pid)
}

// TestResumeImport_TOCTOU — the seam itself refuses (profile id drifted): the
// page re-renders consent with the FRESH id.
func TestResumeImport_TOCTOU(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	calls := stubBuild(t, nil, errConsentRefused)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")

	pid := seedPerson(t, uuid.MustParse(fx.op.ID))

	w := selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"resume_text": {"Jane"}, "replace_person_id": {strconv.Itoa(pid)}, "confirm_replace": {"1"}})
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, *calls, 1)
	require.Contains(t, w.Body.String(), "Build failed")
	require.Contains(t, w.Body.String(), strconv.Itoa(pid)) // fresh id re-rendered
}

func TestResumeImport_Limiter(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	calls := stubBuild(t, &jobs.MasterResumeBuildResult{PersonID: 1}, nil)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")

	for i := 0; i < resumeImportRateLimit; i++ {
		w := selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
			url.Values{"resume_text": {"x"}})
		require.Equal(t, http.StatusOK, w.Code)
	}
	w := selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"resume_text": {"x"}})
	require.Contains(t, w.Body.String(), "Too many imports")
	require.Len(t, *calls, resumeImportRateLimit)
}

// TestResumeImport_TenantScope — the account identity comes from the session,
// never the form: no field can steer the build at another account.
func TestResumeImport_TenantScope(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	calls := stubBuild(t, &jobs.MasterResumeBuildResult{PersonID: 2}, nil)

	userID := newUserAccount(t, fx.pool, "import-user@t.example", "user-pass-12345")
	cookies := selfServeLogin(t, fx.handler, "import-user@t.example", "user-pass-12345")

	w := selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"resume_text": {"Bob resume"}, "account_id": {fx.op.ID}})
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, *calls, 1)
	require.Equal(t, userID, (*calls)[0].aid)
}

// TestResumeImport_MergeMode — mode=merge routes to the PLAN seam and renders
// the preview (never the destructive build seam, never an immediate write);
// rebuild still routes to the build seam under consent; unknown mode → 400.
func TestResumeImport_MergeMode(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	planCalls := stubPlan(t, &jobs.ResumePlan{PersonID: 7, Baseline: "{}"}, nil)
	buildCalls := stubBuild(t, &jobs.MasterResumeBuildResult{PersonID: 7}, nil)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")

	pid := seedPerson(t, uuid.MustParse(fx.op.ID))

	w := selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"resume_text": {"Jane Doe updated"}, "mode": {"merge"}})
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, *planCalls, 1)
	require.Equal(t, uuid.MustParse(fx.op.ID), (*planCalls)[0].aid)
	require.Empty(t, *buildCalls)

	// mode=rebuild (or absent) still routes to the destructive seam
	w = selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"resume_text": {"Jane Doe full"}, "mode": {"rebuild"},
			"replace_person_id": {strconv.Itoa(pid)}, "confirm_replace": {"1"}})
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, *buildCalls, 1)
	require.Len(t, *planCalls, 1) // unchanged

	// unknown mode → 400, neither seam fires
	w = selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"resume_text": {"Jane"}, "mode": {"bogus"},
			"replace_person_id": {strconv.Itoa(pid)}, "confirm_replace": {"1"}})
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Len(t, *planCalls, 1)
	require.Len(t, *buildCalls, 1)
}

// --- merge preview/apply ---

type planCall struct {
	aid   uuid.UUID
	text  string
	merge bool
}

func stubPlan(t *testing.T, res *jobs.ResumePlan, err error) *[]planCall {
	t.Helper()
	calls := new([]planCall)
	prev := planResumeBuild
	planResumeBuild = func(ctx context.Context, aid uuid.UUID, text string, merge bool) (*jobs.ResumePlan, error) {
		*calls = append(*calls, planCall{aid, text, merge})
		return res, err
	}
	t.Cleanup(func() { planResumeBuild = prev })
	return calls
}

type applyCall struct {
	aid  uuid.UUID
	plan *jobs.ResumePlan
	pid  int
}

func stubApply(t *testing.T, res *jobs.MasterResumeBuildResult, err error) *[]applyCall {
	t.Helper()
	calls := new([]applyCall)
	prev := applyResumePlan
	applyResumePlan = func(ctx context.Context, aid uuid.UUID, plan *jobs.ResumePlan, pid int) (*jobs.MasterResumeBuildResult, error) {
		*calls = append(*calls, applyCall{aid, plan, pid})
		return res, err
	}
	t.Cleanup(func() { applyResumePlan = prev })
	return calls
}

// mode=merge plans and renders the diff — no consent checkbox needed (the
// preview destroys nothing), no write seam fires.
func TestResumeImport_MergePreview(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	planCalls := stubPlan(t, &jobs.ResumePlan{
		PersonID: 1,
		Baseline: `{"person":{"name":"Old Name"},"skills":[{"name":"OldSkill","category":"","level":""}]}`,
	}, nil)
	applyCalls := stubApply(t, nil, nil)
	buildCalls := stubBuild(t, nil, nil)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")

	seedPerson(t, uuid.MustParse(fx.op.ID))

	// No confirm_replace — preview requires no consent.
	w := selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"resume_text": {"Jane updated resume"}, "mode": {"merge"}})
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, *planCalls, 1)
	require.True(t, (*planCalls)[0].merge)
	require.Empty(t, *applyCalls)
	require.Empty(t, *buildCalls)
	body := w.Body.String()
	require.Contains(t, body, "Apply merge")
	require.Contains(t, body, "merge_payload")
	// The stub plan differs from the baseline → the diff surfaces removals.
	require.Contains(t, body, "OldSkill")
}

// mode=apply verifies the signed payload and calls the write seam with the
// plan's own person id as the consent.
func TestResumeImport_MergeApply(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	applyCalls := stubApply(t, &jobs.MasterResumeBuildResult{PersonID: 9}, nil)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")
	aid := uuid.MustParse(fx.op.ID)
	seedPerson(t, aid)

	tok, err := signMergePayload([]byte(fx.csrfKey), &mergeApplyPayload{
		AccountID: aid.String(), HasChanges: true,
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
		Plan:      &jobs.ResumePlan{PersonID: 5, Baseline: "{}"},
	})
	require.NoError(t, err)

	w := selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"mode": {"apply"}, "merge_payload": {tok}})
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, *applyCalls, 1)
	require.Equal(t, aid, (*applyCalls)[0].aid)
	require.Equal(t, 5, (*applyCalls)[0].pid)
}

// Tampered / foreign / expired payloads are all refused without a write.
func TestResumeImport_MergeApplyPayloadGuards(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	applyCalls := stubApply(t, &jobs.MasterResumeBuildResult{}, nil)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")
	aid := uuid.MustParse(fx.op.ID)

	good := func() string {
		tok, err := signMergePayload([]byte(fx.csrfKey), &mergeApplyPayload{
			AccountID: aid.String(), ExpiresAt: time.Now().Add(time.Minute).Unix(),
			HasChanges: true, Plan: &jobs.ResumePlan{PersonID: 5},
		})
		require.NoError(t, err)
		return tok
	}

	// tampered signature — flip the last sig char
	bad := good()
	c := bad[len(bad)-1]
	flip := "A"
	if c == 'A' {
		flip = "B"
	}
	bad = bad[:len(bad)-1] + flip
	w := selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"mode": {"apply"}, "merge_payload": {bad}})
	require.Contains(t, w.Body.String(), "refused")
	require.Empty(t, *applyCalls)

	// foreign account id inside a validly-signed payload
	other, err := signMergePayload([]byte(fx.csrfKey), &mergeApplyPayload{
		AccountID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Minute).Unix(),
		HasChanges: true, Plan: &jobs.ResumePlan{PersonID: 5},
	})
	require.NoError(t, err)
	w = selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"mode": {"apply"}, "merge_payload": {other}})
	require.Contains(t, w.Body.String(), "refused")
	require.Empty(t, *applyCalls)

	// expired
	exp, err := signMergePayload([]byte(fx.csrfKey), &mergeApplyPayload{
		AccountID: aid.String(), ExpiresAt: time.Now().Add(-time.Minute).Unix(),
		HasChanges: true, Plan: &jobs.ResumePlan{PersonID: 5},
	})
	require.NoError(t, err)
	w = selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"mode": {"apply"}, "merge_payload": {exp}})
	require.Contains(t, w.Body.String(), "refused")
	require.Empty(t, *applyCalls)
}

// A payload minted for an empty diff (HasChanges=false) must refuse on apply —
// otherwise a crafted form would run a full wipe+rewrite for nothing.
func TestResumeImport_MergeApplyNoopRefused(t *testing.T) {
	fx := newSelfServeFixture(t)
	wireResumeDB(t)
	applyCalls := stubApply(t, &jobs.MasterResumeBuildResult{}, nil)
	cookies := selfServeLogin(t, fx.handler, "admin-selfserve@t.example", "admin-pass-12345")
	aid := uuid.MustParse(fx.op.ID)

	tok, err := signMergePayload([]byte(fx.csrfKey), &mergeApplyPayload{
		AccountID: aid.String(), ExpiresAt: time.Now().Add(time.Minute).Unix(),
		HasChanges: false, Plan: &jobs.ResumePlan{PersonID: 5},
	})
	require.NoError(t, err)
	w := selfServePost(t, fx.handler, cookies, fx.csrfKey, adminBasePath+"/resume/import",
		url.Values{"mode": {"apply"}, "merge_payload": {tok}})
	require.Contains(t, w.Body.String(), "refused")
	require.Empty(t, *applyCalls)
}
