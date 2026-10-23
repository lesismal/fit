package fastcgi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/fcgi"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// countingListener counts the connections it accepts.
type countingListener struct {
	net.Listener
	accepted atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return c, err
}

// release lets /slow answer.
var release = make(chan struct{}, 16)

// app is the FastCGI application of the tests, Go's own fcgi server.
func app(t *testing.T, network, addr string) *countingListener {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	cl := &countingListener{Listener: ln}
	go func() {
		_ = fcgi.Serve(cl, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			env := fcgi.ProcessEnv(r)
			// Go's fcgi keeps SCRIPT_NAME and PATH_INFO from ProcessEnv.
			script := strings.TrimPrefix(env["SCRIPT_FILENAME"], env["DOCUMENT_ROOT"])
			switch script {
			case "/env.php", "/index.php", "/a.php":
				w.Header().Set("X-Script", script)
				for _, k := range []string{"SCRIPT_FILENAME", "PATH_TRANSLATED", "DOCUMENT_ROOT", "REQUEST_SCHEME", "REDIRECT_STATUS", "APP_ENV"} {
					fmt.Fprintf(w, "%s=%s\n", k, env[k])
				}
				fmt.Fprintf(w, "QUERY=%s\nMETHOD=%s\nURI=%s\nX_TEST=%s\nPROXY=%s\nREMOTE=%v\n",
					r.URL.RawQuery, r.Method, r.URL.RequestURI(), r.Header.Get("X-Test"), r.Header.Get("Proxy"), r.RemoteAddr != "")
			case "/echo.php":
				body, _ := io.ReadAll(r.Body)
				sum := sha256.Sum256(body)
				fmt.Fprintf(w, "%d %s", len(body), hex.EncodeToString(sum[:]))
			case "/big.php":
				w.Header().Set("Content-Type", "application/octet-stream")
				chunk := bytes.Repeat([]byte("0123456789abcdef"), 4096)
				for range 256 { // 16 MiB
					if _, err := w.Write(chunk); err != nil {
						return
					}
				}
			case "/status.php":
				w.Header().Set("Location", "/elsewhere")
				w.WriteHeader(stdhttp.StatusMovedPermanently)
			case "/slow.php":
				select {
				case <-release:
				case <-time.After(5 * time.Second):
				}
				fmt.Fprint(w, "slow")
			default:
				stdhttp.NotFound(w, r)
			}
		}))
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return cl
}

