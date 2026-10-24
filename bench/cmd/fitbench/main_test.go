package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// runArgs are the flags of a run against the fake docker, quick by default:
// no scenario and no idle connections; extra flags override them.
func runArgs(root, bin, out string, extra ...string) []string {
	return append([]string{"-root", root, "-bin", bin, "-out", out, "-servers", "fit", "-scenarios", "", "-idle", "",
		"-protocols", "http/1.1,h3", "-network", "bridge"}, extra...)
}

func readReport(t *testing.T, path string) Report {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestRunFlagErrors(t *testing.T) {
	for _, args := range [][]string{
		{"-nope"},
		{"-servers", "bogus"},
		{"-scenarios", "bogus"},
		{"-protocols", "bogus"},
		{"-idle", "bogus"},
	} {
		if err := run(context.Background(), args); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
}

func TestRunFails(t *testing.T) {
	for _, tt := range []struct {
		name  string
		rules []fakeRule
		extra []string
		// noConfigs leaves bench/configs out of the root, noBin the tools.
		noConfigs, noBin bool
		// out is where the results go, under the test's directory;
		// "file/x.json" is under a file, "dir" a directory.
		out string
	}{
		{name: "info", rules: []fakeRule{{Match: "info", Fail: true}}},
		{name: "info garbage", rules: []fakeRule{{Match: "info", Out: "many"}}},
		{name: "network flag", extra: []string{"-network", "bogus"}},
		{name: "no tools", noBin: true},
		{name: "no configs", noConfigs: true},
		{name: "loadgen build", rules: []fakeRule{{Match: "-t fitbench-loadgen --build-arg", Fail: true}}},
		{name: "network create", rules: []fakeRule{{Match: "network create", Fail: true}}},
		{name: "www copy", rules: []fakeRule{{Match: "cp -r", Fail: true}}},
		{name: "loadgen run", rules: []fakeRule{{Match: "--name fitbench-loadgen --ulimit", Fail: true}}},
		{name: "h3 build", rules: []fakeRule{{Match: "Dockerfile.h3", Fail: true}}},
		{name: "h3 run", rules: []fakeRule{{Match: "--name fitbench-loadgen-h3 --ulimit", Fail: true}}},
		{name: "backend", rules: []fakeRule{{Match: "--name fitbench-backend ", Fail: true}}},
		{name: "ws backend", rules: []fakeRule{{Match: "--name fitbench-wsbackend", Fail: true}}},
		{name: "cache backend", rules: []fakeRule{{Match: "--name fitbench-cachebackend", Fail: true}}},
		{name: "out under a file", out: "file/x.json"},
		{name: "out a directory", out: "dir"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			installFake(t, tt.rules...)
			root, bin := fakeRoot(t)
			if tt.noConfigs {
				if err := os.RemoveAll(filepath.Join(root, "bench", "configs")); err != nil {
					t.Fatal(err)
				}
			}
			if tt.noBin {
				bin = filepath.Join(bin, "missing")
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "dir"), 0o755); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "results.json")
			if tt.out != "" {
				out = filepath.Join(dir, filepath.FromSlash(tt.out))
			}
			if err := run(context.Background(), runArgs(root, bin, out, tt.extra...)); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

// TestRunModuleRoot runs fitbench without -root, from within the module,
// up to Docker failing.
func TestRunModuleRoot(t *testing.T) {
	installFake(t, fakeRule{Match: "info", Fail: true})
	err := run(context.Background(), []string{"-servers", "fit"})
	if err == nil || !strings.Contains(err.Error(), "docker info") {
		t.Fatalf("got %v, want docker info's error", err)
	}
}

// TestRunQuick runs everything but the servers' runs, over host networking.
func TestRunQuick(t *testing.T) {
	installFake(t)
	root, bin := fakeRoot(t)
	out := filepath.Join(t.TempDir(), "sub", "results.json")
	if err := run(context.Background(), runArgs(root, bin, out, "-network", "host", "-nginx-image", "nginx:test")); err != nil {
		t.Fatal(err)
	}
	rep := readReport(t, out)
	p := rep.Params
	if p.Network != "host" || p.ServerCPUs != "0-6" || p.ClientCPUs != "7-13" || p.BackendCPUs != "14-15" || p.Connections != 256 {
		t.Errorf("params: %+v", p)
	}
	if rep.Host["docker_cpus"] != "16" || rep.Host["kernel"] != "Linux 6.8.0" || rep.Host["memory"] != "32768 kB" ||
		rep.Host["architecture"] != "x86_64" || rep.Host["model_name"] == "" {
		t.Errorf("host: %v", rep.Host)
	}
	if v := rep.Versions; !strings.HasPrefix(v["wrk"], "wrk 4.2.0") || v["h2load"] != "h2load nghttp2/1.64.0" ||
		v["h2load-h3"] != "h2load nghttp2/1.64.0" || v["docker"] != "27.3.1" {
		t.Errorf("versions: %v", v)
	}
	work := filepath.Join(root, "bench", ".work")
	for name, want := range map[string]string{
		"configs/fit/fit.konf":     `backend 127.0.0.1:9000 workers 7`,
		"configs/nginx/nginx.conf": "127.0.0.1:9000\n",
		"fit":                      "binary",
		"idleconn":                 "binary",
	} {
		if data, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(name))); err != nil || string(data) != want {
			t.Errorf("%s: %q %v, want %q", name, data, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "certs", "cert.pem")); err != nil {
		t.Error(err)
	}
}

// TestRunAll runs two servers through a scenario each protocol serves, one
// only HTTP/1.1 does, and idle connections, over a bridge network.
func TestRunAll(t *testing.T) {
	installFake(t)
	root, bin := fakeRoot(t)
	out := filepath.Join(t.TempDir(), "results.json")
	err := run(context.Background(), runArgs(root, bin, out, "-servers", "traefik,fit", "-scenarios", "fixed,cache-zipf",
		"-idle", "http1", "-idle-settle", "1ms", "-warmup", "1s", "-duration", "1s",
		"-server-cpus", "0-3", "-client-cpus", "4-7", "-backend-cpus", "8"))
	if err != nil {
		t.Fatal(err)
	}
	rep := readReport(t, out)
	if p := rep.Params; p.ServerCPUs != "0-3" || p.ClientCPUs != "4-7" || p.BackendCPUs != "8" {
		t.Errorf("params: %+v", p)
	}
	got := map[string]Result{}
	for _, r := range rep.Results {
		got[r.Server+" "+r.Scenario+" "+r.Protocol] = r
	}
	if len(rep.Results) != 8 || len(got) != 8 {
		t.Fatalf("%d results: %+v", len(rep.Results), rep.Results)
	}
	for _, key := range []string{"traefik fixed http/1.1", "traefik fixed h3", "traefik cache-zipf http/1.1", "fit cache-zipf h3"} {
		if got[key].Skipped == "" {
			t.Errorf("%s not skipped: %+v", key, got[key])
		}
	}
	if r := got["fit fixed http/1.1"]; r.Error != "" || r.Requests != 1000000 || r.Tool != "wrk" || r.MemoryBytes != 4194304 {
		t.Errorf("fixed over HTTP/1.1: %+v", r)
	}
	if r := got["fit fixed h3"]; r.Error != "" || r.Requests != 1603673 || r.Tool != "h2load-h3" || r.InFlight != 64*32 {
		t.Errorf("fixed over HTTP/3: %+v", r)
	}
	// The cache backend's count is out of reach on a bridge network.
	if r := got["fit cache-zipf http/1.1"]; !strings.Contains(r.Error, "the backend's count") {
		t.Errorf("cache-zipf: %+v", r)
	}
	if len(rep.Idle) != 2 || rep.Idle[1].Server != "fit" || rep.Idle[1].Connections != 4 || rep.Idle[1].ClosedByServer != 1 ||
		rep.Idle[1].AnonBefore != 1<<20 || rep.Idle[1].MemoryAfter != 4194304 {
		t.Errorf("idle: %+v", rep.Idle)
	}
	if rep.Versions["fit"] != "fake 1.0; built today" || rep.Images["fit"] != "image@sha256:0123" || rep.StartupAnon["fit"] != 1<<20 {
		t.Errorf("versions %v, images %v, anon %v", rep.Versions, rep.Images, rep.StartupAnon)
	}
}

// testHarness is a harness as run sets one up over host networking, on the
// fake root.
func testHarness(t *testing.T) *harness {
	t.Helper()
	root, _ := fakeRoot(t)
	bench := filepath.Join(root, "bench")
	return &harness{
		bench: bench, work: filepath.Join(bench, ".work"), images: map[string]string{"x": "x:1"},
		serverHost: "127.0.0.1", cacheAdmin: "127.0.0.1:9004", threads: 2, conns: 4, h2conns: 2, h2streams: 4,
		warmup: time.Second, duration: 2 * time.Second, idleConns: 50000, idleSettle: time.Millisecond,
		report: Report{Host: map[string]string{}, Versions: map[string]string{}, Images: map[string]string{},
			StartupGoroutines: map[string]int{}, StartupAnon: map[string]int64{}},
	}
}

func TestMeasure(t *testing.T) {
	ctx := context.Background()
	s := server{name: "x"}
	t.Run("cache", func(t *testing.T) {
		installFake(t)
		h := testHarness(t)
		r := h.measure(ctx, s, "cache-hot", "http/1.1", 1<<20)
		if r.Error != "" || r.HitRatio == nil || *r.HitRatio != 1 || r.BackendRequests != 0 || r.Requests != 1000000 ||
			r.AnonStartBytes != 1<<20 || r.AnonBytes != 1<<20 || r.MemoryBytes != 4194304 || r.InFlight != 4 || r.CPUPerRequestUS < 0 {
			t.Errorf("%+v", r)
		}
	})
	t.Run("h2c", func(t *testing.T) {
		installFake(t)
		h := testHarness(t)
		h.warmup = 0
		r := h.measure(ctx, s, "fixed", "h2c", 0)
		if r.Error != "" || r.Tool != "h2load" || r.InFlight != 8 || r.HitRatio != nil || r.Requests != 1603673 {
			t.Errorf("%+v", r)
		}
	})
	for _, tt := range []struct {
		name string
		rule fakeRule
		want string
	}{
		{"check", fakeRule{Match: "%{http_version}", Fail: true}, "check "},
		{"warmup", fakeRule{Match: "-d 1s", Fail: true}, "warmup: "},
		{"count before", fakeRule{Match: "9004/status", Fail: true}, "the backend's count"},
		{"count after", fakeRule{Match: "9004/status", Fail: true, After: 1}, "the backend's count"},
		{"load", fakeRule{Match: "-d 2s", Fail: true}, "docker exec"},
		{"report", fakeRule{Match: "-d 2s", Out: "garbage"}, "wrk printed no report"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			installFake(t, tt.rule)
			h := testHarness(t)
			if r := h.measure(ctx, s, "cache-hot", "http/1.1", 0); !strings.Contains(r.Error, tt.want) {
				t.Errorf("got %q, want %q", r.Error, tt.want)
			}
		})
	}
}

func TestRunServer(t *testing.T) {
	ctx := context.Background()
	t.Run("start fails", func(t *testing.T) {
		installFake(t, fakeRule{Match: "--name fitbench-server", Fail: true})
		h := testHarness(t)
		h.scenarios, h.protocols = []string{"fixed", "static"}, []string{"http/1.1", "h2c"}
		h.runServer(ctx, server{name: "x", scenarios: []string{"fixed"}, ready: "/fixed"})
		res := h.report.Results
		if len(res) != 4 || res[0].Skipped == "" || res[1].Skipped == "" || res[2].Error == "" || res[3].Error == "" {
			t.Errorf("%+v", res)
		}
	})
	t.Run("restart fails", func(t *testing.T) {
		installFake(t, fakeRule{Match: "--name fitbench-server", Fail: true, After: 1})
		admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("go_goroutines 9\n"))
		}))
		defer admin.Close()
		h := testHarness(t)
		h.scenarios, h.protocols = []string{"fixed"}, []string{"http/1.1", "h2c"}
		h.runServer(ctx, server{name: "x", scenarios: []string{"fixed"}, ready: "/fixed", version: []string{"x", "-v"},
			admin: strings.TrimPrefix(admin.URL, "http://")})
		res := h.report.Results
		if len(res) != 2 || res[0].Error != "" || res[0].GoroutinesMax != 9 || res[1].Error == "" {
			t.Errorf("%+v", res)
		}
		if h.report.Versions["x"] != "fake 1.0" || h.report.Images["x"] != "image@sha256:0123" || h.report.StartupGoroutines["x"] != 9 {
			t.Errorf("%+v", h.report)
		}
		if h.serverCgroup != "" {
			t.Errorf("cgroup %q left", h.serverCgroup)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		installFake(t)
		h := testHarness(t)
		h.scenarios, h.protocols = []string{"fixed"}, []string{"http/1.1"}
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		h.runServer(ctx, server{name: "x", scenarios: []string{"fixed"}, ready: "/fixed"})
		if res := h.report.Results; len(res) != 1 || res[0].Error == "" {
			t.Errorf("%+v", res)
		}
	})
}

