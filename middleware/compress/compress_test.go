package compress

import (
	"bufio"
	"bytes"
	"context"
	"io"
	stdhttp "net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	_ "github.com/lesismal/fit/middleware/cache"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/runtime"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".test_text", New: func() any { return new(textHandler) }})
}

// long is a body that compresses well.
var long = strings.Repeat(`{"name": "a record", "value": 12345, "tags": ["x", "y"]}`+"\n", 4000)

// events tells the streaming handler to go on, once the test has read the
// first event.
var events = make(chan struct{}, 1)

// counted counts the requests /counted reached the handler with.
var counted atomic.Int64

// textHandler answers by path.
type textHandler struct{}

func (textHandler) ServeHTTP(c *fibhttp.Context) {
	h := c.Header()
	switch c.Request.URL.Path {
	case "/whole":
		_ = c.WriteResponse(fibhttp.Response{StatusCode: 200, Header: stdhttp.Header{
			"Content-Type": {"application/json"}, "Etag": {`"v1"`}}, Body: []byte(long)})
	case "/stream":
		h.Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(c, "data: one\n\n")
		c.Flush()
		// Its client going ends the wait, as it would a real stream's: the
		// filter is given back once the response ends.
		select {
		case <-events:
		case <-c.Context().Done():
			return
		case <-time.After(5 * time.Second):
		}
		_, _ = io.WriteString(c, "data: two\n\n")
	case "/large":
		h.Set("Content-Type", "text/plain")
		for i := 0; i < len(long); i += 10000 {
			_, _ = io.WriteString(c, long[i:min(i+10000, len(long))])
		}
	case "/counted":
		counted.Add(1)
		_ = c.Respond(200, "application/json", []byte(long))
	case "/small":
		_ = c.Respond(200, "text/plain", []byte("tiny"))
	case "/png":
		_ = c.Respond(200, "image/png", []byte(long))
	case "/encoded":
		_ = c.WriteResponse(fibhttp.Response{StatusCode: 200, Header: stdhttp.Header{
			"Content-Type": {"text/plain"}, "Content-Encoding": {"gzip"}}, Body: []byte(long)})
	case "/partial":
		_ = c.WriteResponse(fibhttp.Response{StatusCode: 206, Header: stdhttp.Header{
			"Content-Type": {"text/plain"}, "Content-Range": {"bytes 0-9/100"}}, Body: []byte(long)})
	case "/notransform":
		_ = c.WriteResponse(fibhttp.Response{StatusCode: 200, Header: stdhttp.Header{
			"Content-Type": {"text/plain"}, "Cache-Control": {"no-transform"}}, Body: []byte(long)})
	}
}

func serve(t *testing.T, params string) string {
	return serveWith(t, `[{"module": "compress", "params": `+params+`}]`)
}

