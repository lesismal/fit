package ws

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// wssCert is a self-signed certificate for 127.0.0.1, and its fingerprint.
func wssCert(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	c, k, err := transport.NewSelfSigned("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(c, k)
	if err != nil {
		t.Fatal(err)
	}
	return cert, transport.Fingerprint(cert)
}

// listenWSS serves links on a wss:// listener at path with cert, echoing
// what they get.
func listenWSS(t *testing.T, path string, cert tls.Certificate) transport.Listener {
	t.Helper()
	l, err := transport.Listen(tunneltest.Engine(t), "wss://127.0.0.1:0"+path, transport.ListenOptions{Cert: &cert}, func(c transport.Conn) transport.Handler {
		return newRecorder(c, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// Over TLS, sends of every size, up to several frames' worth, echo whole
// and in order; the close goes through TLS too.
func TestWSSEcho(t *testing.T) {
	cert, fp := wssCert(t)
	l := listenWSS(t, "/link", cert)
	if !strings.HasPrefix(l.URL(), "wss://127.0.0.1:") || !strings.HasSuffix(l.URL(), "/link") {
		t.Fatalf("URL %q", l.URL())
	}
	var r *recorder
	c, err := transport.Dial(tunneltest.Engine(t), l.URL(), 5*time.Second, transport.CheckBy(fp, "", false), func(c transport.Conn) transport.Handler {
		r = newRecorder(c, false)
		return r
	})
	if err != nil {
		t.Fatal(err)
	}
	var want []byte
	for i, n := range []int{1, 125, 126, 0xffff, 0x10000, maxFrame + 3, 3 * maxFrame} {
		b := bytes.Repeat([]byte{byte('a' + i)}, n)
		want = append(want, b...)
		if err := c.Send(append([]byte(nil), b...)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the echo", func() bool { return r.len() >= len(want) })
	if !bytes.Equal(r.bytes(), want) {
		t.Fatal("the echo differs")
	}
	c.CloseAfterSend()
	waitClosed(t, r.closed)
	if err := c.Send([]byte("x")); err == nil {
		t.Error("a send after the close went out")
	}
}

// A wss dial checks the hub's certificate: another than the pinned one,
// or a tls:// listener's protocol, fails it; so does insecure.
func TestWSSRefused(t *testing.T) {
	cert, _ := wssCert(t)
	_, other := wssCert(t)
	l := listenWSS(t, "/link", cert)
	e := tunneltest.Engine(t)
	open := func(c transport.Conn) transport.Handler { return newRecorder(c, false) }
	if _, err := transport.Dial(e, l.URL(), 5*time.Second, transport.ClientOptions{PinSHA256: other}, open); err == nil || !strings.Contains(err.Error(), "does not match the pin") {
		t.Fatalf("another pin: %v", err)
	}
	if _, err := transport.Dial(e, l.URL(), 5*time.Second, transport.ClientOptions{InsecureSkipVerify: true}, open); err == nil || !strings.Contains(err.Error(), "takes no insecure") {
		t.Fatalf("insecure: %v", err)
	}
	if _, err := transport.Dial(e, l.URL(), 5*time.Second, transport.ClientOptions{}, open); err == nil {
		t.Fatal("the system's roots took a self-signed certificate")
	}
	// The path is still the listener's.
	pin := transport.ClientOptions{PinSHA256: transport.Fingerprint(cert)}
	if _, err := transport.Dial(e, strings.TrimSuffix(l.URL(), "/link")+"/other", 5*time.Second, pin, open); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("another path: %v", err)
	}
	// A tls:// listener speaks another application protocol.
	tl, err := transport.Listen(e, "tls://127.0.0.1:0", transport.ListenOptions{Cert: &cert}, func(c transport.Conn) transport.Handler { return newRecorder(c, false) })
	if err != nil {
		t.Fatal(err)
	}
	defer tl.Close()
	if _, err := transport.Dial(e, "wss://"+strings.TrimPrefix(tl.URL(), "tls://")+"/link", 5*time.Second, pin, open); err == nil {
		t.Fatal("a wss dial to a tls listener worked")
	}
	if _, err := transport.Listen(e, "wss://127.0.0.1:0/x", transport.ListenOptions{}, open); err == nil {
		t.Fatal("a wss listener without a certificate")
	}
	if _, err := transport.Dial(e, "wss://%zz/x", time.Second, transport.ClientOptions{}, open); err == nil {
		t.Fatal("dialed a bad URL")
	}
}

// The review's attacks on wss://: an address whose fragment or query would
// have overridden the pin, swapped the CAs, or dropped the pin for the
// system's roots. Dial refuses each before it connects; with the address
// alone, the pin the options give refuses a certificate that is not the
// hub's.
func TestWSSAddressCannotChangeTheCheck(t *testing.T) {
	evil, evilFP := wssCert(t)
	_, realFP := wssCert(t)
	l := listenWSS(t, "/tunnel", evil)
	e := tunneltest.Engine(t)
	var opened atomic.Int32
	open := func(c transport.Conn) transport.Handler { opened.Add(1); return newRecorder(c, false) }
	real := transport.ClientOptions{PinSHA256: realFP}
	for _, attack := range []string{
		l.URL() + "#?pin=" + evilFP + "&",
		l.URL() + "#?pin=" + evilFP + "&?pin=" + realFP,
		l.URL() + "#?ca=/etc/ssl/cert.pem&",
		l.URL() + "#?x=",
		l.URL() + "?pin=" + evilFP,
		"wss://u:p@" + strings.TrimPrefix(l.URL(), "wss://"),
		"ws://" + strings.TrimPrefix(l.URL(), "wss://") + "#?insecure=1",
	} {
		_, err := transport.Dial(e, attack, 5*time.Second, real, open)
		if err == nil || !strings.Contains(err.Error(), "which an address does not") {
			t.Errorf("%q: %v", attack, err)
		}
	}
	if _, err := transport.Dial(e, l.URL(), 5*time.Second, real, open); err == nil || !strings.Contains(err.Error(), "does not match the pin") {
		t.Fatalf("the real pin took another certificate: %v", err)
	}
	if opened.Load() != 0 {
		t.Fatal("a link opened")
	}
}

// The request asks for the path alone; how the hub's certificate is
// checked is not in it. A wss URL without a port is 443.
func TestWSSRequestLine(t *testing.T) {
	cert, fp := wssCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{ALPN}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	line := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		s, _ := bufio.NewReader(c).ReadString('\n')
		line <- s
	}()
	go func() {
		_, _ = transport.Dial(tunneltest.Engine(t), "wss://"+ln.Addr().String()+"/tunnel", 2*time.Second, transport.ClientOptions{PinSHA256: fp}, func(c transport.Conn) transport.Handler { return newRecorder(c, false) })
	}()
	select {
	case s := <-line:
		if s != "GET /tunnel HTTP/1.1\r\n" {
			t.Fatalf("request line %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no request")
	}
	u, _ := url.Parse("wss://example.invalid/x")
	dial, err := dialer(tunneltest.Engine(t), u, time.Second, transport.ClientOptions{PinSHA256: fp}, nil)
	if err != nil || dial == nil || u.Host != "example.invalid:443" {
		t.Fatalf("a wss URL without a port: %v %v", u, err)
	}
}

// A WebSocket handshake cut in the TLS handshake fails the dial.
func TestWSSCut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, fp := wssCert(t)
	if _, err := transport.Dial(tunneltest.Engine(t), "wss://"+ln.Addr().String()+"/x", 5*time.Second, transport.ClientOptions{PinSHA256: fp}, func(c transport.Conn) transport.Handler { return newRecorder(c, false) }); err == nil {
		t.Fatal("a dial to a server that closed worked")
	}
}
