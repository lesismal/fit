package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// ctr is a container, written as the daemon lists it.
func ctr(name string, labels map[string]string, ip string, ports ...int) container {
	var c container
	c.ID = "id-" + name
	c.Names = []string{"/" + name}
	c.State = "running"
	c.Status = "Up 1 second"
	c.Labels = labels
	for _, p := range ports {
		c.Ports = append(c.Ports, struct {
			IP          string `json:"IP"`
			PrivatePort int    `json:"PrivatePort"`
			PublicPort  int    `json:"PublicPort"`
			Type        string `json:"Type"`
		}{PrivatePort: p, Type: "tcp"})
	}
	if ip != "" {
		c.NetworkSettings.Networks = map[string]struct {
			IPAddress         string `json:"IPAddress"`
			GlobalIPv6Address string `json:"GlobalIPv6Address"`
		}{"net": {IPAddress: ip}}
	}
	return c
}

// decoded is a part, decoded for comparing.
func decoded(t *testing.T, part []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(part, &m); err != nil {
		t.Fatalf("the part is not JSON: %v: %s", err, part)
	}
	return m
}

func jsonOf(t *testing.T, s string) map[string]any {
	t.Helper()
	return decoded(t, []byte(s))
}

func TestTranslate(t *testing.T) {
	opts := &config.DockerProvider{}
	cs := []container{
		// Two replicas of a Compose service, with a route.
		ctr("shop-web-2", map[string]string{
			"fit.enable": "true", "com.docker.compose.project": "shop", "com.docker.compose.service": "web",
			"fit.routes.main.host": "shop.example, www.shop.example", "fit.routes.main.path": "/*",
			"fit.routes.main.priority": "5", "fit.routes.main.listeners": "web",
			"fit.upstream": `{"load_balancing": "least_conn"}`,
		}, "10.0.0.3", 8080),
		ctr("shop-web-1", map[string]string{
			"fit.enable": "true", "com.docker.compose.project": "shop", "com.docker.compose.service": "web",
			"fit.routes.main.host": "shop.example, www.shop.example", "fit.routes.main.path": "/*",
			"fit.routes.main.priority": "5", "fit.routes.main.listeners": "web",
			"fit.upstream": `{"load_balancing": "least_conn"}`,
		}, "10.0.0.2", 8080),
		// A named container with two ports, one chosen, a handler of its
		// own, middleware, and a layer 4 service on the other port.
		ctr("api", map[string]string{
			"fit.enable": "true", "fit.name": "api", "fit.port": "9000",
			"fit.routes.r.expr":         `PathPrefix("/api/")`,
			"fit.routes.r.middleware":   `[{"module": "rate_limit", "params": {"rate": 10}}]`,
			"fit.routes.r.handler":      `{"module": "respond", "params": {"body": "hi"}}`,
			"fit.services.db.listeners": "tcp", "fit.services.db.port": "5432", "fit.services.db.proxy_protocol": "v2",
		}, "10.0.0.4", 9000, 5432),
		// Left out: not enabled, enabled false, unhealthy, starting.
		ctr("plain", map[string]string{"fit.routes.x.host": "x.example"}, "10.0.0.5", 80),
		ctr("off", map[string]string{"fit.enable": "false"}, "10.0.0.6", 80),
		func() container {
			c := ctr("sick", map[string]string{"fit.enable": "true"}, "10.0.0.7", 80)
			c.Status = "Up 3 minutes (unhealthy)"
			return c
		}(),
		func() container {
			c := ctr("young", map[string]string{"fit.enable": "true"}, "10.0.0.8", 80)
			c.Status = "Up 1 second (health: starting)"
			return c
		}(),
		// Left out with problems.
		ctr("twoports", map[string]string{"fit.enable": "true"}, "10.0.0.9", 80, 443),
		ctr("noports", map[string]string{"fit.enable": "true"}, "10.0.0.10"),
		ctr("badlabel", map[string]string{"fit.enable": "true", "fit.routes.a.colour": "red"}, "10.0.0.11", 80),
		ctr("badjson", map[string]string{"fit.enable": "true", "fit.routes.a.middleware": "{"}, "10.0.0.12", 80),
		ctr("nonet", map[string]string{"fit.enable": "true"}, "", 80),
		ctr("wrongnet", map[string]string{"fit.enable": "true", "fit.network": "other"}, "10.0.0.13", 80),
		ctr("dotkey", map[string]string{"fit.enable": "true", "fit.routes.a.b.host": "x"}, "10.0.0.14", 80),
		ctr("svc", map[string]string{"fit.enable": "true", "fit.services.s.port": "1"}, "10.0.0.15", 80),
		ctr("upname", map[string]string{"fit.enable": "true", "fit.upstream": `{"name": "x"}`}, "10.0.0.16", 80),
	}
	part, problems := translate(cs, opts)
	want := jsonOf(t, `{
  "upstreams": [
    {"name": "api", "addresses": ["10.0.0.4:9000"]},
    {"name": "api.db", "addresses": ["10.0.0.4:5432"]},
    {"name": "shop-web", "addresses": ["10.0.0.2:8080", "10.0.0.3:8080"], "load_balancing": "least_conn"}
  ],
  "routes": [
    {"name": "api.r", "match": {"expr": "PathPrefix(\"/api/\")"},
     "middleware": [{"module": "rate_limit", "params": {"rate": 10}}],
     "handler": {"module": "respond", "params": {"body": "hi"}}},
    {"name": "shop-web.main", "listeners": ["web"], "priority": 5,
     "match": {"host": ["shop.example", "www.shop.example"], "path": ["/*"]},
     "handler": {"module": "proxy", "params": {"upstream": "shop-web"}}}
  ],
  "services": [
    {"name": "api.db", "listeners": ["tcp"], "upstream": "api.db", "proxy_protocol": "v2"}
  ]
}`)
	if got := decoded(t, part); !reflect.DeepEqual(got, want) {
		t.Errorf("part:\n%s", part)
	}
	wantProblems := []string{
		`container badjson: label fit.routes.a.middleware: must be a JSON array of middleware, as in the config`,
		`container badlabel: label fit.routes.a.colour: a route has no setting "colour"`,
		`container dotkey: label fit.routes.a.b.host: a route has no setting "b.host"`,
		`container nonet: it has no address on any network`,
		`container noports: it exposes no TCP port: set fit.port`,
		`container svc: service s: set fit.services.s.listeners`,
		`container twoports: it exposes TCP ports [80 443]: set fit.port`,
		`container upname: label fit.upstream: name is the container's: set fit.name`,
		`container wrongnet: it is not on network "other"`,
	}
	if !reflect.DeepEqual(problems, wantProblems) {
		t.Errorf("problems:\n%s\nwant:\n%s", strings.Join(problems, "\n"), strings.Join(wantProblems, "\n"))
	}
	// The same containers, in another order, give the same part.
	rev := make([]container, len(cs))
	for i := range cs {
		rev[len(cs)-1-i] = cs[i]
	}
	if again, _ := translate(rev, opts); string(again) != string(part) {
		t.Errorf("another order gave another part:\n%s\n%s", again, part)
	}
	// Nothing taken is an empty part.
	if empty, _ := translate(nil, opts); string(empty) != "{}" {
		t.Errorf("no containers: %s", empty)
	}
}

