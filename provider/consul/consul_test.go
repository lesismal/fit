package consul

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/provider/internal/watch"
)

// inst is an instance of a service, as the fake catalog keeps it.
type inst struct {
	svc, id, node, addr string
	port                int
	tags                []string
	passing             bool
}

func (i inst) entry() entry {
	var e entry
	e.Node.Node, e.Node.Address = i.node, "192.168.0.1"
	e.Service.ID, e.Service.Service, e.Service.Tags, e.Service.Address, e.Service.Port = i.id, i.svc, i.tags, i.addr, i.port
	return e
}

// fakeConsul answers the parts of Consul's API the providers ask for, with
// blocking queries on one index for everything.
type fakeConsul struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	index uint64
	wake  chan struct{} // closed at each change
	insts []inst
	kv    map[string]string
	token string // the token asked for; none when empty
	seen  []string
	fail  bool
}

func newFakeConsul(t *testing.T) *fakeConsul {
	f := &fakeConsul{t: t, index: 10, wake: make(chan struct{}), kv: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// change changes the fake's state under its lock, and moves its index.
func (f *fakeConsul) change(fn func()) {
	f.mu.Lock()
	fn()
	f.index++
	close(f.wake)
	f.wake = make(chan struct{})
	f.mu.Unlock()
}

func (f *fakeConsul) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, r.URL.RequestURI()+" token="+r.Header.Get("X-Consul-Token"))
	if f.fail {
		f.mu.Unlock()
		http.Error(w, "no leader", http.StatusInternalServerError)
		return
	}
	if f.token != "" && r.Header.Get("X-Consul-Token") != f.token {
		f.mu.Unlock()
		http.Error(w, "ACL not found", http.StatusForbidden)
		return
	}
	if idx := r.URL.Query().Get("index"); idx != "" {
		want, _ := strconv.ParseUint(idx, 10, 64)
		wait, err := time.ParseDuration(r.URL.Query().Get("wait"))
		if err != nil {
			f.mu.Unlock()
			http.Error(w, "bad wait", http.StatusBadRequest)
			return
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		for f.index <= want {
			wake := f.wake
			f.mu.Unlock()
			select {
			case <-wake:
			case <-timer.C:
				f.mu.Lock()
				goto answer
			case <-r.Context().Done():
				return
			}
			f.mu.Lock()
		}
	}
answer:
	defer f.mu.Unlock()
	w.Header().Set("X-Consul-Index", strconv.FormatUint(f.index, 10))
	path := r.URL.Path
	var out any
	switch {
	case path == "/v1/catalog/services":
		m := map[string][]string{}
		for _, i := range f.insts {
			for _, tag := range i.tags {
				if !slices.Contains(m[i.svc], tag) {
					m[i.svc] = append(m[i.svc], tag)
				}
			}
			if m[i.svc] == nil {
				m[i.svc] = []string{}
			}
		}
		out = m
	case path == "/v1/health/state/any":
		out = []any{}
	case strings.HasPrefix(path, "/v1/health/service/"):
		name := strings.TrimPrefix(path, "/v1/health/service/")
		es := []entry{}
		for _, i := range f.insts {
			if i.svc == name && (i.passing || r.URL.Query().Get("passing") == "") {
				es = append(es, i.entry())
			}
		}
		out = es
	case strings.HasPrefix(path, "/v1/kv/"):
		prefix := strings.TrimPrefix(path, "/v1/kv/")
		type kv struct {
			Key   string
			Value []byte
		}
		var kvs []kv
		for k, v := range f.kv {
			if strings.HasPrefix(k, prefix) {
				kvs = append(kvs, kv{k, []byte(v)})
			}
		}
		if kvs == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		out = kvs
	default:
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(out)
}

func (f *fakeConsul) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

// background runs run until the test ends, and waits for it to return.
func background(t *testing.T, run func(context.Context, func([]byte)), update func([]byte)) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx, update)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func shorten(t *testing.T) {
	bw, gap := blockWait, minPollGap
	blockWait, minPollGap = 2*time.Second, 10*time.Millisecond
	d, lo, hi := watch.Debounce, watch.MinBackoff, watch.MaxBackoff
	watch.Debounce, watch.MinBackoff, watch.MaxBackoff = 20*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() {
		blockWait, minPollGap = bw, gap
		watch.Debounce, watch.MinBackoff, watch.MaxBackoff = d, lo, hi
	})
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *logLines) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func decoded(t *testing.T, part []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(part, &m); err != nil {
		t.Fatalf("the part is not JSON: %v: %s", err, part)
	}
	return m
}

