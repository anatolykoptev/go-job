package sources

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHNTimeFilter(t *testing.T) {
	tests := []struct {
		name      string
		timeRange string
		wantEmpty bool
		maxAge    time.Duration
	}{
		{"day filter", "day", false, 25 * time.Hour},
		{"week filter", "week", false, 8 * 24 * time.Hour},
		{"month filter", "month", false, 31 * 24 * time.Hour},
		{"year filter", "year", false, 366 * 24 * time.Hour},
		{"empty string", "", true, 0},
		{"invalid", "century", true, 0},
		{"case insensitive", "Month", false, 31 * 24 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hnTimeFilter(tt.timeRange)
			if tt.wantEmpty {
				if result != "" {
					t.Errorf("hnTimeFilter(%q) = %q, want empty", tt.timeRange, result)
				}
				return
			}
			if result == "" {
				t.Fatalf("hnTimeFilter(%q) returned empty", tt.timeRange)
			}
			// Should be "created_at_i>TIMESTAMP"
			if !strings.HasPrefix(result, "created_at_i>") {
				t.Errorf("hnTimeFilter(%q) = %q, missing prefix", tt.timeRange, result)
			}
			tsStr := strings.TrimPrefix(result, "created_at_i>")
			ts, err := strconv.ParseInt(tsStr, 10, 64)
			if err != nil {
				t.Fatalf("failed to parse timestamp: %v", err)
			}
			// Verify the timestamp is fresh (within expected range)
			age := time.Since(time.Unix(ts, 0))
			if age > tt.maxAge {
				t.Errorf("timestamp too old: age=%v, maxAge=%v", age, tt.maxAge)
			}
			// Verify it's not in the future
			if ts > time.Now().Unix() {
				t.Errorf("timestamp is in the future: %d", ts)
			}
		})
	}
}

func TestHNTimeFilterFreshness(t *testing.T) {
	// Verify that timestamps are computed at call time, not cached
	filter1 := hnTimeFilter("day")
	// Extract timestamp
	ts1Str := strings.TrimPrefix(filter1, "created_at_i>")
	ts1, _ := strconv.ParseInt(ts1Str, 10, 64)

	// The timestamp should be very close to now - 24h
	expected := time.Now().Add(-24 * time.Hour).Unix()
	diff := ts1 - expected
	if diff < -2 || diff > 2 {
		t.Errorf("timestamp drift: got %d, expected ~%d (diff=%d)", ts1, expected, diff)
	}
	fmt.Printf("hnTimeFilter freshness OK: drift=%ds\n", diff)
}
