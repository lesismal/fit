package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certtest"
)

// syncBuf is an output fit writes to from several goroutines, which a test
// waits on.
type syncBuf struct {
	mu   sync.Mutex
	b    bytes.Buffer
	wake chan struct{} // closed at each write
}

func newSyncBuf() *syncBuf { return &syncBuf{wake: make(chan struct{})} }

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.WriteString(plain(string(p)))
	close(s.wake)
	s.wake = make(chan struct{})
	return len(p), nil
}

// logLine is a line of fit's log in its default format, line: the time, the
// level, the message and its logger.
var logLine = regexp.MustCompile(`(?m)^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d{3} (?:DEBUG|INFO|WARN|ERROR) +(.*?) logger=[a-z_]+`)

// plain turns the lines of fit's log in s into "fit: " and their messages,
// with what follows the logger's name, so that a test can look for what fit
// says whatever time and level it says it at.
func plain(s string) string { return logLine.ReplaceAllString(s, "fit: $1") }

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitFor waits until the output holds want n times or more, and returns it.
func (s *syncBuf) waitFor(t *testing.T, want string, n int) string {
	t.Helper()
	return s.waitUntil(t, fmt.Sprintf("%d of %q", n, want), func(out string) bool { return strings.Count(out, want) >= n })
}

// waitUntil waits until ok says the output is what is waited for, and
// returns it.
func (s *syncBuf) waitUntil(t *testing.T, what string, ok func(string) bool) string {
	t.Helper()
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	for {
		s.mu.Lock()
		out, wake := s.b.String(), s.wake
		s.mu.Unlock()
		if ok(out) {
			return out
		}
		select {
		case <-wake:
		case <-timeout.C:
			t.Fatalf("waiting for %s in %q", what, out)
		}
	}
}

// served is a fit serve runs in the background.
type served struct {
	out, err *syncBuf
	reload   chan struct{}
	cancel   context.CancelFunc
	done     chan int
}

func startServe(t *testing.T, args ...string) *served {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &served{out: newSyncBuf(), err: newSyncBuf(), reload: make(chan struct{}), cancel: cancel, done: make(chan int, 1)}
	go func() { s.done <- serve(ctx, func() {}, s.reload, args, s.out, s.err) }()
	t.Cleanup(func() {
		cancel()
		<-s.done
	})
	return s
}

// stop stops fit and returns its exit code.
func (s *served) stop(t *testing.T) int {
	t.Helper()
	s.cancel()
	select {
	case code := <-s.done:
		s.done <- code // for the cleanup
		return code
	case <-time.After(60 * time.Second):
		t.Fatalf("fit did not stop: %q %q", s.out.String(), s.err.String())
		return 0
	}
}

