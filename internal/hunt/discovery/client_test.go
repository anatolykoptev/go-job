package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cannedResponse returns a minimal go-search REST bridge response with the
// given sources in the "structured" field (the primary path).
func cannedResponse(sources []restSource) []byte {
	out := restResponse{}
	out.Structured.Sources = sources
	b, _ := json.Marshal(out)
	return b
}

// TestClient_DiscoverBoardURLs_RequestContract asserts that the client targets
// the REST bridge path (/api/tools/research) with the correct method, Content-Type,
// and body fields — and does NOT set an Accept header that would trigger the
// StreamableHTTPHandler's "must contain both application/json and
// text/event-stream" check (which would 400 on POST /mcp).
//
// RED if: path changed back to /mcp, Content-Type removed, query/depth fields missing.
func TestClient_DiscoverBoardURLs_RequestContract(t *testing.T) {
	var capturedReq *http.Request
	var capturedBody restRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cannedResponse([]restSource{
			{Index: 1, Title: "Acme", URL: "https://boards.greenhouse.io/acme"},
		}))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.DiscoverBoardURLs(context.Background(), "engineer site:boards.greenhouse.io")
	require.NoError(t, err)

	// Path must be the REST bridge, NOT /mcp (which would require Accept negotiation).
	assert.Equal(t, restAPIPath, capturedReq.URL.Path,
		"must target REST bridge path, not StreamableHTTP /mcp")

	// Method.
	assert.Equal(t, http.MethodPost, capturedReq.Method)

	// Content-Type must be JSON (REST bridge requires it).
	assert.Equal(t, "application/json", capturedReq.Header.Get("Content-Type"))

	// Accept must NOT be set to a value that gates on text/event-stream (or be absent).
	// The REST bridge does not check Accept; the StreamableHTTPHandler would 400 on
	// just "application/json" — so we assert Accept is empty OR does not force that path.
	accept := capturedReq.Header.Get("Accept")
	assert.Empty(t, accept,
		"REST bridge does not need Accept; setting it to application/json would 400 on /mcp")

	// Body fields.
	assert.Contains(t, capturedBody.Query, "boards.greenhouse.io")
	assert.Equal(t, "fast", capturedBody.Depth)
}

func TestClient_DiscoverBoardURLs_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, restAPIPath, r.URL.Path)

		var req restRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "fast", req.Depth)
		assert.Contains(t, req.Query, "boards.greenhouse.io")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cannedResponse([]restSource{
			{Index: 1, Title: "Acme Jobs", URL: "https://boards.greenhouse.io/acme", Snippet: "10 jobs"},
			{Index: 2, Title: "Beta Jobs", URL: "https://boards.greenhouse.io/beta", Snippet: "5 jobs"},
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

// TestClient_DiscoverBoardURLs_TextContentFallback verifies that when
// structured.sources is empty, the client re-parses content[0].text as JSON.
// This mirrors what go-search returns when the research pipeline produces no
// structured output but still encodes sources in the text content blob.
func TestClient_DiscoverBoardURLs_TextContentFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// structured.sources empty; sources encoded in content[0].text instead.
		inner := restOutput{Sources: []restSource{
			{Index: 1, URL: "https://jobs.ashbyhq.com/gamma", Title: "Gamma"},
		}}
		innerJSON, _ := json.Marshal(inner)
		outer := restResponse{}
		outer.Content = []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{{Type: "text", Text: string(innerJSON)}}
		b, _ := json.Marshal(outer)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	results, err := c.DiscoverBoardURLs(context.Background(), "engineer site:jobs.ashbyhq.com")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "https://jobs.ashbyhq.com/gamma", results[0].URL)
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
		out := restResponse{IsError: true}
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

func TestClient_DiscoverBoardURLs_EmptySources_ReturnsNilNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cannedResponse(nil))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	results, err := c.DiscoverBoardURLs(context.Background(), "engineer")
	assert.Nil(t, err)
	assert.Nil(t, results)
}