// upstreams returns the part's upstreams' names and addresses.
func upstreams(t *testing.T, part []byte) map[string][]string {
	t.Helper()
	var p struct {
		Upstreams []struct {
			Name      string   `json:"name"`
			Addresses []string `json:"addresses"`
		} `json:"upstreams"`
	}
	if err := json.Unmarshal(part, &p); err != nil {
		t.Fatalf("the part is not JSON: %v: %s", err, part)
	}
	m := map[string][]string{}
	for _, u := range p.Upstreams {
		m[u.Name] = u.Addresses
	}
	return m
}

// nextPart waits for a part that ok takes, passing over others: the
// follower sends one again every refresh_interval.
func nextPart(t *testing.T, ch <-chan []byte, ok func([]byte) bool) []byte {
	t.Helper()
	deadline := time.After(10 * time.Second)
	var last []byte
	for {
		select {
		case p := <-ch:
			if ok(p) {
				return p
			}
			last = p
		case <-deadline:
			t.Fatalf("no such part; the last: %s", last)
		}
	}
}

func TestTranslate(t *testing.T) {
	opts := &config.ConsulProvider{}
	services := map[string][]entry{
		"web": {
			inst{svc: "web", id: "web-2", node: "n2", addr: "10.0.0.3", port: 8080,
				tags: []string{"primary", "fit.enable=true", "fit.routes.main.host=shop.example", "fit.routes.main.path=/*", `fit.upstream={"load_balancing": "least_conn"}`}}.entry(),
			inst{svc: "web", id: "web-1", node: "n1", addr: "10.0.0.2", port: 8080,
				tags: []string{"fit.enable=true", "fit.routes.main.host=shop.example", "fit.routes.main.path=/*", `fit.upstream={"load_balancing": "least_conn"}`}}.entry(),
			// Its node's address, as an instance registered without one.
			inst{svc: "web", id: "web-3", node: "n3", port: 8080, tags: []string{"fit.enable=true"}}.entry(),
			// Not taken.
			inst{svc: "web", id: "web-4", node: "n4", addr: "10.0.0.9", port: 8080, tags: []string{"fit.enable=false"}}.entry(),
			inst{svc: "web", id: "web-5", node: "n5", addr: "10.0.0.9", port: 8080}.entry(),
		},
		// A name of its own, a port of its own, and a layer 4 service.
		"db-primary": {
			inst{svc: "db-primary", id: "db", node: "n1", addr: "10.0.1.1", port: 1,
				tags: []string{"fit.enable=true", "fit.name=db", "fit.port=5432", "fit.services.pg.listeners=tcp", "fit.services.pg.port=5433"}}.entry(),
		},
		"bad": {
			inst{svc: "bad", id: "b1", node: "n1", addr: "10.0.2.1", port: 80, tags: []string{"fit.enable"}}.entry(),
			inst{svc: "bad", id: "b2", node: "n1", addr: "10.0.2.2", port: 80, tags: []string{"fit.enable=true", "fit.routes.r.colour=red"}}.entry(),
			inst{svc: "bad", id: "b3", node: "n1", addr: "10.0.2.3", tags: []string{"fit.enable=true"}}.entry(),
			inst{svc: "bad", id: "b4", node: "n1", addr: "10.0.2.4", port: 80, tags: []string{"fit.enable=true", "fit.name=no way"}}.entry(),
			inst{svc: "bad", id: "b5", node: "n1", addr: "10.0.2.5", port: 80, tags: []string{"fit.enable=true", `fit.upstream={"addresses": []}`}}.entry(),
		},
	}
	part, problems := translate(services, opts)
	want := decoded(t, []byte(`{
  "upstreams": [
    {"name": "db", "addresses": ["10.0.1.1:5432"]},
    {"name": "db.pg", "addresses": ["10.0.1.1:5433"]},
    {"name": "web", "addresses": ["10.0.0.2:8080", "10.0.0.3:8080", "192.168.0.1:8080"], "load_balancing": "least_conn"}
  ],
  "routes": [
    {"name": "web.main", "match": {"host": ["shop.example"], "path": ["/*"]},
     "handler": {"module": "proxy", "params": {"upstream": "web"}}}
  ],
  "services": [
    {"name": "db.pg", "listeners": ["tcp"], "upstream": "db.pg"}
  ]
}`))
	if got := decoded(t, part); !reflect.DeepEqual(got, want) {
		t.Errorf("part:\n%s", part)
	}
	wantProblems := []string{
		"service bad, instance b1 on node n1: tag fit.enable: is not <key>=<value>",
		`service bad, instance b2 on node n1: tag fit.routes.r.colour: a route has no setting "colour"`,
		"service bad, instance b3 on node n1: it has no port: tag it fit.port=<port>",
		`service bad, instance b4 on node n1: "no way" is not a valid name for an upstream: tag it fit.name=<name>`,
		"service bad, instance b5 on node n1: tag fit.upstream: addresses are the services'",
	}
	if !reflect.DeepEqual(problems, wantProblems) {
		t.Errorf("problems:\n%s\nwant:\n%s", strings.Join(problems, "\n"), strings.Join(wantProblems, "\n"))
	}

	// With expose_all, an instance with no tags is taken, but one tagged
	// enable=false still is not.
	part, _ = translate(map[string][]entry{"web": services["web"][3:]}, &config.ConsulProvider{ExposeAll: true, TagPrefix: "app"})
	if got := upstreams(t, part); !reflect.DeepEqual(got, map[string][]string{"web": {"10.0.0.9:8080", "10.0.0.9:8080"}}) {
		t.Errorf("expose_all with another prefix: %v", got)
	}
}

