package listener

import (
	"crypto/tls"
	"fmt"
	"net"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/runtime"
)

// BenchmarkTLSHandshake measures full TLS 1.3 handshakes with a listener,
// each on a new connection and without resumption, by several clients at
// once.
func BenchmarkTLSHandshake(b *testing.B) {
	cert, err := certtest.Write(b.TempDir(), "a.example")
	if err != nil {
		b.Fatal(err)
	}
	c, err := config.Load(fmt.Appendf(nil, `{"listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1"],
    "tls": {"certificates": [{"cert_file": %q, "key_file": %q}]}}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]}`, cert.CertFile, cert.KeyFile))
	if err != nil {
		b.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{})
	if err != nil {
		b.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := Start(rt)
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		_ = s.Close()
		rt.Close()
	}()
	addr := s.Addrs()[0].Addr.String()
	cfg := &tls.Config{RootCAs: certtest.Pool(cert), ServerName: "a.example", SessionTicketsDisabled: true,
		MinVersion: tls.VersionTLS13}
	b.SetParallelism(4)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			raw, err := net.Dial("tcp", addr)
			if err != nil {
				b.Error(err)
				return
			}
			// Reset rather than close, so that the client's ports do not
			// wait out TIME_WAIT and run short.
			_ = raw.(*net.TCPConn).SetLinger(0)
			conn := tls.Client(raw, cfg)
			if err := conn.Handshake(); err != nil {
				b.Error(err)
			}
			_ = raw.Close()
		}
	})
}
