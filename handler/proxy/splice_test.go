package proxy

import (
	"fmt"
	"io"
	stdhttp "net/http"
	"runtime"
	"strings"
	"testing"
)

// A large upload framed by its length goes to an HTTP/1.1 upstream by
// splice(2) on Linux, and every other way the ordinary way: to an h2c or h2
// upstream, which fib's client declines it for, chunked or small, which are
// never offered, and with splice_uploads false. Every byte arrives either way,
// and a request behind each on the client's connection is answered after it.
func TestSpliceUploads(t *testing.T) {
	linux := runtime.GOOS == "linux"
	for _, tt := range []struct {
		name, proto, params string
		size                int64
		length              bool
		// offered and declined are what the proxy's counters should move by
		// on Linux; elsewhere nothing is offered.
		offered, declined int64
	}{
		{"http/1.1", "http/1.1", `{"upstream": "app"}`, 8 << 20, true, 1, 0},
		{"h2c upstream", "h2c", `{"upstream": "app"}`, 8 << 20, true, 1, 1},
		{"h2 upstream", "h2", `{"upstream": "app"}`, 8 << 20, true, 1, 1},
		{"chunked", "http/1.1", `{"upstream": "app"}`, 8 << 20, false, 0, 0},
		{"small", "http/1.1", `{"upstream": "app"}`, 100 << 10, true, 0, 0},
		{"turned off", "http/1.1", `{"upstream": "app", "splice_uploads": false}`, 8 << 20, true, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			backend := serveBackend(t, tt.proto, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				n, err := checkPattern(r.Body, 0, 0)
				if err != nil {
					stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
					return
				}
				_, _ = fmt.Fprintf(w, "%s %d %d", r.URL.Path, n, r.ContentLength)
			}))
			base := startFit(t, config1(tt.params), backend)
			offers, declines := spliceOffers.Load(), spliceDeclines.Load()
			c := slowClient(false)
			for i, path := range []string{"/first", "/second"} {
				var body io.Reader = &patternReader{n: tt.size}
				if !tt.length {
					body = io.MultiReader(body)
				}
				req, _ := stdhttp.NewRequest("PUT", base+path, body)
				req.ContentLength = -1
				if tt.length {
					req.ContentLength = tt.size
				}
				resp, err := c.Do(req)
				if err != nil {
					t.Fatalf("upload %d: %v", i, err)
				}
				got, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				want := fmt.Sprintf("%s %d ", path, tt.size)
				if resp.StatusCode != 200 || !strings.HasPrefix(string(got), want) {
					t.Fatalf("upload %d: %d %q, want %q...", i, resp.StatusCode, got, want)
				}
			}
			wantOffered, wantDeclined := 2*tt.offered, 2*tt.declined
			if !linux {
				wantOffered, wantDeclined = 0, 0
			}
			if n := spliceOffers.Load() - offers; n != wantOffered {
				t.Errorf("%d uploads offered for splicing, want %d", n, wantOffered)
			}
			if n := spliceDeclines.Load() - declines; n != wantDeclined {
				t.Errorf("%d offers declined, want %d", n, wantDeclined)
			}
		})
	}
}
