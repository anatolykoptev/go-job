package jobs

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const code4renaURL = "https://code4rena.com/audits"

var code4renaCacheKey = "code4rena_audits"

// code4renaActiveStatuses are the RSC payload status values that represent ongoing or
// recently-live audits. "Completed" is excluded as it represents finished contests.
// Statuses observed 2026-05-20: LiveJudging, Judging, Reporting, Judging Complete, Completed.
var code4renaActiveStatuses = map[string]bool{
	"Open":          true,
	"Live":          true,
	"LiveJudging":   true,
	"Judging":       true,
	"Reporting":     true,
	"Judging Complete": true,
}

// Code4rena RSC regex patterns.
// Code4rena uses Next.js App Router; the RSC stream embeds JSON with single-escaped
// quotes. Example in the HTML source: \"slug\":\"2026-04-k2\" (one backslash per quote).
// In a Go raw string `\\"` matches the literal two-character sequence backslash+quote.
var reC4RSlug = regexp.MustCompile(`\\"slug\\":\\"(20\d{2}-\d{2}-[^\\]{1,80})\\"`)
var reC4RTitle = regexp.MustCompile(`\\"title\\":\\"([^\\]{1,200})\\"`)
var reC4RStatus = regexp.MustCompile(`\\"status\\":\\"([^\\]{1,60})\\"`)
var reC4RAmount = regexp.MustCompile(`\\"formattedAmount\\":\\"([^\\]{1,100})\\"`)

// reC4RAuditHref is the fallback: extract slug from href="/audits/SLUG" DOM links.
var reC4RAuditHref = regexp.MustCompile(`href="/audits/(20\d{2}-\d{2}-[^"]{1,80})"`)

// SearchCode4rena fetches audit contests from code4rena.com.
// Uses go-wowa Chrome render. Results are cached.
func SearchCode4rena(ctx context.Context, limit int) ([]engine.SecurityProgram, error) {
	engine.IncrCode4renaRequests()
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.SecurityProgram](ctx, code4renaCacheKey); ok {
		slog.Debug("code4rena: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	programs, err := fetchCode4rena(ctx)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, code4renaCacheKey, "", programs)
	if len(programs) > limit {
		programs = programs[:limit]
	}
	slog.Info("code4rena: fetched audits", slog.Int("count", len(programs)))
	return programs, nil
}

func fetchCode4rena(ctx context.Context) ([]engine.SecurityProgram, error) {
	html, err := fetchRenderedHTML(ctx, code4renaURL)
	if err != nil {
		return nil, err
	}
	return parseCode4renaHTML(html), nil
}

// parseCode4renaHTML extracts audit contests from the rendered Code4rena HTML.
//
// Code4rena embeds RSC (React Server Components) flight data with escaped JSON in
// script tags. Each audit object contains slug, title, status, and formattedAmount.
// We extract all slug+title+status triples, keep only active statuses, and attach
// the formattedAmount found nearest to the slug.
// Fallback: extract slugs from href="/audits/SLUG" DOM links when RSC parse yields 0.
func parseCode4renaHTML(html string) []engine.SecurityProgram {
	programs := parseCode4renaRSC(html)
	if len(programs) > 0 {
		return programs
	}

	// Fallback: DOM href extraction — no title/prize available.
	return parseCode4renaFallback(html)
}

func parseCode4renaRSC(html string) []engine.SecurityProgram {
	slugMatches := reC4RSlug.FindAllStringSubmatchIndex(html, -1)
	if len(slugMatches) == 0 {
		return nil
	}

	var programs []engine.SecurityProgram

	for _, slugIdx := range slugMatches {
		slug := html[slugIdx[2]:slugIdx[3]]

		// Find the RSC JSON object that contains this slug.
		// Objects open with { and close with }. We find the nearest { before the slug
		// (within 2 KB) to isolate the containing object and avoid cross-object matches.
		slugStart := slugIdx[0]
		objStart := strings.LastIndex(html[max(0, slugStart-2000):slugStart], "{")
		if objStart < 0 {
			objStart = max(0, slugStart-2000)
		} else {
			objStart = max(0, slugStart-2000) + objStart
		}

		objEnd := slugIdx[1] + 2000
		if objEnd > len(html) {
			objEnd = len(html)
		}
		block := html[objStart:objEnd]

		titleM := reC4RTitle.FindStringSubmatch(block)
		statusM := reC4RStatus.FindStringSubmatch(block)
		amountM := reC4RAmount.FindStringSubmatch(block)

		if titleM == nil || statusM == nil {
			continue
		}

		status := statusM[1]
		if !code4renaActiveStatuses[status] {
			continue
		}

		title := titleM[1]
		maxBounty := ""
		if amountM != nil {
			// formattedAmount is "$$135,000 in USDC" — strip one leading $ to get "$135,000 in USDC".
			raw := amountM[1]
			if strings.HasPrefix(raw, "$$") {
				raw = raw[1:]
			}
			maxBounty = raw
		}

		programs = append(programs, engine.SecurityProgram{
			Name:      title,
			Platform:  "code4rena",
			URL:       "https://code4rena.com/audits/" + slug,
			MaxBounty: maxBounty,
			Type:      "audit_contest",
			Managed:   true,
		})
	}
	return programs
}

func parseCode4renaFallback(html string) []engine.SecurityProgram {
	matches := reC4RAuditHref.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		slog.Info("code4rena: no audit cards found in rendered HTML")
		return nil
	}

	seen := make(map[string]bool, len(matches))
	programs := make([]engine.SecurityProgram, 0, len(matches))
	for _, m := range matches {
		slug := m[1]
		if seen[slug] {
			continue
		}
		seen[slug] = true
		programs = append(programs, engine.SecurityProgram{
			Name:     prettifyC4RSlug(slug),
			Platform: "code4rena",
			URL:      "https://code4rena.com/audits/" + slug,
			Type:     "audit_contest",
			Managed:  true,
		})
	}
	return programs
}

// prettifyC4RSlug converts "2026-04-k2" → "K2 (2026-04)".
func prettifyC4RSlug(slug string) string {
	parts := strings.SplitN(slug, "-", 3)
	if len(parts) < 3 {
		return slug
	}
	year, month, rest := parts[0], parts[1], parts[2]
	if len(year) != 4 {
		return slug
	}
	name := titleCase(strings.ReplaceAll(rest, "-", " "))
	return name + " (" + year + "-" + month + ")"
}
