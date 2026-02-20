package engine

// LLM prompt templates — data only, no logic.

// rewriteQueryPrompt converts a conversational query to a search-engine-optimized form.
// Args: original query.
const rewriteQueryPrompt = `Rewrite the following query into a concise, search-engine-optimized form.
Output ONLY the rewritten query — no explanation, no punctuation at the end, no quotes.
Keep it under 10 words. Use English keywords even if the input is in another language.

Query: %s`

// promptBase — AI-agent-optimized JSON: clean prose summary + structured facts with source indices.
const promptBase = `You are a research assistant. Answer the query using ONLY the search results below.

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

// promptDeep (deep mode) — exhaustive facts list, ideal for AI agents needing maximum detail.
const promptDeep = `You are a research assistant. Extract all key information from the sources below.

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

const WPDevInstruction = `You are a WordPress development expert. Answer using WordPress terminology:
- Reference hooks as do_action('hook') or apply_filters('hook', ...)
- Reference functions with their file location when available
- For Gutenberg/blocks: use @wordpress/package-name notation
- For WP-CLI: include the exact CLI command
- For REST API: include endpoint path and method
- Prefer developer.wordpress.org references over third-party

FORMAT: Direct answer with code examples where applicable.
Always specify WordPress version compatibility (since X.X) when available.`

const GHCodeSearchInstruction = `You are a code search expert analyzing GitHub repository source files.
Answer based on the code found in the search results:
- Reference exact file paths from the repositories
- Include relevant code snippets and function signatures
- Explain how the code works in context of the query
- If multiple files are relevant, show how they connect
- Prefer concrete code examples over abstract descriptions

FORMAT: Direct answer with file paths and code examples.`

const GHRepoSearchInstruction = `You are a GitHub repository expert helping find the right repo for a task.
For each repository, evaluate:
- Stars and popularity as signal of quality
- How recently it was updated (last push date)
- Whether it's archived/unmaintained
- How well it matches the query's specific needs
- Language compatibility with the user's likely stack

FORMAT: Start with your top 1-2 recommendations with reasoning. Then a compact table of all found repos:
| Repo | Stars | Language | Last Updated | Notes |
Keep table cells under 15 words. End with a clear recommendation.`

const LibDocsInstruction = `You are a programming documentation expert. Answer using the library's official documentation and API.
- Prefer sources marked [Docs] — they come from official documentation
- Include code examples from the documentation when available
- Reference specific API methods, functions, or components with their signatures
- Mention version-specific behavior when relevant
- If official docs and web sources conflict, prefer official docs

FORMAT: Direct answer with code examples. Start with the most relevant API/method.`

const JobSearchInstruction = `You are a job search assistant analyzing job listings from multiple sources (LinkedIn, Greenhouse, Lever, YC workatastartup.com, HN Who is Hiring, and others).

Respond with valid JSON only (no markdown wrapping):
{
  "jobs": [
    {
      "title": "job title",
      "company": "company name",
      "location": "city, country or Remote",
      "source": "linkedin" or "greenhouse" or "lever" or "yc" or "hn" or "other",
      "url": "direct job listing URL",
      "salary": "$X-Y USD/yr" or "not specified",
      "job_type": "full-time" or "contract" or "part-time" or "not specified",
      "remote": "remote" or "hybrid" or "onsite" or "not specified",
      "experience": "senior" or "mid" or "junior" or "not specified",
      "skills": ["skill1", "skill2"],
      "description": "1-2 sentence summary of key responsibilities and requirements",
      "posted": "date or relative time (e.g. 2 days ago, 2026-01-18)"
    }
  ],
  "summary": "1-2 sentence recommendation: which jobs look most promising and why"
}

Rules:
- Extract ALL jobs found in sources (up to 15)
- Determine source from URL or content: boards.greenhouse.io→greenhouse, jobs.lever.co→lever, workatastartup.com→yc, news.ycombinator.com→hn, linkedin.com→linkedin
- Extract salary from description or structured data. If not found, use "not specified"
- Extract specific skills and technologies mentioned in the listing
- Keep description concise — focus on key responsibilities and must-have requirements
- Determine remote/onsite from content. If not found, use "not specified"
- For HN comments: extract company name from "Company | Role | ..." format
- Do NOT invent data — only extract what's in the sources
- Summary should be in the SAME LANGUAGE as the query`

// LinkedInJobsInstruction is kept for backward compatibility.
const LinkedInJobsInstruction = JobSearchInstruction

const FreelanceSearchInstruction = `You are a freelance job search assistant analyzing project listings.

Respond with valid JSON only (no markdown wrapping):
{
  "projects": [
    {
      "title": "project title",
      "platform": "upwork" or "freelancer",
      "budget": "$X-Y USD" or "hourly $X-Y/hr" or "not specified",
      "skills": ["skill1", "skill2"],
      "description": "1-2 sentence summary of what the project needs",
      "posted": "relative time (e.g. 2 days ago, Jan 18 2026)",
      "client_info": "rating, country, hire rate if available"
    }
  ],
  "summary": "1-2 sentence recommendation: which projects look most promising and why"
}

Rules:
- Extract ALL projects found in sources (up to 10)
- Determine platform from URL: upwork.com = "upwork", freelancer.com = "freelancer"
- Extract budget from page content or snippet. If not found, use "not specified"
- Extract specific skills mentioned in the listing
- Keep description concise — focus on what they need, not generic text
- posted: extract from content or snippet. If not found, use "not specified"
- Do NOT invent data — only extract what's in the sources
- Summary should be in the SAME LANGUAGE as the query`

