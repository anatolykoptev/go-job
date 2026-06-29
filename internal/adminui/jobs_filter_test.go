package adminui

import (
	"net/url"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/hunt"
)

// TestJobsFilter_NoInjection asserts request values (including SQL-injection
// payloads) reach the WHERE clause ONLY as bind args, never concatenated into the
// SQL string, and that a malicious sort key/dir cannot inject into ORDER BY.
// Regression gate for the gosec-G201 blind spot (gosec does not inspect pgx string
// args) — load-bearing now that /admin/jobs has a filter bar.
func TestJobsFilter_NoInjection(t *testing.T) {
	vals := url.Values{
		"q":      {"x'; DROP TABLE hunt_jobs;--"},
		"status": {"open' OR '1'='1"}, // not in Allowed -> must be dropped
		"source": {"lever"},           // valid -> must filter
	}
	conds, args := jobsFilter.Where(vals, 1)

	for _, bad := range []string{"DROP TABLE", "OR '1'='1", "--", "x'"} {
		if strings.Contains(conds, bad) {
			t.Fatalf("injection payload leaked into WHERE %q (found %q)", conds, bad)
		}
	}
	if conds != "" && !strings.Contains(conds, "$") {
		t.Fatalf("non-empty conds without a bind placeholder is suspicious: %q", conds)
	}
	// q (ILike, 1 arg) + source (Eq, 1 arg); status not in Allowed -> dropped.
	if len(args) != 2 {
		t.Fatalf("want 2 bind args (q + source), got %d: %v", len(args), args)
	}

	// ORDER BY cannot be injected: non-spec sort key/dir falls back to the default.
	ob := jobsSpec.OrderBy(jobsSpec.Resolve("title'; DROP TABLE x;--", "asc; DROP"))
	for _, bad := range []string{"DROP", ";", "'"} {
		if strings.Contains(ob, bad) {
			t.Fatalf("injection leaked into ORDER BY %q (found %q)", ob, bad)
		}
	}
}

// TestStatusFilterAllowed_SubsetOfHuntAllStatuses verifies that every value in
// jobStatusFilterAllowed is a member of hunt.AllStatuses (P4 SSOT enforcement).
// Red-on-revert: adding a pipeline-stage value (applied/rejected/offer/interviewing)
// to jobStatusFilterAllowed will cause this test to fail.
func TestStatusFilterAllowed_SubsetOfHuntAllStatuses(t *testing.T) {
	canonical := make(map[string]bool, len(hunt.AllStatuses))
	for _, s := range hunt.AllStatuses {
		canonical[s] = true
	}
	for _, s := range jobStatusFilterAllowed {
		if !canonical[s] {
			t.Errorf("jobStatusFilterAllowed: %q is not in hunt.AllStatuses — remove cross-plane value", s)
		}
	}
}

// TestStatusFilter_OpenPasses verifies the status=open filter passes and produces a
// condition referencing j.status.
func TestStatusFilter_OpenPasses(t *testing.T) {
	vals := url.Values{"status": {"open"}}
	conds, args := jobsFilter.Where(vals, 1)
	if conds == "" {
		t.Error("jobsFilter: status=open should produce a WHERE condition, got empty")
	}
	if len(args) == 0 {
		t.Error("jobsFilter: status=open should produce at least one bind arg")
	}
	if !strings.Contains(conds, "j.status") {
		t.Errorf("jobsFilter: status filter conds must reference j.status, got: %s", conds)
	}
}

// TestStatusFilter_PipelineValueDropped verifies that former cross-plane values
// (applied/interviewing/rejected/offer) are silently dropped by the filter
// because they are not in hunt.AllStatuses and therefore not in jobStatusFilterAllowed.
func TestStatusFilter_PipelineValueDropped(t *testing.T) {
	for _, bad := range []string{"applied", "interviewing", "rejected", "offer"} {
		vals := url.Values{"status": {bad}}
		conds, args := jobsFilter.Where(vals, 1)
		if strings.Contains(conds, "j.status") {
			t.Errorf("jobsFilter: pipeline-stage value %q must be dropped from status filter, but conds=%q still references j.status", bad, conds)
		}
		if len(args) != 0 {
			t.Errorf("jobsFilter: pipeline-stage value %q must produce zero bind args, got %d", bad, len(args))
		}
	}
}
