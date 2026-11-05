package quic

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"
	fq "github.com/lesismal/fib/http3/quic"
	fibtls "github.com/lesismal/fib/tls"

	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// testCert is a self-signed certificate for localhost.
func testCert(t *testing.T) tls.Certificate {
	t.Helper()
	certPEM, keyPEM, err := NewSelfSigned("localhost")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// server is a quic listener whose links are sinks, made by open unless it
// says otherwise.
type server struct {
	l     transport.Listener
	links chan *sink
}

func listenWith(t *testing.T, addr string, o transport.QUIC, refuse bool) *server {
	t.Helper()
	cert := testCert(t)
	s := &server{links: make(chan *sink, 8)}
	var err error
	s.l, err = transport.Listen(tunneltest.Engine(t), addr, transport.ListenOptions{Cert: &cert, QUIC: o}, func(c transport.Conn) transport.Handler {
		k := newSink(c, false)
		s.links <- k
		if refuse {
			return nil
		}
		return k
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.l.Close() })
	return s
}

func (s *server) hostport(t *testing.T) string {
	t.Helper()
	hp, err := ParseURL(s.l.URL())
	if err != nil {
		t.Fatal(err)
	}
	return hp
}

func (s *server) link(t *testing.T) *sink {
	t.Helper()
	select {
	case k := <-s.links:
		return k
	case <-time.After(10 * time.Second):
		t.Fatal("no link opened at the server")
		return nil
	}
}

// rawClient is a QUIC connection to a server that does, once its handshake
// is over, what the test says, rather than open a link.
type rawClient struct {
	c    *conn
	over chan error
}

func dialRaw(t *testing.T, hostport string, shook func(c *conn)) *rawClient {
	t.Helper()
	tc, err := ClientTLSConfig(ClientOptions{ServerName: "localhost", InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	rc := &rawClient{c: &conn{client: true}, over: make(chan error, 1)}
	rc.c.handshake = shook
	rc.c.over = func(_ *conn, err error) { rc.over <- err }
	cfg := config(transport.QUIC{})
	cfg.TLSConfig = tc
	if fibtls.NewQUICClient(tc) != nil {
		cfg.NewTLS = func(bool, *tls.Config) fq.TLSConn { return fibtls.NewQUICClient(tc) }
	}
	done := make(chan error, 1)
	err = tunneltest.Engine(t).DialWithHandler("udp", hostport, 5*time.Second, clientPath{rc.c}, func(fc *fib.Connection, err error) {
		if err == nil {
			rc.c.mu.Lock()
			rc.c.local = fc.LocalAddr()
			rc.c.qc, err = fq.Dial(fc, fc.RemoteAddr(), cfg, rc.c)
			rc.c.mu.Unlock()
		}
		done <- err
	})
	if err == nil {
		err = <-done
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rc.c.Close() })
	return rc
}

func (rc *rawClient) waitOver(t *testing.T) {
	t.Helper()
	select {
	case <-rc.over:
	case <-time.After(10 * time.Second):
		t.Fatal("the connection did not close")
	}
}

func waitSink(t *testing.T, s *sink) error {
	t.Helper()
	select {
	case <-s.closed:
		return s.err
	case <-time.After(10 * time.Second):
		t.Fatal("the link did not close")
		return nil
	}
}

// openStream opens the client's stream and writes b on it, with fin.
func openStream(b []byte, fin bool, keep func(*fq.Stream)) func(*conn) {
	return func(c *conn) {
		qs, err := c.conn().OpenStream()
		if err != nil {
			return
		}
		qs.Context = c
		if keep != nil {
			keep(qs)
		}
		_ = qs.Write(b, fin)
	}
}

// A server closes a connection whose stream does not start as a link's.
func TestServerRefusesStream(t *testing.T) {
	s := listenWith(t, "quic://127.0.0.1:0", transport.QUIC{}, false)
	for _, tc := range []struct {
		name  string
		shook func(*conn)
	}{
		{"bad version", openStream([]byte{9, 'x'}, false, nil)},
		{"empty", openStream(nil, true, nil)},
		{"unidirectional", func(c *conn) {
			if qs, err := c.conn().OpenUniStream(); err == nil {
				// The server's refusal, which asks it to stop sending,
				// ends the connection here.
				qs.Context = c
				_ = qs.Write([]byte{streamVersion}, false)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialRaw(t, s.hostport(t), tc.shook).waitOver(t)
		})
	}
}

// A link the server's open turns away closes its connection.
func TestServerOpenRefused(t *testing.T) {
	s := listenWith(t, "quic://127.0.0.1:0", transport.QUIC{}, true)
	rc := dialRaw(t, s.hostport(t), openStream([]byte{streamVersion}, false, nil))
	s.link(t)
	rc.waitOver(t)
}

// A stream the client finishes, resets or stops ends the server's link.
func TestServerStreamEnds(t *testing.T) {
	s := listenWith(t, "quic://127.0.0.1:0", transport.QUIC{}, false)
	for _, tc := range []struct {
		name string
		end  func(*fq.Stream)
		code uint64
	}{
		{"fin", func(qs *fq.Stream) { _ = qs.Write(nil, true) }, 0},
		{"reset", func(qs *fq.Stream) { qs.Reset(7) }, 7},
		{"stop sending", func(qs *fq.Stream) { qs.StopSending(8) }, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streams := make(chan *fq.Stream, 1)
			dialRaw(t, s.hostport(t), openStream([]byte{streamVersion, 'x'}, false, func(qs *fq.Stream) { streams <- qs }))
			k := s.link(t)
			waitFor(t, "the link's byte", func() bool { return k.len() == 1 })
			tc.end(<-streams)
			err := waitSink(t, k)
			var se *StreamError
			switch {
			case tc.code == 0 && err != nil:
				t.Errorf("err %v", err)
			case tc.code != 0 && (!errors.As(err, &se) || se.Code != tc.code):
				t.Errorf("err %v", err)
			}
		})
	}
}

// The server closes a connection that carries no link once its handshake
// has waited long enough for the link's stream.
func TestServerIdleConn(t *testing.T) {
	s := listenWith(t, "quic://127.0.0.1:0", transport.QUIC{HandshakeTimeout: 200 * time.Millisecond}, false)
	rc := dialRaw(t, s.hostport(t), func(*conn) {})
	rc.waitOver(t)
}

// A stream the server opens to a client is refused; a dial without a
// timeout takes the default.
func TestClientRefusesStream(t *testing.T) {
	s := listenWith(t, "quic://127.0.0.1:0", transport.QUIC{}, false)
	c, err := transport.Dial(tunneltest.Engine(t), DialURL(s.hostport(t), "", true), 0, func(c transport.Conn) transport.Handler { return newSink(c, false) })
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.Send([]byte("x"))
	srv := s.link(t)
	waitFor(t, "the server's link", func() bool { return srv.len() == 1 })
	sc := srv.c.(*conn)
	qs, err := sc.conn().OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	// Taken for the link's own, the stream's refusal ends the link, which
	// shows that it came.
	qs.Context = sc
	_ = qs.Write([]byte{streamVersion}, false)
	var se *StreamError
	if err := waitSink(t, srv); !errors.As(err, &se) || se.Code != codeRefused {
		t.Errorf("err %v", err)
	}
}

// A link the client ends after what it sent goes out whole.
func TestClientCloseAfterSend(t *testing.T) {
	f := newFixture(t, false)
	c, _ := f.dial(t)
	if a := c.RemoteAddr(); a == nil || a.String() != strings.TrimPrefix(f.l.URL(), "quic://") {
		t.Errorf("remote addr %v", a)
	}
	_ = c.Send([]byte("bye"))
	c.CloseAfterSend()
	waitFor(t, "the server's link", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.srv) == 1 })
	f.mu.Lock()
	srv := f.srv[0]
	f.mu.Unlock()
	if err := waitSink(t, srv); err != nil {
		t.Errorf("err %v", err)
	}
	if srv.len() != 3 {
		t.Errorf("got %d bytes", srv.len())
	}
	if err := c.Send([]byte("more")); !errors.Is(err, net.ErrClosed) {
		t.Errorf("send after the close: %v", err)
	}
}

