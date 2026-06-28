package discovery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawCannedResponse builds a minimal raw_web_search REST bridge response with
// the given results encoded as a JSON string in content[0].text.
func rawCannedResponse(results []rawSearchResult) []byte {
	inner := rawSearchOutput{Results: results, Total: len(results)}
	innerJSON, _ := json.Marshal(inner)
	env := rawSearchEnvelope{}
	env.Content = []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{{Type: "text", Text: string(innerJSON)}}
	b, _ := json.Marshal(env)
	return b
}

// TestClient_DiscoverBoardURLs_RequestContract asserts that the client targets
// the raw_web_search REST bridge path (/api/tools/raw_web_search) with the
// correct method, Content-Type, and body — and does NOT include the deprecated
// "depth" or "board_discovery" fields that belonged to the old /research path.
//
// RED if:
//   - path is changed back to /api/tools/research
//   - "depth" or "board_discovery" fields appear in the request body
//   - Content-Type is removed
//   - Accept header is set to a value that would 400 on /mcp
func TestClient_DiscoverBoardURLs_RequestContract(t *testing.T) {
	var capturedReq *http.Request
	var capturedBody rawSearchRequest
	var rawBodyBytes []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		rawBodyBytes, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(rawBodyBytes, &capturedBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(rawCannedResponse([]rawSearchResult{
			{URL: "https://boards.greenhouse.io/acme", Title: "Acme"},
		}))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.DiscoverBoardURLs(context.Background(), "engineer site:boards.greenhouse.io")
	require.NoError(t, err)

	// Path must be the raw_web_search REST bridge, NOT /api/tools/research.
	assert.Equal(t, restAPIPath, capturedReq.URL.Path,
		"must target raw_web_search path, not the research pipeline")
	assert.Equal(t, "/api/tools/raw_web_search", capturedReq.URL.Path)

	// Method.
	assert.Equal(t, http.MethodPost, capturedReq.Method)

	// Content-Type must be JSON (REST bridge requires it).
	assert.Equal(t, "application/json", capturedReq.Header.Get("Content-Type"))

	// Accept must NOT be set to a value that gates on text/event-stream (or absent).
	accept := capturedReq.Header.Get("Accept")
	assert.Empty(t, accept,
		"REST bridge does not need Accept; setting it to application/json would 400 on /mcp")

	// Query field must be present.
	assert.Contains(t, capturedBody.Query, "boards.greenhouse.io")

	// raw_web_search does not accept "depth" or "board_discovery" — these are
	// research-pipeline-only fields.  If either appears, the caller is sending
	// the old research-tool body to the wrong endpoint.
	var rawBodyMap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rawBodyBytes, &rawBodyMap))
	assert.NotContains(t, rawBodyMap, "depth",
		"raw_web_search does not accept depth; must not be sent (belongs to /research)")
	assert.NotContains(t, rawBodyMap, "board_discovery",
		"raw_web_search does not accept board_discovery; must not be sent (belongs to /research)")
}

func TestClient_DiscoverBoardURLs_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, restAPIPath, r.URL.Path)

		var req rawSearchRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Contains(t, req.Query, "boards.greenhouse.io")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(rawCannedResponse([]rawSearchResult{
			{URL: "https://boards.greenhouse.io/acme", Title: "Acme Jobs", Description: "10 jobs"},
			{URL: "https://boards.greenhouse.io/beta", Title: "Beta Jobs", Description: "5 jobs"},
		}))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	results, err := c.DiscoverBoardURLs(context.Background(), "engineer site:boards.greenhouse.io")
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "https://boards.greenhouse.io/acme", results[0].URL)
	assert.Equal(t, "Acme Jobs", results[0].Title)
}

// TestClient_DiscoverBoardURLs_DDGUnwrapAndFilter verifies the three URL
// transformations applied to raw_web_search results:
//
//  1. DDG redirect (duckduckgo.com/l/?uddg=<url>) is unwrapped to the real URL
//     and passes if the real URL is an ATS board host.
//  2. DDG ad/tracking URL (duckduckgo.com/y.js?...) carries no redirect param
//     and is dropped.
//  3. Non-board clean URL (e.g. a general web result) is dropped by host filter.
//  4. Clean ATS board URL passes through unchanged.
//
// RED if:
//   - unwrapDDG stops decoding the "uddg" param → DDG-wrapped board URL is lost
//   - isATSBoardHost allows non-board hosts → noise appears in results
//   - DDG y.js ad URL survives → ad noise appears in results
func TestClient_DiscoverBoardURLs_DDGUnwrapAndFilter(t *testing.T) {
	// Build a DDG-wrapped redirect to a real Lever board URL.
	realLeverURL := "https://jobs.lever.co/widgetco"
	ddgWrapped := "https://duckduckgo.com/l/?uddg=" + realLeverURL + "&rut=abc"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(rawCannedResponse([]rawSearchResult{
			// 1. DDG-wrapped board URL → should unwrap and pass.
			{URL: ddgWrapped, Title: "Widgetco Jobs"},
			// 2. DDG ad/tracking URL (no uddg param) → should be dropped.
			{URL: "https://duckduckgo.com/y.js?ad=true&u3=https://ad.example.com", Title: "Ad"},
			// 3. Clean non-board URL → dropped by host filter.
			{URL: "https://www.example.com/jobs", Title: "Example Jobs"},
			// 4. Clean Ashby board URL → passes straight through.
			{URL: "https://jobs.ashbyhq.com/gamma", Title: "Gamma Jobs"},
		}))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	results, err := c.DiscoverBoardURLs(context.Background(), "engineer site:jobs.lever.co")
	require.NoError(t, err)
	require.Len(t, results, 2, "expected exactly 2 board URLs (DDG-wrapped lever + clean ashby)")

	// DDG-wrapped Lever URL must be unwrapped.
	assert.Equal(t, realLeverURL, results[0].URL)
	assert.Equal(t, "Widgetco Jobs", results[0].Title)

	// Clean Ashby URL must pass unchanged.
	assert.Equal(t, "https://jobs.ashbyhq.com/gamma", results[1].URL)
}

