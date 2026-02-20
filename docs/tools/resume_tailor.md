# Tool: `resume_tailor`

> **Category:** Resume | **Source:** `internal/engine/jobs/resume.go`

Rewrite resume sections to better match a specific job description. Returns tailored sections, keyword diff, and a complete tailored resume ready to use.

---

## Input

| Parameter         | Type   | Required | Description |
|------------------|--------|----------|-------------|
| `resume_text`    | string | ✅       | Resume as plain text |
| `job_description`| string | ✅       | Job description to tailor for |

---

## Output

```json
{
  "tailored_sections": {
    "Summary": "Results-driven Go engineer with 5+ years building distributed systems...",
    "Skills": "Go, gRPC, Kubernetes, Prometheus, PostgreSQL, Terraform"
  },
  "added_keywords": ["gRPC", "Prometheus", "distributed systems"],
  "removed_keywords": ["PHP", "jQuery"],
  "diff_summary": "Added gRPC and Prometheus to skills, reordered experience bullets to highlight distributed systems work, removed legacy frontend stack.",
  "tailored_resume": "John Doe\nSenior Go Engineer\n..."
}
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `tailored_sections` | map[string]string | Rewritten sections by name (Summary, Skills, Experience, etc.) |
| `added_keywords` | []string | Keywords added to align with JD |
| `removed_keywords` | []string | Keywords removed as irrelevant to this JD |
| `diff_summary` | string | Human-readable summary of all changes made |
| `tailored_resume` | string | Complete rewritten resume as plain text |

---

## Typical Workflow

```
resume_analyze        → see current ATS score + gaps
resume_tailor         → fix gaps, add missing keywords
resume_analyze again  → verify improved score
cover_letter_generate → write cover letter for the tailored resume
```

---

## Notes

- **Not cached** — LLM-generated, context-dependent.
- The LLM preserves factual accuracy — it reorders and reframes existing experience, does not fabricate new roles or skills.
- `tailored_resume` is the full resume with all sections merged; `tailored_sections` gives granular per-section diffs.
- For best results, provide the complete resume (all sections) and the full JD.

---

## Implementation

- **File:** `internal/engine/jobs/resume.go` — `TailorResume()`
- **LLM prompt:** `resumeTailorPrompt` (2 `%s` placeholders: resume, job description)
- **Registration:** `internal/jobserver/register.go`
- **Tests:** `internal/engine/jobs/resume_test.go`
