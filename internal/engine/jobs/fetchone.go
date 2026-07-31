package jobs

import (
	"context"
	"fmt"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// contentLimitFetchVacancy is the max characters passed to SummarizeJobResults
// for a single vacancy page. 8000 chars gives the LLM enough JD text without
// blowing the token budget.
const contentLimitFetchVacancy = 8000

// FetchVacancy fetches a single vacancy page via the go-wowa render seam,
// extracts a JobListing via the shared LLM extractor, and backfills URL/source/JobID.
//
// On partial-failure (rendered HTML OK but LLM returns empty title+description),
// extract_quality is "weak" and the raw HTML is stored in the listing's description
// so the row is never silently dropped.
//
// Returns (listing, extractQuality, error):
//   - extractQuality == "ok"   — title and description populated by LLM
//   - extractQuality == "weak" — render succeeded but LLM extraction thin; raw HTML stored
//   - error non-nil            — render itself failed (network, go-wowa down, etc.)
func FetchVacancy(ctx context.Context, targetURL, sourceHint, companyHint string) (engine.JobListing, string, error) {
	html, err := fetchRenderedHTML(ctx, targetURL)
	if err != nil {
		return engine.JobListing{}, "", fmt.Errorf("fetchone: render %s: %w", targetURL, err)
	}

	// Extract readable text from the rendered HTML before passing to the LLM.
	// FetchURLContent uses the same extractorInst.Extract path; we mirror it here
	// to avoid feeding raw HTML (with <script>, <style>, nav etc.) to the model.
	// go-engine head-truncates source #0 to ~8KB, so sending raw HTML wastes
	// the budget on markup rather than JD text.
	cleanText, extractErr := engine.ExtractHTMLText(ctx, html, targetURL)
	if extractErr != nil || strings.TrimSpace(cleanText) == "" {
		// Extraction failed or produced nothing — fall back to raw HTML so the
		// row is still captured; LLM will see markup but at least we have a row.
		cleanText = html
	}

	// Build the minimal SearxngResult and content map that SummarizeJobResults expects.
	stub := engine.SearxngResult{
		URL:     targetURL,
		Title:   targetURL, // placeholder title; LLM will extract the real one
		Content: "",
	}
	contents := map[string]string{targetURL: cleanText}

	out, err := engine.SummarizeJobResults(ctx, "single vacancy", engine.JobSearchInstructionFor(1), contentLimitFetchVacancy, []engine.SearxngResult{stub}, contents)
	if err != nil {
		return engine.JobListing{}, "", fmt.Errorf("fetchone: LLM extract %s: %w", targetURL, err)
	}

	var job engine.JobListing
	if len(out.Jobs) > 0 {
		job = out.Jobs[0]
	}

	// Backfill URL — LLM may omit it. Belt-and-suspenders: SummarizeJobResults
	// already backfills URL from the stub, but we repeat it here for safety.
	if job.URL == "" {
		job.URL = targetURL
	}
	// Backfill Source from URL when unset, then allow caller hint to override.
	if job.Source == "" {
		job.Source = SourceFromURL(targetURL)
	}
	if sourceHint != "" {
		job.Source = sourceHint
	}
	// Backfill Company from caller hint when LLM returned nothing.
	if companyHint != "" && job.Company == "" {
		job.Company = companyHint
	}
	// Backfill JobID from URL when unset.
	if job.JobID == "" {
		job.JobID = ExtractJobID(targetURL)
	}

	// Partial-failure path: render succeeded but LLM produced no meaningful output.
	// Store raw HTML in description so the row is captured for later enrichment.
	if strings.TrimSpace(job.Title) == "" && strings.TrimSpace(job.Description) == "" {
		// Truncate HTML to avoid DB column overflow (description is text, unlimited, but
		// keep it reasonable — 16 KB is enough to represent a typical JD page).
		const rawHTMLLimit = 16384
		raw := html
		if len(raw) > rawHTMLLimit {
			raw = raw[:rawHTMLLimit]
			// Walk back to a valid UTF-8 rune boundary so we never split a multi-byte
			// sequence — Postgres rejects invalid UTF-8 in text columns.
			for len(raw) > 0 && raw[len(raw)-1]&0xC0 == 0x80 {
				raw = raw[:len(raw)-1]
			}
		}
		job.Description = raw
		return job, "weak", nil
	}

	return job, "ok", nil
}
