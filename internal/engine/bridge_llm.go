package engine

// bridge_llm.go provides LLM wrapper functions delegating to go-engine/llm.

import (
	"context"

	"github.com/anatolykoptev/go-engine/llm"
)

// CallLLM sends a prompt using the configured temperature and max_tokens.
func CallLLM(ctx context.Context, prompt string) (string, error) {
	reg.Incr(MetricLLMCalls)
	raw, err := llmInst.Complete(ctx, prompt)
	if err != nil {
		reg.Incr(MetricLLMErrors)
		return "", err
	}
	return raw, nil
}

// RewriteQuery uses the LLM to convert a conversational query into search form.
func RewriteQuery(ctx context.Context, query string) string {
	return llmInst.RewriteQuery(ctx, query)
}

// ExpandSearchQueries generates semantically diverse query variants.
func ExpandSearchQueries(ctx context.Context, query string, n int) ([]string, error) {
	return llmInst.ExpandSearchQueries(ctx, query, n)
}

// ExpandWebSearchQueries generates diverse web search query variants.
func ExpandWebSearchQueries(ctx context.Context, query string, n int) ([]string, error) {
	return llmInst.ExpandWebSearchQueries(ctx, query, n)
}

// BuildSourcesText formats search results and fetched content for LLM context.
func BuildSourcesText(results []SearxngResult, contents map[string]string, contentLimit int) string {
	return llm.BuildSourcesText(results, contents, contentLimit)
}

// summarizeWithLLM builds context from search results and calls the LLM API.
func summarizeWithLLM(ctx context.Context, query string, results []SearxngResult, contents map[string]string) (*LLMStructuredOutput, error) {
	return llmInst.Summarize(ctx, query, cfg.MaxContentChars, results, contents)
}

// SummarizeWithInstruction summarizes search results using a custom instruction.
func SummarizeWithInstruction(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*LLMStructuredOutput, error) {
	return llmInst.SummarizeWithInstruction(ctx, query, instruction, contentLimit, results, contents)
}

// SummarizeDeep summarizes using exhaustive fact extraction.
func SummarizeDeep(ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*LLMStructuredOutput, error) {
	return llmInst.SummarizeDeep(ctx, query, instruction, contentLimit, results, contents)
}

// SummarizeToJSON builds an LLM prompt from search results and parses as JSON.
func SummarizeToJSON[T any](ctx context.Context, query, instruction string, contentLimit int, results []SearxngResult, contents map[string]string) (*T, string, error) {
	return llm.SummarizeToJSON[T](ctx, llmInst, query, instruction, contentLimit, results, contents)
}
