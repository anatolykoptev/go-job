package jobserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/quality"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerJobQualityScore(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_quality_score",
		Description: "Compute a deterministic 0-100 job-posting quality score with no LLM. Scores salary presence, direct-apply URL, freshness, description length, agency detection, and source quality. Accepts either a job_url (fetched and parsed) or an inline job_description. Returns the total score, a per-factor breakdown, and a verdict band (high/medium/low/skip). Use as a cheap pre-filter before expensive LLM-based fit scoring.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.JobQualityScoreInput) (*mcp.CallToolResult, engine.JobQualityScoreOutput, error) {
		if input.JobURL == "" && input.JobDescription == "" {
			return nil, engine.JobQualityScoreOutput{}, errors.New("either job_url or job_description is required")
		}

		desc := input.JobDescription
		title := input.JobTitle

		// If a URL is provided, fetch its content to enrich the description.
		if input.JobURL != "" {
			fetchedTitle, fetchedDesc, err := engine.FetchURLContent(ctx, input.JobURL)
			if err == nil && fetchedDesc != "" {
				// Prefer the fetched description if the inline one is empty or very short.
				if len(desc) < 200 {
					desc = fetchedDesc
				}
				if title == "" {
					title = fetchedTitle
				}
			}
			// Fetch failure is non-fatal — score what we have.
		}

		qIn := quality.Input{
			Title:       title,
			Company:     input.Company,
			URL:         input.JobURL,
			Description: desc,
			Source:      input.Source,
			SalaryMin:   input.SalaryMin,
			SalaryMax:   input.SalaryMax,
		}

		result := quality.Score(qIn)

		breakdown := map[string]int{
			"salary":             result.Breakdown.Salary,
			"direct_apply":       result.Breakdown.DirectApply,
			"freshness":          result.Breakdown.Freshness,
			"description_length": result.Breakdown.DescriptionLength,
			"not_agency":         result.Breakdown.NotAgency,
			"source_quality":     result.Breakdown.SourceQuality,
			"has_description":    result.Breakdown.HasDescription,
		}

		summary := fmt.Sprintf("Quality score: %d/100 (%s). Breakdown: salary=%d, direct_apply=%d, freshness=%d, description_length=%d, not_agency=%d, source_quality=%d, has_description=%d.",
			result.Score, result.Verdict,
			result.Breakdown.Salary, result.Breakdown.DirectApply,
			result.Breakdown.Freshness, result.Breakdown.DescriptionLength,
			result.Breakdown.NotAgency, result.Breakdown.SourceQuality,
			result.Breakdown.HasDescription)

		return nil, engine.JobQualityScoreOutput{
			Score:     result.Score,
			Verdict:   result.Verdict,
			Breakdown: breakdown,
			Summary:   summary,
		}, nil
	})
}

// qualityScoreFromListing computes a quality.Result from an engine.JobListing
// without a PostedAt timestamp (job_search results carry a human-readable
// "posted" string, not a parsed time). Freshness will score 0 in this path.
// Used by job_search to annotate each result with a quality_score field.
func qualityScoreFromListing(j engine.JobListing) quality.Result {
	in := quality.Input{
		Title:       j.Title,
		Company:     j.Company,
		URL:         j.URL,
		Description: j.Description,
		Source:      j.Source,
	}
	// Parse numeric salary from SalaryMin/SalaryMax pointers if present.
	if j.SalaryMin != nil {
		in.SalaryMin = *j.SalaryMin
	}
	if j.SalaryMax != nil {
		in.SalaryMax = *j.SalaryMax
	}
	return quality.Score(in)
}

// extractSourceFromURL guesses the job board name from a URL hostname,
// reusing the same logic as extractSource but returning a quality-source
// string suitable for sourceQualityScore. Kept here to avoid importing
// extractSource (which returns a different label set).
func extractSourceForQuality(jobURL string) string {
	u := strings.ToLower(jobURL)
	switch {
	case strings.Contains(u, "greenhouse"):
		return "greenhouse"
	case strings.Contains(u, "lever.co"):
		return "lever"
	case strings.Contains(u, "ashbyhq"):
		return "ashby"
	case strings.Contains(u, "workatastartup") || strings.Contains(u, "ycombinator"):
		return "yc"
	case strings.Contains(u, "linkedin"):
		return "linkedin"
	case strings.Contains(u, "indeed"):
		return "indeed"
	default:
		return ""
	}
}
