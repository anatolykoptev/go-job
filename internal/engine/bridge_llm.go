package engine

// bridge_llm.go provides LLM wrapper functions delegating to go-engine/llm.

import (
	"context"
	"strings"

	"github.com/anatolykoptev/go-engine/llm"
)

// defaultCharsPerToken is the average characters per LLM token for budget estimation.
const defaultCharsPerToken = 3.5

// completeLLM is the package-level seam used by CallLLM, overridable in unit tests.
var completeLLM = func(ctx context.Context, prompt string, opts ...llm.ChatOption) (string, error) {
	return llmInst.Complete(ctx, prompt, opts...)
}

// IsModelUnsupportedError reports whether err is a model-specific unavailability
// or unsupported-model error (e.g. HTTP 401 ModelError "Model x-preview-f-free is not supported.").
func IsModelUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "modelerror") ||
		(strings.Contains(msg, "model ") && (strings.Contains(msg, "is not supported") ||
			strings.Contains(msg, "not supported") ||
			strings.Contains(msg, "not available") ||
			strings.Contains(msg, "not found") ||
			strings.Contains(msg, "does not exist")))
}

// CallLLM sends a prompt using the configured temperature and max_tokens.
// If the primary model fails with an unsupported/unavailable model error,
// it falls back across LLM_MODEL_FALLBACK models.
// OBS-6: records call duration in the llm_request_duration_seconds histogram.
func CallLLM(ctx context.Context, prompt string) (string, error) {
	if reg != nil {
		reg.Incr(MetricLLMCalls)
		timer := reg.StartTimer(MetricLLMRequestDuration)
		defer timer.Stop()
	}
	raw, err := completeLLM(ctx, prompt)
	if err != nil && IsModelUnsupportedError(err) {
		for _, fbModel := range llm.ParseModelFallbackChain(cfg.LLMModelFallback) {
			if fbModel == "" || fbModel == cfg.LLMModel {
				continue
			}
			fbRaw, fbErr := completeLLM(ctx, prompt, llm.WithChatModel(fbModel))
			if fbErr == nil {
				return fbRaw, nil
			}
			err = fbErr
			if !IsModelUnsupportedError(fbErr) {
				break
			}
		}
	}
	if err != nil {
		if reg != nil {
			reg.Incr(MetricLLMErrors)
		}
		return "", err
	}
	return raw, nil
}

// BuildSourcesText formats search results and fetched content for LLM context.
func BuildSourcesText(results []SearxngResult, contents map[string]string, contentLimit int) string {
	return llm.BuildSourcesText(results, contents, contentLimit, defaultCharsPerToken)
}

// summarizeWithLLM builds context from search results and calls the LLM API.
func summarizeWithLLM(ctx context.Context, query string, results []SearxngResult, contents map[string]string) (*LLMStructuredOutput, error) {
	return llmInst.Summarize(ctx, query, cfg.MaxContentChars, defaultCharsPerToken, results, contents)
}

// SummarizeWithInstruction summarizes search results using a custom instruction.
func SummarizeWithInstruction(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*LLMStructuredOutput, error) {
	return llmInst.SummarizeWithInstruction(ctx, query, instruction, contentLimit, defaultCharsPerToken, results, contents)
}

// SummarizeDeep summarizes using exhaustive fact extraction.
func SummarizeDeep(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*LLMStructuredOutput, error) {
	return llmInst.SummarizeDeep(ctx, query, instruction, contentLimit, defaultCharsPerToken, results, contents)
}

// SummarizeToJSON builds an LLM prompt from search results and parses as JSON.
func SummarizeToJSON[T any](ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*T, string, error) {
	return llm.SummarizeToJSON[T](ctx, llmInst, query, instruction, contentLimit, defaultCharsPerToken, results, contents)
}
