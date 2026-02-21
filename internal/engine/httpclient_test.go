package engine

import (
	"testing"

	stealth "github.com/anatolykoptev/go-stealth"
)

func TestNewBrowserClient(t *testing.T) {
	bc, err := stealth.NewClient(stealth.WithTimeout(10))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if bc == nil {
		t.Fatal("NewClient() returned nil")
	}
}

func TestChromeHeaders(t *testing.T) {
	h := ChromeHeaders()

	required := []string{"accept", "accept-language", "user-agent"}
	for _, key := range required {
		if _, ok := h[key]; !ok {
			t.Errorf("ChromeHeaders() missing key %q", key)
		}
	}

	ua := h["user-agent"]
	if ua == "" {
		t.Error("user-agent is empty")
	}
	// Should contain Chrome identifier
	if len(ua) < 20 {
		t.Errorf("user-agent too short: %q", ua)
	}
}