// js quotes s as a JSON string.
func js(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

var (
	listenerRE = regexp.MustCompile(`fit: listener "[^"]+" on (\S+) `)
	adminRE    = regexp.MustCompile(`fit: admin API on (\S+)\n`)
)

// fakeOTLP is a receiver that refuses every export.
func fakeOTLP(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, "full", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestServeReload reloads a running fit, as SIGHUP does, with configs that
// apply and configs that do not, and loads one through the Admin API.
func TestServeReload(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "fit.json")
	common := `"admin": {"address": "127.0.0.1:0"},
"access_log": {"output": ` + js(filepath.Join(dir, "access.log")) + `},
"telemetry": {"endpoint": ` + js(fakeOTLP(t)) + `, "traces": {"interval": "200ms"}, "metrics": {"interval": "200ms"}},`
	writeFile(t, cfg, `{`+common+`
"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"routes": [{"handler": {"module": "respond"}}]}`)
	s := startServe(t, "-c", cfg, "-watch-certs=false")
	out := s.out.waitFor(t, "fit: exporting telemetry to ", 1)
	m := adminRE.FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, `fit: listener "web" on 127.0.0.1:`) {
		t.Fatalf("started: %q %q", out, s.err.String())
	}
	adminAddr := m[1]
	// What fails in the background is logged.
	s.err.waitFor(t, "fit: telemetry: exporting ", 1)

	// A new listener, an old name and a setting that waits for a restart.
	writeFile(t, cfg, `{`+common+`
"acme": {"email": "a@example.com"},
"listeners": [{"name": "web2", "address": "127.0.0.1:0"}],
"routes": [{"middleware": [{"module": "compress", "params": {"min_length": 512}}], "handler": {"module": "respond"}}]}`)
	s.reload <- struct{}{}
	out = s.out.waitFor(t, "(config version 2)", 1)
	stderr := s.err.String()
	if !strings.Contains(out, "fit: reloading "+cfg) || !strings.Contains(out, `fit: listener "web" on 127.0.0.1:`) ||
		!strings.Contains(out, `removed`) || !strings.Contains(out, `fit: listener "web2" on 127.0.0.1:`) ||
		!strings.Contains(stderr, "acme changed; the change takes effect when fit restarts") ||
		!strings.Contains(stderr, "min_length: deprecated") {
		t.Fatalf("reloaded: %q %q", out, stderr)
	}

	// Each reload that fails leaves the running config.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	for i, tt := range []struct{ doc, want string }{
		{`{"listeners": [`, cfg},
		{`{"listeners": []}`, "at least one listener is needed"},
		{`{"listeners": [{"name": "web", "address": ` + js(occupied.Addr().String()) + `}], "routes": [{"handler": {"module": "respond"}}]}`, "the running config stays: "},
		{"", "fit: stat "},
	} {
		if tt.doc == "" {
			if err := os.Remove(cfg); err != nil {
				t.Fatal(err)
			}
		} else {
			writeFile(t, cfg, tt.doc)
		}
		s.reload <- struct{}{}
		const failed = "fit: reload failed; the running config stays"
		s.err.waitUntil(t, tt.want, func(out string) bool {
			return strings.Count(out, failed) == i+1 && strings.Contains(out[strings.LastIndex(out, failed):], tt.want)
		})
	}

	// The Admin API loads one, and fit says so.
	doc := `{` + common + `
"listeners": [{"name": "web3", "address": "127.0.0.1:0"}],
"routes": [{"middleware": [{"module": "compress", "params": {"min_length": 512}}], "handler": {"module": "respond"}}]}`
	resp, err := http.Post("http://"+adminAddr+"/load", "application/json", strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("load: %d %s", resp.StatusCode, body)
	}
	out = s.out.waitFor(t, "fit: loaded a config from the admin API (config version 3)", 1)
	if !strings.Contains(out, `fit: listener "web3" on 127.0.0.1:`) {
		t.Errorf("loaded: %q", out)
	}
	if code := s.stop(t); code != 0 || !strings.HasSuffix(s.out.String(), "fit: shutting down\nfit: stopped\n") {
		t.Fatalf("stop: %d %q %q", code, s.out.String(), s.err.String())
	}
}

// TestServeWatch applies the config again when its file changes, and the
// certificates when theirs do.
func TestServeWatch(t *testing.T) {
	dir := t.TempDir()
	cert, err := certtest.Write(dir, "localhost")
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "fit.json")
	doc := `{"listeners": [{"name": "web", "address": "127.0.0.1:0",
  "tls": {"certificates": [{"cert_file": ` + js(cert.CertFile) + `, "key_file": ` + js(cert.KeyFile) + `}]}}],
"routes": [{"handler": {"module": "respond", "params": {"body": "%s"}}}]}`
	writeFile(t, cfg, fmt.Sprintf(doc, "one"))
	s := startServe(t, "-c", cfg, "-watch", "-watch-interval", "20ms")
	s.out.waitFor(t, "fit: watching "+cfg+"\n", 1)

	writeFile(t, cfg, fmt.Sprintf(doc, "two"))
	out := s.out.waitFor(t, "fit: reloaded "+cfg+" (config version 2)", 1)
	if !strings.Contains(out, "fit: "+cfg+" changed; reloading\n") {
		t.Errorf("changed: %q", out)
	}

	// SIGHUP reloads it too, and the watcher does not apply it again.
	writeFile(t, cfg, fmt.Sprintf(doc, "three"))
	s.reload <- struct{}{}
	s.out.waitFor(t, "fit: reloading "+cfg+"\n", 1)
	s.out.waitFor(t, "fit: reloaded "+cfg+" (config version 3)", 1)

	// A file that cannot be read is reported, and the config stays.
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	s.err.waitFor(t, "fit: watching "+cfg+": ", 1)
	writeFile(t, cfg, fmt.Sprintf(doc, "three"))

	// New certificate files are loaded; a key that does not parse is not.
	if _, err := certtest.Write(dir, "localhost"); err != nil {
		t.Fatal(err)
	}
	out = s.out.waitFor(t, "fit: reloaded certificate files (config version ", 1)
	if !strings.Contains(out, "fit: certificate files changed; reloading\n") {
		t.Errorf("certificates: %q", out)
	}
	writeFile(t, cert.KeyFile, "not a key")
	s.err.waitFor(t, "; the running certificates stay\n", 1)
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q %q", code, s.out.String(), s.err.String())
	}
}

