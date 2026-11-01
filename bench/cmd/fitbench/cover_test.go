package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// cancelLater is a context that reports itself cancelled once Err has been
// asked after times times, but whose Done never closes: the commands it runs
// are not killed, and fitbench stops only where it checks Err.
type cancelLater struct {
	context.Context
	after atomic.Int32
}

func newCancelLater(after int32) *cancelLater {
	c := &cancelLater{Context: context.Background()}
	c.after.Store(after)
	return c
}

func (c *cancelLater) Err() error {
	if c.after.Add(-1) >= 0 {
		return nil
	}
	return context.Canceled
}

// installFakeGo puts a fake go first on PATH for t, which builds nothing
// and succeeds: the test binary again, answering as the fake docker does.
func installFakeGo(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestMainCommand runs the command itself, everything but the servers' runs.
func TestMainCommand(t *testing.T) {
	installFake(t)
	root, bin := fakeRoot(t)
	out := filepath.Join(t.TempDir(), "results.json")
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = append([]string{"fitbench"}, runArgs(root, bin, out)...)
	main()
	if rep := readReport(t, out); rep.Params.Network != "bridge" {
		t.Errorf("params: %+v", rep.Params)
	}
}

// Outside a module, fitbench needs -root.
func TestRunOutsideModule(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := run(context.Background(), []string{"-servers", "fit"}); err == nil || !strings.Contains(err.Error(), "within the fit repository") {
		t.Errorf("got %v", err)
	}
}

// With fewer than four CPUs nothing is pinned, and the load generators take
// a thread for each.
func TestRunFewCPUs(t *testing.T) {
	installFake(t, fakeRule{Match: "info", Out: "2 x86_64"})
	root, bin := fakeRoot(t)
	out := filepath.Join(t.TempDir(), "results.json")
	if err := run(context.Background(), runArgs(root, bin, out)); err != nil {
		t.Fatal(err)
	}
	if p := readReport(t, out).Params; p.ServerCPUs != "" || p.ClientCPUs != "" || p.BackendCPUs != "" || p.Connections != 256 {
		t.Errorf("params: %+v", p)
	}
}

// Without -bin, fit and idleconn are built for Docker's architecture.
func TestRunBuildsTools(t *testing.T) {
	t.Run("built", func(t *testing.T) {
		installFake(t)
		installFakeGo(t)
		root, bin := fakeRoot(t)
		out := filepath.Join(t.TempDir(), "results.json")
		if err := run(context.Background(), runArgs(root, bin, out, "-bin", "")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unknown architecture", func(t *testing.T) {
		installFake(t, fakeRule{Match: "info", Out: "16 sparc"})
		root, bin := fakeRoot(t)
		err := run(context.Background(), runArgs(root, bin, filepath.Join(t.TempDir(), "r.json"), "-bin", ""))
		if err == nil || !strings.Contains(err.Error(), "sparc") {
			t.Errorf("got %v", err)
		}
	})
}

// Each step of a run that can fail stops it.
func TestRunFailsLater(t *testing.T) {
	t.Run("certificate", func(t *testing.T) {
		installFake(t)
		root, bin := fakeRoot(t)
		work := filepath.Join(root, "bench", ".work")
		if err := os.MkdirAll(work, 0o755); err != nil {
			t.Fatal(err)
		}
		// The certificates' directory is taken by a file.
		if err := os.WriteFile(filepath.Join(work, "certs"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := run(context.Background(), runArgs(root, bin, filepath.Join(t.TempDir(), "r.json"))); err == nil {
			t.Error("no error")
		}
	})
	t.Run("config unreadable", func(t *testing.T) {
		installFake(t)
		root, bin := fakeRoot(t)
		if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "bench", "configs", "dangling.conf")); err != nil {
			t.Skip("no symbolic links here:", err)
		}
		if err := run(context.Background(), runArgs(root, bin, filepath.Join(t.TempDir(), "r.json"))); err == nil {
			t.Error("no error")
		}
	})
	// Interrupted while a backend is not ready yet, or before the servers.
	for _, tt := range []struct {
		name  string
		rules []fakeRule
	}{
		{"backend", []fakeRule{{Match: "fitbench-backend:9000/", Out: "500"}}},
		{"cache backend", []fakeRule{{Match: "fitbench-cachebackend:9003/cache/ready", Out: "500"}}},
		{"servers", nil},
	} {
		t.Run("interrupted at "+tt.name, func(t *testing.T) {
			installFake(t, tt.rules...)
			root, bin := fakeRoot(t)
			err := run(newCancelLater(0), runArgs(root, bin, filepath.Join(t.TempDir(), "r.json")))
			if err != context.Canceled {
				t.Errorf("got %v", err)
			}
		})
	}
}

// waitReady asks again until the server answers.
func TestWaitReadyRetries(t *testing.T) {
	state := installFake(t, fakeRule{Match: "127.0.0.1:8080/", Out: "500", Times: 1})
	if err := testHarness(t).waitReady(context.Background(), "x", "http://127.0.0.1:8080/"); err != nil {
		t.Fatal(err)
	}
	if n, _ := os.ReadFile(filepath.Join(state, "count-0")); string(n) != "2" {
		t.Errorf("asked %s times", n)
	}
}

func TestRunServerInterrupted(t *testing.T) {
	s := server{name: "x", scenarios: []string{"fixed"}, ready: "/fixed"}
	t.Run("after starting", func(t *testing.T) {
		installFake(t)
		h := testHarness(t)
		h.scenarios, h.protocols = []string{"fixed"}, []string{"http/1.1"}
		h.runServer(newCancelLater(0), s)
		if len(h.report.Results) != 0 {
			t.Errorf("%+v", h.report.Results)
		}
	})
	t.Run("before ready", func(t *testing.T) {
		installFake(t, fakeRule{Match: "127.0.0.1:8080/fixed", Out: "500"})
		h := testHarness(t)
		h.scenarios, h.protocols = []string{"fixed"}, []string{"http/1.1"}
		h.runServer(newCancelLater(0), s)
		if res := h.report.Results; len(res) != 1 || res[0].Error != context.Canceled.Error() {
			t.Errorf("%+v", res)
		}
	})
}

func TestMeasureIdleFails(t *testing.T) {
	t.Run("not ready", func(t *testing.T) {
		installFake(t, fakeRule{Match: "127.0.0.1:8080/fixed", Out: "500"})
		h := testHarness(t)
		h.idle = []string{"http1"}
		h.runIdle(newCancelLater(1), server{name: "x", idle: []string{"http1"}, ready: "/fixed"})
		if idle := h.report.Idle; len(idle) != 1 || idle[0].Error != context.Canceled.Error() {
			t.Errorf("%+v", idle)
		}
	})
	t.Run("no docker for idleconn", func(t *testing.T) {
		installFake(t)
		empty := t.TempDir()
		// Docker goes from PATH once the server's goroutines are counted,
		// just before idleconn runs.
		admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			os.Setenv("PATH", empty)
			_, _ = w.Write([]byte("go_goroutines 9\n"))
		}))
		defer admin.Close()
		h := testHarness(t)
		h.idle = []string{"http1"}
		h.runIdle(context.Background(), server{name: "x", idle: []string{"http1"}, ready: "/fixed", admin: strings.TrimPrefix(admin.URL, "http://")})
		if idle := h.report.Idle; len(idle) != 1 || !strings.Contains(idle[0].Error, "docker") {
			t.Errorf("%+v", idle)
		}
	})
}

