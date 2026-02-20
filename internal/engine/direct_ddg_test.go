package engine

import "testing"

func TestExtractVQD(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "single quotes",
			body: `some html vqd='4-123456789_abc' more html`,
			want: "4-123456789_abc",
		},
		{
			name: "double quotes",
			body: `vqd="4-987654321_xyz"`,
			want: "4-987654321_xyz",
		},
		{
			name: "no quotes",
			body: `nrj('/d.js?q=test&vqd=4-abcdef123&kl=wt-wt')`,
			want: "4-abcdef123",
		},
		{
			name: "not found",
			body: `<html>no token here</html>`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractVQD(tt.body)
			if got != tt.want {
				t.Errorf("extractVQD() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseDDGResponse(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		wantCount int
		wantErr   bool
	}{
		{
			name: "valid json array",
			data: `[
				{"t":"Go TLS Client","a":"A library for TLS fingerprinting","u":"https://example.com/tls","c":"https://example.com/tls"},
				{"t":"Another Result","a":"Description here","u":"https://example.org/result","c":""}
			]`,
			wantCount: 2,
		},
		{
			name: "skip ddg internal links",
			data: `[
				{"t":"Real Result","a":"Content","u":"https://example.com/real","c":""},
				{"t":"DDG Ad","a":"Ad content","u":"https://duckduckgo.com/y.js?ad_provider","c":""}
			]`,
			wantCount: 1,
		},
		{
			name: "skip empty title/url",
			data: `[
				{"t":"","a":"No title","u":"https://example.com","c":""},
				{"t":"Valid","a":"Good","u":"","c":""},
				{"t":"Real","a":"Yes","u":"https://example.com/ok","c":""}
			]`,
			wantCount: 1,
		},
		{
			name:      "invalid json",
			data:      `not json at all`,
			wantCount: 0,
			wantErr:   true,
		},
		{
			name: "html in title stripped",
			data: `[{"t":"<b>Bold</b> Title","a":"<b>snippet</b>","u":"https://example.com","c":""}]`,
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := parseDDGResponse([]byte(tt.data))
			if (err != nil) != tt.wantErr {
				t.Errorf("parseDDGResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if len(results) != tt.wantCount {
				t.Errorf("parseDDGResponse() returned %d results, want %d", len(results), tt.wantCount)
			}
			// Verify HTML stripping
			if tt.name == "html in title stripped" && len(results) > 0 {
				if results[0].Title != "Bold Title" {
					t.Errorf("HTML not stripped from title: %q", results[0].Title)
				}
				if results[0].Content != "snippet" {
					t.Errorf("HTML not stripped from content: %q", results[0].Content)
				}
			}
		})
	}
}

func TestParseDDGHTML(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		wantCount int
	}{
		{
			name: "standard results",
			html: `<html><body>
				<div class="result">
					<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage1&rut=abc">First Result</a>
					<a class="result__snippet">First description.</a>
				</div>
				<div class="result">
					<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Fpage2&rut=def">Second Result</a>
					<a class="result__snippet">Second description.</a>
				</div>
			</body></html>`,
			wantCount: 2,
		},
		{
			name: "direct urls",
			html: `<html><body>
				<div class="result">
					<a class="result__a" href="https://example.com/direct">Direct URL</a>
					<a class="result__snippet">Content.</a>
				</div>
			</body></html>`,
			wantCount: 1,
		},
		{
			name:      "no results",
			html:      `<html><body><p>No results</p></body></html>`,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := parseDDGHTML([]byte(tt.html))
			if err != nil {
				t.Fatalf("parseDDGHTML() error = %v", err)
			}
			if len(results) != tt.wantCount {
				t.Errorf("parseDDGHTML() returned %d results, want %d", len(results), tt.wantCount)
			}
		})
	}
}

func TestDDGUnwrapURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com&rut=abc", "https://example.com"},
		{"https://example.com/direct", "https://example.com/direct"},
		{"", ""},
		{"/relative/path", ""},
	}

	for _, tt := range tests {
		got := ddgUnwrapURL(tt.input)
		if got != tt.want {
			t.Errorf("ddgUnwrapURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCleanHTML(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<b>bold</b> text", "bold text"},
		{"plain text", "plain text"},
		{`<a href="url">link</a>`, "link"},
		{"", ""},
	}

	for _, tt := range tests {
		got := CleanHTML(tt.input)
		if got != tt.want {
			t.Errorf("CleanHTML(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
