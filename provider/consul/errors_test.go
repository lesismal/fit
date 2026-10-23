package consul

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/apiclient"
	"github.com/lesismal/fit/provider"
)

// TestKinds checks that the package registers its kinds with provider.
func TestKinds(t *testing.T) {
	for _, tc := range []struct {
		kind string
		pc   *config.Provider
		want string
	}{
		{"consul", &config.Provider{Consul: &config.ConsulProvider{Endpoint: "http://127.0.0.1:1"}}, "watching Consul's catalog at http://127.0.0.1:1"},
		{"consul_kv", &config.Provider{ConsulKV: &config.ConsulKVProvider{Endpoint: "http://127.0.0.1:1"}}, `watching Consul's keys under "fit/" at http://127.0.0.1:1`},
	} {
		k, ok := provider.LookupKind(tc.kind)
		if !ok {
			t.Fatalf("kind %s is not registered", tc.kind)
		}
		p, watching := k("p", tc.pc, nil)
		if p.Name() != "p" || watching != tc.want {
			t.Errorf("%s: %q, %q", tc.kind, p.Name(), watching)
		}
	}
}

// answers serves fixed answers by path, a status and a body.
type answers map[string]struct {
	status int
	body   string
}

func (a answers) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ans, ok := a[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(ans.status)
		w.Write([]byte(ans.body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, c := range []conn{
		{tokenFile: missing},
		{files: apiclient.TLSFiles{CA: missing}},
	} {
		if _, err := (&catalogSource{conn: c}).Open(context.Background()); err == nil {
			t.Errorf("catalog %+v: opened", c)
		}
		if _, err := (&kvSource{conn: c, opts: &config.ConsulKVProvider{}}).Open(context.Background()); err == nil {
			t.Errorf("kv %+v: opened", c)
		}
	}
}

func TestCatalogReadFails(t *testing.T) {
	ok := struct {
		status int
		body   string
	}{200, `{"web": ["fit.enable=true"]}`}
	for _, tc := range []struct {
		name string
		a    answers
		want string
	}{
		{"services fail", answers{"/v1/catalog/services": {500, "down"}}, "500 Internal Server Error: down"},
		{"services not JSON", answers{"/v1/catalog/services": {200, "nope"}}, "GET /v1/catalog/services: invalid character"},
		{"health fails", answers{"/v1/catalog/services": ok, "/v1/health/service/web": {503, "busy"}}, "503 Service Unavailable: busy"},
		{"health not JSON", answers{"/v1/catalog/services": ok, "/v1/health/service/web": {200, "{"}}, "GET /v1/health/service/web: unexpected end"},
	} {
		srv := tc.a.server(t)
		s := &catalogSource{opts: &config.ConsulProvider{}, conn: conn{endpoint: srv.URL, timeout: 5 * time.Second}}
		closeSource, err := s.Open(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = s.Read(context.Background())
		closeSource()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestKVReadFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    answers
		want string
	}{
		{"fails", answers{"/v1/kv/fit/": {500, "down"}}, "500 Internal Server Error: down"},
		{"not JSON", answers{"/v1/kv/fit/": {200, "nope"}}, "GET /v1/kv/fit/: invalid character"},
	} {
		srv := tc.a.server(t)
		s := &kvSource{opts: &config.ConsulKVProvider{}, conn: conn{endpoint: srv.URL, timeout: 5 * time.Second}}
		closeSource, err := s.Open(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = s.Read(context.Background())
		closeSource()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
	if p := NewKV("kv", &config.ConsulKVProvider{}, nil); p.Name() != "kv" {
		t.Errorf("Name: %q", p.Name())
	}
}

func TestGetFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Consul-Index", "abc")
		w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	c, err := conn{endpoint: srv.URL, timeout: 5 * time.Second}.open()
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if _, _, err := c.get(context.Background(), "/x", nil, 0); err == nil || !strings.Contains(err.Error(), `X-Consul-Index "abc" is not a number`) {
		t.Errorf("a bad index: %v", err)
	}
	c.endpoint = "::bad"
	if _, _, err := c.get(context.Background(), "/x", nil, 0); err == nil {
		t.Error("a bad endpoint: no error")
	}
}

// TestPollIndexZero checks that an index of 0 is taken as 1, and that one
// gone back starts over.
func TestPollIndexZero(t *testing.T) {
	shorten(t)
	var (
		mu      sync.Mutex
		indexes []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		indexes = append(indexes, r.URL.Query().Get("index"))
		mu.Unlock()
		w.Header().Set("X-Consul-Index", "0")
		w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	c, err := conn{endpoint: srv.URL, timeout: 5 * time.Second}.open()
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := false
	err = c.poll(ctx, "/x", nil, func() { ready = true }, cancel)
	if err == nil || !ready {
		t.Fatalf("poll: %v, ready %v", err, ready)
	}
	// The first query, then one from index 1, which went back to 0.
	mu.Lock()
	defer mu.Unlock()
	if len(indexes) < 2 || indexes[0] != "" || indexes[1] != "1" {
		t.Errorf("indexes: %q", indexes)
	}
}

func TestTranslateNoAddress(t *testing.T) {
	e := inst{svc: "web", id: "w", node: "n", port: 80, tags: []string{"fit.enable=true"}}.entry()
	e.Node.Address = ""
	part, problems := translate(map[string][]entry{"web": {e}}, &config.ConsulProvider{})
	if string(part) != "{}" || len(problems) != 1 || !strings.Contains(problems[0], "it has no address, nor has its node") {
		t.Errorf("part %s, problems %q", part, problems)
	}
	if p := NewCatalog("c", &config.ConsulProvider{}, nil); p.Name() != "c" {
		t.Errorf("Name: %q", p.Name())
	}
}
