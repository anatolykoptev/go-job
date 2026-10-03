package redact_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/redact"
)

// fakeToken has the real Telegram shape: 9-digit bot id, colon, "AA" + 33 chars.
const fakeToken = "123456789:AAabcdefghijklmnopqrstuvwxyz0123456"

// realClientErr drives a REAL http.Client into a failure and returns the
// *url.Error exactly as production gets it from Client.Do.
func realClientErr(t *testing.T, target string, do func(*http.Request) (*http.Response, error)) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader("x=1"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected the request to fail")
	}
	return err
}

func TestHTTPClient_ScrubsRealClientErrors(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second): // body unread => no disconnect signal
		}
	}))
	defer hang.Close()

	cases := map[string]string{
		"unresolvable host": "https://telegram-redact-test.invalid/bot" + fakeToken + "/getMe",
		"hanging server":    hang.URL + "/bot" + fakeToken + "/getMe",
		"file endpoint":     "https://telegram-redact-test.invalid/file/bot" + fakeToken + "/photos/1.jpg",
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			raw := &http.Client{Timeout: 300 * time.Millisecond}

			// Control: the unwrapped client leaks, so the assertion below is not vacuous.
			if leaked := realClientErr(t, target, raw.Do); !strings.Contains(leaked.Error(), fakeToken) {
				t.Fatalf("control: raw client error no longer carries the token (%q); test proves nothing", leaked)
			}

			err := realClientErr(t, target, redact.NewHTTPClient(raw).Do)
			if strings.Contains(err.Error(), fakeToken) || strings.Contains(err.Error(), "AAabcdef") {
				t.Fatalf("token leaked: %q", err)
			}
			if !strings.Contains(err.Error(), "bot<redacted>") {
				t.Fatalf("want bot<redacted> in %q", err)
			}
			var ue *url.Error
			if !errors.As(err, &ue) {
				t.Fatalf("error lost its *url.Error type: %T", err)
			}
		})
	}
}

func TestHTTPClient_ScrubsQueryKey(t *testing.T) {
	const key = "AIzaSyFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE0"
	raw := &http.Client{Timeout: 300 * time.Millisecond}
	target := "https://telegram-redact-test.invalid/v3/search?q=x&key=" + url.QueryEscape(key)
	err := realClientErr(t, target, redact.NewHTTPClient(raw, key).Do)
	if strings.Contains(err.Error(), key) {
		t.Fatalf("key leaked: %q", err)
	}
}

func TestError_WrappedAndNonURLErrors(t *testing.T) {
	leaky := fmt.Errorf("download: %w", errors.New("GET https://api.telegram.org/bot"+fakeToken+"/getFile failed"))
	got := redact.Error(leaky)
	if strings.Contains(got.Error(), fakeToken) {
		t.Fatalf("token leaked through wrapped error: %q", got)
	}
	clean := errors.New("boom")
	if !errors.Is(redact.Error(clean), clean) {
		t.Fatal("a clean error must be returned unchanged")
	}
	if redact.Error(nil) != nil {
		t.Fatal("nil in, nil out")
	}
}

func TestError_DoesNotMutateInput(t *testing.T) {
	ue := &url.Error{Op: "Post", URL: "https://x/bot" + fakeToken + "/getMe", Err: errors.New("boom")}
	_ = redact.Error(ue)
	if !strings.Contains(ue.URL, fakeToken) {
		t.Fatal("input *url.Error was mutated")
	}
}
