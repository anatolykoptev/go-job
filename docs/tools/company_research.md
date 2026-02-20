# Tool: `company_research`

> **Category:** Research | **Source:** `internal/engine/jobs/research.go`

Research a company before applying or interviewing. Returns size, funding, tech stack, culture notes, Glassdoor rating, and recent news — aggregated via SearXNG + LLM synthesis.

---

## Input

| Parameter      | Type   | Required | Description |
|---------------|--------|----------|-------------|
| `company_name`| string | ✅       | Company name (e.g. `Google`, `Яндекс`, `Stripe`, `Тинькофф`) |

---

## Output

```json
{
  "name": "Stripe",
  "size": "8000-10000",
  "founded": "2010",
  "industry": "FinTech / Payments",
  "funding": "Private, $95B valuation",
  "tech_stack": ["Ruby", "Go", "Java", "React", "AWS", "Kafka"],
  "culture_notes": "High-performance engineering culture. Strong emphasis on writing and documentation. Remote-friendly with offices in SF, NYC, Dublin.",
  "recent_news": [
    "Launched Stripe Tax globally in 2024",
    "Expanded to 50 new countries"
  ],
  "glassdoor_rating": 4.1,
  "website": "https://stripe.com",
  "summary": "Stripe is a leading global payments infrastructure company..."
}
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Normalized company name |
| `size` | string | Employee count range |
| `founded` | string | Founding year |
| `industry` | string | Industry / sector |
| `funding` | string | Funding stage or valuation |
| `tech_stack` | []string | Known technologies used |
| `culture_notes` | string | Work culture, remote policy, office locations |
| `recent_news` | []string | Latest notable events (launches, funding, layoffs) |
| `glassdoor_rating` | float | Glassdoor employee rating (if available) |
| `website` | string | Company website |
| `summary` | string | 2–3 sentence overview |

---

## Typical Workflow

```
job_search → find interesting companies
company_research → research each company before applying
salary_research  → check compensation benchmarks
```

---

## Notes

- **Not cached** — LLM-generated, context-dependent.
- Works for both international and Russian companies.
- Data is synthesized from web search results — not a live database. Accuracy depends on public information availability.
- For best results use the official company name (e.g. `Яндекс` not `yandex`).

---

## Implementation

- **File:** `internal/engine/jobs/research.go` — `ResearchCompany()`
- **Registration:** `internal/jobserver/register.go`
- **Tests:** `internal/engine/jobs/research_test.go`