func TestTranslateOptions(t *testing.T) {
	plain := ctr("plain", map[string]string{"app.routes.x.host": "x.example"}, "10.0.0.5", 80)
	plain.Ports[0].PublicPort, plain.Ports[0].IP = 32768, "0.0.0.0"
	host := ctr("host", map[string]string{"app.routes.h.path": "/h"}, "", 8000)
	host.HostConfig.NetworkMode = "host"
	two := ctr("two", map[string]string{"app.routes.t.path": "/t"}, "10.0.0.20", 80)
	two.NetworkSettings.Networks["back"] = two.NetworkSettings.Networks["net"]
	two.NetworkSettings.Networks["back"] = struct {
		IPAddress         string `json:"IPAddress"`
		GlobalIPv6Address string `json:"GlobalIPv6Address"`
	}{GlobalIPv6Address: "fd00::20"}
	off := ctr("off", map[string]string{"app.enable": "false"}, "10.0.0.6", 80)

	addrs := func(part []byte) map[string]any {
		out := map[string]any{}
		for _, u := range decoded(t, part)["upstreams"].([]any) {
			u := u.(map[string]any)
			out[u["name"].(string)] = u["addresses"].([]any)[0]
		}
		return out
	}
	// expose_all takes every container but those enabled false, under
	// another prefix; a container on two networks is reached on the
	// first by name, or the one network chooses.
	opts := &config.DockerProvider{ExposeAll: true, LabelPrefix: "app"}
	part, problems := translate([]container{plain, host, two, off}, opts)
	want := map[string]any{"plain": "10.0.0.5:80", "host": "127.0.0.1:8000", "two": "[fd00::20]:80"}
	if got := addrs(part); !reflect.DeepEqual(got, want) || len(problems) != 0 {
		t.Errorf("expose_all: %v %v", got, problems)
	}
	opts.Network = "net"
	if got := addrs(must(translate([]container{two}, opts))); got["two"] != "10.0.0.20:80" {
		t.Errorf("network: %v", got)
	}
	// use_published_ports sends to the host's port, and leaves out a
	// container whose port is not published.
	opts = &config.DockerProvider{ExposeAll: true, LabelPrefix: "app", UsePublishedPorts: true}
	part, problems = translate([]container{plain, two}, opts)
	if got := addrs(part); !reflect.DeepEqual(got, map[string]any{"plain": "127.0.0.1:32768"}) ||
		!reflect.DeepEqual(problems, []string{"container two: port 80 is not published on the host"}) {
		t.Errorf("use_published_ports: %v %v", got, problems)
	}
	// max_containers takes the first by name.
	opts = &config.DockerProvider{ExposeAll: true, LabelPrefix: "app", MaxContainers: 1}
	part, problems = translate([]container{plain, two}, opts)
	if got := addrs(part); !reflect.DeepEqual(got, map[string]any{"plain": "10.0.0.5:80"}) ||
		!reflect.DeepEqual(problems, []string{"container two: left out: max_containers, 1, are taken"}) {
		t.Errorf("max_containers: %v %v", got, problems)
	}
}

