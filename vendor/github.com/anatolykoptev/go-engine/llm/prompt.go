package llm

// Core prompt templates. Consumers may override these variables
// for customized behavior.

// RewriteQueryPrompt converts a conversational query to search-optimized form.
// Args: original query.
var RewriteQueryPrompt = `Rewrite the following query into a concise, search-engine-optimized form.
Output ONLY the rewritten query — no explanation, no punctuation at the end, no quotes.
Keep it under 10 words. Use English keywords even if the input is in another language.

Query: %s`

// PromptBase — JSON output: clean prose summary + structured facts with source indices.
// Args: date, instruction, query, sources.
var PromptBase = `You are a research assistant. Answer the query using ONLY the search results below.

Current date: %s

Respond with valid JSON only (no markdown, no ` + "`" + `json` + "`" + ` block):
{
  "answer": "2-3 sentence plain-text summary. No markdown. No citation markers.",
  "facts": [
    {"point": "Specific fact as a complete sentence.", "sources": [1, 2]},
    {"point": "Another specific fact with a number or detail.", "sources": [3]}
  ]
}

Rules:
- answer: plain text, 2-3 sentences, NO markdown (no **, ##, -, *), NO [N] citation markers
- facts: 4-8 key points, each a complete informative sentence, with 1-based source indices
- facts should cover the most important, specific information (numbers, names, versions, commands)
- Answer in the SAME LANGUAGE as the query
- Do NOT invent information not present in sources
- If sources conflict, include both versions as separate fact items

%s

Query: %s

Sources:
%s`

// PromptDeep — exhaustive facts extraction for maximum detail.
// Args: date, instruction section, query, sources.
var PromptDeep = `You are a research assistant. Extract all key information from the sources below.

Current date: %s

Respond with valid JSON only (no markdown, no ` + "`" + `json` + "`" + ` block):
{
  "answer": "3-5 sentence plain-text summary covering the main points. No markdown. No citation markers.",
  "facts": [
    {"point": "Specific fact with detail, number, or command.", "sources": [1, 2]},
    {"point": "Another distinct fact.", "sources": [3]}
  ]
}

Requirements:
- answer: plain text, 3-5 sentences, NO markdown, NO [N] citation markers
- facts: 8-15 key points covering all important details from sources
- Each fact: one complete sentence with specific information (versions, numbers, names, code snippets)
- Include code examples as fact points: {"point": "Use 'go build -a' to force rebuild.", "sources": [2]}
- sources array: 1-based indices into the provided Sources list
- Answer in the SAME LANGUAGE as the query
- Do NOT invent information — only use what is in the sources
- Cover different aspects: how it works, why it matters, how to use it, common pitfalls

%s

Query: %s

Sources:
%s`

// ExpandWebQueryPrompt generates web search query variants.
// Args: n, query, n.
var ExpandWebQueryPrompt = `Generate %d different search query variants to find web pages about: "%s"

Rules:
- Use different terminology, synonyms, and alternative technical framings
- Cover different angles: conceptual explanations, tutorials, comparisons, official docs
- Keep each query concise (3-8 words)
- CRITICAL: Preserve unambiguous technology names exactly (e.g. "golang" NOT "Go", "python" NOT "py", "nodejs" NOT "node")
- Do NOT add GitHub-specific syntax (no language:X, topic:X)

Return ONLY a JSON array of %d strings, no other text, no markdown.
Example for "golang HTTP middleware": ["Go net/http handler chain tutorial", "golang web middleware framework examples"]`

// ExpandQueryPrompt generates GitHub-optimized search query variants.
// Args: n, query, n.
var ExpandQueryPrompt = `Generate %d different search query variants for finding GitHub repositories related to: "%s"

Rules:
- Use different terminology, synonyms, alternative technical framings
- Mix natural language with GitHub search syntax (language:X, topic:X, stars:>N)
- Cover different angles: language-specific, domain-focused, use-case-based
- Keep each query concise (2-6 words or tokens)
- CRITICAL: Preserve unambiguous technology names exactly (e.g. "golang" NOT "Go", "python" NOT "py")

Return ONLY a JSON array of %d strings, no other text, no markdown.
Example: ["language:go topic:llm-agent", "golang autonomous ai workflow", "golang orchestration llm tool"]`
