package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// Fixture mirrors the real Oracle CX recruitingCEJobRequisitions response.
const undpFixture = `{
  "items": [
    {
      "SearchId": 1,
      "Keyword": "engineer",
      "TotalJobsCount": 125,
      "requisitionList": {
        "items": [
          {
            "Id": "34365",
            "Title": "Engineering Coordination Associate (NPSA-7)",
            "PostedDate": "2026-05-28",
            "PostingEndDate": "2026-06-15",
            "Language": "US",
            "PrimaryLocation": "Belgrade, Serbia",
            "PrimaryLocationCountry": "RS",
            "WorkplaceTypeCode": "ONSITE",
            "JobFunction": "Project Management",
            "ContractType": "Service Contract",
            "Organization": "UNDP",
            "ShortDescriptionStr": "Lead engineering coordination across the country office programme portfolio."
          },
          {
            "Id": "33418",
            "Title": "Project M&E and Implementation Analyst",
            "PostedDate": "2026-05-28",
            "PostingEndDate": null,
            "PrimaryLocationCountry": "PK",
            "Organization": null,
            "BusinessUnit": "UNDP Pakistan"
          }
        ],
        "count": 2
      }
    }
  ]
}`

func TestSearchUNDPJobs_Fixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		// Cannot use r.URL.Query() — Go ≥1.17 ParseQuery aborts on `;`, and the
		// finder= value intentionally contains literal `;` because Oracle CX
		// expects it. Parse the raw query string by `&` only.
		rawQ := r.URL.RawQuery
		var finder string
		for _, part := range strings.Split(rawQ, "&") {
			if v, ok := strings.CutPrefix(part, "finder="); ok {
				finder = v
				break
			}
		}
		if !strings.HasPrefix(finder, "findReqs;siteNumber=CX_1") {
			t.Errorf("finder must start with literal `findReqs;siteNumber=CX_1`, got %q", finder)
		}
		if !strings.Contains(finder, "keyword=engineer") {
			t.Errorf("finder missing keyword=engineer: %q", finder)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(undpFixture))
	}))
	t.Cleanup(srv.Close)

	origClient := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{
		Timeout:   10 * time.Second,
		Transport: &redirectTransport{target: srv.URL},
	}
	t.Cleanup(func() { engine.Cfg.HTTPClient = origClient })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := SearchUNDPJobs(ctx, "engineer", "", 10)
	if err != nil {
		t.Fatalf("SearchUNDPJobs: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(out))
	}

	j1 := out[0]
	if !strings.Contains(j1.Title, "Engineering Coordination Associate") {
		t.Errorf("[0] title: %q", j1.Title)
	}
	if !strings.HasSuffix(j1.URL, "/job/34365") {
		t.Errorf("[0] URL: %q", j1.URL)
	}
	if !strings.Contains(j1.Content, "Belgrade, Serbia") {
		t.Errorf("[0] location missing: %q", j1.Content)
	}
	if j1.Metadata["source"] != "undp" {
		t.Errorf("[0] metadata source: %q", j1.Metadata["source"])
	}

	// Job 2 has no Organization but BusinessUnit — should fall through.
	j2 := out[1]
	if !strings.Contains(j2.Content, "UNDP Pakistan") {
		t.Errorf("[1] BusinessUnit fallback missing: %q", j2.Content)
	}
}