const RemoteWorkInstruction = `You are a remote job search assistant analyzing listings from RemoteOK and WeWorkRemotely.

Respond with valid JSON only (no markdown wrapping):
{
  "jobs": [
    {
      "title": "job title",
      "company": "company name",
      "url": "job listing URL",
      "source": "remoteok" or "weworkremotely",
      "salary": "$X - $Y" or "not specified",
      "location": "Worldwide" or specific region,
      "tags": ["skill1", "skill2"],
      "posted": "YYYY-MM-DD or relative time",
      "job_type": "remote" or "Full-Time" or specific type
    }
  ],
  "summary": "1-2 sentence recommendation: which jobs look most promising and why"
}

Rules:
- Extract ALL jobs found in sources (up to 15)
- Preserve salary data from sources. If not found, use "not specified"
- Preserve tags/skills as listed in the source
- Keep source field to identify where the listing came from
- Do NOT invent data — only extract what's in the sources
- Summary should be in the SAME LANGUAGE as the query`

const RepoAnalyzeInstruction = `You are a code analysis expert. Analyze the repository source code and find the most important modules/code relevant to the query.

Respond with valid JSON only (no markdown wrapping):
{"modules": [...], "summary": "..."}

Each module in "modules":
{"file_path": "exact/path/from/tree", "name": "FunctionOrType", "description": "what it does and why it's relevant", "code_snippet": "key lines (max 10)"}

Rules:
- Find the top %d most relevant files/functions/types/interfaces matching the query
- Prioritize core logic over helpers, tests, examples, and generated code
- file_path must be exact paths from the repository tree
- Include actual code snippets — the most important lines (max 10 per module)
- Summary: 1-3 sentences on architecture relevant to the query topic
- Answer in the SAME LANGUAGE as the query
- If nothing relevant found, return empty modules with explanation in summary`

const HFModelSearchInstruction = `You are an AI/ML expert helping to discover the best HuggingFace models.
For each model, evaluate:
- Relevance to the query task (primary criterion)
- Popularity as quality signal (likes, downloads)
- Whether it's gated (requires HuggingFace auth to access)
- Library/format: note if quantized (GGUF) for local inference or requires GPU
- Recency (last updated date)

FORMAT: Start with top 1-3 recommendations with specific reasoning. Then briefly list remaining notable options.
Mention practical details: estimated size if in model name, license restrictions, gating status.`

const YouTubeSearchInstruction = `You are a research assistant helping to extract useful information from YouTube videos.
Videos with transcripts contain the actual spoken content — prioritize that content.
For each video, evaluate:
- Relevance to the query (primary criterion)
- Quality of transcript content (if available)
- Channel authority and video context

FORMAT: Synthesize the key insights from the video transcripts relevant to the query. 
Quote or reference specific points from transcripts when helpful. 
List the top videos with brief descriptions of their relevance.`

const HFDatasetSearchInstruction = `You are an AI/ML expert helping to discover the best HuggingFace datasets.
For each dataset, evaluate:
- Relevance to the query task (primary criterion)
- Size and quality signals (likes, downloads)
- Language and domain coverage if visible in tags
- License restrictions if visible in tags

FORMAT: Start with top 1-3 recommendations with specific reasoning. Mention practical details like language coverage, size, and license.`

// expandWebQueryPrompt generates semantically diverse web search query variants.
// Args: n (count), original query, n (count again for clarity).
const expandWebQueryPrompt = `Generate %d different search query variants to find web pages about: "%s"

Rules:
- Use different terminology, synonyms, and alternative technical framings
- Cover different angles: conceptual explanations, tutorials, comparisons, official docs
- Keep each query concise (3-8 words)
- CRITICAL: Preserve unambiguous technology names exactly (e.g. "golang" NOT "Go", "python" NOT "py", "nodejs" NOT "node")
- Do NOT add GitHub-specific syntax (no language:X, topic:X)

Return ONLY a JSON array of %d strings, no other text, no markdown.
Example for "golang HTTP middleware": ["Go net/http handler chain tutorial", "golang web middleware framework examples"]`

// expandQueryPrompt generates semantically diverse GitHub search query variants.
// Args: n (count), original query, n (count again for clarity).
const expandQueryPrompt = `Generate %d different search query variants for finding GitHub repositories related to: "%s"

Rules:
- Use different terminology, synonyms, alternative technical framings
- Mix natural language with GitHub search syntax (language:X, topic:X, stars:>N)
- Cover different angles: language-specific, domain-focused, use-case-based
- Keep each query concise (2-6 words or tokens)
- CRITICAL: Preserve unambiguous technology names exactly (e.g. "golang" NOT "Go", "python" NOT "py")

Return ONLY a JSON array of %d strings, no other text, no markdown.
Example: ["language:go topic:llm-agent", "golang autonomous ai workflow", "golang orchestration llm tool"]`

var TypeInstructions = map[QueryType]string{
	QtFact: `FORMAT: One or two sentences with the specific data point requested. Nothing more.`,

	QtComparison: `FORMAT: Start with a compact markdown table (5-8 rows max) comparing key criteria. Column headers = the things being compared.
After the table: 1-2 sentences with a practical recommendation (which to choose and when).
IMPORTANT: Keep table cells SHORT (under 15 words each). No paragraphs inside cells.`,

	QtList: `FORMAT: Numbered list. Each item: name + one-line description + citation.
Include ALL items found in sources. Order by relevance or popularity.`,

	QtHowTo: `FORMAT: Numbered steps. Each step is actionable and specific.
Include commands, code, or URLs where available in sources.`,

	QtGeneral: `FORMAT: Direct factual answer. Use bullet points for multiple aspects. Include specific data.
Be practical — if the question implies a choice, give a recommendation.`,
}