// TestServeErrors starts fit with configs that load but cannot run.
func TestServeErrors(t *testing.T) {
	dir := t.TempDir()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	missing := js(filepath.Join(dir, "missing", "file"))
	const rest = `"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond"}}]}`
	for _, tt := range []struct{ name, doc, want string }{
		{"access log", `{"access_log": {"output": ` + missing + `}, ` + rest, "fit: access log: "},
		{"traces", `{"telemetry": {"headers_file": ` + missing + `, "traces": {}}, ` + rest, "telemetry"},
		{"metrics", `{"admin": {"address": "127.0.0.1:0"}, "telemetry": {"headers_file": ` + missing + `, "metrics": {}}, ` + rest, "telemetry"},
		{"acme", `{"acme": {"ca_file": ` + missing + `}, ` + rest, "acme.ca_file"},
		{"admin", `{"admin": {"address": "127.0.0.1:0", "token_file": ` + missing + `}, ` + rest, "fit: admin: token_file"},
		{"compile", `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond", "params": {"status": 101}}}]}`, "101 is an informational status"},
		{"listener", `{"listeners": [{"name": "web", "address": ` + js(occupied.Addr().String()) + `}], "routes": [{"handler": {"module": "respond"}}]}`, "fit: listener \"web\": "},
	} {
		cfg := filepath.Join(dir, "fit.json")
		writeFile(t, cfg, tt.doc)
		code, stdout, stderr := runFit("run", "-c", cfg)
		if code != 1 || !strings.Contains(stderr, tt.want) {
			t.Errorf("%s: %d %q %q, want %q", tt.name, code, stdout, stderr, tt.want)
		}
	}
	if code, _, stderr := runFit("run", "-bogus"); code != 2 || !strings.Contains(stderr, "-bogus") {
		t.Errorf("bad flag: %d %q", code, stderr)
	}
}

