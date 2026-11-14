package discovery

import (
	"errors"
	"strings"
	"testing"
)

func TestReadLimitedBody(t *testing.T) {
	t.Run("body within limit", func(t *testing.T) {
		body, err := readLimitedBody(strings.NewReader("12345"), 5)
		if err != nil {
			t.Fatalf("readLimitedBody() error = %v", err)
		}
		if got := string(body); got != "12345" {
			t.Fatalf("readLimitedBody() = %q, want %q", got, "12345")
		}
	})

	t.Run("body over limit", func(t *testing.T) {
		body, err := readLimitedBody(strings.NewReader("123456"), 5)
		if !errors.Is(err, errBodyTruncated) {
			t.Fatalf("readLimitedBody() error = %v, want errBodyTruncated", err)
		}
		if body != nil {
			t.Fatalf("readLimitedBody() body = %q, want nil", body)
		}
	})
}