// serveWith serves test_text behind the middleware listed.
func serveWith(t *testing.T, middleware string) string {
	t.Helper()
	c, err := config.Load([]byte(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "routes": [{"middleware": ` + middleware + `, "handler": {"module": "test_text"}}]
}`))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{})
	if err != nil {
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := listener.Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		rt.Close()
	})
	return "http://" + s.Addrs()[0].Addr.String()
}

func clients() map[string]*stdhttp.Client {
	var h1, h2 stdhttp.Protocols
	h1.SetHTTP1(true)
	h2.SetUnencryptedHTTP2(true)
	return map[string]*stdhttp.Client{
		"http/1.1": {Transport: &stdhttp.Transport{Protocols: &h1, DisableCompression: true}, Timeout: 10 * time.Second},
		"h2c":      {Transport: &stdhttp.Transport{Protocols: &h2, DisableCompression: true}, Timeout: 10 * time.Second},
	}
}

// decode returns body decoded from encoding.
func decode(t *testing.T, encoding string, body io.Reader) io.Reader {
	t.Helper()
	switch encoding {
	case "":
		return body
	case Gzip:
		r, err := gzip.NewReader(body)
		if err != nil {
			t.Fatal(err)
		}
		return r
	case Zstd:
		r, err := zstd.NewReader(body)
		if err != nil {
			t.Fatal(err)
		}
		return r
	case Brotli:
		return brotli.NewReader(body)
	}
	t.Fatalf("encoding %q", encoding)
	return nil
}

type got struct {
	status   int
	encoding string
	vary     string
	etag     string
	wire     int
	body     string
}

func fetch(t *testing.T, c *stdhttp.Client, method, url, accept string) got {
	t.Helper()
	req, _ := stdhttp.NewRequest(method, url, nil)
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	g := got{status: resp.StatusCode, encoding: resp.Header.Get("Content-Encoding"), vary: resp.Header.Get("Vary"),
		etag: resp.Header.Get("Etag"), wire: len(raw)}
	if resp.ContentLength >= 0 && resp.ContentLength != int64(len(raw)) && method != "HEAD" {
		t.Errorf("%s: Content-Length %d for %d bytes", url, resp.ContentLength, len(raw))
	}
	if g.encoding == "gzip" && strings.HasSuffix(url, "/encoded") {
		g.body = string(raw)
		return g
	}
	body, err := io.ReadAll(decode(t, g.encoding, bytes.NewReader(raw)))
	if err != nil {
		t.Fatalf("%s %s: %v", url, g.encoding, err)
	}
	g.body = string(body)
	return g
}

func TestCompress(t *testing.T) {
	base := serve(t, `{}`)
	for proto, c := range clients() {
		for _, tt := range []struct {
			path, accept, encoding string
		}{
			{"/whole", "gzip", Gzip},
			{"/whole", "br", Brotli},
			{"/whole", "zstd", Zstd},
			{"/whole", "gzip, br, zstd", Zstd}, // equal: the server's order
			{"/whole", "gzip;q=1, br;q=0.5, zstd;q=0.2", Gzip},
			{"/whole", "*", Zstd},
			{"/whole", "*;q=0.5, gzip", Gzip},
			{"/whole", "x-gzip", Gzip},
			{"/whole", "identity", ""},
			{"/whole", "gzip;q=0", ""},
			{"/whole", "", ""},
			{"/large", "br", Brotli},
			{"/large", "gzip", Gzip},
		} {
			g := fetch(t, c, "GET", base+tt.path, tt.accept)
			if g.encoding != tt.encoding || g.body != long || g.vary != "Accept-Encoding" {
				t.Errorf("%s %s %q: encoding %q, vary %q, %d bytes, want %q", proto, tt.path, tt.accept, g.encoding, g.vary, len(g.body), tt.encoding)
			}
			if tt.encoding != "" && g.wire > len(long)/10 {
				t.Errorf("%s %s %q: %d bytes on the wire for %d", proto, tt.path, tt.accept, g.wire, len(long))
			}
			if tt.path == "/whole" && (tt.encoding != "" && g.etag != `W/"v1"` || tt.encoding == "" && g.etag != `"v1"`) {
				t.Errorf("%s %q: ETag %q", proto, tt.accept, g.etag)
			}
		}
		for _, path := range []string{"/small", "/png", "/encoded", "/partial", "/notransform"} {
			g := fetch(t, c, "GET", base+path, "gzip, br, zstd")
			if path == "/encoded" {
				if g.encoding != "gzip" || g.body != long {
					t.Errorf("%s %s: recompressed or changed: %q", proto, path, g.encoding)
				}
				continue
			}
			if g.encoding != "" || g.vary != "" {
				t.Errorf("%s %s: encoding %q, vary %q", proto, path, g.encoding, g.vary)
			}
		}
		g := fetch(t, c, "HEAD", base+"/whole", "gzip")
		if g.encoding != "" || g.vary != "Accept-Encoding" {
			t.Errorf("%s HEAD: encoding %q, vary %q", proto, g.encoding, g.vary)
		}
	}
}

// TestCompressStream checks that a streamed response stays streamed: its
// first event arrives, decompressed, before the handler writes the second.
func TestCompressStream(t *testing.T) {
	base := serve(t, `{}`)
	for proto, c := range clients() {
		for _, enc := range []string{Gzip, Brotli, Zstd} {
			req, _ := stdhttp.NewRequest("GET", base+"/stream", nil)
			req.Header.Set("Accept-Encoding", enc)
			resp, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Header.Get("Content-Encoding") != enc {
				t.Fatalf("%s: %q", proto, resp.Header.Get("Content-Encoding"))
			}
			r := bufio.NewReader(decode(t, enc, resp.Body))
			start := time.Now()
			line, err := r.ReadString('\n')
			if err != nil || line != "data: one\n" || time.Since(start) > 2*time.Second {
				t.Fatalf("%s %s: %q %v after %v", proto, enc, line, err, time.Since(start))
			}
			events <- struct{}{}
			rest, _ := io.ReadAll(r)
			resp.Body.Close()
			if string(rest) != "\ndata: two\n\n" {
				t.Errorf("%s %s: then %q", proto, enc, rest)
			}
		}
	}
}

// TestCompressBounded checks max_in_flight: past it responses go
// uncompressed, and a stream whose client goes gives its place back.
func TestCompressBounded(t *testing.T) {
	base := serve(t, `{"max_in_flight": 1}`)
	c := clients()["http/1.1"]
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := stdhttp.NewRequestWithContext(ctx, "GET", base+"/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("the stream is not compressed")
	}
	// The stream holds the one place.
	if g := fetch(t, clients()["h2c"], "GET", base+"/whole", "gzip"); g.encoding != "" || g.body != long {
		t.Errorf("past max_in_flight: %q", g.encoding)
	}
	cancel()
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if g := fetch(t, clients()["h2c"], "GET", base+"/whole", "gzip"); g.encoding == "gzip" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stream's place was not given back when its client went")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-events: // the handler's wait, if it took none
	default:
	}
}

// TestCompressReleased checks that a response compressed to its end gives
// its place back once fib has sent what Close returned: with one place,
// responses one after another, whole and streamed, over each protocol, are
// all compressed, and none is left in flight.
func TestCompressReleased(t *testing.T) {
	base := serve(t, `{"max_in_flight": 1}`)
	for proto, c := range clients() {
		for range 3 {
			for _, path := range []string{"/whole", "/large"} {
				g := fetch(t, c, "GET", base+path, "gzip, br, zstd")
				if g.encoding != Zstd || g.body != long {
					t.Fatalf("%s %s: %q, %d bytes", proto, path, g.encoding, len(g.body))
				}
			}
		}
	}
}

// TestCacheOutside checks the config the docs give for responses that may
// be kept: cache before compress keeps each compressed response, by
// Accept-Encoding, so that a request the cache answers is not compressed
// again, and one asking for another encoding is a response of its own.
func TestCacheOutside(t *testing.T) {
	base := serveWith(t, `[{"module": "cache", "params": {"zone": "compress_test", "default_ttl": "1m"}}, {"module": "compress"}]`)
	c := clients()["http/1.1"]
	start := counted.Load()
	for _, tt := range []struct {
		accept, encoding string
		calls            int64
	}{
		{"gzip", Gzip, 1},
		{"gzip", Gzip, 1}, // kept
		{"zstd", Zstd, 2},
		{"zstd", Zstd, 2},
		{"", "", 3},
		{"gzip", Gzip, 3},
	} {
		g := fetch(t, c, "GET", base+"/counted", tt.accept)
		if g.encoding != tt.encoding || g.body != long || counted.Load()-start != tt.calls {
			t.Fatalf("%q: %q, %d bytes, %d calls, want %q and %d", tt.accept, g.encoding, len(g.body), counted.Load()-start, tt.encoding, tt.calls)
		}
	}
}

func TestNegotiate(t *testing.T) {
	offered := []string{Zstd, Brotli, Gzip}
	for accept, want := range map[string]string{
		"":                          "",
		"gzip":                      Gzip,
		"GZIP":                      Gzip,
		"deflate":                   "",
		"br;q=0.9, gzip;q=0.8":      Brotli,
		"br; q=0.1, *":              Zstd,
		"zstd;q=0, br;q=0, *;q=0.3": Gzip,
		"gzip;q=abc":                Gzip,
	} {
		if got := negotiate([]string{accept}, offered); got != want {
			t.Errorf("%q: %q, want %q", accept, got, want)
		}
	}
	if got := negotiate([]string{"zstd", "gzip"}, []string{Gzip, Zstd}); got != Gzip {
		t.Errorf("the server's order: %q", got)
	}
}

func TestCompressParams(t *testing.T) {
	for _, tt := range []struct{ params, err string }{
		{`{"encodings": ["deflate"]}`, `"deflate" is not one of`},
		{`{"encodings": ["gzip", "gzip"]}`, "listed twice"},
		{`{"level": "max"}`, `"max" is not one of`},
		{`{"gzip_level": 10}`, "gzip_level"},
		{`{"brotli_level": 0.5}`, "brotli_level"},
		{`{"zstd_level": 23}`, "zstd_level"},
	} {
		v, err := config.ParseValue([]byte(`{"module": "compress", "params": `+tt.params+`}`), "middleware")
		if err != nil {
			t.Fatal(err)
		}
		var ref config.ModuleRef
		err = v.Decode(&ref)
		if err == nil {
			_, err = module.Load(module.Middlewares, ref, nil)
		}
		if err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("%s: %v, want %q", tt.params, err, tt.err)
		}
	}
}
