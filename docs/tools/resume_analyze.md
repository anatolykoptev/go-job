# Tool: `resume_analyze`

> **Category:** Resume | **Source:** `internal/engine/jobs/resume.go`

Analyze a resume against a job description. Returns ATS compatibility score (0–100), matched and missing keywords, experience gaps, and actionable recommendations.

---

## Input

| Parameter         | Type   | Required | Description |
|------------------|--------|----------|-------------|
| `resume_text`    | string | ✅       | Resume as plain text (paste directly — no PDF parsing) |
| `job_description`| string | ✅       | Full job description text |

---

## Output

```json
{
  "ats_score": 72,
  "matching_keywords": ["Go", "REST API", "PostgreSQL"],
  "missing_keywords": ["gRPC", "Kubernetes", "Prometheus"],
  "gaps": [
    "No distributed systems experience at scale",
    "Missing cloud certifications"
  ],
  "recommendations": [
    "Add gRPC project to portfolio section",
    "Mention Prometheus monitoring in experience bullets",
    "Add Kubernetes deployment experience"
  ],
  "summary": "Strong backend match but missing cloud/container keywords. ATS score can be improved to 85+ by adding Kubernetes and gRPC."
}
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `ats_score` | int | ATS compatibility score 0–100 |
| `matching_keywords` | []string | Keywords present in both resume and JD |
| `missing_keywords` | []string | Keywords in JD but absent from resume |
| `gaps` | []string | Experience or qualification gaps |
| `recommendations` | []string | Concrete actions to improve the score |
| `summary` | string | Short overall assessment |

---

## Typical Workflow

```
resume_analyze → identify gaps
              → resume_tailor (fix the gaps)
              → cover_letter_generate (write cover letter)
```

---

## Notes

- **Not cached** — LLM-generated, context-dependent.
- Resume must be plain text. For PDF/DOCX: extract text first (e.g. `pdftotext`, `pandoc`).
- ATS score reflects keyword density and section structure relative to the JD.

---

## Implementation

- **File:** `internal/engine/jobs/resume.go` — `AnalyzeResume()`
- **LLM prompt:** `resumeAnalyzePrompt` (2 `%s` placeholders: resume, job description)
- **Registration:** `internal/jobserver/register.go`
- **Tests:** `internal/engine/jobs/resume_test.go`
