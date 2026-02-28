package engine

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	stealth "github.com/anatolykoptev/go-stealth"
)

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"http 429", &stealth.HttpStatusError{StatusCode: 429}, true},
		{"http 502", &stealth.HttpStatusError{StatusCode: 502}, true},
		{"http 503", &stealth.HttpStatusError{StatusCode: 503}, true},
		{"regular error", errors.New("something"), false},
		{"timeout", &net.DNSError{IsTimeout: true}, true},
		{"op error", &net.OpError{Op: "dial", Err: errors.New("refused")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stealth.IsRetryable(tt.err); got != tt.want {
				t.Errorf("IsRetryable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryDoSuccess(t *testing.T) {
	rc := stealth.RetryConfig{MaxRetries: 3, InitialWait: time.Millisecond, MaxWait: 10 * time.Millisecond, Multiplier: 2}
	calls := 0
	got, err := RetryDo(context.Background(), rc, func() (string, error) {
		calls++
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ok" {
		t.Errorf("got %q, want %q", got, "ok")
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryDoRetryThenSuccess(t *testing.T) {
	rc := stealth.RetryConfig{MaxRetries: 3, InitialWait: time.Millisecond, MaxWait: 10 * time.Millisecond, Multiplier: 2}
	calls := 0
	got, err := RetryDo(context.Background(), rc, func() (string, error) {
		calls++
		if calls < 3 {
			return "", &stealth.HttpStatusError{StatusCode: 503}
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ok" {
		t.Errorf("got %q, want %q", got, "ok")
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestRetryDoExhausted(t *testing.T) {
	rc := stealth.RetryConfig{MaxRetries: 2, InitialWait: time.Millisecond, MaxWait: 10 * time.Millisecond, Multiplier: 2}
	calls := 0
	_, err := RetryDo(context.Background(), rc, func() (string, error) {
		calls++
		return "", &stealth.HttpStatusError{StatusCode: 502}
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if calls != 3 { // initial + 2 retries
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestRetryDoNonRetryable(t *testing.T) {
	rc := stealth.RetryConfig{MaxRetries: 3, InitialWait: time.Millisecond, MaxWait: 10 * time.Millisecond, Multiplier: 2}
	calls := 0
	_, err := RetryDo(context.Background(), rc, func() (string, error) {
		calls++
		return "", errors.New("permanent error")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry for non-retryable), got %d", calls)
	}
}

func TestRetryDoContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	rc := stealth.RetryConfig{MaxRetries: 3, InitialWait: time.Millisecond, MaxWait: 10 * time.Millisecond, Multiplier: 2}
	_, err := RetryDo(ctx, rc, func() (string, error) {
		return "", &stealth.HttpStatusError{StatusCode: 503}
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
