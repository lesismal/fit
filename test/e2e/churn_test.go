//go:build !windows

package e2e

import (
	"bufio"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// churnBackend answers / with "ok", and /stream with 80 lines of 1KB at once
// and then 50 more, one every 20ms: a download that lasts a second, past
// many config swaps.
func churnBackend(t *testing.T) string {
	pad := strings.Repeat("x", 1000)
	s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path != "/stream" {
			_, _ = io.WriteString(w, "ok")
			return
		}
		for i := range 130 {
			if _, err := fmt.Fprintf(w, "%s %d\n", pad, i); err != nil {
				return
			}
			if i >= 80 {
				w.(stdhttp.Flusher).Flush()
				time.Sleep(20 * time.Millisecond)
			}
		}
	}))
	t.Cleanup(s.Close)
	return s.Listener.Addr().String()
}

func churnConfig(backend string, i int) string {
	extra := ""
	if i/100%2 == 1 {
		extra = `, {"name": "extra", "address": "127.0.0.1:0"}`
	}
	return fmt.Sprintf(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}%s],
  "upstreams": [{"name": "app", "addresses": [%q], "idle_timeout": "%ds"}],
  "routes": [
    {"match": {"path": ["/fixed"]}, "handler": {"module": "respond", "params": {"body": "v%d"}}},
    {"handler": {"module": "proxy", "params": {"upstream": "app"}}}
  ],
  "admin": {"address": "127.0.0.1:0", "history": 3}
}`, extra, backend, 60+i/50%2, i%2)
}

// TestChurn swaps the config 2,000 times through the Admin API while
// requests run: short ones over HTTP/1.1 and h2c, on kept and new
// connections, to a fixed response and through the proxy, and downloads
// that each outlast many swaps. The swaps change the fixed response every
// time, the upstream (so its pool is made anew) every 50, and add or remove
// a listener every 100. Nothing may fail, and every download must arrive
// whole.
func TestChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("a churn of 2,000 swaps")
	}
	backend := churnBackend(t)
	f, addrs := startFit(t, churnConfig(backend, 0))
	base, api := "http://"+addrs["web"], "http://"+addrs["admin"]

	// fit prints lines for each swap; keep reading them.
	draining := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-f.lines:
			case <-draining:
				return
			}
		}
	}()

	var (
		stop                 atomic.Bool
		short, longs, failed atomic.Int64
		mu                   sync.Mutex
		failures             []string
		wg                   sync.WaitGroup
	)
	fail := func(format string, args ...any) {
		failed.Add(1)
		mu.Lock()
		if len(failures) < 10 {
			failures = append(failures, fmt.Sprintf(format, args...))
		}
		mu.Unlock()
	}
	for i := range 8 {
		wg.Go(func() {
			c := client(i%2 == 1)
			fresh := i%4 >= 2
			if fresh {
				c.Transport.(*stdhttp.Transport).DisableKeepAlives = true
			}
			path := "/fixed"
			if i >= 4 {
				path = "/proxy"
			}
			for !stop.Load() {
				if fresh {
					time.Sleep(5 * time.Millisecond)
				}
				resp, err := c.Get(base + path)
				if err != nil {
					fail("%s: %v", path, err)
					continue
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				switch {
				case err != nil, resp.StatusCode != 200:
					fail("%s: %d %v", path, resp.StatusCode, err)
				case path == "/fixed" && string(body) != "v0" && string(body) != "v1", path == "/proxy" && string(body) != "ok":
					fail("%s: %q", path, body)
				default:
					short.Add(1)
				}
			}
		})
	}
	for i := range 4 {
		wg.Go(func() {
			c := client(i%2 == 1)
			for !stop.Load() {
				resp, err := c.Get(base + "/stream")
				if err != nil {
					fail("/stream: %v", err)
					continue
				}
				n := 0
				s := bufio.NewScanner(resp.Body)
				for s.Scan() {
					n++
				}
				resp.Body.Close()
				if s.Err() != nil || n != 130 || resp.StatusCode != 200 {
					fail("/stream: %d, %d lines, %v", resp.StatusCode, n, s.Err())
					continue
				}
				longs.Add(1)
			}
		})
	}

	began := time.Now()
	admin := client(false)
	const swaps = 2000
	for i := 1; i <= swaps; i++ {
		req, _ := stdhttp.NewRequest("POST", api+"/load", strings.NewReader(churnConfig(backend, i)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := admin.Do(req)
		if err != nil {
			t.Fatalf("swap %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("swap %d: %d %s", i, resp.StatusCode, body)
		}
		// Paced, so that each download spans a few hundred swaps.
		time.Sleep(3 * time.Millisecond)
	}
	took := time.Since(began)
	// Let the downloads under way finish.
	time.Sleep(1500 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
	close(draining)
	<-drained

	t.Logf("%d swaps in %v; %d short requests, %d downloads, %d failed", swaps, took.Round(time.Millisecond), short.Load(), longs.Load(), failed.Load())
	if failed.Load() > 0 {
		t.Fatalf("%d failed, first: %v", failed.Load(), failures)
	}
	if short.Load() < 1000 || longs.Load() < 16 {
		t.Fatalf("too little traffic: %d short, %d downloads", short.Load(), longs.Load())
	}
	if code, _ := f.stop(t); code != 0 {
		t.Fatalf("stop: exit %d", code)
	}
}
