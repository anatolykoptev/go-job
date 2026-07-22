package jobs

import (
	"testing"

	linkedin "github.com/anatolykoptev/go-linkedin"
)

func TestBuildLinkedInClientConfig_StealthDefault(t *testing.T) {
	// Flag unset → stealth path: WowaURL empty, current behavior unchanged.
	t.Setenv("LINKEDIN_TRANSPORT", "")
	t.Setenv("GO_WOWA_BASE_URL", "http://go-wowa:8906")
	t.Setenv("INTERNAL_SERVICE_SECRET", "s3cr3t")

	creds := map[string]string{"JSESSIONID": "abc", "li_at": "xyz"}
	cfg := buildLinkedInClientConfig(creds)

	if cfg.WowaURL != "" {
		t.Fatalf("stealth: WowaURL want empty, got %q", cfg.WowaURL)
	}
	if cfg.Session != "" {
		t.Fatalf("stealth: Session want empty, got %q", cfg.Session)
	}
	if cfg.InternalSecret != "" {
		t.Fatalf("stealth: InternalSecret want empty, got %q", cfg.InternalSecret)
	}
	if cfg.Cookies["JSESSIONID"] != "abc" || cfg.Cookies["li_at"] != "xyz" {
		t.Fatalf("stealth: Cookies not propagated, got %#v", cfg.Cookies)
	}
}

func TestBuildLinkedInClientConfig_StealthExplicit(t *testing.T) {
	// Flag = "stealth" → also stealth path.
	t.Setenv("LINKEDIN_TRANSPORT", "stealth")
	t.Setenv("GO_WOWA_BASE_URL", "http://go-wowa:8906")
	t.Setenv("INTERNAL_SERVICE_SECRET", "s3cr3t")

	creds := map[string]string{"JSESSIONID": "abc"}
	cfg := buildLinkedInClientConfig(creds)

	if cfg.WowaURL != "" {
		t.Fatalf("stealth-explicit: WowaURL want empty, got %q", cfg.WowaURL)
	}
}

func TestBuildLinkedInClientConfig_CDP(t *testing.T) {
	t.Setenv("LINKEDIN_TRANSPORT", "cdp")
	t.Setenv("GO_WOWA_BASE_URL", "http://go-wowa:8906")
	t.Setenv("INTERNAL_SERVICE_SECRET", "s3cr3t")

	creds := map[string]string{"JSESSIONID": "abc", "li_at": "xyz"}
	cfg := buildLinkedInClientConfig(creds)

	if cfg.WowaURL != "http://go-wowa:8906" {
		t.Fatalf("cdp: WowaURL want http://go-wowa:8906, got %q", cfg.WowaURL)
	}
	if cfg.Session != linkedin.SessionNameLinkedIn {
		t.Fatalf("cdp: Session want %q, got %q", linkedin.SessionNameLinkedIn, cfg.Session)
	}
	if cfg.InternalSecret != "s3cr3t" {
		t.Fatalf("cdp: InternalSecret want s3cr3t, got %q", cfg.InternalSecret)
	}
	if cfg.Cookies["JSESSIONID"] != "abc" || cfg.Cookies["li_at"] != "xyz" {
		t.Fatalf("cdp: Cookies not propagated, got %#v", cfg.Cookies)
	}
}

func TestBuildLinkedInClientConfig_CDPDefaultWowaBaseURL(t *testing.T) {
	// CDP flag set but GO_WOWA_BASE_URL unset → default base URL.
	t.Setenv("LINKEDIN_TRANSPORT", "cdp")
	t.Setenv("GO_WOWA_BASE_URL", "")
	t.Setenv("INTERNAL_SERVICE_SECRET", "s3cr3t")

	cfg := buildLinkedInClientConfig(map[string]string{"JSESSIONID": "abc"})

	if cfg.WowaURL != "http://go-wowa:8906" {
		t.Fatalf("cdp-default: WowaURL want http://go-wowa:8906, got %q", cfg.WowaURL)
	}
}