// An Admin API that cuts its answer short counts nothing.
func TestGoroutinesCutShort(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("go_goroutines"))
	}))
	defer admin.Close()
	h := &harness{serverHost: "127.0.0.1"}
	if _, err := h.goroutines(context.Background(), server{admin: strings.TrimPrefix(admin.URL, "http://")}); err == nil {
		t.Error("no error")
	}
}

// The server's cgroup is found where Docker's cgroupfs layout puts it; the
// fake container's ID climbs out of /sys to a directory of the test's.
func TestFindCgroupFound(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cgroups are Linux's")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "memory.stat"), []byte("anon 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installFake(t, fakeRule{Match: "inspect", Out: "../../../.." + dir})
	if got := findCgroup(context.Background(), serverName); filepath.Clean(got) != dir {
		t.Errorf("found %q, want %q", got, dir)
	}
}

// TestMeasureRated measures the API over h2 at a fixed rate, with idle
// connections held: two load processes, whose latency percentiles come from
// their logs, and the TLS session curl reports.
func TestMeasureRated(t *testing.T) {
	ctx := context.Background()
	s := server{name: "x"}
	t.Run("ok", func(t *testing.T) {
		installFake(t,
			fakeRule{Match: "-s -v -k", Out: "* SSL connection using TLSv1.3 / TLS_AES_128_GCM_SHA256\n"},
			fakeRule{Match: "cut -f3", Out: "100 900 2000 5000\n"})
		h := testHarness(t)
		h.rate, h.holdIdleConns = 1000, 4
		cg := t.TempDir()
		if err := os.WriteFile(filepath.Join(cg, "cpu.stat"), []byte("usage_usec 1500\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		h.backendCgroups = map[string]string{backendName: cg, cacheName: cg}
		r := h.measure(ctx, s, "api", "h2", 0)
		if r.Error != "" || r.Tool != "h2load" || r.TLS != "TLSv1.3 / TLS_AES_128_GCM_SHA256" || r.IdleHeld != 4 || r.IdleClosed != 1 {
			t.Fatalf("%+v", r)
		}
		if l := r.Latency; l == nil || l.P50 != 0.1 || l.P90 != 0.9 || l.P99 != 2 || l.P999 != 5 || l.Mean <= 0 {
			t.Errorf("latency %+v", r.Latency)
		}
	})
	for _, tt := range []struct {
		name string
		rule fakeRule
		want string
	}{
		{"hold fails", fakeRule{Match: "/fitbench/idleconn", Out: "no route\n", Fail: true}, "holding idle connections: idleconn: "},
		{"percentiles fail", fakeRule{Match: "cut -f3", Fail: true}, "the latency percentiles: "},
		{"percentiles garbage", fakeRule{Match: "cut -f3", Out: "none"}, `the latency percentiles: "none`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			installFake(t, tt.rule)
			h := testHarness(t)
			h.rate, h.holdIdleConns = 1000, 4
			if r := h.measure(ctx, s, "api", "h2", 0); !strings.Contains(r.Error, tt.want) {
				t.Errorf("got %q, want %q", r.Error, tt.want)
			}
		})
	}
}

// A run without logs has no percentiles to read.
func TestRatePercentilesNoLogs(t *testing.T) {
	var l Latency
	if err := (&harness{}).ratePercentiles(context.Background(), []loadRun{{}}, &l); err != nil || l.P50 != 0 {
		t.Errorf("%v %+v", err, l)
	}
}

// A scenario whose file is missing is not measured.
func TestMeasureNoFile(t *testing.T) {
	installFake(t)
	h := testHarness(t)
	if err := os.Remove(filepath.Join(h.bench, "www", "static", "small.html")); err != nil {
		t.Fatal(err)
	}
	if r := h.measure(context.Background(), server{name: "x"}, "static", "http/1.1", 0); r.Error == "" {
		t.Errorf("%+v", r)
	}
}

// The backends' CPU is read from their cgroups when this host has them.
func TestBackendCPU(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 1500\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &harness{backendCgroups: map[string]string{backendName: dir, cacheName: filepath.Join(dir, "missing")}}
	if d, ok := h.backendCPU(backendName); !ok || d != 1500*time.Microsecond {
		t.Errorf("backend: %v %v", d, ok)
	}
	if _, ok := h.backendCPU(cacheName); ok {
		t.Error("a cgroup without cpu.stat read")
	}
	if _, ok := h.backendCPU("other"); ok {
		t.Error("a backend without a cgroup read")
	}
}

// The data files cannot be written under a file.
func TestWriteFilesUnderAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	under := filepath.Join(file, "dir")
	if err := writeSite(under, under, false); err == nil {
		t.Error("writeSite")
	}
	if err := writeBackendFiles(under); err == nil {
		t.Error("writeBackendFiles")
	}
	if err := writeData(under); err == nil {
		t.Error("writeData")
	}
	if err := writeURIs(filepath.Join(under, "uris.txt"), "http://x", "/site/"); err == nil {
		t.Error("writeURIs")
	}
}

