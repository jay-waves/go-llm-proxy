package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"

	"testing"
	"time"

	"go-llm-proxy/internal/httputil"
)

func TestConnectSettingsDefaults(t *testing.T) {
	url, app := (&Config{}).ConnectSettings()
	if url != "http://connect:8080" || app != "go-llm-proxy" {
		t.Fatalf("defaults: %q %q", url, app)
	}
	url, app = (&Config{Connect: &ConnectConfig{URL: "http://hub.example.com/", Application: "a"}}).ConnectSettings()
	if url != "http://hub.example.com" || app != "a" {
		t.Fatalf("explicit: %q %q", url, app)
	}
	if (&Config{}).EffectiveHealthInterval() != 30*time.Second {
		t.Fatal("default health interval should be 30s")
	}
	if (&Config{HealthInterval: 5}).EffectiveHealthInterval() != 5*time.Second {
		t.Fatal("health_interval should be honoured")
	}
}

// fakeHub serves /v1/catalog/<site> from a fixed set of sites.
type fakeHub struct {
	mu       sync.Mutex
	catalog  string // JSON served for known sites
	requests []string
	apps     []string
	status   int // when non-zero, every reply uses this status
}

func (f *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.Path)
	f.apps = append(f.apps, r.Header.Get("X-Eidonix-Application"))
	status, body := f.status, f.catalog
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.Header().Set("X-Eidonix-Error", "internal")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"hub broke"}}`))
		return
	}
	// Like the hub: /v1/catalog/<site> answers with the site object itself,
	// or 404 unknown_site.
	if site, ok := strings.CutPrefix(r.URL.Path, "/v1/catalog/"); ok {
		var cat HubCatalog
		_ = json.Unmarshal([]byte(body), &cat)
		for _, s := range cat.Sites {
			if s.Name == site {
				one, _ := json.Marshal(s)
				_, _ = w.Write(one)
				return
			}
		}
		w.Header().Set("X-Eidonix-Error", "unknown_site")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"unknown_site","message":"no such site"}}`))
		return
	}
	_, _ = w.Write([]byte(body))
}

const hubCatalogJSON = `{"sites":[
 {"name":"site-a","online":true,"services":[
   {"name":"chat","kind":"http","healthy":true,"models":["model-a"]},
   {"name":"sick","kind":"http","healthy":false}]},
 {"name":"site-b","online":false,"services":[{"name":"chat","kind":"http","healthy":true}]}
]}`

func connectBackendHealth(t *testing.T, hub *httptest.Server, urls ...string) map[string]BackendHealth {
	t.Helper()
	httputil.SetConnect(hub.URL, "test-proxy")
	t.Cleanup(func() { httputil.SetConnect("", "") })
	models := make([]ModelConfig, len(urls))
	for i, u := range urls {
		models[i] = ModelConfig{Name: "m" + string(rune('a'+i)), Backends: []BackendConfig{{URL: u}}, Timeout: 300}
	}
	cs := NewTestConfigStore(&Config{Models: models, Connect: &ConnectConfig{URL: hub.URL, Application: "test-proxy"}})
	hs := NewHealthStore(cs, time.Hour, 2*time.Second)
	hs.RecheckAll()
	hs.wg.Wait()
	return hs.GetBackendStatus()
}

func TestConnectHealthFromCatalog(t *testing.T) {
	fh := &fakeHub{catalog: hubCatalogJSON}
	hub := httptest.NewServer(fh)
	defer hub.Close()

	st := connectBackendHealth(t, hub,
		"connect://site-a/chat/v1",    // up
		"connect://site-a/sick/v1",    // service unhealthy
		"connect://site-a/missing/v1", // service not offered
		"connect://site-b/chat/v1",    // site offline (hub returns no site-a data, but site-b entry)
		"connect://site-zzz/chat/v1",  // unknown site
	)
	want := map[string]struct {
		online bool
		errSub string
	}{
		"connect://site-a/chat/v1":    {true, ""},
		"connect://site-a/sick/v1":    {false, "unhealthy"},
		"connect://site-a/missing/v1": {false, "does not offer"},
		"connect://site-b/chat/v1":    {false, "offline"},
		"connect://site-zzz/chat/v1":  {false, "unknown site"},
	}
	for url, w := range want {
		got, ok := st[url]
		if !ok {
			t.Errorf("%s: no health entry", url)
			continue
		}
		if got.Online != w.online || !strings.Contains(got.Error, w.errSub) {
			t.Errorf("%s: online=%v err=%q, want online=%v err~%q", url, got.Online, got.Error, w.online, w.errSub)
		}
	}

	// One catalog request per site, never a probe through the hub itself.
	fh.mu.Lock()
	defer fh.mu.Unlock()
	perSite := map[string]int{}
	for _, p := range fh.requests {
		perSite[p]++
		if !strings.HasPrefix(p, "/v1/catalog/") {
			t.Errorf("unexpected hub request %q (probe through the hub?)", p)
		}
	}
	if len(fh.requests) != 3 {
		t.Errorf("expected 3 catalog requests (one per site), got %d: %v", len(fh.requests), fh.requests)
	}
	for p, n := range perSite {
		if n != 1 {
			t.Errorf("%s fetched %d times", p, n)
		}
	}
	for _, a := range fh.apps {
		if a != "test-proxy" {
			t.Errorf("catalog request application label = %q", a)
		}
	}
}

