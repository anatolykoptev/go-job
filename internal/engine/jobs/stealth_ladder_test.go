package jobs

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestStealthLadderFetch_Tier1Success(t *testing.T) {
	origStealth := StealthFetchSeam
	origOx := OxFetchSeam
	defer func() {
		StealthFetchSeam = origStealth
		OxFetchSeam = origOx
	}()

	oxCalled := false
	StealthFetchSeam = func(ctx context.Context, method, targetURL string, headers map[string]string, reqBody []byte) (int, []byte, error) {
		return http.StatusOK, []byte("<html>stealth tier 1 success</html>"), nil
	}
	OxFetchSeam = func(ctx context.Context, method, targetURL string, headers map[string]string, reqBody []byte) (int, []byte, error) {
		oxCalled = true
		return http.StatusOK, []byte("ox-browser body"), nil
	}

	status, body, err := StealthLadderFetch(context.Background(), "GET", "https://example.com/jobs", nil, nil)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("expected 200 status, got: %d", status)
	}
	if string(body) != "<html>stealth tier 1 success</html>" {
		t.Errorf("unexpected body: %s", string(body))
	}
	if oxCalled {
		t.Errorf("Tier 2 OxFetchSeam should not have been called on Tier 1 success")
	}
}

func TestStealthLadderFetch_Tier1RefusalEscalatesToTier2(t *testing.T) {
	origStealth := StealthFetchSeam
	origOx := OxFetchSeam
	defer func() {
		StealthFetchSeam = origStealth
		OxFetchSeam = origOx
	}()

	oxCalled := false
	StealthFetchSeam = func(ctx context.Context, method, targetURL string, headers map[string]string, reqBody []byte) (int, []byte, error) {
		return http.StatusForbidden, nil, errors.New("403 Forbidden")
	}
	OxFetchSeam = func(ctx context.Context, method, targetURL string, headers map[string]string, reqBody []byte) (int, []byte, error) {
		oxCalled = true
		return http.StatusOK, []byte("<html>ox-browser rescued</html>"), nil
	}

	status, body, err := StealthLadderFetch(context.Background(), "GET", "https://example.com/jobs", nil, nil)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("expected 200 status, got: %d", status)
	}
	if string(body) != "<html>ox-browser rescued</html>" {
		t.Errorf("unexpected body: %s", string(body))
	}
	if !oxCalled {
		t.Errorf("expected Tier 2 OxFetchSeam to be called after Tier 1 403 refusal")
	}
}

func TestStealthLadderFetch_OxBrowserBlockedReturnsErrConnectorBlocked(t *testing.T) {
	origStealth := StealthFetchSeam
	origOx := OxFetchSeam
	defer func() {
		StealthFetchSeam = origStealth
		OxFetchSeam = origOx
	}()

	StealthFetchSeam = func(ctx context.Context, method, targetURL string, headers map[string]string, reqBody []byte) (int, []byte, error) {
		return http.StatusForbidden, nil, errors.New("403 Forbidden")
	}
	OxFetchSeam = func(ctx context.Context, method, targetURL string, headers map[string]string, reqBody []byte) (int, []byte, error) {
		return 0, nil, ErrConnectorBlocked
	}

	_, _, err := StealthLadderFetch(context.Background(), "GET", "https://example.com/jobs", nil, nil)
	if !errors.Is(err, ErrConnectorBlocked) {
		t.Fatalf("expected ErrConnectorBlocked, got: %v", err)
	}
}
