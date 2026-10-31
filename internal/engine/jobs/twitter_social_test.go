package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anatolykoptev/go-twitter/social"
	"github.com/anatolykoptev/go_job/internal/engine"
)

// TestSearchTwitter_SocialPathLeasesWithEndpointHintAndReports pins the
// go-twitter v0.7.0 contract the go-social path of searchTwitter depends on:
// each lease carries ?endpoint=SearchTimeline and a verdict is reported back.
// Each lease is a distinct account whose proxy refuses connections, so the
// call fails fast with an account-level verdict; no traffic leaves the test.
//
// Mutation: in twitter.go searchViaSocial, replace the twitter.SearchWithSocial
// call with a bare `_, err := sc.AcquireAccount(ctx, "twitter"); return nil, err`
// => no endpoint hint and no report => both assertions go RED.
// The rate_limit window needs a real X response (the GraphQL base URL is
// package-private in go-twitter); go-twitter covers it in ratelimit_headers_test.go.
func TestSearchTwitter_SocialPathLeasesWithEndpointHintAndReports(t *testing.T) {
	var (
		mu      sync.Mutex
		leases  []string
		reports []map[string]any
		n       int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/twitter/account":
			mu.Lock()
			n++
			id := fmt.Sprintf("acc-%d", n)
			leases = append(leases, r.URL.RawQuery)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(social.Credentials{
				ID:          id,
				Credentials: map[string]string{"username": "u" + id, "auth_token": "t", "ct0": "c"},
				Proxy:       "http://127.0.0.1:1",
				ExpiresIn:   60,
			})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/twitter/report/"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			reports = append(reports, body)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	prevSC, prevTW := engine.Cfg.SocialClient, engine.Cfg.TwitterClient
	engine.Cfg.SocialClient = social.NewClient(srv.URL, "tok", "go-job")
	engine.Cfg.TwitterClient = nil
	t.Cleanup(func() { engine.Cfg.SocialClient, engine.Cfg.TwitterClient = prevSC, prevTW })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = SearchTwitterJobsRaw(ctx, "golang hiring", 5) // fails: proxy refuses

	mu.Lock()
	defer mu.Unlock()
	if len(leases) == 0 {
		t.Fatal("no lease request reached go-social")
	}
	for _, q := range leases {
		if !strings.Contains(q, "endpoint=SearchTimeline") {
			t.Errorf("lease query %q lacks endpoint=SearchTimeline", q)
		}
	}
	if len(reports) == 0 {
		t.Fatal("no usage report reached go-social")
	}
	for i, r := range reports {
		if s, _ := r["status"].(string); s == "" {
			t.Errorf("report %d has no status", i)
		}
	}
}