func TestConnectHealthHubDown(t *testing.T) {
	fh := &fakeHub{status: http.StatusBadGateway}
	hub := httptest.NewServer(fh)
	defer hub.Close()
	st := connectBackendHealth(t, hub, "connect://site-a/chat/v1")
	got := st["connect://site-a/chat/v1"]
	if got.Online || !strings.Contains(got.Error, "hub broke") {
		t.Fatalf("hub error should make backend down with the hub's reason: %+v", got)
	}

	// Unreachable hub.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	st = connectBackendHealth(t, dead, "connect://site-a/chat/v1")
	got = st["connect://site-a/chat/v1"]
	if got.Online || !strings.Contains(got.Error, "unreachable") {
		t.Fatalf("unreachable hub: %+v", got)
	}
}

func TestConnectHealthFeedsListener(t *testing.T) {
	hub := httptest.NewServer(&fakeHub{catalog: hubCatalogJSON})
	defer hub.Close()
	httputil.SetConnect(hub.URL, "")
	t.Cleanup(func() { httputil.SetConnect("", "") })

	cs := NewTestConfigStore(&Config{Models: []ModelConfig{
		{Name: "m", Timeout: 300, Backends: []BackendConfig{{URL: "connect://site-a/chat/v1"}, {URL: "connect://site-b/chat/v1"}}},
	}, Connect: &ConnectConfig{URL: hub.URL}})
	hs := NewHealthStore(cs, time.Hour, 2*time.Second)
	var mu sync.Mutex
	seen := map[string]bool{}
	hs.SetBackendListener(func(url string, online bool) { mu.Lock(); seen[url] = online; mu.Unlock() })
	hs.RecheckAll()
	hs.wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if !seen["connect://site-a/chat/v1"] || seen["connect://site-b/chat/v1"] {
		t.Fatalf("listener results: %v", seen)
	}
	if m := hs.GetStatus()["m"]; !m.Online {
		t.Fatalf("model with one healthy replica should be online: %+v", m)
	}
}

