package adminui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/anatolykoptev/go-panel/csrf"
	"github.com/anatolykoptev/go_job/internal/hunt"
	"github.com/anatolykoptev/go_job/internal/hunt/score"
)

// ---------------------------------------------------------------------------
// rescoreJob unit tests — no pool, no HTTP required
// ---------------------------------------------------------------------------

// spyScoreSetter records every SetJobScore call for assertion.
type spyScoreSetter struct {
	calls  int
	lastID int64
	lastSR hunt.ScoreResult
	retErr error
}

func (s *spyScoreSetter) SetJobScore(ctx context.Context, id int64, sr hunt.ScoreResult) error {
	s.calls++
	s.lastID = id
	s.lastSR = sr
	return s.retErr
}

// TestRescoreJob_TransientLLMFail_NoSetJobScore verifies that when ScoreForce
// returns a transient fail-open result (LLM attempted but failed), rescoreJob
// does NOT call SetJobScore — the prior analysis on the row is preserved.
//
// RED-on-revert: remove the guard in rescoreJob → spy.calls == 1 (clobbers row).
func TestRescoreJob_TransientLLMFail_NoSetJobScore(t *testing.T) {
	prof := &score.ScoringProfile{
		Seniority:  "Staff",
		CoreSkills: []string{"Go"},
	}
	job := hunt.Job{
		ID:          42,
		Title:       "Go Engineer",
		Description: "Go distributed systems",
	}

	// Ensure fail-open path is active so LLM error returns FitBandUnscored (not an error).
	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")

	// Simulate cliproxyapi 503 / transient proxy error.
	deps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("proxy 503: upstream unavailable")
		},
	}

	spy := &spyScoreSetter{}
	result, persisted, err := rescoreJob(context.Background(), 42, job, prof, deps, spy)
	if err != nil {
		t.Fatalf("want nil err on transient LLM fail, got %v", err)
	}
	if persisted {
		t.Errorf("want persisted=false (prior score preserved), got true")
	}
	// RED-on-revert: remove the guard in rescoreJob → calls==1 and this fails.
	if spy.calls != 0 {
		t.Errorf("SetJobScore must NOT be called on transient LLM fail; calls=%d", spy.calls)
	}
	if result.FitBand != hunt.FitBandUnscored {
		t.Errorf("want FitBand=%q (guard discriminator), got %q", hunt.FitBandUnscored, result.FitBand)
	}
	if result.LLMResult != "llm_error" {
		t.Errorf("want LLMResult=%q (guard discriminator), got %q", "llm_error", result.LLMResult)
	}
}

// TestRescoreJob_ParseFail_NoSetJobScore verifies the guard also fires when the
// LLM returns non-JSON (parse_fail class) — same transient-fail path.
func TestRescoreJob_ParseFail_NoSetJobScore(t *testing.T) {
	prof := &score.ScoringProfile{
		Seniority:  "Staff",
		CoreSkills: []string{"Go"},
	}
	job := hunt.Job{ID: 7, Title: "Staff Eng", Description: "Go systems"}

	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")

	deps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			return "Sorry I cannot score this role.", nil // non-JSON → parse_fail
		},
	}

	spy := &spyScoreSetter{}
	_, persisted, err := rescoreJob(context.Background(), 7, job, prof, deps, spy)
	if err != nil {
		t.Fatalf("want nil err on parse_fail, got %v", err)
	}
	if persisted {
		t.Errorf("want persisted=false (prior score preserved), got true")
	}
	if spy.calls != 0 {
		t.Errorf("SetJobScore must NOT be called on parse_fail; calls=%d", spy.calls)
	}
}

// TestRescoreJob_Success_CallsSetJobScore verifies the happy path: a valid LLM
// JSON response persists exactly once to the store.
func TestRescoreJob_Success_CallsSetJobScore(t *testing.T) {
	prof := &score.ScoringProfile{
		Seniority:  "Staff",
		CoreSkills: []string{"Go"},
	}
	job := hunt.Job{ID: 99, Title: "Staff Eng", Description: "Go Rust distributed systems"}

	t.Setenv("HUNT_SCORE_FAIL_OPEN", "true")

	deps := score.ScorerDeps{
		Jaccard: func(kw, text string) float64 { return 50 },
		LLM: func(_ context.Context, _ string) (string, error) {
			return `{"fit_score":82,"fit_reasons":["Go expert"],"fit_gaps":[],"success_band":"STRONG","success_reasoning":"strong match","over_under":"well_matched"}`, nil
		},
	}

	spy := &spyScoreSetter{}
	_, persisted, err := rescoreJob(context.Background(), 99, job, prof, deps, spy)
	if err != nil {
		t.Fatalf("want nil err on success, got %v", err)
	}
	if !persisted {
		t.Errorf("want persisted=true on success")
	}
	if spy.calls != 1 {
		t.Errorf("SetJobScore must be called exactly once; calls=%d", spy.calls)
	}
	if spy.lastID != 99 {
		t.Errorf("want lastID=99, got %d", spy.lastID)
	}
	if spy.lastSR.FitBand != "strong" {
		t.Errorf("want FitBand=%q, got %q", "strong", spy.lastSR.FitBand)
	}
}

// ---------------------------------------------------------------------------
// rescoreHandler HTTP-layer tests (CSRF + bad-ID)
// ---------------------------------------------------------------------------

// TestRescoreHandler_CSRFReject verifies that a missing CSRF token returns 403
// before any DB call is made.
// RED-on-revert: remove the csrf.Verify call in rescoreHandler → 400 or 500.
func TestRescoreHandler_CSRFReject(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") // 32 bytes
	a := buildTestAuth(key)
	// nil pool + nil store — expect 403 before any DB access.
	handler := rescoreHandler(nil, nil, a, key)

	form := url.Values{}
	// _csrf intentionally omitted → should get 403.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/jobs/1/rescore",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403 for missing CSRF, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestRescoreHandler_CSRFBadMAC verifies that a token with an invalid MAC
// returns 403. (Token format is "<expiry>|<mac>"; this tests MAC rejection,
// not clock-based expiry. Renamed from TestRescoreHandler_CSRFExpired because
// "1|invalidmac" fails on MAC mismatch, not expiry.)
func TestRescoreHandler_CSRFBadMAC(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := rescoreHandler(nil, nil, a, key)

	// Far-future expiry to avoid clock sensitivity; wrong MAC is what triggers 403.
	badMACToken := "9999999999|deadbeefdeadbeef"

	form := url.Values{}
	form.Set(csrf.FormField, badMACToken)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/jobs/1/rescore",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("want 403 for bad MAC, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestRescoreHandler_BadID verifies that a non-numeric id returns 400
// before CSRF validation or any DB call.
func TestRescoreHandler_BadID(t *testing.T) {
	key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a := buildTestAuth(key)
	handler := rescoreHandler(nil, nil, a, key)

	form := url.Values{}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/admin/jobs/abc/rescore",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "abc")

	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for non-numeric id, got %d", rr.Code)
	}
}
