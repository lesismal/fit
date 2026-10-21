package proxy

import (
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// splitConfig is a config with upstreams a, b and c, whose addresses the
// test gives in that order, and a route proxying with params.
func splitConfig(params string, a, b, c string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "upstreams": [{"name": "a", "addresses": ["` + a + `"], "max_fails": 1, "fail_timeout": "1m"},
                {"name": "b", "addresses": ["` + b + `"]},
                {"name": "c", "addresses": ["` + c + `"]}],
  "routes": [{"handler": {"module": "proxy", "params": ` + params + `}}]
}`
}

func TestSplitWeights(t *testing.T) {
	a, aHits := countingServer(t, 200, "a")
	b, bHits := countingServer(t, 200, "b")
	c, _ := countingServer(t, 200, "c")
	base := startFit(t, splitConfig(`{"upstreams": [{"upstream": "a", "weight": 3}, {"upstream": "b", "weight": 1}]}`, a, b, c))
	const n = 800
	for range n {
		if code, _ := do(t, "GET", base+"/", ""); code != 200 {
			t.Fatalf("%d", code)
		}
	}
	// 600 and 200 expected; the bounds are about five deviations out.
	if ah, bh := aHits.Load(), bHits.Load(); ah+bh != n || ah < 540 || ah > 660 {
		t.Errorf("a %d, b %d of %d, want about 3:1", ah, bh, n)
	}
}

func TestSplitFailover(t *testing.T) {
	b, bHits := countingServer(t, 200, "backup")
	c, _ := countingServer(t, 200, "c")
	base := startFit(t, splitConfig(`{"upstream": "a", "failover": "b"}`, refused(t), b, c))
	// The first fails and takes a's one backend out of rotation for a
	// minute (max_fails 1); the rest go to the failover.
	if code, _ := do(t, "GET", base+"/", ""); code != 502 {
		t.Errorf("first: %d, want 502", code)
	}
	for i := range 5 {
		if code, body := do(t, "GET", base+"/", ""); code != 200 || body != "backup" {
			t.Errorf("%d: %d %q", i, code, body)
		}
	}
	if n := bHits.Load(); n != 5 {
		t.Errorf("the failover got %d", n)
	}
}

// mirrorServer records the requests it gets, after a delay.
type mirrorServer struct {
	*httptest.Server
	delay time.Duration
	mu    sync.Mutex
	got   []string
	count atomic.Int32
}

func newMirrorServer(t *testing.T, delay time.Duration) *mirrorServer {
	m := &mirrorServer{delay: delay}
	m.Server = httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.got = append(m.got, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("X-Test")+" "+string(body))
		m.mu.Unlock()
		m.count.Add(1)
		time.Sleep(m.delay)
		_, _ = io.WriteString(w, "mirror")
	}))
	t.Cleanup(m.Close)
	return m
}

func TestMirror(t *testing.T) {
	a, aHits := countingServer(t, 200, "main")
	m := newMirrorServer(t, 0)
	base := startFit(t, splitConfig(`{"upstream": "a", "mirror": {"upstream": "c"}}`, a, refused(t), m.Listener.Addr().String()))
	req, _ := stdhttp.NewRequest("POST", base+"/p?q=1", strings.NewReader("payload"))
	req.Header.Set("X-Test", "t")
	resp, err := client(false).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "main" || aHits.Load() != 1 {
		t.Fatalf("main: %d %q", resp.StatusCode, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for m.count.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.got) != 1 || m.got[0] != "POST /p?q=1 t payload" {
		t.Errorf("the mirror got %q", m.got)
	}
}

// TestMirrorDoesNotSlow sends requests through a mirror that takes a second
// to answer: they are answered at once, and the copies stop at the bound.
func TestMirrorDoesNotSlow(t *testing.T) {
	a, _ := countingServer(t, 200, "main")
	m := newMirrorServer(t, time.Second)
	base := startFit(t, splitConfig(`{"upstream": "a", "mirror": {"upstream": "c"}}`, a, refused(t), m.Listener.Addr().String()))
	start := time.Now()
	var wg sync.WaitGroup
	for range 400 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, _ := do(t, "GET", base+"/", ""); code != 200 {
				t.Errorf("%d", code)
			}
		}()
	}
	wg.Wait()
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Errorf("400 requests took %v beside a mirror that takes 1s", d)
	}
	time.Sleep(1500 * time.Millisecond)
	if n := m.count.Load(); n == 0 || n > maxMirrored {
		t.Errorf("the mirror got %d copies, want 1 to %d", n, maxMirrored)
	}
}

func TestMirrorPercent(t *testing.T) {
	a, _ := countingServer(t, 200, "main")
	m := newMirrorServer(t, 0)
	base := startFit(t, splitConfig(`{"upstream": "a", "mirror": {"upstream": "c", "percent": 25}}`, a, refused(t), m.Listener.Addr().String()))
	for range 400 {
		do(t, "GET", base+"/", "")
	}
	time.Sleep(200 * time.Millisecond)
	if n := m.count.Load(); n < 60 || n > 140 {
		t.Errorf("%d of 400 copied, want about 100", n)
	}
}

func TestSplitParams(t *testing.T) {
	for _, tt := range []struct{ params, err string }{
		{`{}`, "upstream or upstreams is needed"},
		{`{"upstream": "a", "upstreams": [{"upstream": "b", "weight": 1}]}`, "set one"},
		{`{"upstreams": [{"upstream": "nope", "weight": 1}]}`, `upstreams[0].upstream: there is no upstream named "nope"`},
		{`{"upstreams": [{"upstream": "a", "weight": 0}]}`, "minimum 1"},
		{`{"upstream": "a", "failover": "nope"}`, `failover: there is no upstream named "nope"`},
		{`{"upstream": "a", "mirror": {"upstream": "nope"}}`, `mirror.upstream: there is no upstream named "nope"`},
		{`{"upstream": "a", "mirror": {"upstream": "b", "percent": 101}}`, "percent"},
	} {
		if _, err := compileDoc(t, splitConfig(tt.params, "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3")); err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("%s: %v, want %q", tt.params, err, tt.err)
		}
	}
}
