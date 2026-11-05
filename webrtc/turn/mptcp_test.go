package turn

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/mptcptest"
)

// freeTCPAddr returns a TCP address on loopback that was free a moment ago.
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// turnMPTCPMember is a TURN server on loopback that listens listen_tcp and
// listen_tls, the latter with a certificate of the test's, both on ports of
// the kernel's, with Multipath TCP as mptcp says.
func turnMPTCPMember(t *testing.T, mptcp bool) (*config.TURNServer, *x509.CertPool) {
	t.Helper()
	certFile, keyFile, pool := certFiles(t, t.TempDir())
	m := member()
	m.MPTCP = mptcp
	m.ListenTCP = []string{"127.0.0.1:0"}
	m.ListenTLS = []string{"127.0.0.1:0"}
	m.TLS = &config.TURNTLS{Certificates: []config.Certificate{{CertFile: certFile, KeyFile: keyFile}}}
	return m, pool
}

// TestTURNMPTCP: webrtc.turn.mptcp listens listen_tcp and listen_tls with
// Multipath TCP, and a client asking for it gets it on either; the TLS
// listener still handshakes on such a connection. Only a kernel that offers
// Multipath TCP can tell, so the test skips elsewhere.
func TestTURNMPTCP(t *testing.T) {
	mptcptest.SkipUnlessSupported(t)
	m, pool := turnMPTCPMember(t, true)
	s := startRole(t, startEngine(t), m)
	tcpAddrs, tlsAddrs := s.StreamAddrs()
	if len(tcpAddrs) != 1 || len(tlsAddrs) != 1 {
		t.Fatalf("tcp %v, tls %v", tcpAddrs, tlsAddrs)
	}

	tcpConn, err := mptcptest.Dial(tcpAddrs[0].String())
	if err != nil {
		t.Fatalf("dial listen_tcp: %v", err)
	}
	defer tcpConn.Close()
	if !mptcptest.InUse(tcpConn) {
		t.Fatal("listen_tcp is plain TCP under mptcp")
	}

	tlsConn, err := mptcptest.Dial(tlsAddrs[0].String())
	if err != nil {
		t.Fatalf("dial listen_tls: %v", err)
	}
	defer tlsConn.Close()
	if !mptcptest.InUse(tlsConn) {
		t.Fatal("listen_tls is plain TCP under mptcp")
	}
	_ = tlsConn.SetDeadline(time.Now().Add(10 * time.Second))
	overTLS := tls.Client(tlsConn, &tls.Config{RootCAs: pool, ServerName: "turn.test"})
	if err := overTLS.Handshake(); err != nil {
		t.Fatalf("the TLS handshake over Multipath TCP: %v", err)
	}
	if !mptcptest.InUse(tlsConn) {
		t.Fatal("the TLS listener's connection lost Multipath TCP")
	}
}

// TestTURNMPTCPOff: without mptcp listen_tcp and listen_tls are plain TCP
// even when a client asks for Multipath TCP, which falls back from it.
func TestTURNMPTCPOff(t *testing.T) {
	mptcptest.SkipUnlessSupported(t)
	m, pool := turnMPTCPMember(t, false)
	s := startRole(t, startEngine(t), m)
	tcpAddrs, tlsAddrs := s.StreamAddrs()
	if len(tcpAddrs) != 1 || len(tlsAddrs) != 1 {
		t.Fatalf("tcp %v, tls %v", tcpAddrs, tlsAddrs)
	}
	for name, addr := range map[string]string{"listen_tcp": tcpAddrs[0].String(), "listen_tls": tlsAddrs[0].String()} {
		conn, err := mptcptest.Dial(addr)
		if err != nil {
			t.Fatalf("dial %s: %v", name, err)
		}
		if mptcptest.InUse(conn) {
			_ = conn.Close()
			t.Fatalf("%s uses Multipath TCP without mptcp", name)
		}
		if name == "listen_tls" {
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			if err := tls.Client(conn, &tls.Config{RootCAs: pool, ServerName: "turn.test"}).Handshake(); err != nil {
				_ = conn.Close()
				t.Fatalf("the TLS handshake: %v", err)
			}
		}
		_ = conn.Close()
	}
}

// TestTURNMPTCPRestart: mptcp is fixed when fib's TURN server is made, as
// its addresses are, so a change of only mptcp starts a new server on the
// address the old one held, and the addresses stay the same. This needs no
// kernel support: what is checked is that the change is acted on.
func TestTURNMPTCPRestart(t *testing.T) {
	m := member()
	m.ListenTCP = []string{freeTCPAddr(t)}
	other := *m
	other.MPTCP = true
	if staticKey(m) == staticKey(&other) {
		t.Fatal("mptcp is not part of the TURN server's static settings")
	}

	s := startRole(t, startEngine(t), m)
	tcpAddrs, _ := s.StreamAddrs()
	if len(tcpAddrs) != 1 || tcpAddrs[0].String() != m.ListenTCP[0] {
		t.Fatalf("listen_tcp before the change: %v", tcpAddrs)
	}
	if err := s.Apply(&other); err != nil {
		t.Fatal(err)
	}
	if n := s.restarts.Load(); n != 1 {
		t.Fatalf("%d restarts after a change of mptcp, want 1", n)
	}
	tcpAddrs, _ = s.StreamAddrs()
	if len(tcpAddrs) != 1 || tcpAddrs[0].String() != m.ListenTCP[0] {
		t.Fatalf("listen_tcp after the change: %v", tcpAddrs)
	}
	conn, err := net.Dial("tcp", m.ListenTCP[0])
	if err != nil {
		t.Fatalf("the restarted listen_tcp: %v", err)
	}
	_ = conn.Close()
}
