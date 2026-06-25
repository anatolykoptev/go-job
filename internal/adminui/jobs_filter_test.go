package adminui

import (
	"net/url"
	"strings"
	"testing"
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
