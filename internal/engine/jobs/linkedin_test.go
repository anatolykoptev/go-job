package jobs

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestExtractJobID(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "clean numeric URL",
			url:  "https://www.linkedin.com/jobs/view/4335742219",
			want: "4335742219",
		},
		{
			name: "slug URL",
			url:  "https://www.linkedin.com/jobs/view/golang-developer-at-ceipal-4335742219",
			want: "4335742219",
		},
		{
			name: "URL with query params",
			url:  "https://www.linkedin.com/jobs/view/4335742219?trk=jobs_biz",
			want: "4335742219",
		},
		{
			name: "invalid URL",
			url:  "https://www.linkedin.com/jobs/search/",
			want: "",
		},
		{
			name: "empty",
			url:  "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractJobID(tt.url)
			if got != tt.want {
				t.Errorf("ExtractJobID(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestParseLinkedInHTML(t *testing.T) {
	// Realistic LinkedIn Guest API HTML card
	html := `<ul>
<li>
<div class="base-card">
  <a class="base-card__full-link" href="https://www.linkedin.com/jobs/view/golang-developer-at-acme-4335742219?trk=test">
    <span class="sr-only">Golang Developer</span>
  </a>
  <div class="base-search-card__info">
    <h3 class="base-search-card__title">Golang Developer</h3>
    <h4 class="base-search-card__subtitle">
      Acme Corp
    </h4>
    <div class="job-search-card__location">San Francisco, CA</div>
    <time class="job-search-card__listdate" datetime="2026-01-23">2 weeks ago</time>
  </div>
</div>
</li>
<li>
<div class="base-card">
  <a class="base-card__full-link" href="https://www.linkedin.com/jobs/view/senior-go-engineer-9876543210">
    <span class="sr-only">Senior Go Engineer</span>
  </a>
  <div class="base-search-card__info">
    <h3 class="base-search-card__title">Senior Go Engineer</h3>
    <h4 class="base-search-card__subtitle">
      BigTech Inc
    </h4>
    <div class="job-search-card__location">Remote</div>
  </div>
</div>
</li>
</ul>`

	jobs := parseLinkedInHTML(html)

	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	// First job
	j := jobs[0]
	if j.Title != "Golang Developer" {
		t.Errorf("job[0].Title = %q, want %q", j.Title, "Golang Developer")
	}
	if j.Company != "Acme Corp" {
		t.Errorf("job[0].Company = %q, want %q", j.Company, "Acme Corp")
	}
	if j.Location != "San Francisco, CA" {
		t.Errorf("job[0].Location = %q, want %q", j.Location, "San Francisco, CA")
	}
	if j.JobID != "4335742219" {
		t.Errorf("job[0].JobID = %q, want %q", j.JobID, "4335742219")
	}
	if j.Posted != "2026-01-23" {
		t.Errorf("job[0].Posted = %q, want %q", j.Posted, "2026-01-23")
	}

	// Second job
	j2 := jobs[1]
	if j2.Title != "Senior Go Engineer" {
		t.Errorf("job[1].Title = %q, want %q", j2.Title, "Senior Go Engineer")
	}
	if j2.Location != "Remote" {
		t.Errorf("job[1].Location = %q, want %q", j2.Location, "Remote")
	}
}

func TestGetAttr(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<a href="https://example.com" class="link">text</a>`))
	// Find the <a> element
	a := findElements(doc, "a")[0]

	if got := getAttr(a, "href"); got != "https://example.com" {
		t.Errorf("getAttr(href) = %q, want %q", got, "https://example.com")
	}
	if got := getAttr(a, "class"); got != "link" {
		t.Errorf("getAttr(class) = %q, want %q", got, "link")
	}
	if got := getAttr(a, "missing"); got != "" {
		t.Errorf("getAttr(missing) = %q, want empty", got)
	}
}

func TestTextContent(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div><span>Hello</span> <b>World</b></div>`))
	div := findElements(doc, "div")[0]
	got := strings.TrimSpace(textContent(div))
	if got != "Hello World" {
		t.Errorf("textContent = %q, want %q", got, "Hello World")
	}
}

func TestFindByClass(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<div><span class="target">Found</span><span class="other">Not</span></div>`))
	n := findByClass(doc, "target")
	if n == nil {
		t.Fatal("expected to find element with class 'target'")
	}
	if got := strings.TrimSpace(textContent(n)); got != "Found" {
		t.Errorf("found element text = %q, want %q", got, "Found")
	}

	if n := findByClass(doc, "nonexistent"); n != nil {
		t.Error("expected nil for nonexistent class")
	}
}

func TestParseLinkedInHTMLWithNestedCompanyLink(t *testing.T) {
	// Test that the new parser correctly handles <a> tags inside subtitle
	// (this was a known issue with the old string-based parser)
	htmlStr := `<ul><li>
<div class="base-card">
  <a class="base-card__full-link" href="https://www.linkedin.com/jobs/view/4335742219">
    <span class="sr-only">Test Job</span>
  </a>
  <h3 class="base-search-card__title">Test Job</h3>
  <h4 class="base-search-card__subtitle"><a href="/company/acme">Acme Corp</a></h4>
  <div class="job-search-card__location">NYC</div>
</div>
</li></ul>`

	jobs := parseLinkedInHTML(htmlStr)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Company != "Acme Corp" {
		t.Errorf("Company = %q, want %q (should strip inner <a> tag)", jobs[0].Company, "Acme Corp")
	}
}

func TestExtractJSONLD(t *testing.T) {
	t.Run("valid JobPosting", func(t *testing.T) {
		html := `<html><head>
<script type="application/ld+json">{"@type":"JobPosting","title":"Go Developer","description":"<p>Build APIs</p>","hiringOrganization":{"name":"Acme"},"employmentType":"FULL_TIME"}</script>
</head></html>`

		got := extractJSONLD(html)
		if got == "" {
			t.Fatal("expected non-empty result")
		}
		if !containsStr(got, "Go Developer") {
			t.Error("missing title")
		}
		if !containsStr(got, "Acme") {
			t.Error("missing company")
		}
		if !containsStr(got, "FULL_TIME") {
			t.Error("missing employment type")
		}
	})

	t.Run("no JobPosting", func(t *testing.T) {
		html := `<html><body><p>no json-ld here</p></body></html>`
		got := extractJSONLD(html)
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})
}

func containsStr(s, sub string) bool {
return strings.Contains(s, sub)
}
