package jobserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/engine/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerJobMatchScore(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_match_score",
		Description: "Score job listings against a resume using keyword overlap analysis (Jaccard similarity). Searches jobs across LinkedIn, Indeed, and YC, then ranks each result by how well it matches the resume text. Returns jobs sorted by match_score (0–100) with lists of matching and missing keywords.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, input engine.JobMatchScoreInput) (*mcp.CallToolResult, engine.JobMatchScoreOutput, error) {
		if input.Resume == "" {
			return nil, engine.JobMatchScoreOutput{}, fmt.Errorf("resume is required")
		}
		if input.Query == "" {
			return nil, engine.JobMatchScoreOutput{}, fmt.Errorf("query is required")
		}

		resumeKW := jobs.ExtractResumeKeywords(input.Resume)

		platform := strings.ToLower(strings.TrimSpace(input.Platform))
		if platform == "" {
			platform = "all"
		}

		var mu sync.Mutex
		var allResults []engine.SearxngResult
		var wg sync.WaitGroup

		if platform == "all" || platform == "linkedin" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				liJobs, err := jobs.SearchLinkedInJobs(ctx, input.Query, input.Location, "", "", "", "", "", 50, false)
				if err != nil {
					slog.Warn("job_match_score: linkedin error", slog.Any("error", err))
					return
				}
				rs := jobs.LinkedInJobsToSearxngResults(ctx, liJobs, 5)
				mu.Lock()
				allResults = append(allResults, rs...)
				mu.Unlock()
			}()
		}

		if platform == "all" || platform == "indeed" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rs, err := jobs.SearchIndeedJobsFiltered(ctx, input.Query, input.Location, "", "", 15)
				if err != nil {
					slog.Warn("job_match_score: indeed error", slog.Any("error", err))
					return
				}
				mu.Lock()
				allResults = append(allResults, rs...)
				mu.Unlock()
			}()
		}

		if platform == "all" || platform == "yc" || platform == "startup" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rs, err := jobs.SearchYCJobs(ctx, input.Query, input.Location, 10)
				if err != nil {
					slog.Warn("job_match_score: yc error", slog.Any("error", err))
					return
				}
				mu.Lock()
				allResults = append(allResults, rs...)
				mu.Unlock()
			}()
		}

		if platform == "all" || platform == "hn" || platform == "startup" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rs, err := jobs.SearchHNJobs(ctx, input.Query, 10)
				if err != nil {
					slog.Warn("job_match_score: hn error", slog.Any("error", err))
					return
				}
				mu.Lock()
				allResults = append(allResults, rs...)
				mu.Unlock()
			}()
		}

		wg.Wait()

		if len(allResults) == 0 {
			return nil, engine.JobMatchScoreOutput{Query: input.Query, Summary: "No jobs found."}, nil
		}

		// Dedup by URL.
		seen := make(map[string]bool)
		var deduped []engine.SearxngResult
		for _, r := range allResults {
			if r.URL != "" && !seen[r.URL] {
				seen[r.URL] = true
				deduped = append(deduped, r)
			}
		}

		// Score each result against resume keywords.
		scored := make([]engine.JobMatchResult, 0, len(deduped))
		for _, r := range deduped {
			jobText := r.Title + " " + r.Content
			score, matching, missing := jobs.ScoreJobMatch(resumeKW, jobText)

			// Split "Title at Company" LinkedIn format into separate fields.
			title, company := r.Title, ""
			if parts := strings.SplitN(r.Title, " at ", 2); len(parts) == 2 {
				title = parts[0]
				company = parts[1]
			}

			snippet := engine.TruncateRunes(r.Content, 300, "...")

			scored = append(scored, engine.JobMatchResult{
				Title:            title,
				Company:          company,
				URL:              r.URL,
				Source:           extractSource(r.URL),
				Snippet:          snippet,
				MatchScore:       score,
				MatchingKeywords: matching,
				MissingKeywords:  missing,
			})
		}

		// Sort by score descending.
		sort.Slice(scored, func(i, j int) bool {
			return scored[i].MatchScore > scored[j].MatchScore
		})
		if len(scored) > 15 {
			scored = scored[:15]
		}

		topScore := 0.0
		if len(scored) > 0 {
			topScore = scored[0].MatchScore
		}
		summary := fmt.Sprintf("Scored %d jobs for %q. Top match: %.1f/100.", len(scored), input.Query, topScore)

		return nil, engine.JobMatchScoreOutput{
			Query:   input.Query,
			Jobs:    scored,
			Summary: summary,
		}, nil
	})
}

// extractSource guesses the job board name from a URL hostname.
func extractSource(jobURL string) string {
	u, err := url.Parse(jobURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	switch {
	case strings.Contains(host, "linkedin"):
		return "linkedin"
	case strings.Contains(host, "indeed"):
		return "indeed"
	case strings.Contains(host, "workatastartup"):
		return "yc"
	case strings.Contains(host, "ycombinator"):
		return "hn"
	case strings.Contains(host, "greenhouse"):
		return "greenhouse"
	case strings.Contains(host, "lever"):
		return "lever"
	case strings.Contains(host, "remoteok"):
		return "remoteok"
	case strings.Contains(host, "remotive"):
		return "remotive"
	default:
		return host
	}
}
