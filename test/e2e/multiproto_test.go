//go:build !windows

package e2e

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	stdhttp "net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certtest"
)

// TestOnePortManyProtocols serves HTTP/1.1 and h2 over fit's TLS, TLS passed
// through to a backend by its server name, SOCKS and raw TCP on one port; a
// second listener with tls has a tcp service without server_names, which
// takes the bytes no other protocol does, and a third serves HTTPS and SOCKS
// alone.
func TestOnePortManyProtocols(t *testing.T) {
	dir := t.TempDir()
	site, err := certtest.Write(dir, "fit.example")
	if err != nil {
		t.Fatal(err)
	}
	passCert, err := certtest.Write(t.TempDir(), "pass.example")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(passCert.CertFile, passCert.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	pass := serveTCP(t, func(c net.Conn) {
		tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{pair}})
		_ = tc.SetDeadline(time.Now().Add(10 * time.Second))
		if tc.Handshake() == nil {
			_, _ = tc.Write([]byte("pass:" + tc.ConnectionState().ServerName))
		}
	})
	echo := serveTCP(t, func(c net.Conn) { _, _ = io.Copy(c, c) })
	tlsBlock := `"tls": {"certificates": [{"cert_file": "` + site.CertFile + `", "key_file": "` + site.KeyFile + `"}]}`
	socks := `"socks": {"clients": ["127.0.0.1"], "allow": ["127.0.0.1"]}`
	_, addrs := startFit(t, `{
  "listeners": [
    {"name": "one", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2", "tcp", "socks"], `+tlsBlock+`, `+socks+`},
    {"name": "two", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2", "tcp"], `+tlsBlock+`},
    {"name": "three", "address": "127.0.0.1:0", "protocols": ["http/1.1", "socks"], `+tlsBlock+`, `+socks+`}],
  "upstreams": [{"name": "pass", "addresses": ["`+pass+`"]}, {"name": "raw", "addresses": ["`+echo+`"]}],
  "services": [
    {"name": "pass", "listeners": ["one"], "upstream": "pass", "server_names": ["pass.example"]},
    {"name": "raw", "listeners": ["one", "two"], "upstream": "raw"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "site"}}}]
}`)

	for _, name := range []string{"one", "two", "three"} {
		protos := []bool{false, true}
		if name == "three" {
			protos = protos[:1]
		}
		for _, h2 := range protos {
			tr := &stdhttp.Transport{
				TLSClientConfig: &tls.Config{RootCAs: certtest.Pool(site)},
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, network, addrs[name])
				},
				Protocols: new(stdhttp.Protocols),
			}
			tr.Protocols.SetHTTP1(!h2)
			tr.Protocols.SetHTTP2(h2)
			resp, err := (&stdhttp.Client{Transport: tr, Timeout: 5 * time.Second}).Get("https://fit.example/")
			if err != nil {
				t.Fatalf("%s h2=%v: %v", name, h2, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != "site" || resp.ProtoMajor != map[bool]int{false: 1, true: 2}[h2] {
				t.Errorf("%s h2=%v: %s %q", name, h2, resp.Proto, body)
			}
			tr.CloseIdleConnections()
		}
	}

	// Passed through: the backend's own certificate, and the name it was
	// asked for.
	c := dialTCP(t, addrs["one"])
	tc := tls.Client(c, &tls.Config{ServerName: "pass.example", RootCAs: certtest.Pool(passCert)})
	got, err := io.ReadAll(tc)
	if err != nil || string(got) != "pass:pass.example" {
		t.Fatalf("passed through: %q, %v", got, err)
	}

	for _, name := range []string{"one", "three"} {
		c := dialTCP(t, addrs[name])
		if code := socks5Connect(t, c, echo); code != 0 {
			t.Fatalf("%s: SOCKS reply %d", name, code)
		}
		echoes(t, c, "socks to "+name)
	}

	for _, name := range []string{"one", "two"} {
		echoes(t, dialTCP(t, addrs[name]), "\x00raw to "+name)
	}
}

// serveTCP serves each connection with serve, and closes it after.
func serveTCP(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				serve(c)
			}()
		}
	}()
	return l.Addr().String()
}

func dialTCP(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// socks5Connect asks for a SOCKS5 CONNECT to dest, an IPv4 address and
// port, over c, and returns the reply's code.
func socks5Connect(t *testing.T, c net.Conn, dest string) byte {
	t.Helper()
	ap := netip.MustParseAddrPort(dest)
	req := append([]byte{5, 1, 0, 5, 1, 0, 1}, ap.Addr().AsSlice()...)
	if _, err := c.Write(binary.BigEndian.AppendUint16(req, ap.Port())); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2+10)
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatalf("SOCKS reply %x: %v", reply, err)
	}
	return reply[3]
}

// echoes checks that what c sends comes back.
func echoes(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != msg {
		t.Fatalf("echoed %q, %v", got, err)
	}
}
