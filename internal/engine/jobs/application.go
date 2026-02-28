package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// ApplicationPrepResult is a complete application package.
type ApplicationPrepResult struct {
	Analysis      *ResumeAnalysisResult  `json:"analysis"`
	CoverLetter   *CoverLetterResult     `json:"cover_letter"`
	InterviewPrep *InterviewPrepResult   `json:"interview_prep"`
	CompanyInfo   *CompanyResearchResult  `json:"company_info,omitempty"`
	Summary       string                 `json:"summary"`
}

// PrepareApplication generates a complete application package:
// resume analysis + cover letter + interview prep + optional company research.
func PrepareApplication(ctx context.Context, resume, jobDescription, company, tone string) (*ApplicationPrepResult, error) {
	if tone == "" {
		tone = "professional"
	}

	resumeTrunc := engine.TruncateRunes(resume, 4000, "")
	jdTrunc := engine.TruncateRunes(jobDescription, 3000, "")

	var result ApplicationPrepResult
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error

	setErr := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}

	// 1. Resume analysis
	wg.Add(1)
	go func() {
		defer wg.Done()
		analysis, err := AnalyzeResume(ctx, resumeTrunc, jdTrunc)
		if err != nil {
			slog.Warn("application_prep: resume analysis failed", slog.Any("error", err))
			setErr(fmt.Errorf("resume analysis: %w", err))
			return
		}
		mu.Lock()
		result.Analysis = analysis
		mu.Unlock()
	}()

	// 2. Cover letter
	wg.Add(1)
	go func() {
		defer wg.Done()
		cl, err := GenerateCoverLetter(ctx, resumeTrunc, jdTrunc, tone)
		if err != nil {
			slog.Warn("application_prep: cover letter failed", slog.Any("error", err))
			setErr(fmt.Errorf("cover letter: %w", err))
			return
		}
		mu.Lock()
		result.CoverLetter = cl
		mu.Unlock()
	}()

	// 3. Interview prep
	wg.Add(1)
	go func() {
		defer wg.Done()
		ip, err := PrepareInterview(ctx, resumeTrunc, jdTrunc, company, "all")
		if err != nil {
			slog.Warn("application_prep: interview prep failed", slog.Any("error", err))
			setErr(fmt.Errorf("interview prep: %w", err))
			return
		}
		mu.Lock()
		result.InterviewPrep = ip
		mu.Unlock()
	}()

	// 4. Optional company research
	if company != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cr, err := ResearchCompany(ctx, company)
			if err != nil {
				slog.Warn("application_prep: company research failed", slog.Any("error", err))
				return // non-fatal
			}
			mu.Lock()
			result.CompanyInfo = cr
			mu.Unlock()
		}()
	}

	wg.Wait()

	if firstErr != nil {
		return nil, fmt.Errorf("application_prep: %w", firstErr)
	}

	// Build summary from sub-results
	var parts []string
	if result.Analysis != nil {
		parts = append(parts, fmt.Sprintf("ATS Score: %d/100.", result.Analysis.ATSScore))
	}
	if result.CoverLetter != nil {
		parts = append(parts, fmt.Sprintf("Cover letter: %d words (%s tone).", result.CoverLetter.WordCount, result.CoverLetter.Tone))
	}
	if result.InterviewPrep != nil {
		parts = append(parts, fmt.Sprintf("Interview prep: %d questions generated.", len(result.InterviewPrep.Questions)))
	}
	if result.CompanyInfo != nil {
		parts = append(parts, fmt.Sprintf("Company research: %s.", result.CompanyInfo.Name))
	}
	result.Summary = strings.Join(parts, " ")

	return &result, nil
}
