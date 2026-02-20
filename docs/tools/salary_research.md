# Tool: `salary_research`

> **Category:** Research | **Source:** `internal/engine/jobs/research.go`

Research salary ranges for a role and location. Aggregates data from levels.fyi, Glassdoor, LinkedIn, hh.ru, Хабр Карьера via SearXNG search + LLM synthesis.

---

## Input

| Parameter    | Type   | Required | Description |
|-------------|--------|----------|-------------|
| `role`      | string | ✅       | Job title (e.g. `Senior Go Developer`, `Data Engineer`, `Product Manager`) |
| `location`  | string | —        | City, country, or region (e.g. `San Francisco`, `Remote`, `Москва`, `Germany`) |
| `experience`| string | —        | `junior` \| `mid` \| `senior` \| `lead` |

---

## Output

```json
{
  "role": "Senior Go Developer",
  "location": "San Francisco",
  "currency": "USD",
  "p25": 160000,
  "median": 195000,
  "p75": 230000,
  "sources": ["levels.fyi", "glassdoor.com", "linkedin.com"],
  "notes": "Equity not included. Varies significantly by company tier (FAANG vs startup).",
  "updated_at": "2026"
}
```

### Fields

| Field | Type | Description |
|-------|------|-------------|
| `role` | string | Normalized role name |
| `location` | string | Location used for research |
| `currency` | string | `USD`, `EUR`, `RUB`, etc. |
| `p25` | int | 25th percentile (lower bound) |
| `median` | int | Median salary |
| `p75` | int | 75th percentile (upper bound) |
| `sources` | []string | Data sources consulted |
| `notes` | string | Caveats: equity, bonuses, tier differences |
| `updated_at` | string | Data recency estimate |

---

## Source Selection

The tool automatically selects sources based on location:

| Location type | Primary sources |
|--------------|----------------|
| **International** (US, EU, etc.) | levels.fyi, glassdoor.com, linkedin.com/salary |
| **Russian** (Москва, Россия, russia, moscow, спб, ru…) | hh.ru, career.habr.com, zarplata.ru |
| **Remote / unspecified** | levels.fyi, glassdoor.com, remote.com |

SearXNG runs **3 parallel queries** per research call, then LLM synthesizes the results into structured JSON.

---

## Notes

- **Not cached** — LLM-generated, context-dependent.
- Salary figures are estimates based on publicly available data — not real-time API data.
- For Russian locations, output currency is `RUB` by default.
- `experience` level is included in search queries to improve relevance.

---

## Implementation

- **File:** `internal/engine/jobs/research.go` — `ResearchSalary()`
- **Helpers:** `buildSalaryQueries()`, `isRussianLocation()`
- **Registration:** `internal/jobserver/register.go`
- **Tests:** `internal/engine/jobs/research_test.go`