// A dial whose open turns the link away fails.
func TestDialOpenRefused(t *testing.T) {
	f := newFixture(t, false)
	_, err := transport.Dial(tunneltest.Engine(t), f.url(), 5*time.Second, func(transport.Conn) transport.Handler { return nil })
	if !errors.Is(err, transport.ErrClosedOpening) {
		t.Errorf("err %v", err)
	}
}

// The connections past MaxConns are not let in.
func TestMaxConns(t *testing.T) {
	s := listenWith(t, "quic://127.0.0.1:0", transport.QUIC{MaxConns: 1}, false)
	open := func(c transport.Conn) transport.Handler { return newSink(c, false) }
	e := tunneltest.Engine(t)
	c, err := transport.Dial(e, DialURL(s.hostport(t), "", true), 5*time.Second, open)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := transport.Dial(e, DialURL(s.hostport(t), "", true), 300*time.Millisecond, open); err == nil {
		t.Error("a connection past MaxConns was let in")
	}
}

func TestDialErrors(t *testing.T) {
	e := tunneltest.Engine(t)
	open := func(c transport.Conn) transport.Handler { return newSink(c, false) }
	for _, addr := range []string{
		"quic://127.0.0.1:1/path",
		"quic://127.0.0.1:1?%zz",
		"quic://127.0.0.1:1?pin=zz",
		"quic://host.invalid:1?insecure=1",
		"quic://127.0.0.1:99999?insecure=1",
	} {
		if _, err := transport.Dial(e, addr, time.Second, open); err == nil {
			t.Errorf("dialed %q", addr)
		}
	}
}

