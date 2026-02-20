package engine

import "testing"

func TestFilterByScore(t *testing.T) {
	results := []SearxngResult{
		{Title: "a", Score: 10.0},
		{Title: "b", Score: 5.0},
		{Title: "c", Score: 1.0},
		{Title: "d", Score: 0.5},
		{Title: "e", Score: 0.1},
	}

	t.Run("filters below threshold", func(t *testing.T) {
		got := FilterByScore(results, 3.0, 1)
		if len(got) != 2 {
			t.Errorf("expected 2 results, got %d", len(got))
		}
	})

	t.Run("respects minKeep", func(t *testing.T) {
		got := FilterByScore(results, 100.0, 3)
		if len(got) != 3 {
			t.Errorf("expected 3 results (minKeep), got %d", len(got))
		}
	})

	t.Run("returns all when fewer than minKeep", func(t *testing.T) {
		small := results[:2]
		got := FilterByScore(small, 100.0, 5)
		if len(got) != 2 {
			t.Errorf("expected 2 results (all available), got %d", len(got))
		}
	})

	t.Run("no filter when threshold is 0", func(t *testing.T) {
		got := FilterByScore(results, 0, 1)
		if len(got) != 5 {
			t.Errorf("expected all 5 results, got %d", len(got))
		}
	})
}

func TestDedupByDomain(t *testing.T) {
	results := []SearxngResult{
		{Title: "a1", URL: "https://example.com/1"},
		{Title: "a2", URL: "https://example.com/2"},
		{Title: "a3", URL: "https://example.com/3"},
		{Title: "b1", URL: "https://other.com/1"},
		{Title: "b2", URL: "https://other.com/2"},
	}

	t.Run("limits per domain", func(t *testing.T) {
		got := DedupByDomain(results, 2)
		if len(got) != 4 {
			t.Errorf("expected 4 results, got %d", len(got))
		}
		// First two from example.com, first two from other.com
		exampleCount := 0
		for _, r := range got {
			if r.Title[:1] == "a" {
				exampleCount++
			}
		}
		if exampleCount != 2 {
			t.Errorf("expected 2 from example.com, got %d", exampleCount)
		}
	})

	t.Run("max 1 per domain", func(t *testing.T) {
		got := DedupByDomain(results, 1)
		if len(got) != 2 {
			t.Errorf("expected 2 results, got %d", len(got))
		}
	})

	t.Run("skips invalid URLs", func(t *testing.T) {
		bad := []SearxngResult{{Title: "bad", URL: "://invalid"}}
		got := DedupByDomain(bad, 5)
		if len(got) != 0 {
			t.Errorf("expected 0 results for invalid URL, got %d", len(got))
		}
	})
}
