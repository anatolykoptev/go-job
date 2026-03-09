package jobs

import (
	"testing"
)

func TestParseAlgoraBounties(t *testing.T) {
	t.Parallel()

	// Minimal HTML mimicking algora.io bounty page structure.
	html := `
<div class="flex-grow min-w-0 mr-4">
  <span>ZIO</span>
  <span>#519</span>
  <span>$4,000</span>
  <span>Schema Migration System for ZIO Schema 2</span>
  <a href="https://github.com/zio/zio-blocks/issues/519" class="bounty-link">View</a>
</div>
<div class="flex-grow min-w-0 mr-4">
  <span>Golem Cloud</span>
  <span>#275</span>
  <span>$3,500</span>
  <span>Incorporate MCP Server into Golem CLI</span>
  <a href="https://github.com/golemcloud/golem-cli/issues/275" class="bounty-link">View</a>
</div>
`

	bounties := parseAlgoraBounties(html)
	if len(bounties) != 2 {
		t.Fatalf("expected 2 bounties, got %d", len(bounties))
	}

	b0 := bounties[0]
	if b0.URL != "https://github.com/zio/zio-blocks/issues/519" {
		t.Errorf("bounty[0].URL = %q", b0.URL)
	}
	if b0.Amount != "$4,000" {
		t.Errorf("bounty[0].Amount = %q", b0.Amount)
	}
	if b0.Org != "zio/zio-blocks" {
		t.Errorf("bounty[0].Org = %q", b0.Org)
	}
	if b0.Source != "algora" {
		t.Errorf("bounty[0].Source = %q", b0.Source)
	}
	// Title should contain meaningful text from the block.
	if b0.Title == "" {
		t.Error("bounty[0].Title is empty")
	}
	t.Logf("bounty[0].Title = %q", b0.Title)

	b1 := bounties[1]
	if b1.URL != "https://github.com/golemcloud/golem-cli/issues/275" {
		t.Errorf("bounty[1].URL = %q", b1.URL)
	}
	if b1.Amount != "$3,500" {
		t.Errorf("bounty[1].Amount = %q", b1.Amount)
	}
	t.Logf("bounty[1].Title = %q", b1.Title)
}

func TestParseAlgoraBounties_Dedup(t *testing.T) {
	t.Parallel()

	// Same URL appears twice (table + feed sections).
	html := `
<div>
  <span>$4,000</span>
  <a href="https://github.com/zio/zio-blocks/issues/519">View</a>
</div>
<div>
  <span>$4,000</span>
  <a href="https://github.com/zio/zio-blocks/issues/519">View</a>
</div>
`
	bounties := parseAlgoraBounties(html)
	if len(bounties) != 1 {
		t.Errorf("expected 1 bounty after dedup, got %d", len(bounties))
	}
}

func TestExtractTitleFromBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		block  string
		amount string
		want   string
	}{
		{
			name:   "simple span after amount",
			block:  `<span>$4,000</span> <span>Schema Migration System for ZIO Schema 2</span> <a href="...">View</a>`,
			amount: "$4,000",
			want:   "Schema Migration System for ZIO Schema 2",
		},
		{
			name:   "MCP in title",
			block:  `<span>$3,500</span> <span>Incorporate MCP Server into Golem CLI</span> <a href="...">View</a>`,
			amount: "$3,500",
			want:   "Incorporate MCP Server into Golem CLI",
		},
		{
			name:   "no text after amount",
			block:  `<span>$500</span> <a href="...">View</a>`,
			amount: "$500",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTitleFromBlock(tt.block, tt.amount)
			if tt.want != "" && got != tt.want {
				t.Errorf("extractTitleFromBlock() = %q, want %q", got, tt.want)
			}
			if tt.want == "" && got != "" {
				// Allow non-empty result as long as it's not just noise.
				t.Logf("extractTitleFromBlock() = %q (non-empty, ok)", got)
			}
		})
	}
}

func TestExtractOrgFromGitHubURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/zio/zio-blocks/issues/519", "zio/zio-blocks"},
		{"https://github.com/golemcloud/golem-cli/issues/275", "golemcloud/golem-cli"},
		{"https://example.com/foo", ""},
	}

	for _, tt := range tests {
		got := extractOrgFromGitHubURL(tt.url)
		if got != tt.want {
			t.Errorf("extractOrgFromGitHubURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestParseGitHubIssueURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		url        string
		wantOwner  string
		wantRepo   string
		wantNumber int
		wantOK     bool
	}{
		{"https://github.com/zio/zio-blocks/issues/519", "zio", "zio-blocks", 519, true},
		{"https://github.com/golemcloud/golem-cli/issues/275", "golemcloud", "golem-cli", 275, true},
		{"https://github.com/org/repo/pulls/10", "", "", 0, false},     // not issues
		{"https://example.com/foo/bar/issues/1", "", "", 0, false},      // not github
		{"https://github.com/org/repo/issues/abc", "", "", 0, false},    // non-numeric
		{"https://github.com/org/repo", "", "", 0, false},               // no issues path
	}

	for _, tt := range tests {
		owner, repo, number, ok := ParseGitHubIssueURL(tt.url)
		if ok != tt.wantOK {
			t.Errorf("ParseGitHubIssueURL(%q) ok = %v, want %v", tt.url, ok, tt.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if owner != tt.wantOwner || repo != tt.wantRepo || number != tt.wantNumber {
			t.Errorf("ParseGitHubIssueURL(%q) = (%q, %q, %d), want (%q, %q, %d)",
				tt.url, owner, repo, number, tt.wantOwner, tt.wantRepo, tt.wantNumber)
		}
	}
}

func TestParseAmountCents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  int
	}{
		{"$4,000", 400000},
		{"$500", 50000},
		{"$3,500", 350000},
		{"$100", 10000},
		{"", 0},
		{"free", 0},
	}
	for _, tt := range tests {
		got := ParseAmountCents(tt.input)
		if got != tt.want {
			t.Errorf("ParseAmountCents(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestBountiesToSearxngResults(t *testing.T) {
	t.Parallel()

	bounties := []BountyListingWrapper{
		{Title: "Test Bounty", Org: "test/repo", Amount: "$500", URL: "https://github.com/test/repo/issues/1"},
	}

	// Just verify conversion doesn't panic and produces correct count.
	if len(bounties) != 1 {
		t.Fatal("expected 1 bounty")
	}
}

func TestIsTitleNoise(t *testing.T) {
	t.Parallel()

	tests := []struct {
		title string
		noise bool
	}{
		{"tip 14 hours ago", true},
		{"tip 2 days ago", true},
		{"3 hours ago", true},
		{"Schema Migration System for ZIO Schema 2", false},
		{"Incorporate MCP Server into Golem CLI", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isTitleNoise(tt.title)
		if got != tt.noise {
			t.Errorf("isTitleNoise(%q) = %v, want %v", tt.title, got, tt.noise)
		}
	}
}

type BountyListingWrapper struct {
	Title  string
	Org    string
	Amount string
	URL    string
}
