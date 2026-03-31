package social

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquireAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/twitter/account", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "go-job", r.Header.Get("X-Consumer"))
		require.NoError(t, json.NewEncoder(w).Encode(Credentials{
			ID:          "abc-123",
			Credentials: map[string]string{"username": "testuser", "auth_token": "tok", "ct0": "ct"},
			Proxy:       "http://proxy:8080",
		}))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "go-job")
	creds, err := c.AcquireAccount(context.Background(), "twitter")
	require.NoError(t, err)
	assert.Equal(t, "abc-123", creds.ID)
	assert.Equal(t, "testuser", creds.Credentials["username"])
	assert.Equal(t, "http://proxy:8080", creds.Proxy)
}

func TestAcquireAccount_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "go-job")
	_, err := c.AcquireAccount(context.Background(), "twitter")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "503")
}

func TestReportUsage(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token", "go-job")
	err := c.ReportUsage(context.Background(), "twitter", "abc-123", "success")
	require.NoError(t, err)
	assert.Equal(t, "/twitter/report/abc-123", gotPath)
	assert.Contains(t, gotBody, `"status":"success"`)
}