// serve runs fit with the fastcgi handler of params on every route.
func serve(t *testing.T, params string) string {
	t.Helper()
	c, err := config.Load([]byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "routes": [{"handler": {"module": "fastcgi", "params": ` + params + `}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{Engine: engine})
	if err != nil {
		stop()
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
		stop()
	})
	return "http://" + s.Addrs()[0].Addr.String()
}

func clients() map[string]*stdhttp.Client {
	var h1, h2 stdhttp.Protocols
	h1.SetHTTP1(true)
	h2.SetUnencryptedHTTP2(true)
	return map[string]*stdhttp.Client{
		"http/1.1": {Transport: &stdhttp.Transport{Protocols: &h1}, Timeout: 20 * time.Second,
			CheckRedirect: func(*stdhttp.Request, []*stdhttp.Request) error { return stdhttp.ErrUseLastResponse }},
		"h2c": {Transport: &stdhttp.Transport{Protocols: &h2}, Timeout: 20 * time.Second,
			CheckRedirect: func(*stdhttp.Request, []*stdhttp.Request) error { return stdhttp.ErrUseLastResponse }},
	}
}

func get(t *testing.T, c *stdhttp.Client, req *stdhttp.Request) (*stdhttp.Response, string) {
	t.Helper()
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func TestParams(t *testing.T) {
	cl := app(t, "tcp", "127.0.0.1:0")
	base := serve(t, `{"address": "`+cl.Addr().String()+`", "root": "/srv/www", "env": {"APP_ENV": "test"}}`)
	for proto, c := range clients() {
		req, _ := stdhttp.NewRequest("GET", base+"/env.php/extra/path?a=1&b=2", nil)
		req.Header.Set("X-Test", "yes")
		req.Header.Set("Proxy", "http://evil")
		resp, body := get(t, c, req)
		for _, want := range []string{
			"SCRIPT_FILENAME=/srv/www/env.php\n", "PATH_TRANSLATED=/srv/www/extra/path\n", "DOCUMENT_ROOT=/srv/www\n", "REQUEST_SCHEME=http\n",
			"REDIRECT_STATUS=200\n", "APP_ENV=test\n", "QUERY=a=1&b=2\n", "METHOD=GET\n", "URI=/env.php/extra/path?a=1&b=2\n",
			"X_TEST=yes\n", "PROXY=\n", "REMOTE=true\n",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: no %q in\n%s", proto, want, body)
			}
		}
		if resp.StatusCode != 200 || resp.Header.Get("X-Script") != "/env.php" {
			t.Errorf("%s: %d %v", proto, resp.StatusCode, resp.Header)
		}
	}
}

// TestKeepAlive checks that requests one after another reuse a connection,
// and that the status and Location a script sets are the response's.
func TestKeepAlive(t *testing.T) {
	cl := app(t, "tcp", "127.0.0.1:0")
	base := serve(t, `{"address": "`+cl.Addr().String()+`", "root": "/srv"}`)
	c := clients()["http/1.1"]
	for range 20 {
		req, _ := stdhttp.NewRequest("GET", base+"/env.php", nil)
		if resp, _ := get(t, c, req); resp.StatusCode != 200 {
			t.Fatal(resp.StatusCode)
		}
	}
	if n := cl.accepted.Load(); n != 1 {
		t.Errorf("%d connections for 20 requests one after another", n)
	}
	req, _ := stdhttp.NewRequest("GET", base+"/status.php", nil)
	if resp, _ := get(t, c, req); resp.StatusCode != 301 || resp.Header.Get("Location") != "/elsewhere" {
		t.Errorf("%d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	req, _ = stdhttp.NewRequest("GET", base+"/nothing.php", nil)
	if resp, _ := get(t, c, req); resp.StatusCode != 404 {
		t.Errorf("a script's 404: %d", resp.StatusCode)
	}
}

// TestLargeBodies sends a large upload and takes a large download, over
// each protocol, the download read slowly at first.
func TestLargeBodies(t *testing.T) {
	cl := app(t, "tcp", "127.0.0.1:0")
	base := serve(t, `{"address": "`+cl.Addr().String()+`", "root": "/srv"}`)
	upload := bytes.Repeat([]byte("upload!"), 12<<17) // 10.5 MiB, under fit's default body limit
	sum := sha256.Sum256(upload)
	for proto, c := range clients() {
		req, _ := stdhttp.NewRequest("POST", base+"/echo.php", bytes.NewReader(upload))
		if _, body := get(t, c, req); body != fmt.Sprintf("%d %s", len(upload), hex.EncodeToString(sum[:])) {
			t.Errorf("%s: upload: %q", proto, body)
		}
		// Chunked, of no declared length.
		req, _ = stdhttp.NewRequest("POST", base+"/echo.php", io.MultiReader(bytes.NewReader(upload[:1000])))
		req.ContentLength = -1
		if resp, _ := get(t, c, req); resp.StatusCode != 200 {
			t.Errorf("%s: a chunked upload: %d", proto, resp.StatusCode)
		}
		req, _ = stdhttp.NewRequest("GET", base+"/big.php", nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond) // a slow client, at first
		n, err := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil || n != 16<<20 {
			t.Errorf("%s: download: %d %v", proto, n, err)
		}
	}
}

// TestTimeoutsAndLimits checks a script slower than response_header_timeout
// (504), and that past max_conns and max_pending a request gets 503.
func TestTimeoutsAndLimits(t *testing.T) {
	cl := app(t, "tcp", "127.0.0.1:0")
	base := serve(t, `{"address": "`+cl.Addr().String()+`", "root": "/srv", "response_header_timeout": "300ms"}`)
	c := clients()["h2c"]
	req, _ := stdhttp.NewRequest("GET", base+"/slow.php", nil)
	if resp, _ := get(t, c, req); resp.StatusCode != 504 {
		t.Errorf("a slow script: %d", resp.StatusCode)
	}
	base = serve(t, `{"address": "`+cl.Addr().String()+`", "root": "/srv", "max_conns": 1, "max_pending": 1}`)
	// One busy, one waiting, the third refused.
	codes := make(chan int, 3)
	var wg sync.WaitGroup
	for i := range 3 {
		wg.Go(func() {
			time.Sleep(time.Duration(i) * 50 * time.Millisecond)
			req, _ := stdhttp.NewRequest("GET", base+"/slow.php", nil)
			resp, _ := get(t, c, req)
			codes <- resp.StatusCode
		})
	}
	if code := <-codes; code != 503 {
		t.Errorf("past max_pending: %d", code)
	}
	for range 3 {
		release <- struct{}{}
	}
	wg.Wait()
	for range 2 {
		if code := <-codes; code != 200 {
			t.Errorf("the busy and the waiting: %d", code)
		}
	}
	for len(release) > 0 {
		<-release
	}
}

// TestCutConnections checks an application that cuts its connection in the
// middle of a response, and a client that goes in the middle of one.
func TestCutConnections(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	closed := make(chan struct{}, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 64<<10)
				// Wait for the request, then half a response.
				_, _ = conn.Read(buf)
				out := appendRecord(nil, typeStdout, []byte("Content-Type: text/plain\r\n\r\npart of it"))
				_, _ = conn.Write(out)
				_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
				_, err := conn.Read(buf)
				if err != nil && !strings.Contains(err.Error(), "timeout") {
					closed <- struct{}{} // fit closed it: its client went
				}
			}()
		}
	}()
	base := serve(t, `{"address": "`+ln.Addr().String()+`", "root": "/srv"}`)
	for proto, c := range clients() {
		resp, err := c.Get(base + "/cut.php")
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		// HTTP/1.1 closes the connection; HTTP/2 ends the stream with a
		// trailer that says so.
		if err == nil && resp.Trailer.Get("Fit-Error") == "" {
			t.Errorf("%s: a response cut short read whole", proto)
		}
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := stdhttp.NewRequestWithContext(ctx, "GET", base+"/gone.php", nil)
		resp, err = c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = resp.Body.Read(make([]byte, 4))
		cancel()
		resp.Body.Close()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Errorf("%s: the application's connection stayed open after the client went", proto)
		}
	}
}