// TestServeShutdownTimeout shuts down with a request in flight past the
// timeout.
func TestServeShutdownTimeout(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer up.Close()
	defer close(release)

	cfg := filepath.Join(t.TempDir(), "fit.json")
	writeFile(t, cfg, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"upstreams": [{"name": "app", "addresses": [`+js(strings.TrimPrefix(up.URL, "http://"))+`]}],
"routes": [{"handler": {"module": "proxy", "params": {"upstream": "app"}}}]}`)
	s := startServe(t, "-c", cfg, "-watch-certs=false", "-shutdown-timeout", "200ms")
	m := listenerRE.FindStringSubmatch(s.out.waitFor(t, "fit: listener ", 1))
	conn, err := net.Dial("tcp", m[1])
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatalf("the request did not reach the upstream: %q", s.err.String())
	}
	if code := s.stop(t); code != 1 || !strings.Contains(s.err.String(), "fit: shutdown took longer than 200ms; connections still open were closed\n") {
		t.Fatalf("%d %q %q", code, s.out.String(), s.err.String())
	}
}

// fakeKV answers Consul's KV API for one key, fit/a, with blocking queries.
type fakeKV struct {
	mu    sync.Mutex
	index uint64
	value string
	wake  chan struct{} // closed at each change
	reads int           // the reads that do not block
	read  chan struct{} // closed at each of them
	srv   *httptest.Server
}

func newFakeKV(t *testing.T, value string) *fakeKV {
	f := &fakeKV{index: 10, value: value, wake: make(chan struct{}), read: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKV) set(value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.value = value
	f.index++
	close(f.wake)
	f.wake = make(chan struct{})
}

// waitReads waits for n more reads that do not block.
func (f *fakeKV) waitReads(t *testing.T, n int) {
	t.Helper()
	f.mu.Lock()
	want := f.reads + n
	f.mu.Unlock()
	timeout := time.After(30 * time.Second)
	for {
		f.mu.Lock()
		reads, read := f.reads, f.read
		f.mu.Unlock()
		if reads >= want {
			return
		}
		select {
		case <-read:
		case <-timeout:
			t.Fatalf("waiting for %d reads", n)
		}
	}
}

func (f *fakeKV) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if r.URL.Query().Get("index") == "" {
		f.reads++
		close(f.read)
		f.read = make(chan struct{})
	}
	if idx := r.URL.Query().Get("index"); idx != "" {
		want, _ := strconv.ParseUint(idx, 10, 64)
		for f.index <= want {
			wake := f.wake
			f.mu.Unlock()
			select {
			case <-wake:
			case <-r.Context().Done():
				return
			}
			f.mu.Lock()
		}
	}
	index, value := f.index, f.value
	f.mu.Unlock()
	w.Header().Set("X-Consul-Index", strconv.FormatUint(index, 10))
	_ = json.NewEncoder(w).Encode([]struct {
		Key   string
		Value []byte
	}{{"fit/a", []byte(value)}})
}

// TestServeProviders applies what a provider provides, and reports what is
// refused.
func TestServeProviders(t *testing.T) {
	kv := newFakeKV(t, `{"routes": [{"match": {"path": ["/kv"]}, "handler": {"module": "respond"}}]}`)
	cfg := filepath.Join(t.TempDir(), "fit.json")
	writeFile(t, cfg, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
"providers": [{"name": "kv", "consul_kv": {"endpoint": `+js(kv.srv.URL)+`, "refresh_interval": "50ms"}}],
"routes": [{"handler": {"module": "respond"}}]}`)
	s := startServe(t, "-c", cfg, "-watch-certs=false")
	out := s.out.waitFor(t, "fit: applied what provider kv provides (config version ", 1)
	if !strings.Contains(out, "fit: provider kv ") {
		t.Errorf("started: %q", out)
	}
	kv.set(`{"routes": [{"handler": {"module": "respond", "params": {"status": 101}}}]}`)
	stderr := s.err.waitFor(t, "101 is an informational status", 1)
	if !strings.Contains(stderr, "fit: what provider kv provides was refused; the running config stays\nfit: provider kv") {
		t.Errorf("refused: %q", stderr)
	}
	// Sent again each refresh_interval, it is refused again, and not
	// reported again.
	kv.waitReads(t, 3)
	if n := strings.Count(s.err.String(), "was refused"); n != 1 {
		t.Errorf("reported %d times: %q", n, s.err.String())
	}
	kv.set(`{"routes": [{"match": {"path": ["/kv2"]}, "handler": {"module": "respond"}}]}`)
	s.out.waitFor(t, "fit: applied what provider kv provides (config version ", 2)
	// A key that holds no object is left out, and the provider says so.
	kv.set(`[1]`)
	s.err.waitFor(t, "fit: provider kv: ", 1)
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q %q", code, s.out.String(), s.err.String())
	}
}

// TestServeReloadYAML reloads a YAML config that no longer parses.
func TestServeReloadYAML(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "fit.yaml")
	writeFile(t, cfg, "listeners:\n  - name: web\n    address: 127.0.0.1:0\nroutes:\n  - handler: {module: respond}\n")
	s := startServe(t, "-c", cfg, "-watch-certs=false")
	s.out.waitFor(t, "fit: listener ", 1)
	writeFile(t, cfg, "listeners: [\n")
	s.reload <- struct{}{}
	s.err.waitFor(t, "fit: reload failed; the running config stays\nfit: "+cfg, 1)
	if code := s.stop(t); code != 0 {
		t.Fatalf("stop: %d %q %q", code, s.out.String(), s.err.String())
	}
}
