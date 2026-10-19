package proxy

import (
	"bufio"
	"fmt"
	"io"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// reloadable is fit serving a config it can be moved off, as a reload does.
type reloadable struct {
	base   string
	rt     *runtime.Runtime
	server *listener.Server
}

// startReloadable serves doc, with %s standing for addrs, as startFit does,
// with drain as the drain timeout.
func startReloadable(t *testing.T, doc string, drain time.Duration, addrs ...string) (*reloadable, func(doc string)) {
	t.Helper()
	quoted := make([]string, len(addrs))
	for i, a := range addrs {
		quoted[i] = `"` + a + `"`
	}
	load := func(doc string) *config.Config {
		c, err := config.Load([]byte(strings.Replace(doc, "%s", strings.Join(quoted, ", "), 1)))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	env := runtime.Env{Engine: engine}
	g, err := runtime.Compile(load(doc), env)
	if err != nil {
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := listener.Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	s.SetDrainTimeout(drain)
	t.Cleanup(func() {
		_ = s.Close()
		rt.Close()
		stop()
	})
	reload := func(doc string) {
		e := env
		e.Previous = rt.Current()
		g, err := runtime.Compile(load(doc), e)
		e.Previous.Release()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Reload(g); err != nil {
			t.Fatal(err)
		}
	}
	return &reloadable{base: "http://" + s.Addrs()[0].Addr.String(), rt: rt, server: s}, reload
}

// ticker is a backend that sends a 1KB line every 20ms until the client
// goes, after 80 of them at once: a body of no length streams through the
// proxy once 64KB of it have arrived.
func ticker(t *testing.T) string {
	pad := strings.Repeat("x", 1000)
	return serveBackend(t, "http/1.1", stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		for i := range 80 {
			fmt.Fprintf(w, "%s %d\n", pad, i)
		}
		for i := 0; ; i++ {
			if _, err := fmt.Fprintf(w, "%s tick %d\n", pad, i); err != nil {
				return
			}
			w.(stdhttp.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}))
}

// stream opens a streamed download and returns a channel that gets each
// line, closed when the download ends.
func stream(t *testing.T, url string) <-chan string {
	t.Helper()
	c := &stdhttp.Client{Transport: &stdhttp.Transport{}}
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 1024)
	go func() {
		defer resp.Body.Close()
		defer close(lines)
		s := bufio.NewScanner(resp.Body)
		for s.Scan() {
			lines <- s.Text()
		}
	}()
	return lines
}

// count reads lines until the channel closes or d passes, and reports how
// many it got and whether the stream ended.
func count(lines <-chan string, d time.Duration) (n int, ended bool) {
	timeout := time.After(d)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return n, true
			}
			n++
		case <-timeout:
			return n, false
		}
	}
}

// TestDrainCutsOff: a download that outlasts a reload stays on the config it
// began on, through the shared pool, until the drain timeout, and is then
// cut off; one that began after the reload goes on. The backend is not
// taken out of rotation for it.
func TestDrainCutsOff(t *testing.T) {
	backend := ticker(t)
	f, reload := startReloadable(t, config1(`{"upstream": "app"}`), 300*time.Millisecond, backend)
	old := stream(t, f.base+"/")
	if n, ended := count(old, 100*time.Millisecond); n == 0 || ended {
		t.Fatalf("before the reload: %d lines, ended %v", n, ended)
	}
	pool := func() *upstream.Pool {
		g := f.rt.Current()
		defer g.Release()
		p, _ := g.Upstream("app")
		return p
	}
	before := pool()
	reload(config1(`{"upstream": "app", "host_header": "other"}`))
	if pool() != before {
		t.Fatal("the unchanged upstream's pool was not shared")
	}
	fresh := stream(t, f.base+"/")

	// Still served until the drain timeout, then cut off.
	if n, ended := count(old, 200*time.Millisecond); n == 0 || ended {
		t.Fatalf("within the drain timeout: %d lines, ended %v", n, ended)
	}
	if _, ended := count(old, 2*time.Second); !ended {
		t.Fatal("the old download was not cut off after the drain timeout")
	}
	// The new one goes on, and the backend stays in rotation.
	if n, ended := count(fresh, 300*time.Millisecond); n == 0 || ended {
		t.Fatalf("the new download: %d lines, ended %v", n, ended)
	}
	if before.Backends()[0].Down() {
		t.Fatal("the drain counted against the backend")
	}
}

// TestDrainBeforeResponse: a request whose response has not begun when its
// config is drained gets 503.
func TestDrainBeforeResponse(t *testing.T) {
	hold := make(chan struct{})
	backend := serveBackend(t, "http/1.1", stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		select {
		case <-hold:
		case <-r.Context().Done():
		}
	}))
	defer close(hold)
	f, reload := startReloadable(t, config1(`{"upstream": "app"}`), 200*time.Millisecond, backend)
	done := make(chan int, 1)
	go func() {
		resp, err := client(false).Get(f.base + "/")
		if err != nil {
			done <- 0
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	time.Sleep(100 * time.Millisecond)
	reload(config1(`{"upstream": "app", "host_header": "other"}`))
	select {
	case status := <-done:
		if status != 503 {
			t.Fatalf("status %d, want 503", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the request was not drained")
	}
}
