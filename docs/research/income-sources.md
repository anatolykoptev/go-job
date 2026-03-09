# Income Sources Research — March 2026

## 1. Security Bug Bounty Platforms

### API Summary

| Platform | API Endpoint | Auth | Data |
|----------|-------------|------|------|
| **bounty-targets-data** | `raw.githubusercontent.com/arkadiyt/bounty-targets-data/main/data/*.json` | None | Aggregator of all 5 below, updated hourly |
| **YesWeHack** | `api.yeswehack.com/programs?page={N}` | None | Best REST API. Full scopes + reward grids in detail endpoint |
| **Immunefi** | `immunefi.com/public-api/bounties.json` | None | Single JSON dump, all programs. Web3/crypto, up to $1M+ |
| **Bugcrowd** | `bugcrowd.com/engagements.json?category=bug_bounty&page={N}` | None | Paginated JSON. min/max rewards |
| **HackerOne** | `hackerone.com/graphql` (POST) | CSRF token | GraphQL, needs cookie/CSRF dance. Largest platform |
| **Intigriti** | Algolia `aazuksyar4-dsn.algolia.net` | Public key `70d8a3400477311f27ce002ec953aeb0` | Search index. EU-focused |

### Integration Status

- **bounty-targets-data**: Implemented (`security_bounty.go`, `security_parsers.go`)
- **Immunefi**: Implemented (`immunefi.go`)
- **YesWeHack direct API**: Not yet (using BTD aggregator instead)
- **Bugcrowd direct API**: Not yet (using BTD aggregator instead)
- **HackerOne direct API**: Not yet (using BTD aggregator instead)

### Security Tools (Go, integrable as libraries)

| Tool | Repo | Stars | Purpose | Go Library |
|------|------|-------|---------|------------|
| **Nuclei** | projectdiscovery/nuclei | ~27k | Template-based vuln scanner, 4000+ templates | `nuclei/v3/lib` |
| **Subfinder** | projectdiscovery/subfinder | ~10k | Passive subdomain enumeration | `subfinder/v2/pkg` |
| **httpx** | projectdiscovery/httpx | ~8k | HTTP probing, tech fingerprinting | `httpx/runner` |
| **Katana** | projectdiscovery/katana | ~12k | Web crawler, JS rendering, scope-aware | `katana/pkg` |
| **Naabu** | projectdiscovery/naabu | ~5k | Fast port scanner (SYN/CONNECT) | `naabu/v2/pkg` |
| **OWASP Amass** | owasp-amass/amass | ~14k | Attack surface mapping, OSINT | Go library |
| **Dalfox** | hahwul/dalfox | ~11k | XSS scanner | `dalfox/v2` |
| **Patchy** | copyleftdev/patchy | new | MCP server wrapping PD tools — matches our architecture | N/A (MCP) |
| **Web Cache Scanner** | Hackmanit/Web-Cache-Vulnerability-Scanner | ~1.1k | Cache poisoning/deception detection | Go binary |

### Key Insight

**Patchy** is an MCP server wrapping ProjectDiscovery tools with scope enforcement. Could fork or use as basis for our own security scanning MCP.

---

## 2. Freelance/Contract Platforms

### Tier 1 — Public JSON APIs, No Auth

| Platform | Endpoint | Filter | Volume |
|----------|----------|--------|--------|
| **RemoteOK** | `remoteok.com/api?tag=golang` | Tag-based | High |
| **Himalayas.app** | `himalayas.app/jobs/api?q=golang&limit=50` | Keyword + pagination | 102k+ jobs |
| **Freelancer.com** | `freelancer.com/api/projects/0.1/projects/active?query=golang` | Query + job IDs | Very high |
| **Working Nomads** | `workingnomads.com/api/exposed_jobs/?category=development` | Category | Low |
| **Jobicy** | `jobicy.com/api/v2/remote-jobs?tag=devops` | Tag | Very low |

### Tier 2 — RSS Feeds

| Platform | Feed |
|----------|------|
| **We Work Remotely** | `weworkremotely.com/categories/remote-devops-sysadmin-jobs.rss` |

### Tier 3 — Requires Auth

| Platform | Auth | Notes |
|----------|------|-------|
| **Upwork** | OAuth 2.0 | Highest volume, requires registered app |

### No API

Toptal, Contra, Gun.io, Arc.dev, Turing — closed platforms, no public feeds.

### Integration Status

- **RemoteOK**: Implemented (`remoteok.go`)
- **Himalayas**: Implemented (`himalayas.go`)
- **Freelancer.com**: Existing (`tool_freelance.go` uses their API)
- **Working Nomads**: Not yet
- **We Work Remotely**: Not yet (RSS)

---

## 3. Open Source Grants & Funding

### Programmatically Monitorable

| Platform | API | Grant Size | Notes |
|----------|-----|-----------|-------|
| **Open Collective** | GraphQL v2 `api.opencollective.com/graphql/v2` (10 req/min unauth) | Varies | Best API. Query all collectives, budgets, goals |
| **FLOSS.fund** | Directory at `dir.floss.fund/`, `funding.json` standard | $10k-$100k/yr | Open source portal (Go+Postgres). Quarterly eval |
| **Gitcoin** | Allo Protocol Indexer (GraphQL, free) | $1k-$50k | Web3, quadratic funding rounds. $80M+ distributed |
| **NLnet** | Atom feed `nlnet.nl/feed.atom` | 5k-50k EUR | Fixed bimonthly deadlines (1st of even months) |
| **GitHub Sponsors** | GraphQL (auth required) | N/A | Search `FUNDING.yml` via code search API |

### Manual Monitoring Only

| Platform | Grant Size | Notes |
|----------|-----------|-------|
| **Sovereign Tech Fund** | 50k-545k EUR | No API. Blog announcements. Apply at `apply.sovereigntechfund.de` |
| **Open Technology Fund** | $50k-$900k | No API. Rolling applications. Internet freedom focus |
| **Microsoft FOSS Fund** | $12.5k/quarter | Internal nomination only |

### Not Useful

- **Thanks.dev**: Auto-distributes donations via dependency tree. Not grant opportunities.
- **FOSS Funders**: Directory page only.

### Integration Status

None implemented yet. Priority candidates: Open Collective (best API), NLnet (Atom feed + fixed schedule).

---

## 4. Code Bounty Platforms (already integrated)

| Platform | Source | Status |
|----------|--------|--------|
| **Algora.io** | Direct API | Implemented |
| **Opire.dev** | RSC scrape | Implemented |
| **BountyHub.dev** | JSON API | Implemented |
| **Boss.dev** | JSON API | Implemented |
| **Lightning Bounties** | JSON API | Implemented |
| **Collaborators.build** | JSON API | Implemented |
| **Polar.sh** | N/A | Dead — removed Issue Funding in April 2025 |

---

## Architecture Notes

All scrapers follow the same pattern:
1. Cache check (`engine.CacheLoadJSON`)
2. HTTP fetch via proxy (`engine.Cfg.HTTPClient`)
3. JSON parse into typed structs
4. Cache store (`engine.CacheStoreJSON`, 15-min TTL)
5. Return typed slice

Monitors follow `bounty_monitor.go` pattern:
1. Check `VaelorNotifyURL` at startup
2. Initial delayed run (30-60s)
3. Ticker loop (15-30 min)
4. Seen-set in cache for dedup
5. Telegram notifications via vaelor
