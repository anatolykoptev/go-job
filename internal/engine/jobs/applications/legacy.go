package applications

// legacy.go holds the fuzzy slug-based file lookup that was ported from
// go-nerv/internal/admin/page_hunt.go and previously lived in adminui/download.go.
// These functions are the ONLY legacy fallback; deleted in Phase G once the
// cp-migration has run and the ~/sites bind-mount is removed.

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// findApplicationSlug scans legacyDir to find a slug directory whose
// name matches company+title after normalisation. Returns an error when none found.
func findApplicationSlug(legacyDir, company, title string) (string, error) {
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		return "", fmt.Errorf("read legacy dir: %w", err)
	}
	return findApplicationSlugFromEntries(entries, company, title)
}

// findApplicationSlugFromEntries is the testable inner of findApplicationSlug.
// An application dir name is a curated TOKEN SUBSET of company+title (the
// operator drops the less-distinctive title words, e.g. "anthropic-databases").
// Match anchored on the company slug: every token in the dir name must appear
// in the job's company+title token set. The most-specific dir wins (most
// tokens, then longest name); a tie between two distinct dirs is ambiguous and
// rejected (better no link than the wrong job's PDF).
func findApplicationSlugFromEntries(entries []os.DirEntry, company, title string) (string, error) {
	companySlug := slugify(company)
	if companySlug == "" {
		return "", fmt.Errorf("empty company slug for title %q", title)
	}
	jobTokens := make(map[string]bool)
	for _, tok := range strings.Split(slugify(company+"-"+title), "-") {
		if tok != "" {
			jobTokens[tok] = true
		}
	}
	prefix := companySlug + "-"
	bestSlug, bestScore, bestLen := "", 0, 0
	ambiguous := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		matched, allMatch := 0, true
		for _, tok := range strings.Split(name, "-") {
			if tok == "" {
				continue
			}
			if jobTokens[tok] {
				matched++
			} else {
				allMatch = false
				break
			}
		}
		if !allMatch {
			continue
		}
		switch {
		case matched > bestScore || (matched == bestScore && len(name) > bestLen):
			bestSlug, bestScore, bestLen, ambiguous = name, matched, len(name), false
		case matched == bestScore && len(name) == bestLen && name != bestSlug:
			ambiguous = true
		}
	}
	if bestSlug == "" {
		return "", fmt.Errorf("no application slug matching company=%q title=%q", company, title)
	}
	if ambiguous {
		return "", fmt.Errorf("ambiguous application dir for company=%q title=%q", company, title)
	}
	return bestSlug, nil
}

// findApplicationPDF returns the path of the first matching PDF for kind in slugDir,
// using a 2-stage lookup:
//  1. <slugDir>/submit/<kind>*.pdf  — canonical location (resume-coach subagents)
//  2. <slugDir>/<kind>*.pdf         — legacy slug-root fallback (warns via slog)
//
// Returns "" when no PDF is found in either location.
func findApplicationPDF(slugDir, kind string) string {
	submitMatches, _ := filepath.Glob(filepath.Join(slugDir, "submit", kind+"*.pdf"))
	if len(submitMatches) > 0 {
		return submitMatches[0]
	}
	rootMatches, _ := filepath.Glob(filepath.Join(slugDir, kind+"*.pdf"))
	if len(rootMatches) > 0 {
		slog.Warn("findApplicationPDF: serving PDF from legacy slug root; move to submit/",
			"slugDir", slugDir, "kind", kind, "file", filepath.Base(rootMatches[0]))
		return rootMatches[0]
	}
	return ""
}

// slugNonAlphanumRe matches any run of non-alphanumeric characters.
var slugNonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify converts a string to a lower-case dash-separated slug.
func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return slugNonAlphanumRe.ReplaceAllString(b.String(), "-")
}
