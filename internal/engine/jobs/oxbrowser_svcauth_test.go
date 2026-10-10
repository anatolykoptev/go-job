package jobs

// oxbrowser_svcauth_test.go pins the ox-browser#173 contract: once the
// inbound auth gate on ox-browser flips from soft to enforce, every request
// this service sends to the ox-browser origin must carry X-Internal-Secret
// from INTERNAL_SERVICE_SECRET, and the secret must never reach another
// origin — including across a redirect.
//
//   T1: the service-owned client — craigslistOxFetchFetch drives
//       engine.Cfg.HTTPClient, which engine.Init wraps with go-kit svcauth.
//   T2: the go-engine fallback tier — fetch.WithOxBrowser wired in Init —
//       self-authenticates via svcauth.FromEnv; driven through the real
//       engine.FetchProxyBody seam.
//   T3: a different origin, reached directly or via a redirect off the
//       routed ox stand-in, receives no secret.
//
// Falsification (M1): drop the svcauth.WrapClient call in
// internal/engine/config.go Init → T1 fails on got == "" while T2 stays
// green (the library authenticates itself — that asymmetry is exactly why
// the mutation is meaningful).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/anatolykoptev/go_job/internal/engine"
)

const oxTestSecret = "ox-test-secret" //nolint:gosec // G101: test fixture, not a credential

// initEngineWithOx runs the real production wiring — engine.Init — pointed at
// an httptest ox-browser stand-in, and restores engine.Cfg afterwards so the
// wrapped global client does not leak into other tests.
func initEngineWithOx(t *testing.T, oxURL string) {
	t.Helper()
	oldCfg := *engine.Cfg
	engine.Init(engine.Config{
		FetchTimeout: time.Second,
		LLMAPIBase:   "http://127.0.0.1:1/v1", // unreachable: the startup LLM ping fails fast
		OxBrowserURL: oxURL,
	})
	t.Cleanup(func() { *engine.Cfg = oldCfg })
}

