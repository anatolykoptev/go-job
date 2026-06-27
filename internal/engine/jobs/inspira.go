package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// Inspira is the UN Secretariat Careers Portal (careers.un.org), an Angular SPA
// backed by a JSON API at /api/public/opening/jo/list/filteredV2/en.
//
// Coverage: UN Secretariat HQ, Regional Commissions (ECA/ECE/ESCAP/ESCWA/ECLAC),
// UNCTAD, UNEP, UN-Habitat, OCHA, OHCHR, UNODC, UNRWA, and most peace operations.
// NOT covered: UNDP, UNICEF, UNHCR, UNOPS, ITU, WFP, FAO, ILO, WHO, UNESCO — those
// run their own portals.

const inspiraAPI = "https://careers.un.org/api/public/opening/jo/list/filteredV2/en"
const inspiraJobURLFormat = "https://careers.un.org/jobSearchDescription/%d"

type inspiraFilterConfig struct {
	Keyword string   `json:"keyword,omitempty"`
	AOE     []string `json:"aoe"`
	AOI     []string `json:"aoi"`
	EL      []string `json:"el"`
	CT      []string `json:"ct"`
	DS      []string `json:"ds"`
	JN      []string `json:"jn"`
	JF      []string `json:"jf"`
	JC      []string `json:"jc"`
	JLE     []string `json:"jle"`
	Dept    []string `json:"dept"`
	Span    []string `json:"span"`
}

type inspiraPagination struct {
	Page          int    `json:"page"`
	ItemPerPage   int    `json:"itemPerPage"`
	SortBy        string `json:"sortBy"`
	SortDirection int    `json:"sortDirection"`
}

type inspiraRequest struct {
	FilterConfig inspiraFilterConfig `json:"filterConfig"`
	Pagination   inspiraPagination   `json:"pagination"`
}

type inspiraCodedName struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type inspiraDutyStation struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

type inspiraJob struct {
	JobID          int64                `json:"jobId"`
	JobTitle       string               `json:"jobTitle"`
	PostingTitle   string               `json:"postingTitle"`
	JobDescription string               `json:"jobDescription"`
	DutyStation    []inspiraDutyStation `json:"dutyStation"`
	StartDate      string               `json:"startDate"`
	EndDate        string               `json:"endDate"`
	JC             inspiraCodedName     `json:"jc"`   // job category
	JL             inspiraCodedName     `json:"jl"`   // job level
	Dept           inspiraCodedName     `json:"dept"` // department / UN agency
	JobLevel       string               `json:"jobLevel"`
	TotalCount     int                  `json:"totalCount"`
}

type inspiraResponse struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Data    struct {
		List []inspiraJob `json:"list"`
	} `json:"data"`
}

// SearchInspiraJobs queries the careers.un.org public API and returns up to limit
// matching openings as engine.SearxngResult. keyword is matched against title and
// description by Inspira's MongoDB text index; location is ignored in v1 because
// Inspira uses opaque duty-station codes rather than free text.
func SearchInspiraJobs(ctx context.Context, query, _location string, limit int) ([]engine.SearxngResult, error) {
	if limit <= 0 {
		limit = 15
	}
	if limit > 50 {
		limit = 50
	}

	body := inspiraRequest{
		FilterConfig: inspiraFilterConfig{
			Keyword: strings.TrimSpace(query),
			AOE:     []string{},
			AOI:     []string{},
			EL:      []string{},
			CT:      []string{},
			DS:      []string{},
			JN:      []string{},
			JF:      []string{},
			JC:      []string{},
			JLE:     []string{},
			Dept:    []string{},
			Span:    []string{},
		},
		Pagination: inspiraPagination{
			Page:          0,
			ItemPerPage:   limit,
			SortBy:        "startDate",
			SortDirection: -1,
		},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("inspira marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inspiraAPI, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", engine.UserAgentBot)

	resp, err := engine.RetryHTTP(ctx, engine.DefaultRetryConfig, func() (*http.Response, error) {
		return engine.Cfg.HTTPClient.Do(req) //nolint:gosec // careers.un.org public API
	})
	if err != nil {
		return nil, fmt.Errorf("inspira request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("inspira API status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}

	var ir inspiraResponse
	if err := json.Unmarshal(raw, &ir); err != nil {
		return nil, fmt.Errorf("inspira parse: %w", err)
	}
	if ir.Status != 1 {
		return nil, fmt.Errorf("inspira API error: %s", ir.Message)
	}

	out := make([]engine.SearxngResult, 0, len(ir.Data.List))
	for _, j := range ir.Data.List {
		out = append(out, inspiraJobToResult(j))
	}
	slog.Debug("inspira: search complete", slog.Int("results", len(out)))
	return out, nil
}

func inspiraJobToResult(j inspiraJob) engine.SearxngResult {
	title := j.PostingTitle
	if title == "" {
		title = j.JobTitle
	}

	var locParts []string
	for _, ds := range j.DutyStation {
		if ds.Description != "" {
			locParts = append(locParts, toTitleCase(ds.Description))
		}
	}
	location := strings.Join(locParts, ", ")

	var contentB strings.Builder
	if j.Dept.Name != "" {
		fmt.Fprintf(&contentB, "**Org:** %s", j.Dept.Name)
	}
	if location != "" {
		fmt.Fprintf(&contentB, " | **Duty Station:** %s", location)
	}
	if j.JC.Name != "" {
		fmt.Fprintf(&contentB, " | **Category:** %s", j.JC.Name)
	}
	if j.JL.Name != "" && j.JL.Name != j.JC.Name {
		fmt.Fprintf(&contentB, " | **Level:** %s", j.JL.Name)
	}
	if j.StartDate != "" && len(j.StartDate) >= 10 {
		fmt.Fprintf(&contentB, " | **Posted:** %s", j.StartDate[:10])
	}
	if j.EndDate != "" && len(j.EndDate) >= 10 {
		fmt.Fprintf(&contentB, " | **Closes:** %s", j.EndDate[:10])
	}
	if j.JobDescription != "" {
		desc := engine.TruncateRunes(engine.CleanHTML(j.JobDescription), 600, "...")
		contentB.WriteString("\n\n")
		contentB.WriteString(desc)
	}

	return engine.SearxngResult{
		Title:   title,
		Content: contentB.String(),
		URL:     fmt.Sprintf(inspiraJobURLFormat, j.JobID),
		Score:   0.9,
		Metadata: map[string]string{
			memdbKeySource:   sourceInspira,
			"org":      j.Dept.Name,
			"category": j.JC.Name,
			"level":    j.JL.Name,
			"location": location, //nolint:goconst
			"posted":   j.StartDate,
			"closes":   j.EndDate,
		},
	}
}

// toTitleCase converts SHOUTY CASE place names to Title Case. UTF-8 aware
// because the UN duty-station catalog contains accented and non-Latin names
// (Côte d'Ivoire, Ñuble, N'Djamena, etc.).
func toTitleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		if w == "" {
			continue
		}
		r, size := utf8.DecodeRuneInString(w)
		if r == utf8.RuneError {
			continue
		}
		words[i] = string(unicode.ToUpper(r)) + w[size:]
	}
	return strings.Join(words, " ")
}
