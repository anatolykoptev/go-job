package engine

import "testing"

func TestGithubRawURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "blob to raw",
			url:  "https://github.com/anthropics/claude-code/blob/main/README.md",
			want: "https://raw.githubusercontent.com/anthropics/claude-code/main/README.md",
		},
		{
			name: "nested path",
			url:  "https://github.com/owner/repo/blob/v2/src/lib/utils.ts",
			want: "https://raw.githubusercontent.com/owner/repo/v2/src/lib/utils.ts",
		},
		{
			name: "non-github passthrough",
			url:  "https://stackoverflow.com/questions/12345",
			want: "https://stackoverflow.com/questions/12345",
		},
		{
			name: "github non-blob passthrough",
			url:  "https://github.com/owner/repo/issues/1",
			want: "https://github.com/owner/repo/issues/1",
		},
		{
			name: "empty string",
			url:  "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GithubRawURL(tt.url)
			if got != tt.want {
				t.Errorf("GithubRawURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}