func must(part []byte, _ []string) []byte { return part }

// daemon is a fake Docker daemon on a Unix socket: it lists the containers
// a test sets, and streams the events a test sends.
type daemon struct {
	t      *testing.T
	socket string
	srv    *http.Server

	mu         sync.Mutex
	containers []container
	lists      int
	fail       bool // answer 500
	events     chan string
	closeAll   chan struct{} // closed to end the event streams open
	streams    int
}

func newDaemon(t *testing.T) *daemon {
	dir, err := os.MkdirTemp("", "fitdk") // short, for the socket's path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	d := &daemon{t: t, socket: filepath.Join(dir, "d.sock"), events: make(chan string, 16), closeAll: make(chan struct{})}
	ln, err := net.Listen("unix", d.socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.lists++
		if d.fail {
			http.Error(w, `{"message":"broken"}`, http.StatusInternalServerError)
			return
		}
		var filters map[string][]string
		if f := r.URL.Query().Get("filters"); f != "" {
			if err := json.Unmarshal([]byte(f), &filters); err != nil {
				t.Errorf("filters %q: %v", f, err)
			}
		}
		var out []container
		for _, c := range d.containers {
			keep := true
			for _, l := range filters["label"] {
				k, v, _ := strings.Cut(l, "=")
				keep = keep && c.Labels[k] == v
			}
			if keep {
				out = append(out, c)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		if f := r.URL.Query().Get("filters"); f != `{"type":["container"]}` {
			t.Errorf("events filters %q", f)
		}
		d.mu.Lock()
		d.streams++
		closeAll := d.closeAll
		d.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-closeAll:
				return
			case e := <-d.events:
				fmt.Fprintln(w, e)
				w.(http.Flusher).Flush()
			}
		}
	})
	d.srv = &http.Server{Handler: mux}
	go func() { _ = d.srv.Serve(ln) }()
	t.Cleanup(func() {
		d.endStreams()
		_ = d.srv.Close()
	})
	return d
}

