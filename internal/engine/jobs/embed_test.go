package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbedTexts(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := embedResponse{Object: "list", Model: "test"}
		for i := range req.Input {
			resp.Data = append(resp.Data, embedData{
				Object:    "embedding",
				Embedding: []float32{0.5, 0.5},
				Index:     i,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewEmbedClient(srv.URL)
	vecs, err := client.EmbedTexts(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("EmbedTexts failed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
	if len(vecs[0]) != 2 {
		t.Errorf("expected 2-dim vector, got %d", len(vecs[0]))
	}
}

func TestEmbedTexts_EmptyInput(t *testing.T) {
	t.Parallel()

	client := NewEmbedClient("http://localhost:1")
	vecs, err := client.EmbedTexts(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vecs) != 0 {
		t.Errorf("expected 0 vectors, got %d", len(vecs))
	}
}
