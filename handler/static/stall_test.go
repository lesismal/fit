package static

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/http3"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/runtime"
)

// TestStuckClient checks that a client that stops reading a large file,
// over HTTP/1.1, where it goes by sendfile, or HTTP/2 or HTTP/3, where it is
// copied, loses its response after the write stall timeout rather than
// keeping it, and its file, for as long as it stays.
func TestStuckClient(t *testing.T) {
	const size = 64 << 20
	root := t.TempDir()
	f, err := os.Create(filepath.Join(root, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	f.Close()
	cert, err := certtest.Write(t.TempDir(), "fit.example", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load([]byte(fmt.Sprintf(`{
  "listeners": [
    {"name": "web", "address": "127.0.0.1:0", "protocols": ["h2c"]},
    {"name": "quic", "address": "127.0.0.1:0", "protocols": ["h3"], "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}},
    {"name": "h1", "address": "127.0.0.1:0", "protocols": ["http/1.1"]}
  ],
  "limits": {"write_stall_timeout": "1s"},
  "routes": [{"handler": {"module": "static", "params": {"root": %q}}}]
}`, cert.CertFile, cert.KeyFile, root)))
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
	t.Cleanup(func() { _ = s.Close(); rt.Close() })

	t.Run("http/1.1", func(t *testing.T) {
		// fib v1.5.1 does not count a file SendFile queued by sendfile(2)
		// in what the connection has unsent, so OnWritable never paces the
		// transfer, and the stall timeout never cuts it off.
		t.Skip("waits for A16 in fib-api-requests")
		conn, err := net.Dial("tcp", s.Addrs()[2].Addr.String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := io.WriteString(conn, "GET /big.bin HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2500 * time.Millisecond) // past the stall timeout, reading nothing
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := io.Copy(io.Discard, conn)
		if n >= size || isTimeout(err) {
			t.Fatalf("read %d bytes, err %v: the response was not cut off", n, err)
		}
	})
	t.Run("h2c", func(t *testing.T) {
		tr := &stdhttp.Transport{Protocols: new(stdhttp.Protocols)}
		tr.Protocols.SetUnencryptedHTTP2(true)
		resp, err := (&stdhttp.Client{Transport: tr}).Get("http://" + s.Addrs()[0].Addr.String() + "/big.bin")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		time.Sleep(2500 * time.Millisecond) // past the stall timeout, reading nothing
		n, err := io.Copy(io.Discard, resp.Body)
		if err == nil || n >= size {
			t.Fatalf("read %d bytes, err %v: the response was not cut off", n, err)
		}
	})
	t.Run("h3", func(t *testing.T) {
		engine, err := fib.New(fib.DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- engine.Run() }()
		defer func() { engine.Stop(); <-done; _ = engine.Close() }()
		cfg := http3.DefaultClientConfig()
		cfg.TLSConfig = &tls.Config{RootCAs: certtest.Pool(cert), ServerName: "fit.example"}
		cfg.StreamResponseBody = true
		h3 := http3.NewClient(engine, cfg)
		defer h3.Close()
		release := make(chan struct{})
		ended := make(chan error, 1)
		var once sync.Once
		var got int64
		req, _ := stdhttp.NewRequest("GET", "https://"+s.Addrs()[1].Addr.String()+"/big.bin", nil)
		h3.Do(req, func(resp *fibhttp.ClientResponse, err error) {
			if err != nil {
				once.Do(func() { ended <- err })
				return
			}
			resp.OnBody(func(data []byte, fin bool, err error) {
				<-release
				got += int64(len(data))
				if err != nil || fin {
					once.Do(func() { ended <- err })
				}
			})
		})
		time.Sleep(2500 * time.Millisecond)
		close(release)
		select {
		case err := <-ended:
			if err == nil || got >= size {
				t.Fatalf("%d bytes, err %v: the response was not cut off", got, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the stuck response neither ended nor failed")
		}
	})
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// TestSlowReader checks that a client that reads a file slowly, but keeps
// reading, gets all of it however long that takes past the stall timeout.
func TestSlowReader(t *testing.T) {
	const size = 3 << 20
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.bin"), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load([]byte(fmt.Sprintf(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "limits": {"write_stall_timeout": "1s"},
  "routes": [{"handler": {"module": "static", "params": {"root": %q}}}]
}`, root)))
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
	t.Cleanup(func() { _ = s.Close(); rt.Close() })
	for _, h2c := range []bool{false, true} {
		tr := &stdhttp.Transport{Protocols: new(stdhttp.Protocols)}
		if h2c {
			tr.Protocols.SetUnencryptedHTTP2(true)
		} else {
			tr.Protocols.SetHTTP1(true)
		}
		resp, err := (&stdhttp.Client{Transport: tr, Timeout: time.Minute}).Get("http://" + s.Addrs()[0].Addr.String() + "/big.bin")
		if err != nil {
			t.Fatal(err)
		}
		// About 1.3 MB a second: over two seconds, each wait under the
		// stall timeout.
		var n int64
		buf := make([]byte, 64<<10)
		for {
			k, err := resp.Body.Read(buf)
			n += int64(k)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					t.Fatalf("h2c=%v: after %d bytes: %v", h2c, n, err)
				}
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		resp.Body.Close()
		if n != size {
			t.Fatalf("h2c=%v: %d bytes", h2c, n)
		}
	}
}