func TestCatalog(t *testing.T) {
	shorten(t)
	f := newFakeConsul(t)
	f.token = "s3cret"
	web := func(id, addr string, passing bool) inst {
		return inst{svc: "web", id: id, node: "n-" + id, addr: addr, port: 80, passing: passing,
			tags: []string{"fit.enable=true", "fit.routes.r.path=/"}}
	}
	f.change(func() {
		f.insts = []inst{web("w1", "10.0.0.1", true),
			{svc: "other", id: "o1", node: "n1", addr: "10.0.0.9", port: 80, passing: true}} // not enabled
	})
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var l logLines
	p := NewCatalog("consul", &config.ConsulProvider{Endpoint: f.srv.URL, Datacenter: "dc2", TokenFile: tokenFile, RefreshInterval: config.Duration(time.Hour)}, logs.Func(l.logf))
	if p.Name() != "consul" {
		t.Fatalf("Name: %q", p.Name())
	}
	parts := make(chan []byte, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx, func(b []byte) { parts <- b })
	}()
	is := func(want map[string][]string) func([]byte) bool {
		return func(b []byte) bool { return reflect.DeepEqual(upstreams(t, b), want) }
	}

	nextPart(t, parts, is(map[string][]string{"web": {"10.0.0.1:80"}}))
	// An instance is registered.
	f.change(func() { f.insts = append(f.insts, web("w2", "10.0.0.2", true)) })
	nextPart(t, parts, is(map[string][]string{"web": {"10.0.0.1:80", "10.0.0.2:80"}}))
	// One fails its checks.
	f.change(func() { f.insts[0].passing = false })
	nextPart(t, parts, is(map[string][]string{"web": {"10.0.0.2:80"}}))
	// The last is deregistered: nothing.
	f.change(func() { f.insts = f.insts[1:2] })
	nextPart(t, parts, func(b []byte) bool { return string(b) == "{}" })

	// Consul fails, and comes back.
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	f.change(func() {})
	time.Sleep(100 * time.Millisecond)
	f.change(func() { f.fail = false; f.insts = append(f.insts, web("w3", "10.0.0.3", true)) })
	nextPart(t, parts, is(map[string][]string{"web": {"10.0.0.3:80"}}))

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	reqs := f.requests()
	var blocking, health int
	for _, r := range reqs {
		if !strings.Contains(r, "dc=dc2") || !strings.HasSuffix(r, "token=s3cret") {
			t.Errorf("a request without the datacenter or the token: %s", r)
		}
		if strings.Contains(r, "index=") {
			blocking++
		}
		if strings.HasPrefix(r, "/v1/health/service/other") {
			t.Errorf("a service not enabled was asked for: %s", r)
		}
		if strings.HasPrefix(r, "/v1/health/service/web?") && strings.Contains(r, "passing=1") {
			health++
		}
	}
	if blocking == 0 || health == 0 {
		t.Errorf("%d blocking queries, %d of web's health:\n%s", blocking, health, strings.Join(reqs, "\n"))
	}
	if !strings.Contains(l.all(), "500 Internal Server Error: no leader") {
		t.Errorf("logs:\n%s", l.all())
	}
}

