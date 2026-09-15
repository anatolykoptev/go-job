# Tool: `research`

> **Category:** Research | **Source:** `internal/engine/jobs/research.go`
>
> One tool, three subjects. Replaces the retired `salary_research`, `company_research` and
> `person_research` tools.

```
research(subject="salary",  role=…, location=…, experience=…)
research(subject="company", company=…)
research(subject="person",  name=…)
```

**What this tool is.** A SearXNG fan-out plus LLM synthesis over public web results. That
input is public and pasteable, so by
[principles §1](../architecture/principles.md#1-the-criterion-is-what-the-model-is-fed-not-whether-a-model-is-used)
this is a *convenience* surface, not a differentiating one — a chat window with web access
can produce a comparable answer. It is here because it saves a context switch mid-workflow,
and because the output is structured enough to feed the other tools. Do not read its
numbers as proprietary data; they are estimates synthesized from search results.

---

## `subject=salary`

### Input

| Parameter | Type | Required | Description |
|---|---|---|---|
| `role` | string | ✅ | Job title (e.g. `Senior Go Developer`) |
| `location` | string | — | City, country or region (e.g. `San Francisco`, `Remote`, `Москва`) |
| `experience` | string | — | `junior` \| `mid` \| `senior` \| `lead` |

### Output

```json
{
  "role": "Senior Go Developer",
  "location": "San Francisco",
  "currency": "USD",
  "p25": 160000,
  "median": 195000,
  "p75": 230000,
  "sources": ["levels.fyi", "glassdoor.com", "linkedin.com"],
  "notes": "Equity not included. Varies significantly by company tier.",
  "updated_at": "2026"
}
```

### Source selection

| Location type | Primary sources |
|---|---|
| International (US, EU, …) | levels.fyi, glassdoor.com, linkedin.com/salary |
| Russian (Москва, Россия, russia, moscow, спб, ru…) | hh.ru, career.habr.com, zarplata.ru |
| Remote / unspecified | levels.fyi, glassdoor.com, remote.com |

Three parallel SearXNG queries per call, then one LLM synthesis pass into structured JSON.
For Russian locations the output currency defaults to `RUB`.

Helpers: `buildSalaryQueries()`, `isRussianLocation()`.

---

## `subject=company`

### Input

| Parameter | Type | Required | Description |
|---|---|---|---|
| `company` | string | ✅ | Company name (e.g. `Stripe`, `Яндекс`) |

### Output

```json
{
  "name": "Stripe",
  "size": "8000-10000",
  "founded": "2010",
  "industry": "FinTech / Payments",
  "funding": "Private, $95B valuation",
  "tech_stack": ["Ruby", "Go", "Java", "React", "AWS", "Kafka"],
  "culture_notes": "Strong emphasis on writing and documentation. Offices in SF, NYC, Dublin.",
  "recent_news": ["Launched Stripe Tax globally in 2024"],
  "glassdoor_rating": 4.1,
  "website": "https://stripe.com",
  "summary": "Stripe is a global payments infrastructure company…"
}
```

Use the official company name (`Яндекс`, not `yandex`) — the search queries are built from
it verbatim.

---

## `subject=person`

Background, interests and interview tips for a named hiring manager or interviewer,
synthesized the same way.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `name` | string | ✅ | Person name |
| `company` | string | — | Disambiguates a common name |
| `job_title` | string | — | Their role |
| `location` | string | — | Disambiguates further |

---

## Notes

- **Not cached** — LLM-generated and context-dependent.
- Accuracy depends entirely on what is publicly indexed. Treat every field as an estimate.
- `research` is used as an optional enrichment step by `interview_prep`, `pitch_generate`,
  `negotiation_prep` and `resume_generate`; each one bounds it so a slow research substep
  degrades to "no company context" rather than failing the call.

---

## Typical workflow

```
job_search        → find interesting companies
research(company) → due diligence before applying
research(salary)  → compensation benchmark
negotiation_prep  → consumes the salary benchmark
```

---

## Implementation

- **File:** `internal/engine/jobs/research.go` — `ResearchSalary()`, `ResearchCompany()`
- **Registration:** `internal/jobserver/tool_research.go`
- **Tests:** `internal/engine/jobs/research_test.go`
