package proxy

import (
	"fmt"
	"io"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"
)

func limitsConfig(limits, fields string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "limits": {` + limits + `},
  "upstreams": [{"name": "app", "addresses": [%s]` + fields + `}],
  "routes": [{"handler": {"module": "proxy", "params": {"upstream": "app"}}}]
}`
}

// TestMaxPending reaches max_pending: with the one connection busy and two
// requests waiting for it, the next is answered 503 at once.
func TestMaxPending(t *testing.T) {
	release := make(chan struct{})
	backend := serveBackend(t, "http/1.1", stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		<-release
		_, _ = io.WriteString(w, "ok")
	}))
	defer close(release)
	base := startFit(t, limitsConfig("", `, "max_conns": 1, "max_pending": 2`), backend)
	held := make(chan int, 3)
	for range 3 {
		go func() {
			c := client(false)
			c.Timeout = 10 * time.Second
			resp, err := c.Get(base + "/")
			if err != nil {
				held <- 0
				return
			}
			resp.Body.Close()
			held <- resp.StatusCode
		}()
	}
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	resp, err := client(false).Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 || time.Since(start) > time.Second {
		t.Fatalf("one past max_pending: %d after %v", resp.StatusCode, time.Since(start))
	}
	// A gRPC call past it is answered as gRPC answers: UNAVAILABLE.
	req, _ := stdhttp.NewRequest("POST", base+"/svc/Method", strings.NewReader("\x00\x00\x00\x00\x00"))
	req.Header.Set("Content-Type", "application/grpc")
	resp, err = client(true).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Grpc-Status") != "14" {
		t.Fatalf("a gRPC call past max_pending: %d, grpc-status %q", resp.StatusCode, resp.Header.Get("Grpc-Status"))
	}
	release <- struct{}{}
	release <- struct{}{}
	release <- struct{}{}
	for range 3 {
		if code := <-held; code != 200 {
			t.Fatalf("a held request: %d", code)
		}
	}
}

// TestResponseTimeout reaches response_timeout: before the response begins
// it gives 504, and after, the response is cut short.
func TestResponseTimeout(t *testing.T) {
	backend := serveBackend(t, "http/1.1", stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path == "/body" {
			w.Header().Set("Content-Length", "10000000")
			_, _ = w.Write(make([]byte, 200<<10)) // past the stream threshold
			w.(stdhttp.Flusher).Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	base := startFit(t, limitsConfig("", `, "response_timeout": "500ms"`), backend)
	for _, h2c := range []bool{false, true} {
		start := time.Now()
		resp, err := client(h2c).Get(base + "/header")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 504 || time.Since(start) > 3*time.Second {
			t.Errorf("h2c=%v before the response: %d after %v", h2c, resp.StatusCode, time.Since(start))
		}
		resp, err = client(h2c).Get(base + "/body")
		if err != nil {
			t.Fatal(err)
		}
		n, err := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err == nil || n >= 10000000 {
			t.Errorf("h2c=%v during the body: %d bytes, %v", h2c, n, err)
		}
	}
}

// TestStreamedBodyLimit reaches max_body_bytes for a body that streams: one
// that declares a length past it is refused before it is sent, and a chunked
// one that grows past it is answered 413.
func TestStreamedBodyLimit(t *testing.T) {
	backend := serveBackend(t, "http/1.1", stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		_, _ = fmt.Fprint(w, n)
	}))
	base := startFit(t, limitsConfig(`"max_body_bytes": 200000`, ""), backend)
	for _, h2c := range []bool{false, true} {
		for _, tt := range []struct {
			size    int
			chunked bool
			status  int
		}{{150000, false, 200}, {150000, true, 200}, {300000, false, 413}, {300000, true, 413}} {
			var body io.Reader = strings.NewReader(strings.Repeat("x", tt.size))
			if tt.chunked {
				body = io.MultiReader(body)
			}
			req, _ := stdhttp.NewRequest("PUT", base+"/", body)
			if !tt.chunked {
				req.ContentLength = int64(tt.size)
			}
			resp, err := client(h2c).Do(req)
			if err != nil {
				if tt.status == 413 && tt.chunked {
					continue // the connection or stream was ended mid-body
				}
				t.Fatalf("h2c=%v %+v: %v", h2c, tt, err)
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tt.status || tt.status == 200 && string(got) != fmt.Sprint(tt.size) {
				t.Errorf("h2c=%v %+v: %d %q", h2c, tt, resp.StatusCode, got)
			}
		}
	}
}
