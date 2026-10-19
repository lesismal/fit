package proxy

import (
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"runtime"
	"runtime/metrics"
	"strconv"
	"testing"
	"time"
)

// pattern is the body the stream tests move: a cycle of a prime length, so
// that a byte lost, repeated or moved shows.
var pattern = func() []byte {
	b := make([]byte, 4093)
	for i := range b {
		b[i] = byte(i*7 + i/251)
	}
	return b
}()

// patternReader produces n bytes of the pattern.
type patternReader struct{ off, n int64 }

func (r *patternReader) Read(p []byte) (int, error) {
	if r.off >= r.n {
		return 0, io.EOF
	}
	p = p[:min(int64(len(p)), r.n-r.off)]
	for i := range p {
		p[i] = pattern[(r.off+int64(i))%int64(len(pattern))]
	}
	r.off += int64(len(p))
	return len(p), nil
}

// checkPattern reads r to the end, pausing for pause after every pace bytes,
// and reports how many bytes it read or where they first differ from the
// pattern.
func checkPattern(r io.Reader, pace int, pause time.Duration) (int64, error) {
	buf := make([]byte, 32<<10)
	var off, since int64
	for {
		n, err := r.Read(buf)
		for i := range n {
			if buf[i] != pattern[(off+int64(i))%int64(len(pattern))] {
				return off + int64(i), fmt.Errorf("byte %d differs", off+int64(i))
			}
		}
		off += int64(n)
		if since += int64(n); pause > 0 && since >= int64(pace) {
			since = 0
			time.Sleep(pause)
		}
		if errors.Is(err, io.EOF) {
			return off, nil
		}
		if err != nil {
			return off, err
		}
	}
}

// heapWatch samples the live heap, what the last collection found still in
// use, while a test moves its body, and reports how far it rose above where
// it started. A body held whole would show there; garbage waiting for the
// next collection does not.
type heapWatch struct {
	base, peak uint64
	stop       chan struct{}
	done       chan struct{}
}

func liveHeap() uint64 {
	s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

func watchHeap() *heapWatch {
	runtime.GC()
	w := &heapWatch{base: liveHeap(), stop: make(chan struct{}), done: make(chan struct{})}
	w.peak = w.base
	go func() {
		defer close(w.done)
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
			}
			w.peak = max(w.peak, liveHeap())
		}
	}()
	return w
}

// rise stops sampling and returns the peak's rise over the start, in bytes.
func (w *heapWatch) rise() int64 {
	close(w.stop)
	<-w.done
	return int64(w.peak) - int64(w.base)
}

func TestStreamDownload(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		size := int64(streamSize)
		backend := serveBackend(t, proto, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			if r.URL.Query().Get("length") == "1" {
				w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			}
			// Without a length, net/http sends it chunked.
			_, _ = io.Copy(w, &patternReader{n: size})
		}))
		base := startFit(t, config1(`{"upstream": "app"}`), backend)
		for _, tt := range []struct {
			h2c    bool
			length bool
		}{{false, true}, {false, false}, {true, true}, {true, false}} {
			name := fmt.Sprintf("h2c=%v length=%v", tt.h2c, tt.length)
			target := base + "/big"
			if tt.length {
				target += "?length=1"
			}
			watch := watchHeap()
			start := time.Now()
			resp, err := slowClient(tt.h2c).Do(mustGet(t, target))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			// A client slower than the upstream: it pauses after every 4MB.
			n, err := checkPattern(resp.Body, 4<<20, 20*time.Millisecond)
			resp.Body.Close()
			rise := watch.rise()
			if err != nil || n != size {
				t.Fatalf("%s: read %d of %d bytes: %v", name, n, size, err)
			}
			if tt.length && resp.ContentLength != size {
				t.Errorf("%s: Content-Length %d", name, resp.ContentLength)
			}
			if !tt.length && !tt.h2c && (len(resp.TransferEncoding) == 0 || resp.TransferEncoding[0] != "chunked") {
				t.Errorf("%s: Transfer-Encoding %v", name, resp.TransferEncoding)
			}
			t.Logf("%s: %d MB in %v, heap rose %.1f MB", name, size>>20, time.Since(start).Round(time.Millisecond), float64(rise)/(1<<20))
			if rise > maxHeapRise {
				t.Errorf("%s: the heap rose %d MB moving %d MB", name, rise>>20, size>>20)
			}
		}
	})
}

func TestStreamUpload(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		size := int64(streamSize)
		backend := serveBackend(t, proto, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			// An upstream slower than the client: it pauses after every 4MB.
			n, err := checkPattern(r.Body, 4<<20, 20*time.Millisecond)
			if err != nil {
				stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprintf(w, "%d %d %v", n, r.ContentLength, r.TransferEncoding)
		}))
		base := startFit(t, config1(`{"upstream": "app"}`), backend)
		for _, tt := range []struct {
			h2c    bool
			length bool
		}{{false, true}, {false, false}, {true, true}, {true, false}} {
			name := fmt.Sprintf("h2c=%v length=%v", tt.h2c, tt.length)
			var body io.Reader = &patternReader{n: size}
			if !tt.length {
				// Hiding the reader's type keeps net/http from finding a length.
				body = io.MultiReader(body)
			}
			req, _ := stdhttp.NewRequest("PUT", base+"/upload", body)
			if tt.length {
				req.ContentLength = size
			} else {
				req.ContentLength = -1
			}
			watch := watchHeap()
			start := time.Now()
			resp, err := slowClient(tt.h2c).Do(req)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			rise := watch.rise()
			want := fmt.Sprintf("%d %d [chunked]", size, -1)
			if proto != "http/1.1" {
				// HTTP/2 has no chunked framing.
				want = fmt.Sprintf("%d %d []", size, -1)
			}
			if tt.length {
				want = fmt.Sprintf("%d %d []", size, size)
			}
			if resp.StatusCode != 200 || string(got) != want {
				t.Fatalf("%s: %d %q, want %q", name, resp.StatusCode, got, want)
			}
			t.Logf("%s: %d MB in %v, heap rose %.1f MB", name, size>>20, time.Since(start).Round(time.Millisecond), float64(rise)/(1<<20))
			if rise > maxHeapRise {
				t.Errorf("%s: the heap rose %d MB moving %d MB", name, rise>>20, size>>20)
			}
		}
	})
}

func mustGet(t *testing.T, target string) *stdhttp.Request {
	t.Helper()
	req, err := stdhttp.NewRequest("GET", target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// slowClient is client with room for a transfer that takes a while.
func slowClient(h2c bool) *stdhttp.Client {
	c := client(h2c)
	c.Timeout = 5 * time.Minute
	return c
}