func (d *daemon) set(cs ...container) {
	d.mu.Lock()
	d.containers = cs
	d.mu.Unlock()
}

// endStreams ends the event streams open, and lets the next ones last.
func (d *daemon) endStreams() {
	d.mu.Lock()
	defer d.mu.Unlock()
	close(d.closeAll)
	d.closeAll = make(chan struct{})
}

func (d *daemon) state() (lists, streams int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lists, d.streams
}

// parts collects what a provider sends.
type parts struct {
	ch chan []byte
}

func (p *parts) update(data []byte) {
	select {
	case p.ch <- data:
	default:
		// The test reads them as they come; one too many is a test bug.
		panic("parts not read")
	}
}

func (p *parts) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case data := <-p.ch:
		return decoded(t, data)
	case <-time.After(10 * time.Second):
		t.Fatal("no part came")
		return nil
	}
}

func upstreamNames(m map[string]any) []string {
	var names []string
	us, _ := m["upstreams"].([]any)
	for _, u := range us {
		names = append(names, u.(map[string]any)["name"].(string))
	}
	return names
}

func shorten(t *testing.T) {
	d, lo, hi := debounce, minBackoff, maxBackoff
	debounce, minBackoff, maxBackoff = 50*time.Millisecond, 20*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { debounce, minBackoff, maxBackoff = d, lo, hi })
}