func TestListenErrors(t *testing.T) {
	e := tunneltest.Engine(t)
	cert := testCert(t)
	open := func(c transport.Conn) transport.Handler { return newSink(c, false) }
	for _, tc := range []struct {
		addr string
		cert *tls.Certificate
	}{
		{"quic://%zz:1", &cert},
		{"quic://127.0.0.1:0", nil},
		{"quic://256.0.0.1:0", &cert},
	} {
		if _, err := transport.Listen(e, tc.addr, transport.ListenOptions{Cert: tc.cert}, open); err == nil {
			t.Errorf("listened on %q", tc.addr)
		}
	}
}

// A listener on every address is dialed on the loopback, and has no URL
// once closed.
func TestListenerURL(t *testing.T) {
	s := listenWith(t, "quic://0.0.0.0:0", transport.QUIC{}, false)
	if u := s.l.URL(); !strings.HasPrefix(u, "quic://127.0.0.1:") {
		t.Errorf("URL %q", u)
	}
	if err := s.l.Close(); err != nil {
		t.Fatal(err)
	}
	if u := s.l.URL(); u != "" {
		t.Errorf("URL of a closed listener %q", u)
	}
	if err := s.l.Close(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("second close: %v", err)
	}
}

// longHeader is a long header packet of version and type typ, to dcid,
// padded to size.
func longHeader(version uint32, typ byte, dcid []byte, size int) []byte {
	b := []byte{0xc0 | typ<<4, byte(version >> 24), byte(version >> 16), byte(version >> 8), byte(version), byte(len(dcid))}
	b = append(b, dcid...)
	b = append(b, 0, 0) // no source connection ID, no token
	length := size - len(b) - 2
	b = append(b, 0x40|byte(length>>8), byte(length))
	return append(b, make([]byte, length)...)
}