// oxStandIn is an httptest server that records the inbound X-Internal-Secret
// header and answers with a valid ox-browser /fetch response body.
func oxStandIn(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var mu sync.Mutex
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Get("X-Internal-Secret")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":200,"body":"<html>ok</html>"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// TestCraigslistOxFetchSendsInternalSecret (T1) drives the production path:
// engine.Init wraps engine.Cfg.HTTPClient with svcauth, then
// craigslistOxFetchFetch POSTs to <OX_BROWSER_URL>/fetch through it.
// The stand-in must observe the secret.
func TestCraigslistOxFetchSendsInternalSecret(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_SECRET", oxTestSecret)
	srv, got := oxStandIn(t)
	initEngineWithOx(t, srv.URL)

	status, body, err := craigslistOxFetchFetch(context.Background(), "https://sfbay.craigslist.org/search/jjj", nil)
	if err != nil {
		t.Fatalf("craigslistOxFetchFetch: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("craigslistOxFetchFetch status = %d, want 200", status)
	}
	if string(body) != "<html>ok</html>" {
		t.Fatalf("craigslistOxFetchFetch body = %q", body)
	}
	if *got != oxTestSecret {
		t.Fatalf("ox stand-in saw X-Internal-Secret = %q, want %q", *got, oxTestSecret)
	}
}

// TestFetchProxyBodyOxBrowserSendsSecret (T2) drives the library path through
// this service's wiring: engine.Init builds fetcherProxy with
// fetch.WithOxBrowser(<stand-in>), and engine.FetchProxyBody escalates to it
// when the primary fetch fails. The bumped go-engine must attach the secret
// itself (svcauth.FromEnv) — proves the bumped library is on the real path.
func TestFetchProxyBodyOxBrowserSendsSecret(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_SECRET", oxTestSecret)
	srv, got := oxStandIn(t)
	initEngineWithOx(t, srv.URL)

	// 127.0.0.1:1 is never listening — the primary fetch fails fast and the
	// fetcher escalates to the ox-browser tier.
	body, err := engine.FetchProxyBody(context.Background(), "http://127.0.0.1:1/x", nil)
	if err != nil {
		t.Fatalf("FetchProxyBody: %v", err)
	}
	if string(body) != "<html>ok</html>" {
		t.Fatalf("FetchProxyBody body = %q", body)
	}
	if *got != oxTestSecret {
		t.Fatalf("ox stand-in saw X-Internal-Secret = %q, want %q", *got, oxTestSecret)
	}
}

// TestSvcauthSecretNotSentToOtherOrigins (T3): the wrapped shared client must
// not leak the secret — a direct request to an unrouted origin carries none,
// and a redirect OFF the routed ox origin lands without it.
func TestSvcauthSecretNotSentToOtherOrigins(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_SECRET", oxTestSecret)

	var mu sync.Mutex
	var otherSecrets []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		otherSecrets = append(otherSecrets, r.Header.Get("X-Internal-Secret"))
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	defer other.Close()

	var oxSecret string
	var oxSet bool
	ox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oxSecret, oxSet = r.Header.Get("X-Internal-Secret"), true
		http.Redirect(w, r, other.URL+"/landing", http.StatusFound)
	}))
	defer ox.Close()

	initEngineWithOx(t, ox.URL)
	client := engine.Cfg.HTTPClient
	if client == nil {
		t.Fatal("engine.Cfg.HTTPClient is nil after Init")
	}

	// Direct request to the unrouted origin.
	resp, err := client.Get(other.URL + "/direct") //nolint:gosec,noctx // httptest origin, test context irrelevant
	if err != nil {
		t.Fatalf("GET other origin: %v", err)
	}
	resp.Body.Close()

	// Request to the routed ox origin that 302s to the other origin.
	resp, err = client.Get(ox.URL + "/fetch") //nolint:gosec,noctx // httptest origin
	if err != nil {
		t.Fatalf("GET ox origin (redirect): %v", err)
	}
	resp.Body.Close()

	if !oxSet {
		t.Fatal("ox stand-in never saw the redirected request")
	}
	if oxSecret != oxTestSecret {
		t.Fatalf("ox origin saw X-Internal-Secret = %q, want %q", oxSecret, oxTestSecret)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(otherSecrets) != 2 {
		t.Fatalf("other origin saw %d requests, want 2", len(otherSecrets))
	}
	for i, s := range otherSecrets {
		if s != "" {
			t.Fatalf("request %d to other origin carried X-Internal-Secret = %q, want none", i, s)
		}
	}
}

// TestSvcauthKeepsGoWowaSecret guards the shared-client wrap against the
// svcauth strip rule: gowowa_render.go hand-sets X-Internal-Secret on
// engine.Cfg.HTTPClient, and a wrap that did not route go-wowa would strip
// it. Route the GOWOWA_URL origin (same default as gowowa_render.go) so the
// header still lands.
func TestSvcauthKeepsGoWowaSecret(t *testing.T) {
	t.Setenv("INTERNAL_SERVICE_SECRET", oxTestSecret)

	var got string
	wowa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Internal-Secret")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://example.com","html":"<html></html>"}`))
	}))
	defer wowa.Close()

	// Point both the render endpoint var and the svcauth route env at the
	// stand-in so the routed origin matches.
	t.Setenv("GOWOWA_URL", wowa.URL)
	orig := goWowaRenderURL
	goWowaRenderURL = wowa.URL + "/api/v1/render"
	t.Cleanup(func() { goWowaRenderURL = orig })

	initEngineWithOx(t, "")

	if _, err := fetchRenderedHTML(context.Background(), "https://example.com"); err != nil {
		t.Fatalf("fetchRenderedHTML: %v", err)
	}
	if got != oxTestSecret {
		t.Fatalf("go-wowa stand-in saw X-Internal-Secret = %q, want %q — svcauth must route GOWOWA_URL or it strips the hand-set header", got, oxTestSecret)
	}
}