func TestRunIdle(t *testing.T) {
	ctx := context.Background()
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("go_goroutines 9\n"))
	}))
	defer admin.Close()
	s := server{name: "x", idle: []string{"http1"}, ready: "/fixed", admin: strings.TrimPrefix(admin.URL, "http://")}
	t.Run("measured", func(t *testing.T) {
		installFake(t)
		h := testHarness(t)
		h.idle = []string{"http1", "ws"}
		h.runIdle(ctx, s)
		idle := h.report.Idle
		if len(idle) != 2 || idle[1].Skipped == "" {
			t.Fatalf("%+v", idle)
		}
		r := idle[0]
		if r.Error != "" || r.Connections != 4 || r.ClosedByServer != 1 || r.BytesPerConn != 0 ||
			r.GoroutinesBefore != 9 || r.GoroutinesAfter != 9 || r.AnonAfter != 1<<20 || r.MemoryBefore != 4194304 {
			t.Errorf("%+v", r)
		}
	})
	for _, tt := range []struct {
		name string
		rule fakeRule
		want string
	}{
		{"start fails", fakeRule{Match: "--name fitbench-server", Fail: true}, "docker run"},
		{"idleconn fails", fakeRule{Match: "/fitbench/idleconn", Out: "", Fail: true}, "idleconn: "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			installFake(t, tt.rule)
			h := testHarness(t)
			h.idle = []string{"http1"}
			h.runIdle(ctx, s)
			if idle := h.report.Idle; len(idle) != 1 || !strings.Contains(idle[0].Error, tt.want) {
				t.Errorf("got %+v, want %q", idle, tt.want)
			}
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		h := testHarness(t)
		h.idle = []string{"http1"}
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		h.runIdle(ctx, s)
		if len(h.report.Idle) != 0 {
			t.Errorf("%+v", h.report.Idle)
		}
	})
}

