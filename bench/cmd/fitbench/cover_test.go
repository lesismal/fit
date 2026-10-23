package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
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
	if got := findCgroup(context.Background()); filepath.Clean(got) != dir {
		t.Errorf("found %q, want %q", got, dir)
	}
}
