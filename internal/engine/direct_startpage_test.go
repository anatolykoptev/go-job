package engine

import "testing"

func TestParseStartpageHTML(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		wantCount int
	}{
		{
			name: "standard results",
			html: `<html><body>
				<div class="w-gl__result">
					<a class="w-gl__result-title" href="https://example.com/1">First Result</a>
					<p class="w-gl__description">First description text.</p>
				</div>
				<div class="w-gl__result">
					<a class="w-gl__result-title" href="https://example.com/2">Second Result</a>
					<p class="w-gl__description">Second description text.</p>
				</div>
			</body></html>`,
			wantCount: 2,
		},
		{
			name: "fallback selectors",
			html: `<html><body>
				<div class="result">
					<h3><a href="https://example.com/3">Third Result</a></h3>
					<p class="result-description">Third description.</p>
				</div>
			</body></html>`,
			wantCount: 1,
		},
		{
			name: "skip empty href",
			html: `<html><body>
				<div class="w-gl__result">
					<a class="w-gl__result-title" href="">No URL</a>
					<p class="w-gl__description">Missing URL.</p>
				</div>
			</body></html>`,
			wantCount: 0,
		},
		{
			name: "skip startpage internal links",
			html: `<html><body>
				<div class="w-gl__result">
					<a class="w-gl__result-title" href="https://www.startpage.com/do/something">Internal</a>
					<p class="w-gl__description">Internal link.</p>
				</div>
			</body></html>`,
			wantCount: 0,
		},
		{
			name:      "no results",
			html:      `<html><body><p>No results found</p></body></html>`,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := parseStartpageHTML([]byte(tt.html))
			if err != nil {
				t.Fatalf("parseStartpageHTML() error = %v", err)
			}
			if len(results) != tt.wantCount {
				t.Errorf("parseStartpageHTML() returned %d results, want %d", len(results), tt.wantCount)
			}
			// Verify score is 1.0 for all direct results
			for i, r := range results {
				if r.Score != 1.0 {
					t.Errorf("result[%d].Score = %f, want 1.0", i, r.Score)
				}
			}
		})
	}
}

func TestURLEncode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello world", "hello+world"},
		{"a&b=c", "a%26b%3Dc"},
		{"no+change", "no%2Bchange"},
		{"plain", "plain"},
	}

	for _, tt := range tests {
		got := urlEncode(tt.input)
		if got != tt.want {
			t.Errorf("urlEncode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