func TestCheck(t *testing.T) {
	ctx := context.Background()
	t.Run("ok", func(t *testing.T) {
		installFake(t)
		h := testHarness(t)
		for _, p := range []string{"http/1.1", "h2c", "h2", "h3"} {
			if err := h.check(ctx, "https://127.0.0.1:8445/static/small.html", p, ""); err != nil {
				t.Errorf("%s: %v", p, err)
			}
		}
	})
	t.Run("no static file", func(t *testing.T) {
		h := testHarness(t)
		h.bench = t.TempDir()
		if err := h.check(ctx, "http://127.0.0.1:8080/static/small.html", "http/1.1", ""); err == nil {
			t.Error("no error")
		}
	})
	for _, tt := range []struct{ name, out, want string }{
		{"no status", "nonewline", "curl printed"},
		{"wrong body", "wrong\n200 1.1", "got "},
		{"wrong version", "hello, world\n200 2", "got "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			installFake(t, fakeRule{Match: "%{http_version}", Out: tt.out})
			h := testHarness(t)
			if err := h.check(ctx, "http://127.0.0.1:8080/fixed", "http/1.1", "hello, world"); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadRuns(t *testing.T) {
	h := &harness{threads: 4, conns: 2, h2conns: 8, h2streams: 16}
	wrk, h2, h3 := protocols[0], protocols[3], protocols[4]
	headers := strings.Join(headerFlags(apiHeaders), " ")
	for _, tt := range []struct {
		name string
		sc   scenario
		p    protocol
		d    time.Duration
		rate int
		want []string
	}{
		{"wrk", scenario{name: "fixed"}, wrk, 30 * time.Second, 0,
			[]string{"exec fitbench-loadgen wrk -t 4 -c 4 -d 30s --timeout 10s --latency -s /fitbench/report.lua U"}},
		{"wrk script", scenario{name: "cache-zipf", script: "zipf.lua"}, wrk, 100 * time.Millisecond, 0,
			[]string{"exec fitbench-loadgen wrk -t 4 -c 4 -d 1s --timeout 10s --latency -s /fitbench/zipf.lua U"}},
		{"wrk churn", scenario{name: "churn-10", kind: kindChurn, arg: "10"}, wrk, 5 * time.Second, 0,
			[]string{"exec fitbench-loadgen wrk -t 4 -c 4 -d 5s --timeout 10s --latency -s /fitbench/churn.lua U -- 10"}},
		{"h2load", scenario{name: "fixed"}, h2, 5 * time.Second, 0,
			[]string{"exec fitbench-loadgen h2load -t 4 -c 8 -m 16 -D 5 U"}},
		{"h2load-h3", scenario{name: "fixed"}, h3, 5 * time.Second, 0,
			[]string{"exec fitbench-loadgen-h3 h2load -t 4 -c 8 -m 16 -D 5 --alpn-list=h3 U"}},
		{"api over h2", scenario{name: "api", kind: kindAPI}, h2, 5 * time.Second, 0, []string{
			"exec fitbench-loadgen h2load -t 3 -c 6 -m 16 -D 5 " + headers + " U",
			"exec fitbench-loadgen h2load -t 1 -c 2 -m 16 -D 5 " + headers + " -d /fitbench/data/api-post.bin U"}},
		{"api at a rate over HTTP/1.1", scenario{name: "api", kind: kindAPI}, wrk, 5 * time.Second, 1000, []string{
			"exec fitbench-loadgen h2load -t 3 -c 3 -m 1 -D 5 --h1 " + headers + " --rps 267 --log-file /tmp/fitbench-0.log U",
			"exec fitbench-loadgen h2load -t 1 -c 1 -m 1 -D 5 --h1 " + headers + " -d /fitbench/data/api-post.bin --rps 200 --log-file /tmp/fitbench-1.log U"}},
		{"site", scenario{name: "site", kind: kindSite, arg: "/site/"}, h2, 5 * time.Second, 0, []string{
			"exec fitbench-loadgen h2load -t 4 -c 8 -m 16 -D 5 " + strings.Join(headerFlags(browserHeaders), " ") + " -i /fitbench/data/uris-site-8445.txt"}},
	} {
		h.rate = tt.rate
		runs := h.loadRuns(tt.sc, tt.p, "U", tt.d, true)
		var got []string
		for _, run := range runs {
			got = append(got, strings.Join(run.args, " "))
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.want)
		}
	}
}

func TestGoroutines(t *testing.T) {
	ctx := context.Background()
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			_, _ = w.Write([]byte("go_goroutines 7\n"))
		}
	}))
	defer metrics.Close()
	addr := strings.TrimPrefix(metrics.URL, "http://")
	h := &harness{serverHost: "127.0.0.1"}
	if n, err := h.goroutines(ctx, server{admin: addr}); n != 7 || err != nil {
		t.Errorf("%d %v", n, err)
	}
	if !h.canCount(server{admin: addr}) || h.canCount(server{}) || (&harness{serverHost: serverName}).canCount(server{admin: addr}) {
		t.Error("canCount")
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closedAddr := strings.TrimPrefix(closed.URL, "http://")
	closed.Close()
	empty := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer empty.Close()
	for _, admin := range []string{"%zz", closedAddr, strings.TrimPrefix(empty.URL, "http://")} {
		if _, err := h.goroutines(ctx, server{admin: admin}); err == nil {
			t.Errorf("%s: no error", admin)
		}
	}
}