// The listener answers datagrams of no connection as QUIC says: a version
// it does not speak with a version negotiation, a packet of a connection it
// does not have with a stateless reset, and nothing else.
func TestListenerStrayDatagrams(t *testing.T) {
	s := listenWith(t, "quic://127.0.0.1:0", transport.QUIC{}, false)
	pc, err := net.Dial("udp", s.hostport(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	answer := func(d []byte) []byte {
		t.Helper()
		if _, err := pc.Write(d); err != nil {
			t.Fatal(err)
		}
		_ = pc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		b := make([]byte, 2048)
		n, err := pc.Read(b)
		if err != nil {
			return nil
		}
		return b[:n]
	}
	cid := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	if a := answer(longHeader(0x0a0a0a0a, 0, cid, 1200)); len(a) < 7 || a[0]&0x80 == 0 || a[1]|a[2]|a[3]|a[4] != 0 {
		t.Errorf("answer to an unknown version: %x", a)
	}
	short := append([]byte{0x40}, make([]byte, 40)...)
	if a := answer(short); len(a) == 0 || a[0]&0x80 != 0 {
		t.Errorf("answer to a packet of no connection: %x", a)
	}
	for _, d := range [][]byte{
		short[:10],                                // too short to be answered
		longHeader(fq.Version1, 2, cid, 100),      // a handshake packet
		longHeader(fq.Version1, 0, cid[:4], 1200), // an Initial to a short connection ID
	} {
		if a := answer(d); a != nil {
			t.Errorf("answer to %x...: %x", d[:6], a)
		}
	}
}

func TestCert(t *testing.T) {
	for _, hosts := range [][]string{nil, {"127.0.0.1", "example.com"}} {
		certPEM, keyPEM, err := NewSelfSigned(hosts...)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		if len(hosts) == 0 && leaf.Subject.CommonName != "localhost" {
			t.Errorf("CN %q", leaf.Subject.CommonName)
		}
		if len(hosts) > 0 && (len(leaf.IPAddresses) != 1 || len(leaf.DNSNames) != 1) {
			t.Errorf("IPs %v, names %v", leaf.IPAddresses, leaf.DNSNames)
		}
	}
	if fp := Fingerprint(tls.Certificate{}); fp != "" {
		t.Errorf("fingerprint of no certificate %q", fp)
	}
	conf, err := ClientTLSConfig(ClientOptions{PinSHA256: strings.Repeat("ab", 32)})
	if err != nil {
		t.Fatal(err)
	}
	if err := conf.VerifyPeerCertificate(nil, nil); err == nil {
		t.Error("no certificate matched the pin")
	}
	if _, err := ClientTLSConfig(ClientOptions{PinSHA256: "abcd"}); err == nil {
		t.Error("a short pin was taken")
	}
}

func TestConnBasics(t *testing.T) {
	if s := (&StreamError{Code: 3}).Error(); !strings.Contains(s, "code 3") {
		t.Errorf("Error %q", s)
	}
	c := &conn{}
	if c.RemoteAddr() != nil || c.LocalAddr() != nil || c.String() != "quic <nil>" {
		t.Errorf("addrs %v %v %q", c.RemoteAddr(), c.LocalAddr(), c.String())
	}
	if err := c.Send([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Errorf("send without a stream: %v", err)
	}
	k := newSink(c, false)
	c.h = k
	c.finish(nil)
	c.finish(errors.New("again"))
	if k.err != nil {
		t.Errorf("err %v", k.err)
	}
	c.CloseAfterSend()
	c.OnStreamsAvailable(nil)
	clientPath{c}.OnPriorityData(nil, nil)
	clientPath{c}.OnOpen(nil)
	l := &listener{}
	l.OnOpen(nil)
	l.OnPriorityData(nil, nil)
	if (&peer{}).find(nil) != nil {
		t.Error("found a connection of no ID")
	}
}

// Initials from addresses that never answer, which cost their sender
// nothing, cannot take every handshake: past half of MaxHandshakes, an
// Initial without a token is answered with a Retry and holds nothing, and a
// real client, which answers its Retry, still gets in.
func TestListenerRetry(t *testing.T) {
	s := listenWith(t, "quic://127.0.0.1:0", transport.QUIC{MaxHandshakes: 2}, false)
	retries := 0
	for i := range 8 {
		pc, err := net.Dial("udp", s.hostport(t))
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Close()
		cid := []byte{9, 9, 9, 9, 9, 9, 9, byte(i)}
		if _, err := pc.Write(longHeader(fq.Version1, 0, cid, 1200)); err != nil {
			t.Fatal(err)
		}
		_ = pc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		b := make([]byte, 2048)
		if n, err := pc.Read(b); err == nil && n > 0 && b[0]&0xf0 == 0xf0 {
			retries++
		}
	}
	if retries < 6 {
		t.Fatalf("%d of 8 Initials answered with a Retry", retries)
	}
	open := func(c transport.Conn) transport.Handler { return newSink(c, false) }
	c, err := transport.Dial(tunneltest.Engine(t), DialURL(s.hostport(t), "", true), 3*time.Second, open)
	if err != nil {
		t.Fatalf("a real client: %v", err)
	}
	_ = c.Close()
}
