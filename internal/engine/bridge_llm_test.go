package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	engllm "github.com/anatolykoptev/go-engine/llm"
	kitllm "github.com/anatolykoptev/go-kit/llm"
)

func TestIsModelUnsupportedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			"http_401_model_error",
			errors.New(`llm: HTTP 401 (ModelError): {"error":{"message":"Model x-preview-f-free is not supported.","type":"ModelError","code":401}}`),
			true,
		},
		{
			"model_not_supported_plain",
			errors.New("llm: HTTP 400: model foo-bar is not supported"),
			true,
		},
		{
			"model_not_available",
			errors.New("llm: HTTP 404: model xyz not available"),
			true,
		},
		{
			"model_not_found",
			errors.New("llm: HTTP 404: model xyz not found"),
			true,
		},
		{
			"model_does_not_exist",
			errors.New("llm: HTTP 400: model xyz does not exist"),
			true,
		},
		{
			"auth_error",
			errors.New("llm: HTTP 401: invalid api key"),
			false,
		},
		{
			"network_timeout",
			errors.New("context deadline exceeded"),
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsModelUnsupportedError(tt.err); got != tt.want {
				t.Errorf("IsModelUnsupportedError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestCallLLM_FallbackOnModelError(t *testing.T) {
	InitTestRegistry()
	origComplete := completeLLM
	origCfg := cfg
	t.Cleanup(func() {
		completeLLM = origComplete
		cfg = origCfg
	})

	cfg.LLMModel = "x-preview-f-free"
	cfg.LLMModelFallback = "x-preview-f-free,cerebras-qwen-3-235b,groq-llama-3.3-70b"

	var calls int32
	completeLLM = func(_ context.Context, _ string, _ ...engllm.ChatOption) (string, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return "", errors.New(`llm: HTTP 401 (ModelError): {"error":{"message":"Model x-preview-f-free is not supported.","type":"ModelError","code":401}}`)
		}
		return `{"role":"Go Developer","median":150000}`, nil
	}

	got, err := CallLLM(context.Background(), "salary prompt")
	if err != nil {
		t.Fatalf("CallLLM expected fallback success, got error: %v", err)
	}
	if got != `{"role":"Go Developer","median":150000}` {
		t.Fatalf("CallLLM got %q, want JSON response", got)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected 2 LLM calls (1 primary + 1 fallback), got %d", calls)
	}
}

func TestCallLLM_StopsFallbackOnNonModelError(t *testing.T) {
	InitTestRegistry()
	origComplete := completeLLM
	origCfg := cfg
	t.Cleanup(func() {
		completeLLM = origComplete
		cfg = origCfg
	})

	cfg.LLMModel = "x-preview-f-free"
	cfg.LLMModelFallback = "model-b,model-c"

	var calls int32
	completeLLM = func(_ context.Context, _ string, _ ...engllm.ChatOption) (string, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return "", errors.New(`llm: HTTP 401 (ModelError): {"error":{"message":"Model x-preview-f-free is not supported.","type":"ModelError","code":401}}`)
		}
		return "", errors.New("llm: HTTP 401: invalid bearer token")
	}

	_, err := CallLLM(context.Background(), "salary prompt")
	if err == nil {
		t.Fatal("expected error when fallback fails with auth error")
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected fallback loop to stop after non-model error (2 calls), got %d", calls)
	}
}

func TestKitLLMChain_FailoverOnHTTP401ModelError(t *testing.T) {
	var attempts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"x-preview-f-free"},{"id":"healthy-model"}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if len(attempts) == 0 {
			attempts = append(attempts, "x-preview-f-free")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Model x-preview-f-free is not supported.","type":"ModelError","code":401}}`))
			return
		}
		attempts = append(attempts, "healthy-model")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"median\":140000}"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	eps := kitllm.BuildModelChainEndpoints(srv.URL+"/v1", "test-key", "x-preview-f-free", []string{"healthy-model"})
	client := kitllm.NewClient(srv.URL+"/v1", "test-key", "x-preview-f-free", kitllm.WithEndpoints(eps))

	out, err := client.Complete(context.Background(), "", "test salary")
	if err != nil {
		t.Fatalf("expected kitllm chain to failover past HTTP 401 ModelError, got error: %v", err)
	}
	if out != `{"median":140000}` {
		t.Fatalf("unexpected completion output: %q", out)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected 2 endpoint attempts, got %d (%v)", len(attempts), attempts)
	}
}