func TestClient_DiscoverBoardURLs_5xx_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	results, err := c.DiscoverBoardURLs(context.Background(), "engineer site:boards.greenhouse.io")
	assert.Nil(t, results)
	assert.Error(t, err)
}

func TestClient_DiscoverBoardURLs_IsError_ReturnsError(t *testing.T) {
	// go-search REST bridge can return is_error=true with HTTP 200.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		out := rawSearchEnvelope{IsError: true}
		b, _ := json.Marshal(out)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	results, err := c.DiscoverBoardURLs(context.Background(), "engineer")
	assert.Nil(t, results)
	assert.Error(t, err)
}

func TestClient_DiscoverBoardURLs_Timeout_ReturnsError(t *testing.T) {
	// Use a done channel so the server handler can exit cleanly — blocking on
	// r.Context().Done() in httptest handlers doesn't cancel when the HTTP
	// client times out (server uses its own context).
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-done
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer func() {
		close(done)
		srv.Close()
	}()

	c := NewClient(srv.URL)
	c.timeout = 50 * time.Millisecond

	results, err := c.DiscoverBoardURLs(context.Background(), "engineer")
	assert.Nil(t, results)
	assert.Error(t, err)
}

func TestClient_DiscoverBoardURLs_EmptyResults_ReturnsNilNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(rawCannedResponse(nil))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	results, err := c.DiscoverBoardURLs(context.Background(), "engineer")
	assert.Nil(t, err)
	assert.Nil(t, results)
}

// TestClient_DiscoverBoardURLs_AllURLsEmpty_ReturnsError verifies that when
// raw_web_search returns N results but every result has an empty url field, the
// client returns an error rather than nil,nil.
//
// Rationale: nil,nil is interpreted by discoverJobURLs as an AUTHORITATIVE
// "nothing found" answer that short-circuits local fallback (P4 trusted-empty
// semantics).  A response with results-but-no-URLs is a malformed payload
// (schema drift / partial parse), NOT a genuine empty: treating it as trusted
// empty would silently swallow results the local path might have found.
// Returning an error causes discoverJobURLs to fall through to local scrapers.
//
// RED if the nonEmptyURLCount == 0 guard in client.go is removed — the call
// would return nil,nil and this test fails.
func TestClient_DiscoverBoardURLs_AllURLsEmpty_ReturnsError(t *testing.T) {
	// Three results, all with empty URL — simulates schema drift where the url
	// field was renamed or absent in the go-search response.
	malformed := []rawSearchResult{
		{Title: "Acme Jobs", URL: ""},
		{Title: "Beta Jobs", URL: ""},
		{Title: "Gamma Jobs", URL: ""},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(rawCannedResponse(malformed))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	results, err := c.DiscoverBoardURLs(context.Background(), "engineer")
	assert.Nil(t, results, "malformed all-URL-empty response must not return results")
	assert.Error(t, err, "malformed all-URL-empty response must return error (triggers local fallback)")
	assert.Contains(t, err.Error(), "malformed", "error message should identify it as malformed")
}

// TestClient_DiscoverBoardURLs_NonBoardURLsFiltered verifies that when
// raw_web_search returns results with non-empty URLs that are all non-ATS-board
// hosts (e.g. general web results), the client returns nil, nil rather than an
// error.  This is a legitimate "no ATS boards found" outcome, not schema drift.
//
// RED if non-board filtering causes an error instead of nil,nil — a spurious
// error would trigger local-fallback on a definitively-empty search result.
func TestClient_DiscoverBoardURLs_NonBoardURLsFiltered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(rawCannedResponse([]rawSearchResult{
			{URL: "https://www.linkedin.com/jobs/search?q=golang", Title: "LinkedIn"},
			{URL: "https://indeed.com/jobs?q=golang", Title: "Indeed"},
		}))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	results, err := c.DiscoverBoardURLs(context.Background(), "engineer")
	assert.Nil(t, err, "all-non-board results should return nil error (legitimate empty, not schema drift)")
	assert.Nil(t, results)
}
