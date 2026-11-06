package vod

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/medialib"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// copyLib copies testdata/lib to a new directory, which the test may
// change, and returns it.
func copyLib(t testing.TB) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.Walk("testdata/lib", func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("testdata/lib", p)
		to := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(to, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// fit is fit serving a config with a library and a vod route, as it does:
// compiled, swapped in, its library applied as the role does, and served.
type fit struct {
	env    runtime.Env
	rt     *runtime.Runtime
	server *listener.Server
	base   string
	lib    *medialib.Library
	cfg    *config.Config
}

// libConf is a library films of root, with its params extra.
func libConf(root, extra string) string {
	return `{"name": "films", "roots": [` + quote(root) + `]` + extra + `}`
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

// startVOD serves root as the library films, through a vod handler of
// params on /vod/films/, with the media_auth policies auth (a JSON list, or
// ""), and waits for the library's first scan.
func startVOD(t testing.TB, root, libExtra, params, auth string) *fit {
	t.Helper()
	if auth == "" {
		auth = "[]"
	}
	doc := `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_library": [` + libConf(root, libExtra) + `],
  "media_auth": ` + auth + `,
  "routes": [{"match": {"path": ["/vod/*"]}, "handler": {"module": "vod", "params": ` + params + `}}]
}`
	up, stopUp, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	f := &fit{env: runtime.Env{Metrics: observe.NewMetrics(), Engine: up}, rt: new(runtime.Runtime)}
	f.swap(t, doc)
	if f.server, err = listener.Start(f.rt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.server.Close()
		f.rt.Close()
		medialib.Remove(up)
		stopUp()
	})
	f.base = "http://" + f.server.Addrs()[0].Addr.String()
	f.lib = medialib.Lookup(up, "films")
	scanned(t, f.lib, 1)
	return f
}

// swap compiles doc, applies its libraries and swaps it in, as a reload
// does.
func (f *fit) swap(t testing.TB, doc string) {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, f.env)
	if err != nil {
		t.Fatal(err)
	}
	if err := medialib.Apply(f.env.Engine, c.MediaLibrary, nil); err != nil {
		t.Fatal(err)
	}
	f.rt.Swap(g)
	f.cfg = c
}

// handler returns the vod handler of the running config.
func (f *fit) handler(t testing.TB) *VOD {
	t.Helper()
	g := f.rt.Current()
	defer g.Release()
	for _, m := range g.Modules() {
		if v, ok := m.(*VOD); ok {
			return v
		}
	}
	t.Fatal("no vod handler")
	return nil
}

// scanned waits until l has ended n scans in all.
func scanned(t testing.TB, l *medialib.Library, n uint64) *medialib.Catalog {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		st := l.Status()
		if st.Scans >= n && !st.Scanning {
			return l.Catalog()
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d scans after 20s, want %d: %+v", st.Scans, n, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// rescan scans l again and waits for it.
func rescan(t testing.TB, l *medialib.Library) *medialib.Catalog {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for st := l.Status(); st.Scanning || st.Pending; st = l.Status() {
		if time.Now().After(deadline) {
			t.Fatal("still scanning after 20s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	before := l.Status().Scans
	l.Rescan()
	return scanned(t, l, before+1)
}

// get fetches path of f with the header fields hdr in pairs.
func (f *fit) get(t testing.TB, path string, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	resp, body, err := fetch("GET", f.base+path, hdr...)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

// fetch sends a request without failing a test, for goroutines.
func fetch(method, url string, hdr ...string) (*http.Response, []byte, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, nil, err
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp, body, err
}

// mustGet fetches path and fails unless it is answered want.
func (f *fit) mustGet(t testing.TB, path string, want int, hdr ...string) []byte {
	t.Helper()
	resp, body := f.get(t, path, hdr...)
	if resp.StatusCode != want {
		t.Fatalf("GET %s: %d %q, want %d", path, resp.StatusCode, body, want)
	}
	return body
}

// newEngineKey is an engine of the test's own, a key of medialib's
// registry.
func newEngineKey() *fib.Engine { return new(fib.Engine) }
