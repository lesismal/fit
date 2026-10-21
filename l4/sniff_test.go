package l4_test

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/lesismal/fib/tlstest"

	_ "github.com/lesismal/fit/handler/respond"
)

// socksConnect asks for a SOCKS5 CONNECT to dest, an address or a host name
// and a port, over c, and returns the reply's code.
func socksConnect(t *testing.T, c net.Conn, dest string, command byte) byte {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(c)
	method := make([]byte, 2)
	if _, err := io.ReadFull(r, method); err != nil || method[0] != 5 || method[1] != 0 {
		t.Fatalf("greeting: %x, %v", method, err)
	}
	host, portText, _ := net.SplitHostPort(dest)
	port, _ := strconv.Atoi(portText)
	req := []byte{5, command, 0}
	if a, err := netip.ParseAddr(host); err == nil && a.Is4() {
		req = append(append(req, 1), a.AsSlice()...)
	} else {
		req = append(append(req, 3, byte(len(host))), host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatalf("reply: %v", err)
	}
	rest := map[byte]int{1: 4 + 2, 4: 16 + 2}[head[3]]
	if head[3] == 3 {
		n, _ := r.ReadByte()
		rest = int(n) + 2
	}
	if _, err := io.ReadFull(r, make([]byte, rest)); err != nil {
		t.Fatalf("reply's address: %v", err)
	}
	if r.Buffered() > 0 {
		t.Fatalf("%d bytes after the reply", r.Buffered())
	}
	return head[1]
}

// expectEcho checks that what c sends comes back.
func expectEcho(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != msg {
		t.Fatalf("echoed %q, %v", got, err)
	}
}

func socksConfig(socks string) string {
	return `{"listeners": [{"name": "s", "address": "127.0.0.1:0", "protocols": ["socks"], "socks": ` + socks + `}]}`
}

// TestSOCKS connects SOCKS clients to what the listener's socks allows, by
// address and by a host name whose address decides, and refuses the rest.
func TestSOCKS(t *testing.T) {
	target := backend(t, echo)
	_, port, _ := net.SplitHostPort(target)
	f := startFit(t, socksConfig(`{"clients": ["127.0.0.0/8"], "allow": ["127.0.0.0/8"], "ports": [`+port+`]}`))
	for _, dest := range []string{target, "localhost:" + port} {
		c := f.dial(0)
		if code := socksConnect(t, c, dest, 1); code != 0 {
			t.Fatalf("%s: reply %d", dest, code)
		}
		expectEcho(t, c, "through "+dest)
	}
	for dest, command := range map[string]byte{
		"127.0.0.1:1":       1, // a port not allowed
		"192.0.2.1:" + port: 1, // an address not allowed
		target + "":         2, // BIND
	} {
		want := byte(2)
		if command != 1 {
			want = 7
		}
		if code := socksConnect(t, f.dial(0), dest, command); code != want {
			t.Errorf("%s, command %d: reply %d, want %d", dest, command, code, want)
		}
	}

	// A name whose address deny holds is refused once connected.
	f = startFit(t, socksConfig(`{"clients": ["127.0.0.1"], "deny": ["127.0.0.0/8", "::1"]}`))
	for _, dest := range []string{target, "localhost:" + port} {
		if code := socksConnect(t, f.dial(0), dest, 1); code != 2 {
			t.Errorf("%s: reply %d, want 2", dest, code)
		}
	}
	// A client the rules do not name.
	f = startFit(t, socksConfig(`{"clients": ["10.0.0.0/8"]}`))
	if code := socksConnect(t, f.dial(0), target, 1); code != 2 {
		t.Errorf("a client not allowed: reply %d", code)
	}
}

// TestSOCKSIdleTimeout closes a relay that carries nothing for the idle
// timeout.
func TestSOCKSIdleTimeout(t *testing.T) {
	target := backend(t, echo)
	f := startFit(t, socksConfig(`{"clients": ["127.0.0.1"], "idle_timeout": "300ms"}`))
	c := f.dial(0)
	if code := socksConnect(t, c, target, 1); code != 0 {
		t.Fatalf("reply %d", code)
	}
	for range 4 {
		time.Sleep(100 * time.Millisecond)
		expectEcho(t, c, "x")
	}
	expectClosed(t, c, 3*time.Second)
}

// TestSniffClear serves HTTP/1.1, h2c, TLS passed through by its name, SOCKS
// and raw TCP on one port in the clear.
func TestSniffClear(t *testing.T) {
	target := backend(t, echo)
	f := startFit(t, fmt.Sprintf(`{
  "listeners": [{"name": "one", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c", "tcp", "socks"],
    "socks": {"clients": ["127.0.0.1"]}}],
  "upstreams": [{"name": "a", "addresses": [%q]}, {"name": "f", "addresses": [%q]}],
  "services": [
    {"name": "a", "listeners": ["one"], "upstream": "a", "server_names": ["a.example.com"]},
    {"name": "f", "listeners": ["one"], "upstream": "f"}],
  "routes": [{"listeners": ["one"], "handler": {"module": "respond", "params": {"body": "site"}}}]
}`, tlsBackend(t, "a"), target))

	for _, h2c := range []bool{false, true} {
		tr := &stdhttp.Transport{Protocols: new(stdhttp.Protocols)}
		tr.Protocols.SetHTTP1(!h2c)
		tr.Protocols.SetUnencryptedHTTP2(h2c)
		c := &stdhttp.Client{Transport: tr, Timeout: 5 * time.Second}
		resp, err := c.Get("http://" + f.addr + "/")
		if err != nil {
			t.Fatalf("h2c=%v: %v", h2c, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "site" || resp.ProtoMajor != map[bool]int{false: 1, true: 2}[h2c] {
			t.Errorf("h2c=%v: %s %q", h2c, resp.Proto, body)
		}
		tr.CloseIdleConnections()
	}

	_, client, err := tlstest.Configs()
	if err != nil {
		t.Fatal(err)
	}
	cfg := client.Clone()
	cfg.ServerName = "a.example.com"
	cfg.InsecureSkipVerify = true // the certificate is localhost's
	tc := tls.Client(f.dial(0), cfg)
	_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len("a:a.example.com"))
	if _, err := io.ReadFull(tc, got); err != nil || string(got) != "a:a.example.com" {
		t.Fatalf("TLS by its name: read %q, %v", got, err)
	}

	c := f.dial(0)
	if code := socksConnect(t, c, target, 1); code != 0 {
		t.Fatalf("SOCKS: reply %d", code)
	}
	expectEcho(t, c, "socks")

	// What no other protocol takes, the service without server_names.
	expectEcho(t, f.dial(0), "\x00raw bytes")
}
