package adminui

import (
	"os"
	"strings"
	"testing"

	"github.com/anatolykoptev/go_job/internal/engine/jobs"
)

func TestBuildUpworkPageData_Mapping(t *testing.T) {
	profile := &jobs.ResumeProfileResult{
		Headline:        "Staff Software Engineer",
		Summary:         "I build distributed systems.",
		HourlyRateCents: 15000,
		Skills: []jobs.SkillSummary{
			{Name: "Go"}, {Name: "Rust"}, {Name: "TypeScript"},
			{Name: "Svelte"}, {Name: "Postgres"}, {Name: "Redis"},
			{Name: "Kafka"}, {Name: "Docker"}, {Name: "K8s"},
			{Name: "gRPC"}, {Name: "WebRTC"}, {Name: "ONNX"},
			{Name: "Prometheus"}, {Name: "Grafana"}, {Name: "Terraform"},
			{Name: "ExtraSkill16"},
		},
		Experiences: []jobs.ExperienceSummary{
			{Title: "Staff SWE", Company: "Acme", StartDate: "2020-01", EndDate: "Present"},
		},
	}

	d := buildUpworkPageData(profile)

	if d.Title != "Staff Software Engineer" {
		t.Errorf("Title: got %q want %q", d.Title, "Staff Software Engineer")
	}
	if d.Rate != "$150.00/hr" {
		t.Errorf("Rate: got %q want $150.00/hr", d.Rate)
	}
	if len(d.Skills) != 15 {
		t.Errorf("Skills len: got %d want 15", len(d.Skills))
	}
	if !d.SkillsOver {
		t.Error("SkillsOver: expected true for 16 skills")
	}
	if d.SkillCount != 16 {
		t.Errorf("SkillCount: got %d want 16", d.SkillCount)
	}
	for _, s := range d.Skills {
		if s == "ExtraSkill16" {
			t.Error("ExtraSkill16 should have been capped out")
		}
	}
	if len(d.Employment) != 1 || d.Employment[0].Title != "Staff SWE" {
		t.Errorf("Employment: %+v", d.Employment)
	}
}

func TestBuildUpworkPageData_EmptyRate(t *testing.T) {
	profile := &jobs.ResumeProfileResult{HourlyRateCents: 0}
	d := buildUpworkPageData(profile)
	if d.Rate != "" {
		t.Errorf("Rate should be empty for 0 cents, got %q", d.Rate)
	}
}

func TestBuildUpworkPageData_SkillsUnderCap(t *testing.T) {
	profile := &jobs.ResumeProfileResult{
		Skills: []jobs.SkillSummary{{Name: "Go"}, {Name: "Rust"}},
	}
	d := buildUpworkPageData(profile)
	if d.SkillsOver {
		t.Error("SkillsOver should be false for 2 skills")
	}
	if len(d.Skills) != 2 {
		t.Errorf("Skills len: got %d want 2", len(d.Skills))
	}
}

func TestUpworkFitnessFunction_NoOsReadFile(t *testing.T) {
	src, err := os.ReadFile("upwork.go")
	if err != nil {
		t.Fatalf("read upwork.go: %v", err)
	}
	if strings.Contains(string(src), "os.ReadFile") {
		t.Error("upwork.go must not use os.ReadFile (DB-only page)")
	}
	if strings.Contains(string(src), "APPLICATIONS_DIR") {
		t.Error("upwork.go must not reference APPLICATIONS_DIR (DB-only page)")
	}
}

func TestUpworkFitnessFunction_NoTemplateHTML(t *testing.T) {
	src, err := os.ReadFile("upwork.go")
	if err != nil {
		t.Fatalf("read upwork.go: %v", err)
	}
	if strings.Contains(string(src), "template.HTML") {
		t.Error("upwork.go must not use template.HTML (DB strings must go through auto-escape)")
	}
}
