package notify_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"

	"github.com/anatolykoptev/go_job/internal/hunt/notify"
)

// TestRedactingTransport_ReplacesTokenInURLError verifies that when the base
// transport returns a *url.Error, the token is replaced with "[REDACTED]" in
// the error's URL field and thus in the error string.
//
// REAL-CODE: this exercises the shipped RedactingTransport.RoundTrip. If the
// redaction guard is removed from RoundTrip, the token leaks into err.Error().
func TestRedactingTransport_ReplacesTokenInURLError(t *testing.T) {
	token := "123:ABCsecret"

	// A base transport that always fails with a *url.Error whose URL contains
	// the token — mirroring how the Telegram Bot API embeds the token in the
	// request URL (https://api.telegram.org/bot<token>/getMe).
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		// Simulate the real http.Client behaviour: on a connection failure the
		// *url.Error wraps the request URL (which contains the token for
		// Telegram API calls). We construct one directly to deterministically
		// reproduce the shape without depending on network timing.
		return nil, &url.Error{
			Op:  "Get",
			URL: "https://api.telegram.org/bot" + token + "/getMe",
			Err: errConnectionRefused,
		}
	})

	transport := notify.NewRedactingTransport(base, token)

	// Call RoundTrip directly (not via http.Client.Do) to test the transport
	// in isolation. http.Client.Do wraps the transport error in its own
	// *url.Error using the original request URL — that outer wrapper is handled
	// by the RedactingSlogHandler (second defense layer) in production.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.telegram.org/bot"+token+"/getMe", nil)
	resp, err := transport.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected an error from the failing transport, got nil")
	}

	msg := err.Error()
	if strings.Contains(msg, "ABCsecret") {
		t.Errorf("error message leaked the token: %q", msg)
	}
	if !strings.Contains(msg, "[REDACTED]") {
		t.Errorf("error message does not contain [REDACTED]: %q", msg)
	}
}

// TestRedactingSlogHandler_RedactsTokenFromLog verifies that the
// RedactingSlogHandler replaces the token with "[REDACTED]" in log output.
//
// REAL-CODE: this exercises the shipped RedactingSlogHandler. If the redaction
// in the handler is removed, the token leaks into the log output.
func TestRedactingSlogHandler_RedactsTokenFromLog(t *testing.T) {
	token := "123:ABCsecret"

	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, nil)
	handler := notify.NewRedactingSlogHandler(base, token)
	logger := slog.New(handler)

	logger.Error("telegram send failed", slog.String("url", "https://api.telegram.org/bot"+token+"/getMe"))

	output := buf.String()
	if strings.Contains(output, "ABCsecret") {
		t.Errorf("log output leaked the token: %q", output)
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Errorf("log output does not contain [REDACTED]: %q", output)
	}
}

// TestRedactingSlogHandler_NoDeadlockWithDefaultHandler is a regression test for
// a deadlock that occurred when wrapping slog.Default().Handler() in
// NewRedactingSlogHandler and installing it as the default via slog.SetDefault.
//
// The default handler writes via log.Logger.output, which calls back into
// slog.Default().Handler() — if that's our wrapper, infinite recursion → deadlock.
// The fix: pass nil as base so a standalone TextHandler is created instead.
//
// This test reproduces the exact production wiring (slog.SetDefault) and verifies
// it does not deadlock within a reasonable timeout.
func TestRedactingSlogHandler_NoDeadlockWithDefaultHandler(t *testing.T) {
	token := "123:ABCsecret"

	// Reproduce the exact production wiring from main.go:
	//   slog.SetDefault(slog.New(notify.NewRedactingSlogHandler(nil, token)))
	// The nil base creates a standalone TextHandler, avoiding the recursion.
	prev := slog.Default()
	defer slog.SetDefault(prev)

	slog.SetDefault(slog.New(notify.NewRedactingSlogHandler(nil, token)))

	// This must not deadlock. If it does, the test goroutine hangs and the
	// timeout fires.
	done := make(chan struct{})
	go func() {
		slog.Info("test message after SetDefault", slog.String("url", "https://api.telegram.org/bot"+token+"/getMe"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: slog.Info did not return within 5s after SetDefault with RedactingSlogHandler")
	}
}

// TestBotDebugNeverEnabled verifies that a bot constructed via
// NewBotAPIWithClient (as NewFromEnv now does) has Debug == false. Debug mode
// would log the full request/response including the token.
//
// We can't call NewBotAPIWithClient with a real token (it does a GetMe
// handshake), so we verify the Debug field on a bot constructed with a test
// server that returns a valid GetMe response.
func TestBotDebugNeverEnabled(t *testing.T) {
	// Stand up a fake Telegram API server that responds to /getMe so
	// NewBotAPIWithClient's handshake succeeds without a real token.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test","username":"testbot"}}`))
	}))
	defer srv.Close()

	// NewBotAPIWithClient uses apiEndpoint as a fmt format string with %s for
	// token and %s for method. We point it at the test server.
	endpoint := srv.URL + "/bot%s/%s"
	client := &http.Client{}
	bot, err := tgbotapi.NewBotAPIWithClient("test-token", endpoint, client)
	if err != nil {
		t.Fatalf("NewBotAPIWithClient failed: %v", err)
	}
	if bot.Debug {
		t.Error("bot.Debug is true; must be false to avoid logging the token")
	}
}

// roundTripFunc is an http.RoundTripper that delegates to a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// errConnectionRefused is a sentinel error for the inner Err field.
var errConnectionRefused = &connErr{}

type connErr struct{}

func (e *connErr) Error() string { return "connection refused" }
