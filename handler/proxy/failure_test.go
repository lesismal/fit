//go:build !windows

package proxy

import (
	"errors"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/upstream"
)

// upstreamConfig is a config whose upstream has the given extra fields.
func upstreamConfig(fields string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "upstreams": [{"name": "app", "addresses": [%s]` + fields + `}],
  "routes": [{"handler": {"module": "proxy", "params": {"upstream": "app"}}}]
}`
}

// blackhole returns an address whose connects neither succeed nor fail: a
// listener that never accepts, its backlog filled.
func blackhole(t *testing.T) string {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Listen(fd, 0); err != nil {
		t.Fatal(err)
	}
	sa, _ := syscall.Getsockname(fd)
	addr := fmt.Sprintf("127.0.0.1:%d", sa.(*syscall.SockaddrInet4).Port)
	for range 2000 {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return addr
		}
		t.Cleanup(func() { c.Close() })
	}
	t.Skip("the backlog never filled")
	return ""
}

func status(t *testing.T, c *stdhttp.Client, target string) (int, string, time.Duration) {
	t.Helper()
	start := time.Now()
	resp, err := c.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(body), time.Since(start)
}

func TestConnectTimeout(t *testing.T) {
	base := startFit(t, upstreamConfig(`, "connect_timeout": "200ms"`), blackhole(t))
	for _, h2c := range []bool{false, true} {
		code, body, took := status(t, client(h2c), base+"/")
		if code != 504 || body != "504 Gateway Timeout\n" || took > 3*time.Second {
			t.Errorf("h2c=%v: %d %q after %v", h2c, code, body, took)
		}
	}
}

func TestResponseHeaderTimeout(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		backend := serveBackend(t, proto, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		base := startFit(t, upstreamConfig(`, "response_header_timeout": "150ms"`), backend)
		for _, h2c := range []bool{false, true} {
			code, _, took := status(t, client(h2c), base+"/")
			if code != 504 || took < 100*time.Millisecond || took > 3*time.Second {
				t.Errorf("h2c=%v: %d after %v", h2c, code, took)
			}
		}
	})
}

// stallingBackend sends the first part of a body and then nothing, until the
// test ends: with a Content-Length for ?length=1, chunked otherwise.
func stallingBackend(t *testing.T, proto string) string {
	t.Helper()
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	return serveBackend(t, proto, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Query().Get("length") == "1" {
			w.Header().Set("Content-Length", "1000000")
		}
		_, _ = w.Write(make([]byte, 200<<10)) // past the stream threshold
		w.(stdhttp.Flusher).Flush()
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
}

func TestResponseIdleTimeout(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		base := startFit(t, upstreamConfig(`, "response_idle_timeout": "200ms"`), stallingBackend(t, proto))
		for _, tt := range []struct {
			h2c, length bool
		}{{false, true}, {false, false}, {true, true}, {true, false}} {
			name := fmt.Sprintf("h2c=%v length=%v", tt.h2c, tt.length)
			target := base + "/"
			if tt.length {
				target += "?length=1"
			}
			start := time.Now()
			resp, err := client(tt.h2c).Get(target)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			n, err := io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			took := time.Since(start)
			if resp.StatusCode != 200 || n < 200<<10 || took > 3*time.Second {
				t.Errorf("%s: %d, %d bytes, after %v", name, resp.StatusCode, n, took)
			}
			// The client must be able to tell the body was cut short: an error
			// reading it, or, for an HTTP/2 body of no length, the trailer.
			if tt.h2c && !tt.length {
				if err != nil || resp.Trailer.Get("Fit-Error") == "" {
					t.Errorf("%s: err %v, trailer %v", name, err, resp.Trailer)
				}
			} else if err == nil {
				t.Errorf("%s: a body cut short read as whole", name)
			}
		}
	})
}

func TestUpstreamBreaksMidBody(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		backend := serveBackend(t, proto, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			if r.URL.Query().Get("length") == "1" {
				w.Header().Set("Content-Length", "1000000")
			}
			_, _ = w.Write(make([]byte, 200<<10))
			w.(stdhttp.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
			// Gone, with the body unfinished: the connection closed on
			// HTTP/1, the stream reset on HTTP/2.
			panic(stdhttp.ErrAbortHandler)
		}))
		base := startFit(t, upstreamConfig(""), backend)
		for _, tt := range []struct {
			h2c, length bool
		}{{false, true}, {false, false}, {true, true}, {true, false}} {
			name := fmt.Sprintf("h2c=%v length=%v", tt.h2c, tt.length)
			target := base + "/"
			if tt.length {
				target += "?length=1"
			}
			resp, err := client(tt.h2c).Get(target)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if tt.h2c && !tt.length {
				if resp.Trailer.Get("Fit-Error") == "" {
					t.Errorf("%s: err %v, trailer %v", name, err, resp.Trailer)
				}
			} else if err == nil {
				t.Errorf("%s: a body cut short read as whole", name)
			}
		}
	})
}

// watchedBackend reports, on gone, each request whose client (fit) went
// away before it was answered.
func watchedBackend(t *testing.T, proto string, h func(w stdhttp.ResponseWriter, r *stdhttp.Request)) (string, chan string) {
	t.Helper()
	gone := make(chan string, 16)
	addr := serveBackend(t, proto, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		h(w, r)
		select {
		case <-r.Context().Done():
			gone <- r.URL.Path
		case <-time.After(10 * time.Second):
		}
	}))
	return addr, gone
}

func waitGone(t *testing.T, gone chan string, path string) {
	t.Helper()
	select {
	case got := <-gone:
		if got != path {
			t.Fatalf("%s went, want %s", got, path)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the upstream request for %s was not cancelled", path)
	}
}

func TestClientAbort(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		addr, gone := watchedBackend(t, proto, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			switch r.URL.Path {
			case "/download":
				// A long body, of which the client reads a little.
				for i := 0; i < 1000 && r.Context().Err() == nil; i++ {
					if _, err := w.Write(make([]byte, 64<<10)); err != nil {
						return
					}
					w.(stdhttp.Flusher).Flush()
					time.Sleep(5 * time.Millisecond)
				}
			case "/upload":
				// Reads the body until it fails.
				_, _ = io.Copy(io.Discard, r.Body)
			}
		})
		base := startFit(t, upstreamConfig(""), addr)
		for _, h2c := range []bool{false, true} {
			// Before the header: the client gives up on waiting.
			c := client(h2c)
			c.Timeout = 200 * time.Millisecond
			if _, err := c.Get(base + "/header"); err == nil {
				t.Fatalf("h2c=%v: the header came", h2c)
			}
			waitGone(t, gone, "/header")

			// Mid-download.
			resp, err := client(h2c).Get(base + "/download")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.CopyN(io.Discard, resp.Body, 256<<10); err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			closeIdle(client(h2c))
			waitGone(t, gone, "/download")

			// Mid-upload: a body that stops arriving, then a client that goes.
			pr, pw := io.Pipe()
			req, _ := stdhttp.NewRequest("PUT", base+"/upload", pr)
			req.ContentLength = 10 << 20
			go func() {
				_, _ = pw.Write(make([]byte, 256<<10))
				time.Sleep(200 * time.Millisecond)
				pw.CloseWithError(errors.New("client gave up"))
			}()
			if resp, err := client(h2c).Do(req); err == nil {
				resp.Body.Close()
			}
			waitGone(t, gone, "/upload")
		}
	})
}

// closeIdle drops a client's idle connections, so that a body it closed
// half-read ends its connection or stream.
func closeIdle(c *stdhttp.Client) { c.CloseIdleConnections() }

func TestTrailers(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		backend := serveBackend(t, proto, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			n, _ := io.Copy(io.Discard, r.Body)
			w.Header().Set("Trailer", "X-Sum")
			size := 10
			if r.URL.Query().Get("big") == "1" {
				size = 200 << 10 // streamed past the threshold
			}
			_, _ = w.Write(make([]byte, size))
			w.Header().Set("X-Sum", fmt.Sprintf("in=%d in-trailer=%s out=%d", n, r.Trailer.Get("X-Check"), size))
		}))
		base := startFit(t, upstreamConfig(""), backend)
		for _, h2c := range []bool{false, true} {
			for _, big := range []bool{false, true} {
				name := fmt.Sprintf("h2c=%v big=%v", h2c, big)
				upload := 10
				target := base + "/"
				if big {
					upload, target = 200<<10, base+"/?big=1"
				}
				// A chunked request with a trailer of its own.
				req, _ := stdhttp.NewRequest("POST", target, io.MultiReader(strings.NewReader(strings.Repeat("u", upload))))
				req.ContentLength = -1
				req.Trailer = stdhttp.Header{"X-Check": {"ok"}}
				resp, err := client(h2c).Do(req)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				n, _ := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				out := 10
				if big {
					out = 200 << 10
				}
				want := fmt.Sprintf("in=%d in-trailer=ok out=%d", upload, out)
				if resp.StatusCode != 200 || n != int64(out) || resp.Trailer.Get("X-Sum") != want {
					t.Errorf("%s: %d, %d bytes, trailer %v, want %q", name, resp.StatusCode, n, resp.Trailer, want)
				}
			}
		}
	})
}

func TestStatusOf(t *testing.T) {
	x := &exchange{}
	for _, tt := range []struct {
		err  error
		want int
	}{
		{errors.New("refused"), 502},
		{os.ErrDeadlineExceeded, 504},
		{fibhttp.ErrResponseHeaderTimeout, 504},
		{upstream.ErrBusy, 503},
		{upstream.ErrClosed, 503},
		{&uploadError{errors.New("bad chunk")}, 400},
		{&uploadError{os.ErrDeadlineExceeded}, 408},
	} {
		if got := x.statusOf(tt.err); got != tt.want {
			t.Errorf("%v: %d, want %d", tt.err, got, tt.want)
		}
	}
	x.cancel(errIdleTimeout)
	if got := x.statusOf(errors.New("context canceled")); got != 504 {
		t.Errorf("idle timeout: %d", got)
	}
}
