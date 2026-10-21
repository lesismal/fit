package compress

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/listener"
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
		select {
		case <-events:
		case <-time.After(5 * time.Second):
		}
		_, _ = io.WriteString(c, "data: two\n\n")
	case "/large":
		h.Set("Content-Type", "text/plain")
		for i := 0; i < len(long); i += 10000 {
			_, _ = io.WriteString(c, long[i:min(i+10000, len(long))])
		}
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
	t.Helper()
	c, err := config.Load([]byte(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "routes": [{"middleware": [{"module": "compress", "params": ` + params + `}], "handler": {"module": "test_text"}}]
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

// BenchmarkCompress compresses a 16 KB JSON body sent whole, by encoding.
func BenchmarkCompress(b *testing.B) {
	body := []byte(long[:16<<10])
	for _, enc := range []string{"none", Gzip, Brotli, Zstd} {
		for _, level := range []string{"fastest", "default"} {
			v, _ := config.ParseValue([]byte(`{"module": "compress", "params": {"level": "`+level+`"}}`), "middleware")
			var ref config.ModuleRef
			_ = v.Decode(&ref)
			m, err := module.Load(module.Middlewares, ref, nil)
			if err != nil {
				b.Fatal(err)
			}
			h := m.(module.Middleware).Wrap(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
				_ = c.Respond(200, "application/json", body)
			}))
			b.Run(fmt.Sprintf("%s/%s", enc, level), func(b *testing.B) {
				r := httptest.NewRequest("GET", "/", nil)
				if enc != "none" {
					r.Header.Set("Accept-Encoding", enc)
				}
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				for b.Loop() {
					fibtest.ServeRequest(h, r)
				}
			})
		}
	}
}
