package notify

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// fakeToken has the real Telegram shape: 9-digit bot id, colon, "AA" + 33 chars.
const fakeToken = "123456789:AAabcdefghijklmnopqrstuvwxyz0123456"

// These tests run the REAL chain: a real http.Client fails for real (DNS
// failure / hung server), the real tgbotapi builds the URL from the endpoint
// format, and the error is logged with slog.Any("error", err) exactly as
// main.go does. Nothing fabricates a *url.Error.

// failingEndpoints returns tgbotapi endpoint formats whose requests fail inside
// a real http.Client.
func failingEndpoints(t *testing.T) map[string]string {
	t.Helper()
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second): // body unread => no disconnect signal
		}
	}))
	t.Cleanup(hang.Close)
	return map[string]string{
		"unresolvable host": "https://telegram-redact-test.invalid/bot%s/%s",
		"hanging server":    hang.URL + "/bot%s/%s",
	}
}

func realClient() *http.Client { return &http.Client{Timeout: 300 * time.Millisecond} }

// rawBotErr is the error production would see WITHOUT the client wrapper.
func rawBotErr(t *testing.T, endpoint string) error {
	t.Helper()
	_, err := tgbotapi.NewBotAPIWithClient(fakeToken, endpoint, realClient())
	if err == nil {
		t.Fatal("expected the handshake to fail")
	}
	return fmt.Errorf("hunt notify: create bot: %w", err)
}

func TestNewTelegramBot_RequestErrorCarriesNoToken(t *testing.T) {
	for name, endpoint := range failingEndpoints(t) {
		t.Run(name, func(t *testing.T) {
			// Control: without the wrapper the real client error DOES carry the token.
			if raw := rawBotErr(t, endpoint); !strings.Contains(raw.Error(), fakeToken) {
				t.Fatalf("control: raw error no longer carries the token (%q); test proves nothing", raw)
			}

			_, err := newTelegramBot(fakeToken, endpoint, realClient())
			if err == nil {
				t.Fatal("expected the handshake to fail")
			}
			wrapped := fmt.Errorf("hunt notify: create bot: %w", err)
			if strings.Contains(wrapped.Error(), fakeToken) || strings.Contains(wrapped.Error(), "AAabcdef") {
				t.Fatalf("token leaked in returned error: %q", wrapped)
			}
		})
	}
}

// logAny logs err the way main.go:503 does and returns what reached the sink.
func logAny(h slog.Handler, buf *bytes.Buffer, err error) string {
	slog.New(h).Warn("hunt notify: disabled (bot init failed)", slog.Any("error", err))
	return buf.String()
}

func TestRedactingSlogHandler_AnyError(t *testing.T) {
	for name, endpoint := range failingEndpoints(t) {
		leaky := rawBotErr(t, endpoint) // handler is the only defence in this test
		t.Run(name, func(t *testing.T) {
			var ctl bytes.Buffer
			if out := logAny(slog.NewTextHandler(&ctl, nil), &ctl, leaky); !strings.Contains(out, fakeToken) {
				t.Fatalf("control: unredacted handler no longer leaks (%q); test proves nothing", out)
			}

			handlers := map[string]func(*bytes.Buffer) slog.Handler{
				"production nil-base (ReplaceAttr)": func(b *bytes.Buffer) slog.Handler { return newRedactingStreamHandler(b, fakeToken) },
				"wrapped text handler": func(b *bytes.Buffer) slog.Handler {
					return NewRedactingSlogHandler(slog.NewTextHandler(b, nil), fakeToken)
				},
				"wrapped json handler": func(b *bytes.Buffer) slog.Handler {
					return NewRedactingSlogHandler(slog.NewJSONHandler(b, nil), fakeToken)
				},
				"token unknown to handler": func(b *bytes.Buffer) slog.Handler { return newRedactingStreamHandler(b, "") },
			}
			for hn, mk := range handlers {
				var buf bytes.Buffer
				out := logAny(mk(&buf), &buf, leaky)
				if strings.Contains(out, fakeToken) || strings.Contains(out, "AAabcdef") {
					t.Errorf("%s: token leaked: %s", hn, out)
				}
				if !strings.Contains(out, "hunt notify: create bot") {
					t.Errorf("%s: error text was dropped, not scrubbed: %s", hn, out)
				}
			}
		})
	}
}

func TestRedactingSlogHandler_ErrorInGroupAndWithAttrs(t *testing.T) {
	leaky := rawBotErr(t, "https://telegram-redact-test.invalid/bot%s/%s")
	var buf bytes.Buffer
	l := slog.New(NewRedactingSlogHandler(slog.NewTextHandler(&buf, nil), fakeToken)).With(slog.Any("with", leaky))
	l.Warn("x", slog.Group("g", slog.Any("error", leaky)))
	if strings.Contains(buf.String(), "AAabcdef") {
		t.Fatalf("token leaked via group/With: %s", buf.String())
	}
}

// TestFullChain_BotInitFailure is the production shape end to end:
// newTelegramBot error -> wrapped -> slog.Any("error") -> installed handler.
func TestFullChain_BotInitFailure(t *testing.T) {
	for name, endpoint := range failingEndpoints(t) {
		t.Run(name, func(t *testing.T) {
			_, err := newTelegramBot(fakeToken, endpoint, realClient())
			err = fmt.Errorf("hunt notify: create bot: %w", err)
			var buf bytes.Buffer
			out := logAny(newRedactingStreamHandler(&buf, fakeToken), &buf, err)
			if strings.Contains(out, fakeToken) || strings.Contains(out, "AAabcdef") {
				t.Fatalf("token reached the log sink: %s", out)
			}
		})
	}
}
