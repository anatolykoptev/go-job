package engine

import "testing"

func TestNewBrowserClient(t *testing.T) {
	bc, err := NewBrowserClient()
	if err != nil {
		t.Fatalf("NewBrowserClient() error = %v", err)
	}
	if bc == nil {
		t.Fatal("NewBrowserClient() returned nil")
	}
	if bc.client == nil {
		t.Fatal("BrowserClient.client is nil")
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
