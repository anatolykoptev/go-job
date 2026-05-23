package jobs

// Tests for GithubFetcherAdapter — specifically the state_reason → Merged mapping.
// MAJOR #3: adapter hardcoded Merged:false; closed issues with state_reason="completed"
// were never promoted to StatusMerged, breaking the "bounty claimed" signal.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupGithubTestServer creates an httptest.Server that returns the given issue JSON.
// Sets engine.Cfg.GithubToken and engine.Cfg.HTTPClient to point at the test server.
// Returns cleanup func that restores original engine config values.
func setupGithubTestServer(t *testing.T, issueResp any) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(issueResp); err != nil {
			t.Logf("test server encode error: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, srv.URL
}

// TestGithubFetcherAdapter_CompletedStateReason_SetsMergedTrue verifies that when
// GitHub returns state=closed and state_reason=completed, the adapter emits Merged=true.
// MAJOR #3: state_reason="completed" = bounty was paid out (PR merged by maintainer).
func TestGithubFetcherAdapter_CompletedStateReason_SetsMergedTrue(t *testing.T) {
	issueResp := map[string]any{
		"title":        "Fix the memory leak",
		"state":        "closed",
		"state_reason": "completed",
		"labels":       []any{},
	}
	srv, _ := setupGithubTestServer(t, issueResp)

	// Patch engine config to use test server and token.
	orig := *engine.Cfg
	engine.Cfg.GithubToken = "test-token"
	// Route all GitHub API calls to the test server via host-rewrite transport.
	engine.Cfg.HTTPClient = &http.Client{
		Transport: &hostRewriteTransport{
			target:    srv.URL,
			transport: http.DefaultTransport,
		},
	}
	engine.Cfg.FetchTimeout = 5 * time.Second
	t.Cleanup(func() { *engine.Cfg = orig })

	// Use a GitHub issue URL — owner/repo/issues/N form.
	testURL := "https://github.com/org/repo/issues/42"
	adapter := NewGithubFetcherAdapter()
	info, err := adapter.FetchIssueInfoBatch(context.Background(), []string{testURL})
	require.NoError(t, err)

	result, ok := info[testURL]
	require.True(t, ok, "result must contain the test URL")
	assert.True(t, result.Merged,
		"state_reason=completed must set Merged=true — bounty was paid out (PR merged)")
}

// TestGithubFetcherAdapter_NotPlannedStateReason_SetsMergedFalse verifies that
// state_reason=not_planned → Merged=false (bounty closed without payout).
// MAJOR #3: not_planned = declined/won't-fix, NOT a successful claim.
func TestGithubFetcherAdapter_NotPlannedStateReason_SetsMergedFalse(t *testing.T) {
	issueResp := map[string]any{
		"title":        "Fix the memory leak",
		"state":        "closed",
		"state_reason": "not_planned",
		"labels":       []any{},
	}
	srv, _ := setupGithubTestServer(t, issueResp)

	orig := *engine.Cfg
	engine.Cfg.GithubToken = "test-token"
	engine.Cfg.HTTPClient = &http.Client{
		Transport: &hostRewriteTransport{
			target:    srv.URL,
			transport: http.DefaultTransport,
		},
	}
	engine.Cfg.FetchTimeout = 5 * time.Second
	t.Cleanup(func() { *engine.Cfg = orig })

	testURL := "https://github.com/org/repo/issues/43"
	adapter := NewGithubFetcherAdapter()
	info, err := adapter.FetchIssueInfoBatch(context.Background(), []string{testURL})
	require.NoError(t, err)

	result, ok := info[testURL]
	require.True(t, ok)
	assert.False(t, result.Merged,
		"state_reason=not_planned must NOT set Merged — bounty was declined, not claimed")
}

// TestGithubFetcherAdapter_NoToken_ReturnsError verifies MAJOR #1 fix:
// when GITHUB_TOKEN is unset, adapter returns error (prevents tight loop).
func TestGithubFetcherAdapter_NoToken_ReturnsError(t *testing.T) {
	orig := *engine.Cfg
	engine.Cfg.GithubToken = ""
	t.Cleanup(func() { *engine.Cfg = orig })

	adapter := NewGithubFetcherAdapter()
	_, err := adapter.FetchIssueInfoBatch(context.Background(), []string{"https://github.com/org/repo/issues/1"})
	assert.Error(t, err, "empty GITHUB_TOKEN must return error to prevent tight-loop enrich")
}

// hostRewriteTransport rewrites the Host of every request to the given target URL.
// Used in tests to redirect GitHub API calls to a local httptest.Server.
type hostRewriteTransport struct {
	target    string // e.g. "http://127.0.0.1:PORT"
	transport http.RoundTripper
}

func (h *hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone request to avoid mutating the original.
	r := req.Clone(req.Context())
	// Parse the target to get scheme + host.
	targetReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, h.target, nil)
	if err != nil {
		return nil, err
	}
	r.URL.Scheme = targetReq.URL.Scheme
	r.URL.Host = targetReq.URL.Host
	return h.transport.RoundTrip(r)
}
