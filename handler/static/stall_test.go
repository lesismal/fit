package static

import (
	"crypto/tls"
	"fmt"
	"io"
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

// TestStuckClient checks that a client that stops reading a large file over
// HTTP/2 or HTTP/3, where the file is copied through writes that wait for
// the client, loses its response after the write stall timeout rather than
// holding the handler's goroutine. (A plain HTTP/1 connection gets the file
// by sendfile, which does not wait.)
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
    {"name": "quic", "address": "127.0.0.1:0", "protocols": ["h3"], "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}}
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
