# go-linkedin — LinkedIn Voyager API Client

## Security Rules

- **PIN is one-time.** Every login attempt generates a NEW PIN. Never reuse old PINs.
- **Always use proxy for Login.** Login exposes TLS fingerprint to LinkedIn — datacenter IP = red flag for login. API calls are fine without proxy.
- **API calls: no proxy needed.** Voyager API doesn't check IP, only JA3 TLS fingerprint. Direct connection is 10x faster.
- **Rate limit: 50 req/day, 15-45s jitter.** Mimic human browsing. Never burst.
- **Login attempts: max 2 per hour.** Multiple rapid login attempts from different IPs = account lockout. Wait 15+ min between retries. Use fresh proxy port each time.
- **Cookies bound to JA3, not IP.** Same go-stealth instance for login and API = same JA3 = cookies work.

## Login Flow

1. `Login()` → GET /login → CSRF → POST login-submit
2. LinkedIn returns challenge: **App Challenge** (mobile approve) or **Email PIN**
3. Email PIN: `OnEmailPin` callback must return fresh PIN from user's email
4. App Challenge: `OnChallenge` callback notifies user, `pollChallenge` polls until approve
5. After challenge → follow redirects → extract `li_at` + `JSESSIONID`

## Architecture

- **go-linkedin** = library (no Docker, imported by go-job/go-social/go-startup)
- **go-social** = credential storage + login API (`POST /linkedin/login/{id}` + `POST /linkedin/pin/{session}`)
- **go-job** = MCP tools (linkedin_profile, linkedin_profile_ingest)
- **go-nerv** = entity graph (nerv_linkedin_ingest)

## Key Files

| File | Purpose |
|------|---------|
| `client.go` | Client struct, New(), do(), Cookies() |
| `login.go` | Login(), fetchLoginPage(), submitLogin() |
| `login_challenge.go` | App Challenge poll + Email PIN submit |
| `login_parse.go` | CSRF parser, cookie parser, URL resolver |
| `profile.go` | GetProfile() — dual decoration (WebTopCardCore + TopCardSupplementary) |
| `profile_parse.go` | Experience/Education/Certification parsers |
| `parse.go` | Voyager response parser, includedByType, findProfileByURN |
