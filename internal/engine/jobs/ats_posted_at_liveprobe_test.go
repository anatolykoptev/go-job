package jobs

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// TestLiveProbe_ATSPostedAt is an env-gated EMPIRICAL probe (NOT a CI test). It
// hits the real public ATS board APIs for known slugs, runs the exact production
// fetch → setPostedAtMeta → SearxngResultToHuntJob path, and reports how many
// rows landed with a non-nil PostedAt. Run with:
//
//	PROBE_LIVE_ATS=1 GOWORK=off go test ./internal/engine/jobs/ -run TestLiveProbe_ATSPostedAt -v -count=1
//
// Skipped by default so it never makes CI depend on third-party reachability.
func TestLiveProbe_ATSPostedAt(t *testing.T) {
	if os.Getenv("PROBE_LIVE_ATS") != "1" {
		t.Skip("set PROBE_LIVE_ATS=1 to run the live ATS probe")
	}

	orig := engine.Cfg.HTTPClient
	engine.Cfg.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	t.Cleanup(func() { engine.Cfg.HTTPClient = orig })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	type probe struct {
		platform string
		slug     string
		// build mirrors the production literal: fetch raw jobs, set posted_at meta
		// from the platform's API date field, return the SearxngResults.
		build func() ([]engine.SearxngResult, error)
	}

	greenhouseProbe := func(slug string) func() ([]engine.SearxngResult, error) {
		return func() ([]engine.SearxngResult, error) {
			jobs, err := fetchGreenhouseJobs(ctx, slug)
			if err != nil {
				return nil, err
			}
			out := make([]engine.SearxngResult, 0, len(jobs))
			for _, j := range jobs {
				sr := engine.SearxngResult{Title: j.Title, URL: j.AbsoluteURL, Score: 0.9}
				setPostedAtMeta(&sr, j.UpdatedAt)
				out = append(out, sr)
			}
			return out, nil
		}
	}
	ashbyProbe := func(slug string) func() ([]engine.SearxngResult, error) {
		return func() ([]engine.SearxngResult, error) {
			jobs, err := fetchAshbyJobs(ctx, slug)
			if err != nil {
				return nil, err
			}
			out := make([]engine.SearxngResult, 0, len(jobs))
			for _, j := range jobs {
				sr := engine.SearxngResult{Title: j.Title, URL: j.JobURL, Score: 0.9}
				setPostedAtMeta(&sr, j.PublishedAt)
				out = append(out, sr)
			}
			return out, nil
		}
	}
	leverProbe := func(slug string) func() ([]engine.SearxngResult, error) {
		return func() ([]engine.SearxngResult, error) {
			postings, err := fetchLeverPostings(ctx, slug)
			if err != nil {
				return nil, err
			}
			out := make([]engine.SearxngResult, 0, len(postings))
			for _, p := range postings {
				sr := engine.SearxngResult{Title: p.Text, URL: p.HostedURL, Score: 0.9}
				setPostedAtMeta(&sr, leverCreatedAtToISO(p.CreatedAt))
				out = append(out, sr)
			}
			return out, nil
		}
	}

	probes := []probe{
		{engine.DiscoveryPlatformGreenhouse, "stripe", greenhouseProbe("stripe")},
		{engine.DiscoveryPlatformAshby, "ramp", ashbyProbe("ramp")},
		{engine.DiscoveryPlatformLever, "leverdemo", leverProbe("leverdemo")},
	}

	for _, pr := range probes {
		results, err := pr.build()
		if err != nil {
			t.Logf("[%s/%s] fetch error (skip — third-party reachability): %v", pr.platform, pr.slug, err)
			continue
		}
		var withDate, total int
		var sample string
		for _, r := range results {
			j := SearxngResultToHuntJob(r, pr.platform)
			total++
			if j.PostedAt != nil {
				withDate++
				if sample == "" {
					sample = j.PostedAt.UTC().Format(time.RFC3339)
				}
			}
		}
		t.Logf("[%s/%s] rows=%d posted_at_present=%d sample=%q", pr.platform, pr.slug, total, withDate, sample)
		if total > 0 && withDate == 0 {
			t.Errorf("[%s/%s] %d rows but ZERO posted_at — date threading broken for this platform", pr.platform, pr.slug, total)
		}
	}
}