func TestSample(t *testing.T) {
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("go_goroutines 7\n"))
	}))
	defer metrics.Close()
	cgroup := t.TempDir()
	if err := os.WriteFile(filepath.Join(cgroup, "memory.stat"), []byte("anon 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &harness{serverHost: "127.0.0.1", serverCgroup: cgroup}
	sm := h.sample(context.Background(), server{admin: strings.TrimPrefix(metrics.URL, "http://")})
	if g, a := sm.stop(); g != 7 || a != 5 {
		t.Errorf("%d %d", g, a)
	}
	if g, a := sm.stop(); g != 7 || a != 5 {
		t.Errorf("again: %d %d", g, a)
	}
	// Nothing to sample.
	none := (&harness{}).sample(context.Background(), server{})
	if g, a := none.stop(); g != 0 || a != 0 {
		t.Errorf("none: %d %d", g, a)
	}
}

func TestServerFiles(t *testing.T) {
	ctx := context.Background()
	cgroup := t.TempDir()
	for name, data := range map[string]string{"memory.stat": "anon 5\n", "cpu.stat": "usage_usec 3\n"} {
		if err := os.WriteFile(filepath.Join(cgroup, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &harness{serverCgroup: cgroup}
	if n, ok := h.serverAnon(ctx); n != 5 || !ok {
		t.Errorf("anon %d %v", n, ok)
	}
	if d, ok := h.serverCPU(ctx); d != 3*time.Microsecond || !ok {
		t.Errorf("cpu %v %v", d, ok)
	}
	if _, err := h.serverFile(ctx, "memory.current"); err == nil {
		t.Error("read a missing file")
	}

	// Through docker exec, which fails.
	installFake(t, fakeRule{Match: "cat", Fail: true})
	h.serverCgroup = ""
	if _, ok := h.serverAnon(ctx); ok {
		t.Error("anon read")
	}
	if _, ok := h.serverCPU(ctx); ok {
		t.Error("cpu read")
	}
	if anon, total := h.serverMemory(ctx); anon != 0 || total != 0 {
		t.Errorf("memory %d %d", anon, total)
	}
}

func TestFindCgroup(t *testing.T) {
	installFake(t, fakeRule{Match: "inspect", Fail: true, After: 1})
	// The fake container's cgroup is nowhere on this host.
	if dir := findCgroup(context.Background(), serverName); dir != "" {
		t.Errorf("found %q", dir)
	}
	if dir := findCgroup(context.Background(), serverName); dir != "" {
		t.Errorf("found %q", dir)
	}
}

func TestDockerHelpers(t *testing.T) {
	ctx := context.Background()
	installFake(t, fakeRule{Match: "image inspect", Fail: true}, fakeRule{Match: "curl", Out: "000"})
	if got := imageDigest(ctx, "x:1"); got != "x:1" {
		t.Errorf("digest %q", got)
	}
	if _, err := (&harness{}).backendCount(ctx, "cache"); err == nil {
		t.Error("counted over a bridge network")
	}
	cleanup(true)

	h := testHarness(t)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := h.waitReady(cancelled, "x", "http://127.0.0.1:8080/"); err == nil {
		t.Error("ready when cancelled")
	}
}

func TestWaitReady(t *testing.T) {
	installFake(t)
	if err := testHarness(t).waitReady(context.Background(), "x", "http://127.0.0.1:8080/"); err != nil {
		t.Error(err)
	}
}

func TestBuildTools(t *testing.T) {
	h := &harness{work: t.TempDir()}
	if err := h.buildTools(context.Background(), t.TempDir(), "mips"); err == nil || !strings.Contains(err.Error(), "mips") {
		t.Errorf("unknown architecture: %v", err)
	}
	// An empty directory is no module to build in.
	if err := h.buildTools(context.Background(), t.TempDir(), "x86_64"); err == nil || !strings.Contains(err.Error(), "building ") {
		t.Errorf("empty root: %v", err)
	}
}

func TestCopyToolsUnwritable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&harness{work: filepath.Join(file, "work")}).copyTools(t.TempDir()); err == nil {
		t.Error("no error")
	}
	// The tools are read, but cannot be written over a directory.
	_, bin := fakeRoot(t)
	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, "fit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (&harness{work: work}).copyTools(bin); err == nil {
		t.Error("wrote over a directory")
	}
}

func TestRenderConfigsUnwritable(t *testing.T) {
	root, _ := fakeRoot(t)
	bench := filepath.Join(root, "bench")
	// A file where the rendered configs' directory goes.
	work := filepath.Join(bench, ".work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "configs"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (&harness{bench: bench, work: work}).renderConfigs(nil); err == nil {
		t.Error("no error")
	}
}

func TestWriteCert(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs")
	if err := writeCert(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("no PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname(serverName); err != nil {
		t.Error(err)
	}
	if err := cert.VerifyHostname("127.0.0.1"); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "key.pem")); err != nil {
		t.Error(err)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeCert(filepath.Join(file, "certs")); err == nil {
		t.Error("wrote under a file")
	}
	// cert.pem cannot be written over a directory.
	dir = t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "cert.pem"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeCert(dir); err == nil {
		t.Error("wrote cert.pem over a directory")
	}
}

func TestHelpers(t *testing.T) {
	want := "bridge"
	if runtime.GOOS == "linux" {
		want = "host"
	}
	if got := defaultNetwork(); got != want {
		t.Errorf("defaultNetwork %q", got)
	}
	if got := scenarioOf("cache-zipf"); got.script != "zipf.lua" {
		t.Errorf("scenarioOf %+v", got)
	}
	if got := strings.Join(protocolNames(), ","); got != "http/1.1,https/1.1,h2c,h2,h3" {
		t.Errorf("protocolNames %s", got)
	}
	if got := strings.Join(idleKindNames(), ","); got != "http1,tls,ws" {
		t.Errorf("idleKindNames %s", got)
	}
	got, err := pick("fit,nginx", servers, func(s server) string { return s.name })
	if err != nil || len(got) != 2 || got[0].name != "nginx" || got[1].name != "fit" {
		t.Errorf("pick %+v %v", got, err)
	}
	for _, tt := range []struct{ out, prefix, want string }{
		{"noise\n  wrk 4.2.0 x\nmore", "wrk ", "wrk 4.2.0 x"},
		{"  nothing matches \n", "wrk ", "nothing matches"},
	} {
		if got := firstLine(tt.out, tt.prefix); got != tt.want {
			t.Errorf("firstLine(%q) = %q", tt.out, got)
		}
	}
	if _, err := parseH2Duration("1.2xs"); err == nil {
		t.Error("parsed 1.2xs")
	}
}

func TestParseErrors(t *testing.T) {
	var r Result
	for _, out := range []string{
		"FITBENCH {",
		`FITBENCH {"requests":1,"duration_us":0}`,
	} {
		if err := parseWrk(out, &r); err == nil {
			t.Errorf("parseWrk(%q): no error", out)
		}
	}
	finished := "finished in 1s, 10.00 req/s, 1MB/s\n"
	requests := "requests: 10 total, 10 started, 10 done, 10 succeeded, 0 failed, 0 errored, 0 timeout\n"
	for _, out := range []string{
		finished,
		finished + requests,
		finished + requests + "time for request:  1us  2xs  3us  4us  50%\n",
	} {
		if err := parseH2load(out, &r); err == nil {
			t.Errorf("parseH2load(%q): no error", out)
		}
	}
	if err := parseH2load(finished+requests+"time for request:  1us  2us  3us  4us  50%\n", &r); err != nil || r.ErrorKinds != nil {
		t.Errorf("no status codes: %v %+v", err, r)
	}
	if _, err := parseStubStatus("server accepts handled requests\n 1 1 x\n"); err == nil {
		t.Error("parsed x")
	}
	if _, err := parseGoroutines("go_goroutines x\n"); err == nil {
		t.Error("parsed x")
	}
}

func TestPrint(t *testing.T) {
	ratio := 0.5
	results := []Result{
		{Server: "a", Scenario: "fixed", Protocol: "h2c", Skipped: "cannot"},
		{Server: "b", Scenario: "fixed", Protocol: "h2c", Error: "failed"},
		{Server: "c", Scenario: "fixed", Protocol: "h2c", RequestsPerSec: 10, Latency: &Latency{Mean: 1, Max: 2},
			HitRatio: &ratio, GoroutinesMax: 12},
		{Server: "c", Scenario: "proxy", Protocol: "h2c", RequestsPerSec: 10, Latency: &Latency{}},
	}
	for _, r := range results {
		printResult(r)
	}
	idle := []IdleResult{
		{Server: "c", Kind: "http1", Connections: 4, BytesPerConn: 100, GoroutinesBefore: 3, GoroutinesAfter: 7},
		{Server: "d", Kind: "http1", Error: "failed"},
		{Server: "d", Kind: "tls", Skipped: "cannot"},
	}
	for _, r := range idle {
		printIdle(r)
	}
	printSummary(results, idle)
	printSummary(nil, idle)
	printSummary(nil, nil)
	printGoroutines(Report{Results: results, Idle: idle, StartupGoroutines: map[string]int{"c": 3, "a": 1}})
}