func TestConnectCatalogPollingLoop(t *testing.T) {
	old := connectPollInterval
	connectPollInterval = 30 * time.Millisecond
	defer func() { connectPollInterval = old }()

	fh := &fakeHub{catalog: hubCatalogJSON}
	hub := httptest.NewServer(fh)
	defer hub.Close()
	httputil.SetConnect(hub.URL, "")
	t.Cleanup(func() { httputil.SetConnect("", "") })

	cs := NewTestConfigStore(&Config{Models: []ModelConfig{
		{Name: "m", Timeout: 300, Backends: []BackendConfig{{URL: "connect://site-a/chat/v1"}}},
	}, Connect: &ConnectConfig{URL: hub.URL}})
	hs := NewHealthStore(cs, time.Hour, 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hs.Start(ctx)
	defer hs.Stop()

	time.Sleep(200 * time.Millisecond)
	fh.mu.Lock()
	n := len(fh.requests)
	fh.mu.Unlock()
	if n < 3 {
		t.Fatalf("catalog should be re-polled on its own ticker, saw %d requests", n)
	}
}

func TestConnectURLContextWindowDetection(t *testing.T) {
	var gotPaths []string
	var mu sync.Mutex
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPaths = append(gotPaths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/site-a/glm/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"m","max_model_len":65536}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer hub.Close()
	httputil.SetConnect(hub.URL, "")
	t.Cleanup(func() { httputil.SetConnect("", "") })

	got, err := detectOpenAI(httputil.NewHTTPClient(), "connect://site-a/glm/v1", "m", "")
	if err != nil || got != 65536 {
		t.Fatalf("detectOpenAI via connect = %d, %v (paths %v)", got, err, gotPaths)
	}
}

func TestProbeBackendForModelConnectUsesCatalog(t *testing.T) {
	fh := &fakeHub{catalog: hubCatalogJSON}
	hub := httptest.NewServer(fh)
	defer hub.Close()
	httputil.SetConnect(hub.URL, "")
	t.Cleanup(func() { httputil.SetConnect("", "") })

	res := ProbeBackendForModel("connect://site-a/sick/v1", "", "", "m")
	if res.Reachable || !strings.Contains(res.Error, "unhealthy") {
		t.Fatalf("unhealthy service: %+v", res)
	}
	res = ProbeBackendForModel("connect://site-a/chat/v1", "", "", "m")
	if !res.Reachable {
		t.Fatalf("healthy service should be reachable: %+v", res)
	}
	fh.mu.Lock()
	defer fh.mu.Unlock()
	for _, p := range fh.requests {
		if strings.HasPrefix(p, "/site-a/") && p == "/site-a/chat/v1" {
			t.Errorf("probe must not HEAD through the hub")
		}
	}
}

func TestLoadConfigWithConnectBlock(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	yml := `
listen: ":8080"
health_interval: 7
connect:
  url: http://connect.example.com:8080
  application: edge-proxy
models:
  - name: glm
    backends:
      - url: connect://site-a/chat/v1
keys:
  - {key: k, name: n}
`
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cs, err := NewConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := cs.Get()
	url, app := cfg.ConnectSettings()
	if url != "http://connect.example.com:8080" || app != "edge-proxy" || cfg.EffectiveHealthInterval() != 7*time.Second {
		t.Fatalf("got %q %q %v", url, app, cfg.EffectiveHealthInterval())
	}
	if cfg.Models[0].Backend != "connect://site-a/chat/v1" {
		t.Fatalf("backend bridged as %q", cfg.Models[0].Backend)
	}
}

func TestConnectKeyFile(t *testing.T) {
	path := t.TempDir() + "/key"
	cfg := &Config{Connect: &ConnectConfig{KeyFile: path}}

	if k := cfg.ConnectKey(); k != "" {
		t.Errorf("missing file: key %q", k)
	}
	if err := os.WriteFile(path, []byte("  first-key \nignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if k := cfg.ConnectKey(); k != "first-key" {
		t.Errorf("key = %q", k)
	}
	// Re-read on every call, so a config reload picks up a rotated key.
	if err := os.WriteFile(path, []byte("second-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if k := cfg.ConnectKey(); k != "second-key" {
		t.Errorf("after rotation key = %q", k)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if k := cfg.ConnectKey(); k != "" {
		t.Errorf("empty file: key %q", k)
	}

	if k := (&Config{Connect: &ConnectConfig{Key: " inline "}}).ConnectKey(); k != "inline" {
		t.Errorf("inline key = %q", k)
	}
	if k := (&Config{}).ConnectKey(); k != "" {
		t.Errorf("no connect block: key %q", k)
	}
}

func TestConnectKeyFileParsedAndReloaded(t *testing.T) {
	dir := t.TempDir()
	keyPath := dir + "/key"
	cfgPath := dir + "/config.yaml"
	os.WriteFile(keyPath, []byte("one\n"), 0o600)
	write := func() {
		yaml := "keys: []\nmodels:\n  - name: m\n    backend: http://localhost:1/v1\nconnect:\n  key_file: " + keyPath + "\n"
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	cs, err := NewConfigStore(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := cs.Get()
	if cfg.Connect == nil || cfg.Connect.KeyFile != keyPath || cfg.ConnectKey() != "one" {
		t.Fatalf("parsed connect = %+v key %q", cfg.Connect, cfg.ConnectKey())
	}
	os.WriteFile(keyPath, []byte("two\n"), 0o600)
	write()
	if err := cs.Load(); err != nil {
		t.Fatal(err)
	}
	if k := cs.Get().ConnectKey(); k != "two" {
		t.Errorf("after reload key = %q", k)
	}
}
