package jobs

import (
	"context"
	"encoding/json"
	"math"
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
			http.Error(w, err.Error(), 400)
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
		json.NewEncoder(w).Encode(resp)
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

func TestCosineSimilarity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"identical", []float32{1, 0}, []float32{1, 0}, 1.0},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0.0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1.0},
		{"similar", []float32{0.8, 0.6}, []float32{0.6, 0.8}, 0.96},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CosineSimilarity(tt.a, tt.b)
			if math.Abs(float64(got-tt.want)) > 0.01 {
				t.Errorf("CosineSimilarity() = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestCosineSimilarity_DifferentLengths(t *testing.T) {
	t.Parallel()
	got := CosineSimilarity([]float32{1, 0}, []float32{1})
	if got != 0 {
		t.Errorf("expected 0 for mismatched lengths, got %f", got)
	}
}
