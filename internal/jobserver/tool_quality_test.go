package jobserver

import (
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/quality"
)

// TestExtractSourceForQuality_HNClassifiesAsHN pins the behaviour change from
// the SourceFromURL unification: an HN URL (news.ycombinator.com) used to match
// the broad "ycombinator" arm → "yc" → 15 source-quality points; it now hits
// the news.ycombinator.com arm first → "hn" → 5 points. This aligns
// tool_job_search with JobListingToHunt (which already returned "hn") but
// silently moves every HN job's job_quality_score by −10, so the new value is
// pinned here to surface it as an intentional change rather than a mystery
// regression later.
func TestExtractSourceForQuality_HNClassifiesAsHN(t *testing.T) {
	got := extractSourceForQuality("https://news.ycombinator.com/item?id=12345")
	if got != "hn" {
		t.Errorf("extractSourceForQuality(hn url) = %q, want \"hn\" (was \"yc\" before the SourceFromURL unification)", got)
	}
	// Pin the source-quality point delta: "hn" → 5 (default band), "yc" → 15
	// (directATS). A non-HN ycombinator URL stays "yc" → 15.
	hnRes := quality.Score(quality.Input{
		Title:       "Eng",
		Company:     "co",
		URL:         "https://news.ycombinator.com/item?id=12345",
		Description: "a decent length description with substance to avoid skip band",
		Source:      "hn",
		PostedAt:    ptrTimeNow(),
	})
	if hnRes.Breakdown.SourceQuality != 5 {
		t.Errorf("hn SourceQuality = %d, want 5 (default band; was 15 when HN classified as yc)", hnRes.Breakdown.SourceQuality)
	}
	ycRes := quality.Score(quality.Input{
		Title:       "Eng",
		Company:     "co",
		URL:         "https://www.ycombinator.com/jobs/12345",
		Description: "a decent length description with substance to avoid skip band",
		Source:      "yc",
		PostedAt:    ptrTimeNow(),
	})
	if ycRes.Breakdown.SourceQuality != 15 {
		t.Errorf("yc SourceQuality = %d, want 15 (directATS band, unchanged)", ycRes.Breakdown.SourceQuality)
	}
}

func ptrTimeNow() *time.Time {
	now := time.Now()
	return &now
}
