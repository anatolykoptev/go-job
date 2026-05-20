package jobs

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const cantinaURL = "https://cantina.xyz/competitions"

var cantinaCacheKey = "cantina_competitions"

// reCantinaLink matches competition links with UUID slugs.
// Cantina uses Next.js App Router — no __NEXT_DATA__; titles are in the title attribute.
// Example: <a ... title="Revert Finance / Revert Finance - StableSwap Hooks" href="/competitions/UUID">
var reCantinaLink = regexp.MustCompile(`title="([^"]+)"\s[^>]*href="(/competitions/[0-9a-f-]{36})"`)

// reCantinaAmount matches dollar amounts like $50,000 in the HTML near the competition card.
var reCantinaAmount = regexp.MustCompile(`\$[\d,]+`)

// SearchCantina fetches active audit contests from cantina.xyz.
// Uses go-wowa Chrome render. Results are cached.
func SearchCantina(ctx context.Context, limit int) ([]engine.SecurityProgram, error) {
	engine.IncrCantinaRequests()
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	if cached, ok := engine.CacheLoadJSON[[]engine.SecurityProgram](ctx, cantinaCacheKey); ok {
		slog.Debug("cantina: using cached results", slog.Int("results", len(cached)))
		if len(cached) > limit {
			cached = cached[:limit]
		}
		return cached, nil
	}

	programs, err := fetchCantina(ctx)
	if err != nil {
		return nil, err
	}

	engine.CacheStoreJSON(ctx, cantinaCacheKey, "", programs)
	if len(programs) > limit {
		programs = programs[:limit]
	}
	slog.Info("cantina: fetched contests", slog.Int("count", len(programs)))
	return programs, nil
}

func fetchCantina(ctx context.Context) ([]engine.SecurityProgram, error) {
	html, err := fetchRenderedHTML(ctx, cantinaURL)
	if err != nil {
		return nil, err
	}
	return parseCantinaHTML(html), nil
}

// parseCantinaHTML extracts active competitions from the rendered Cantina HTML.
//
// Cantina uses Next.js App Router (no __NEXT_DATA__). Competition cards are rendered
// into the DOM as <a title="TITLE" href="/competitions/UUID"> anchors.
// Prize amounts appear as $N,NNN text within the card's subtree.
func parseCantinaHTML(html string) []engine.SecurityProgram {
	matches := reCantinaLink.FindAllStringSubmatchIndex(html, -1)
	if len(matches) == 0 {
		slog.Info("cantina: no competition cards found in rendered HTML")
		return nil
	}

	programs := make([]engine.SecurityProgram, 0, len(matches))
	for _, idx := range matches {
		title := html[idx[2]:idx[3]]
		path := html[idx[4]:idx[5]]

		// Sanitize title: "Org / Contest Name" → use full title as Name.
		name := strings.TrimSpace(title)

		// Extract slug from /competitions/UUID.
		slug := strings.TrimPrefix(path, "/competitions/")

		// Find prize amount in the next 2000 bytes after the match start.
		end := idx[1] + 2000
		if end > len(html) {
			end = len(html)
		}
		segment := html[idx[0]:end]
		maxBounty := ""
		if amounts := reCantinaAmount.FindAllString(segment, -1); len(amounts) > 0 {
			// Take the largest dollar value found in the card.
			maxBounty = largestDollarAmount(amounts)
		}

		programs = append(programs, engine.SecurityProgram{
			Name:      name,
			Platform:  "cantina",
			URL:       "https://cantina.xyz/competitions/" + slug,
			MaxBounty: maxBounty,
			Type:      "audit_contest",
			Managed:   true,
		})
	}
	return programs
}

// largestDollarAmount returns the dollar string with the highest numeric value
// from a slice of strings like ["$50,000", "$30,000"]. Returns the first element
// if parsing fails for all entries.
func largestDollarAmount(amounts []string) string {
	best := amounts[0]
	bestVal := parseDollarValue(best)
	for _, a := range amounts[1:] {
		if v := parseDollarValue(a); v > bestVal {
			bestVal = v
			best = a
		}
	}
	return best
}

// parseDollarValue extracts the integer part of a dollar string like "$50,000".
func parseDollarValue(s string) int {
	s = strings.TrimPrefix(s, "$")
	s = strings.ReplaceAll(s, ",", "")
	val := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			break
		}
		val = val*10 + int(ch-'0')
	}
	return val
}
