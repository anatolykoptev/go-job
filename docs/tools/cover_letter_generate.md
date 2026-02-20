# Tool: `cover_letter_generate`

> **Category:** Resume | **Source:** `internal/engine/jobs/resume.go`

Generate a tailored cover letter from a resume and job description. Supports three tones.

---

## Input

| Parameter         | Type   | Required | Description |
|------------------|--------|----------|-------------|
| `resume_text`    | string | ✅       | Resume as plain text |
| `job_description`| string | ✅       | Job description text |
| `tone`           | string | —        | `professional` (default) \| `friendly` \| `concise` |

### Tone guide

| Tone | Length | Style |
|------|--------|-------|
| `professional` | ~250–350 words | Formal, structured, achievement-focused |
| `friendly` | ~200–280 words | Warm, conversational, culture-fit emphasis |
| `concise` | ~120–180 words | Direct, bullet-friendly, respects recruiter's time |

---

## Output

```json
{
  "cover_letter": "Dear Hiring Manager,\n\nI am excited to apply for the Senior Go Engineer position at Stripe...",
  "word_count": 287,
  "tone": "professional"
}
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `cover_letter` | string | Full cover letter text, ready to copy-paste |
| `word_count` | int | Word count of the generated letter |
| `tone` | string | Tone used (echoes input, defaults to `professional`) |

---

## Typical Workflow

```
resume_analyze → identify gaps
resume_tailor  → improve resume
cover_letter_generate → write cover letter for the specific JD
```

---

## Notes

- **Not cached** — LLM-generated, context-dependent.
- Invalid or empty `tone` defaults to `professional`.
- The letter references specific role/company details extracted from the JD — provide the full JD for best results.

---

## Implementation

- **File:** `internal/engine/jobs/resume.go` — `GenerateCoverLetter()`
- **LLM prompt:** `coverLetterPrompt` (3 `%s` placeholders: tone, resume, job description)
- **Registration:** `internal/jobserver/register.go`
- **Tests:** `internal/engine/jobs/resume_test.go`