func TestTryFilesAndUnix(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"index.php", "a.php", "dir/index.php"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755)
		_ = os.WriteFile(filepath.Join(root, f), []byte("<?php"), 0o644)
	}
	sock := filepath.Join(t.TempDir(), "fpm.sock")
	app(t, "unix", sock)
	base := serve(t, `{"address": "unix/`+sock+`", "root": "`+root+`", "try_files": ["{path}", "{path}/index.php", "/index.php"]}`)
	c := clients()["http/1.1"]
	for path, want := range map[string]string{"/a.php": "/a.php", "/blog/post": "/index.php", "/a.php/x": "/a.php", "/": "/index.php"} {
		req, _ := stdhttp.NewRequest("GET", base+path, nil)
		resp, body := get(t, c, req)
		if resp.Header.Get("X-Script") != want {
			t.Errorf("%s: %q, want %q\n%s", path, resp.Header.Get("X-Script"), want, body)
		}
	}
	base = serve(t, `{"address": "unix/`+sock+`", "root": "`+root+`", "try_files": ["{path}"]}`)
	req, _ := stdhttp.NewRequest("GET", base+"/missing.php", nil)
	if resp, _ := get(t, c, req); resp.StatusCode != 404 {
		t.Errorf("no script: %d", resp.StatusCode)
	}
}

func TestSplit(t *testing.T) {
	h := &FastCGI{SplitPath: []string{".php"}, Index: "index.php"}
	for in, want := range map[string][2]string{
		"/index.php":      {"/index.php", ""},
		"/index.php/a/b":  {"/index.php", "/a/b"},
		"/x.phpx/y.php/z": {"/x.phpx/y.php", "/z"},
		"/A.PHP/b":        {"/A.PHP", "/b"},
		"/dir/":           {"/dir/index.php", ""},
		"/plain":          {"/plain", ""},
	} {
		if s, i := h.split(in); s != want[0] || i != want[1] {
			t.Errorf("%s: %q %q", in, s, i)
		}
	}
}

func TestRecords(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 70000)
	b := appendRecord(nil, typeStdout, big)
	b = appendRecord(b, typeEndRequest, []byte{0, 0, 0, 0, 0, 0, 0, 0})
	var got []byte
	ended := false
	var r reader
	// Fed a byte at a time and in one piece, the same comes out.
	for _, step := range []int{1, len(b)} {
		got, ended, r = nil, false, reader{}
		for i := 0; i < len(b); i += step {
			err := r.feed(b[i:min(i+step, len(b))], func(rec record) error {
				switch rec.t {
				case typeStdout:
					got = append(got, rec.content...)
				case typeEndRequest:
					ended = true
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(got, big) || !ended {
			t.Errorf("step %d: %d bytes, ended %v", step, len(got), ended)
		}
	}
	if err := r.feed([]byte{2, 6, 0, 1, 0, 0, 0, 0}, func(record) error { return nil }); err != errProtocol {
		t.Errorf("version 2: %v", err)
	}
	p := appendParam(nil, "N", strings.Repeat("v", 200))
	if len(p) != 1+4+1+200 || p[1]&0x80 == 0 {
		t.Errorf("a long value's length: % x", p[:6])
	}
}
