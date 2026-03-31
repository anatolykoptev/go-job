// internal/engine/jobs/twitter_test.go
package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	twitter "github.com/anatolykoptev/go-twitter"
	"github.com/anatolykoptev/go_job/internal/engine"
	"github.com/anatolykoptev/go_job/internal/social"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchTwitterJobsRaw_ViaSocial(t *testing.T) {
	socialSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/twitter/account":
			_ = json.NewEncoder(w).Encode(social.Credentials{
				ID:          "test-id",
				Credentials: map[string]string{"username": "u", "auth_token": "t", "ct0": "c"},
			})
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer socialSrv.Close()

	engine.Cfg.SocialClient = social.NewClient(socialSrv.URL, "tok", "go-job")
	engine.Cfg.TwitterClient = nil
	defer func() { engine.Cfg.SocialClient = nil }()

	// Will fail at SearchTimeline (fake creds), but proves the social path is attempted
	_, err := SearchTwitterJobsRaw(context.Background(), "golang hiring", 5)
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "not configured")
}

func TestSearchTwitterJobsRaw_FallbackToLocal(t *testing.T) {
	engine.Cfg.SocialClient = nil
	tw, _ := twitter.NewClient(twitter.ClientConfig{OpenAccountCount: 1})
	engine.Cfg.TwitterClient = tw
	defer func() { engine.Cfg.TwitterClient = nil }()

	_, err := SearchTwitterJobsRaw(context.Background(), "test", 5)
	if err != nil {
		assert.NotContains(t, err.Error(), "not configured")
	}
}

func TestSearchTwitter_BothNil(t *testing.T) {
	engine.Cfg.SocialClient = nil
	engine.Cfg.TwitterClient = nil

	_, err := searchTwitter(context.Background(), "test", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestBuildTwitterJobQuery(t *testing.T) {
	assert.Equal(t, "golang hiring", buildTwitterJobQuery("golang hiring"))
	assert.Contains(t, buildTwitterJobQuery("golang developer"), "hiring OR job")
}