// TestProvider follows a fake daemon: the containers at the start, a
// container started and one stopped, exec events passed over, the event
// stream lost and found again, and the daemon failing.
func TestProvider(t *testing.T) {
	shorten(t)
	d := newDaemon(t)
	web := ctr("web", map[string]string{"fit.enable": "true", "fit.routes.r.path": "/"}, "10.0.0.2", 80)
	api := ctr("api", map[string]string{"fit.enable": "true", "fit.routes.r.path": "/api/*"}, "10.0.0.3", 80)
	other := ctr("other", map[string]string{"fit.routes.r.path": "/o"}, "10.0.0.4", 80) // not enabled
	d.set(web, other)

	var logMu sync.Mutex
	var logs []string
	p := New("docker", &config.DockerProvider{Endpoint: "unix://" + d.socket, RefreshInterval: config.Duration(time.Hour)}, func(format string, args ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logMu.Unlock()
	})
	if p.Name() != "docker" {
		t.Fatalf("Name: %q", p.Name())
	}
	got := &parts{ch: make(chan []byte, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx, got.update)
	}()

	if names := upstreamNames(got.next(t)); !reflect.DeepEqual(names, []string{"web"}) {
		t.Fatalf("at the start: %v", names)
	}
	// A container starts.
	d.set(web, api, other)
	d.events <- `{"Type":"container","Action":"start","Actor":{"ID":"id-api"}}`
	if names := upstreamNames(got.next(t)); !reflect.DeepEqual(names, []string{"api", "web"}) {
		t.Fatalf("after a start: %v", names)
	}
	// Exec events, as health checks make, change nothing and list
	// nothing.
	lists, _ := d.state()
	for range 5 {
		d.events <- `{"Type":"container","Action":"exec_start: /bin/true","Actor":{"ID":"id-api"}}`
	}
	time.Sleep(100 * time.Millisecond)
	if after, _ := d.state(); after != lists {
		t.Errorf("exec events listed the containers %d times", after-lists)
	}
	// A container stops; several events make one listing.
	d.set(api, other)
	for _, a := range []string{"kill", "die", "stop"} {
		d.events <- `{"Type":"container","Action":"` + a + `","Actor":{"ID":"id-web"}}`
	}
	if names := upstreamNames(got.next(t)); !reflect.DeepEqual(names, []string{"api"}) {
		t.Fatalf("after a stop: %v", names)
	}
	if after, _ := d.state(); after != lists+1 {
		t.Errorf("three events made %d listings", after-lists)
	}
	select {
	case extra := <-got.ch:
		t.Fatalf("an unchanged listing sent a part: %s", extra)
	case <-time.After(100 * time.Millisecond):
	}

	// The event stream ends: the provider reports it, connects again and
	// lists at once, sending even an unchanged part.
	d.endStreams()
	if names := upstreamNames(got.next(t)); !reflect.DeepEqual(names, []string{"api"}) {
		t.Fatalf("after the stream came back: %v", names)
	}
	if _, streams := d.state(); streams < 2 {
		t.Fatalf("%d event streams", streams)
	}

	// The daemon fails: the provider keeps trying, and comes back once it
	// answers.
	d.mu.Lock()
	d.fail = true
	d.mu.Unlock()
	d.endStreams()
	time.Sleep(200 * time.Millisecond)
	d.set(web)
	d.mu.Lock()
	d.fail = false
	d.mu.Unlock()
	if names := upstreamNames(got.next(t)); !reflect.DeepEqual(names, []string{"web"}) {
		t.Fatalf("after the daemon came back: %v", names)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	logMu.Lock()
	defer logMu.Unlock()
	all := strings.Join(logs, "\n")
	if !strings.Contains(all, "the daemon ended the event stream") || !strings.Contains(all, "500 Internal Server Error") {
		t.Errorf("logs:\n%s", all)
	}
}

// TestProviderRefresh checks that a part is sent every refresh_interval
// whether or not it changed, and that a problem is reported once.
func TestProviderRefresh(t *testing.T) {
	shorten(t)
	d := newDaemon(t)
	d.set(ctr("web", map[string]string{"fit.enable": "true"}, "10.0.0.2", 80),
		ctr("bad", map[string]string{"fit.enable": "true"}, "10.0.0.3", 80, 81))
	var mu sync.Mutex
	var logs []string
	p := New("dk", &config.DockerProvider{Endpoint: "unix://" + d.socket, RefreshInterval: config.Duration(30 * time.Millisecond)}, func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	})
	got := &parts{ch: make(chan []byte, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx, got.update)
	for range 4 {
		if names := upstreamNames(got.next(t)); !reflect.DeepEqual(names, []string{"web"}) {
			t.Fatalf("part: %v", names)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 || logs[0] != "provider dk: container bad: it exposes TCP ports [80 81]: set fit.port" {
		t.Errorf("logs: %q", logs)
	}
}

// TestProviderNoDaemon checks that a daemon that is not there is reported
// and tried again, and that Run returns when its context ends meanwhile.
func TestProviderNoDaemon(t *testing.T) {
	shorten(t)
	var mu sync.Mutex
	n := 0
	p := New("docker", &config.DockerProvider{Endpoint: "unix:///nonexistent/fit-test.sock"}, func(format string, args ...any) {
		mu.Lock()
		n++
		mu.Unlock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx, func([]byte) { t.Error("a part without a daemon") })
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		tries := n
		mu.Unlock()
		if tries >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d tries", tries)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestReadEvents(t *testing.T) {
	changed := make(chan struct{}, 1)
	if err := readEvents(strings.NewReader("{\"Type\":\"container\",\"Action\":\"start\"}\n"), changed); err != nil || len(changed) != 1 {
		t.Errorf("a start: %v, %d", err, len(changed))
	}
	<-changed
	if err := readEvents(strings.NewReader(`{"Type":"network","Action":"connect"}`+"\n"+`{"Type":"container","Action":"exec_die"}`+"\n"), changed); err != nil || len(changed) != 0 {
		t.Errorf("a network event and an exec: %v, %d", err, len(changed))
	}
	if err := readEvents(strings.NewReader("nope\n"), changed); err == nil {
		t.Error("an event that is not JSON")
	}
	if err := readEvents(strings.NewReader(`{"Action":"`+strings.Repeat("x", maxEventBytes)+`"}`+"\n"), changed); err == nil {
		t.Error("an event past maxEventBytes")
	}
}
