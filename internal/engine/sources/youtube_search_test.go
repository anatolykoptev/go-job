package sources

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

// TestDoYouTubeDataSearch_ErrorCarriesNoAPIKey drives the real http.Client into
// a real dial failure. The key is a query parameter, so the *url.Error from
// Client.Do carries it unless doYouTubeDataSearch scrubs it.
func TestDoYouTubeDataSearch_ErrorCarriesNoAPIKey(t *testing.T) {
	const key = "AIzaSyFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE0"

	prevClient, prevRetry := engine.Cfg.HTTPClient, engine.DefaultRetryConfig
	t.Cleanup(func() { engine.Cfg.HTTPClient, engine.DefaultRetryConfig = prevClient, prevRetry })
	engine.DefaultRetryConfig = engine.RetryConfig{MaxRetries: 0}
	engine.Cfg.HTTPClient = &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("dial refused (test)")
		}},
	}

	_, err := doYouTubeDataSearch(context.Background(), "golang", "en", 3, key)
	if err == nil {
		t.Fatal("expected the request to fail")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("API key leaked in returned error: %q", err)
	}
	if !strings.Contains(err.Error(), "dial refused") {
		t.Fatalf("underlying cause was lost, not just the key: %q", err)
	}
}
