# Tool: `job_match_score`

> **Category:** Analysis | **Source:** `internal/engine/jobs/match.go`

Score job listings against a resume using **Jaccard keyword overlap** (0–100). Searches LinkedIn, Indeed, YC, and HN, then ranks each result by how well it matches your resume text. Returns jobs sorted by `match_score` with lists of matching and missing keywords.

---

## Input

| Parameter  | Type   | Required | Description |
|-----------|--------|----------|-------------|
| `resume`  | string | ✅       | Resume text to match against job listings |
| `query`   | string | ✅       | Job search keywords (e.g. `golang developer`, `data engineer`) |
| `location`| string | —        | City, country, or `Remote` |
| `platform`| string | —        | Source filter: `linkedin` \| `indeed` \| `yc` \| `hn` \| `startup` \| `all` (default) |

---

## Output

```json
{
  "query": "golang developer remote",
  "jobs": [
    {
      "title": "Senior Go Engineer",
      "company": "Acme Corp",
      "url": "https://jobs.lever.co/acme/abc123",
      "location": "Remote",
      "source": "linkedin",
      "snippet": "**Company:** Acme Corp\n**Location:** Remote\n...",
      "match_score": 42.5,
      "matching_keywords": ["golang", "kubernetes", "postgresql", "rest", "api"],
      "missing_keywords": ["terraform", "aws", "grpc", "protobuf"]
    }
  ],
  "summary": "Scored 23 jobs for \"golang developer remote\". Top match: 42.5/100."
}
```

---

## Scoring Algorithm

**Jaccard similarity** between resume keywords and job keywords:

```
score = |resume_kw ∩ job_kw| / |resume_kw ∪ job_kw| × 100
```

- **`matching_keywords`** — terms present in both your resume AND the job description (your strengths for this role)
- **`missing_keywords`** — important job keywords absent from your resume (skills gap, top 20)
- **Tokenizer** — lowercased words ≥ 3 chars; preserves tech terms like `c++`, `c#`, `node.js`; filters 40+ stop words (`and`, `the`, `work`, `team`, etc.)
- Score range: 0–100, rounded to 1 decimal

---

## Sources

All searches run in **parallel**:

| Source | Results |
|--------|---------|
| LinkedIn Guest API | up to 50 jobs with detail fetch |
| Indeed iOS GraphQL API | up to 15 jobs |
| YC workatastartup.com | up to 10 jobs |
| HN Who is Hiring | up to 10 jobs |

---

## Implementation

- **File:** `internal/engine/jobs/match.go` — `ExtractResumeKeywords()`, `ScoreJobMatch()`
- **Registration:** `internal/jobserver/register.go` → `registerJobMatchScore()`
- Resume keywords extracted **once** and reused for all job scoring (efficient batch)
- URL dedup applied before scoring
- Returns top 15 results sorted by `match_score` descending