func TestCatalogMaxServices(t *testing.T) {
	shorten(t)
	f := newFakeConsul(t)
	f.change(func() {
		for _, name := range []string{"c", "a", "b"} {
			f.insts = append(f.insts, inst{svc: name, id: name, node: "n", addr: "10.0.0.1", port: 80, passing: true, tags: []string{"fit.enable=true"}})
		}
	})
	var l logLines
	p := NewCatalog("consul", &config.ConsulProvider{Endpoint: f.srv.URL, MaxServices: 2, RefreshInterval: config.Duration(time.Hour)}, logs.Func(l.logf))
	parts := make(chan []byte, 4)
	background(t, p.Run, func(b []byte) { parts <- b })
	got := upstreams(t, nextPart(t, parts, func([]byte) bool { return true }))
	if !reflect.DeepEqual(got, map[string][]string{"a": {"10.0.0.1:80"}, "b": {"10.0.0.1:80"}}) {
		t.Errorf("upstreams: %v", got)
	}
	if !strings.Contains(l.all(), "provider consul: service c: left out: max_services, 2, are taken") {
		t.Errorf("logs:\n%s", l.all())
	}
}

func TestKV(t *testing.T) {
	shorten(t)
	f := newFakeConsul(t)
	route := func(name string) string {
		return `{"routes": [{"name": "` + name + `", "handler": {"module": "respond"}}]}`
	}
	f.change(func() {
		f.kv["fit/a"] = route("a")
		f.kv["fit/bad"] = `{"limits": {}}`
		f.kv["other/x"] = route("x")
	})
	var l logLines
	p := NewKV("kv", &config.ConsulKVProvider{Endpoint: f.srv.URL, RefreshInterval: config.Duration(time.Hour)}, logs.Func(l.logf))
	parts := make(chan []byte, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx, func(b []byte) { parts <- b })
	}()
	routes := func(want string) func([]byte) bool {
		return func(b []byte) bool { return string(b) == want }
	}
	nextPart(t, parts, routes(`{"routes":[{"name":"a","handler":{"module":"respond"}}]}`))
	f.change(func() { f.kv["fit/b"] = route("b") })
	nextPart(t, parts, routes(`{"routes":[{"name":"a","handler":{"module":"respond"}},{"name":"b","handler":{"module":"respond"}}]}`))
	// Every key gone: Consul answers 404, an empty part.
	f.change(func() { f.kv = map[string]string{} })
	nextPart(t, parts, routes(`{}`))
	// The index goes back, as after a restore: the provider starts over.
	f.change(func() { f.index = 3; f.kv["fit/c"] = route("c") })
	nextPart(t, parts, routes(`{"routes":[{"name":"c","handler":{"module":"respond"}}]}`))
	cancel()
	<-done
	if !strings.Contains(l.all(), "provider kv: key fit/bad: ") {
		t.Errorf("logs:\n%s", l.all())
	}
	for _, r := range f.requests() {
		if !strings.HasPrefix(r, "/v1/kv/fit/?") || !strings.Contains(r, "recurse=true") {
			t.Errorf("request: %s", r)
		}
	}
}

func TestEscapePath(t *testing.T) {
	if got := escapePath("fit/a b/c?"); got != "fit/a%20b/c%3F" {
		t.Errorf("escapePath: %q", got)
	}
}
