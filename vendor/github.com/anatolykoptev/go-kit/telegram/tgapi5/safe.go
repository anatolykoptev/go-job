package tgapi5

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"

	"github.com/anatolykoptev/go-kit/telegram/tgsafe"
)

// DefaultSafeTimeout is the HTTP timeout of a bot built by [NewSafeBotAPI]:
// above the 60 s long-poll Telegram clients conventionally use, so a stalled
// connection cannot hang a poll forever.
//
// It bounds the whole request, uploads included: a large media upload that
// needs longer than this fails with a timeout. Consumers that upload big files
// pass [WithBaseClient] with a longer Timeout.
const DefaultSafeTimeout = 75 * time.Second

// SafeOption configures [NewSafeBotAPI].
type SafeOption func(*safeConfig)

type safeConfig struct {
	base     *http.Client
	endpoint string
}

// WithBaseClient supplies the underlying HTTP client (transport, proxy, a
// different timeout). The default is &http.Client{Timeout: DefaultSafeTimeout}.
func WithBaseClient(c *http.Client) SafeOption {
	return func(sc *safeConfig) { sc.base = c }
}

// NewSafeBotAPI is tgbotapi.NewBotAPI with a client that scrubs the bot token
// out of every error ([tgsafe.NewHTTPClient]) and a default timeout. Every
// error the SDK returns for a failed API call is a *url.Error whose text
// carries bot<TOKEN>; with this constructor it carries bot<redacted> and keeps
// its type, so Timeout() and errors.As still work.
//
// The default HTTP timeout is [DefaultSafeTimeout] and also caps uploads; use
// [WithBaseClient] for a longer one. Pair the bot with [SetSafeLogger]:
// GetUpdatesChan prints failed polls through the SDK's package logger.
//
// This targets the OvyFlash fork. Consumers of go-telegram-bot-api/v5 build the
// bot with tgbotapi.NewBotAPIWithClient and tgsafe.NewHTTPClient (see the
// tgsafe package doc).
func NewSafeBotAPI(token string, opts ...SafeOption) (*tgbotapi.BotAPI, error) {
	return newSafeBotAPI(token, tgbotapi.APIEndpoint, opts...)
}

func newSafeBotAPI(token, endpoint string, opts ...SafeOption) (*tgbotapi.BotAPI, error) {
	sc := safeConfig{endpoint: endpoint}
	for _, o := range opts {
		o(&sc)
	}
	if sc.base == nil {
		sc.base = &http.Client{Timeout: DefaultSafeTimeout}
	}
	return tgbotapi.NewBotAPIWithClient(token, sc.endpoint, tgsafe.NewHTTPClient(sc.base, token))
}

var safeLog struct {
	mu     sync.Mutex
	logger *tgsafe.Logger
}

// SetSafeLogger installs a scrubbing logger as the SDK's package logger (stderr
// by default) and registers secrets with it. The SDK's logger is a process
// global that its poll goroutines read without synchronisation, so it is
// swapped exactly once, by the first call, which also fixes the destination
// slog logger l (nil means slog.Default()). Later calls only ADD their secrets
// to that logger (and ignore l), so a second bot's token is scrubbed too and
// calling it after polling has started is safe.
func SetSafeLogger(l *slog.Logger, secrets ...string) error {
	safeLog.mu.Lock()
	defer safeLog.mu.Unlock()
	if safeLog.logger != nil {
		safeLog.logger.AddSecrets(secrets...)
		return nil
	}
	lg := tgsafe.NewLogger(l, secrets...)
	if err := tgbotapi.SetLogger(lg); err != nil {
		return err
	}
	safeLog.logger = lg
	return nil
}

// resetSafeLogger forgets the installed logger (tests only).
func resetSafeLogger() {
	safeLog.mu.Lock()
	defer safeLog.mu.Unlock()
	safeLog.logger = nil
}
