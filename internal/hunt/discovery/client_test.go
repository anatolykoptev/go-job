package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cannedResponse returns a minimal go-search MCP response with the given sources.
func cannedResponse(sources []goSearchSource) []byte {
	out := mcpCallResponse{}
	out.Result.StructuredContent.Sources = sources
	b, _ := json.Marshal(out)
	return b
}

func TestClient_DiscoverBoardURLs_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/mcp", r.URL.Path)

		var req mcpCallRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "research", req.Params.Name)
		assert.Equal(t, "fast", req.Params.Arguments.Depth)
		assert.Contains(t, req.Params.Arguments.Query, "boards.greenhouse.io")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(cannedResponse([]goSearchSource{
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

func TestClient_DiscoverBoardURLs_SSEFramed(t *testing.T) {
	// go-search sometimes serves SSE; verify the client strips the prefix.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		b := cannedResponse([]goSearchSource{
			{Index: 1, URL: "https://jobs.lever.co/acme", Title: "Acme Lever"},
		})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(b))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	results, err := c.DiscoverBoardURLs(context.Background(), "engineer site:jobs.lever.co")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "https://jobs.lever.co/acme", results[0].URL)
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

func TestClient_DiscoverBoardURLs_Timeout_ReturnsError(t *testing.T) {
	// Use a done channel so the server handler can exit cleanly when the test
	// is done — blocking on r.Context().Done() in httptest handlers doesn't
	// cancel when the HTTP client times out (server uses its own context).
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Block until the test signals done (post-assert cleanup).
		<-done
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	// Signal the blocking handler before Close so srv.Close() can drain.
	defer func() {
		close(done)
		srv.Close()
	}()

	c := NewClient(srv.URL)
	c.timeout = 50 * time.Millisecond // shrink for test speed

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

func TestClient_DiscoverBoardURLs_TextContentFallback(t *testing.T) {
	// structuredContent empty → falls back to re-parsing text content blob.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		inner := goSearchOutput{Sources: []goSearchSource{
			{Index: 1, URL: "https://jobs.ashbyhq.com/gamma", Title: "Gamma"},
		}}
		innerJSON, _ := json.Marshal(inner)
		outer := mcpCallResponse{}
		outer.Result.Content = []struct {
			Text string `json:"text"`
		}{{Text: string(innerJSON)}}
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