// The website's files are the same every time, and the URIs drawn from
// them favour the most popular.
func TestSiteFiles(t *testing.T) {
	names, sizes := siteFiles()
	again, _ := siteFiles()
	if len(names) != siteCount || len(sizes) != siteCount || !slices.Equal(names, again) {
		t.Fatalf("%d names, %d sizes", len(names), len(sizes))
	}
	kinds := map[string]bool{}
	for i, n := range names {
		kinds[filepath.Ext(n)] = true
		if sizes[i] < 5<<10 || sizes[i] > 300<<10 {
			t.Errorf("%s: %d bytes", n, sizes[i])
		}
	}
	if len(kinds) != 4 {
		t.Errorf("kinds %v", kinds)
	}
	c := siteContent("a.css", 100)
	if len(c) != 100 || !strings.HasPrefix(string(c), "a.css 0123") {
		t.Errorf("content %q", c)
	}

	path := filepath.Join(t.TempDir(), "uris.txt")
	if err := writeURIs(path, "http://x", "/site/"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	top := "http://x/site/" + names[0]
	n := 0
	for _, l := range lines {
		if l == top {
			n++
		}
	}
	if len(lines) != 100000 || n < 1000 {
		t.Errorf("%d URIs, %d of the most popular", len(lines), n)
	}
}

// Each workload's wrk script, and the upload's body over h2load.
func TestLoadRunsScripts(t *testing.T) {
	h := &harness{threads: 2, conns: 2, h2conns: 2, h2streams: 1}
	wrk, h2 := protocols[0], protocols[3]
	for _, tt := range []struct {
		sc   scenario
		p    protocol
		want string
	}{
		{scenario{name: "api", kind: kindAPI}, wrk, "-s /fitbench/api.lua U"},
		{scenario{name: "site", kind: kindSite, arg: "/site/"}, wrk, "-s /fitbench/site.lua U -- /site/"},
		{scenario{name: "upload", kind: kindUpload, arg: "1048576"}, wrk, "-s /fitbench/upload.lua U -- 1048576"},
		{scenario{name: "upload", kind: kindUpload, arg: "1048576"}, h2, "-D 1 -d /fitbench/data/upload.bin U"},
	} {
		runs := h.loadRuns(tt.sc, tt.p, "U", time.Second, true)
		if got := strings.Join(runs[0].args, " "); len(runs) != 1 || !strings.HasSuffix(got, tt.want) {
			t.Errorf("%s over %s: %q, want it to end %q", tt.sc.name, tt.p.name, got, tt.want)
		}
	}
}

// The idle connections take a loopback address for each four fifths of the
// host's port range.
func TestSourcesPortRange(t *testing.T) {
	h := &harness{serverHost: "127.0.0.1", machine: Machine{PortLo: 1024, PortHi: 11024}}
	if got := h.sources(20000); got != 3 {
		t.Errorf("sources = %d, want 3", got)
	}
}

// A stub_status whose counts are cut short.
func TestParseStubStatusShort(t *testing.T) {
	if _, err := parseStubStatus("server accepts handled requests\n 1 1\n"); err == nil {
		t.Error("no error")
	}
}

// The website and the download are checked against the files written for
// them, and the website's URIs are written for h2load.
func TestMeasureSiteFiles(t *testing.T) {
	installFake(t)
	h := testHarness(t)
	h.warmup, h.data = 0, t.TempDir()
	names, _ := siteFiles()
	site := filepath.Join(h.work, "www-site")
	if err := os.MkdirAll(site, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{names[0], largeFile} {
		if err := os.WriteFile(filepath.Join(site, name), []byte(smallHTML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, s := context.Background(), server{name: "x"}
	if r := h.measure(ctx, s, "site", "h2", 0); r.Error != "" {
		t.Errorf("site: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(h.data, "uris-site-8445.txt")); err != nil {
		t.Errorf("no URIs: %v", err)
	}
	if r := h.measure(ctx, s, "download", "http/1.1", 0); r.Error != "" {
		t.Errorf("download: %+v", r)
	}
	// The URIs cannot be written under a file.
	h.data = filepath.Join(site, largeFile)
	if r := h.measure(ctx, s, "site", "h2", 0); r.Error == "" {
		t.Errorf("site with nowhere for its URIs: %+v", r)
	}
}
