package turn

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/lesismal/fib/webrtc/stun"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// refusedWithin is how long a refused UDP client waits for the answer it
// must not get.
const refusedWithin = 300 * time.Millisecond

// bindingRequest is a STUN Binding request, which the server answers
// without credentials.
func bindingRequest() []byte {
	var m stun.Message
	m.Build(stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}, stun.NewTransactionID())
	m.AddFingerprint()
	return m.Raw
}

// served reports whether a new client of each of s's listeners, UDP, TCP
// and TLS, is served: it gets an answer to a Binding request, or a TLS
// handshake; a refused one gets none, or its connection closed.
func served(t *testing.T, s *Server) (udp, tcp, tlsOK bool) {
	t.Helper()
	buf := make([]byte, 1500)
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(serverAddr(t, s)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write(bindingRequest()); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(refusedWithin))
	_, err = c.Read(buf)
	udp = err == nil

	tcpAddrs, tlsAddrs := s.StreamAddrs()
	tc, err := net.Dial("tcp", tcpAddrs[0].String())
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	if _, err := tc.Write(bindingRequest()); err == nil {
		_ = tc.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err = io.ReadAtLeast(tc, buf, 20)
		tcp = err == nil
	}

	lc, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", tlsAddrs[0].String(), &tls.Config{InsecureSkipVerify: true})
	if err == nil {
		_ = lc.Close()
	}
	return udp, tcp, err == nil
}

// The member's access decides first, then the global one: a refused client
// gets no answer over UDP and its TCP and TLS connections are closed; a
// reload that changes the access alone, or the global access alone, starts
// no new server, and keeps the connections it has.
func TestAccess(t *testing.T) {
	e := startEngine(t)
	mgr := app.NewManager(e, e, nil, nil)
	t.Cleanup(func() { mgr.Stop(context.Background()) })
	global := func(a *config.Access) {
		t.Helper()
		if err := mgr.Apply(&config.Config{Access: a}); err != nil {
			t.Fatal(err)
		}
	}
	certFile, keyFile, _ := certFiles(t, t.TempDir())
	m := member()
	m.ListenTCP = []string{"127.0.0.1:0"}
	m.ListenTLS = []string{"127.0.0.1:0"}
	m.TLS = &config.TURNTLS{Certificates: []config.Certificate{{CertFile: certFile, KeyFile: keyFile}}}
	m.Access = &config.Access{Deny: []string{"127.0.0.1"}}
	s := New(mgr.Host(ID))
	t.Cleanup(func() { s.Stop(context.Background()) })
	apply := func(access *config.Access) {
		t.Helper()
		next := *m
		next.Access = access
		if err := s.Apply(&next); err != nil {
			t.Fatal(err)
		}
	}
	want := func(what string, want bool) {
		t.Helper()
		if udp, tcp, tlsOK := served(t, s); udp != want || tcp != want || tlsOK != want {
			t.Fatalf("%s: udp %v, tcp %v, tls %v served, want %v", what, udp, tcp, tlsOK, want)
		}
	}

	apply(m.Access)
	addr := serverAddr(t, s)
	want("the member's deny", false)

	// The member's allow serves what the global access refuses.
	global(&config.Access{Deny: []string{"127.0.0.0/8"}})
	apply(&config.Access{Allow: []string{"127.0.0.1"}})
	want("the member's allow over a global deny", true)
	kept, err := net.Dial("tcp", func() string { tcp, _ := s.StreamAddrs(); return tcp[0].String() }())
	if err != nil {
		t.Fatal(err)
	}
	defer kept.Close()
	u := newStreamClient(t, kept, addrPort(t, kept.RemoteAddr()), "alice", "pw", false)
	u.mustAllocate(t)

	// Without an access of its own, the global one decides; the client
	// served before keeps its allocation.
	apply(nil)
	want("the global deny", false)
	if s.Allocations() != 1 {
		t.Fatalf("%d allocations after the access changed", s.Allocations())
	}

	// The global access changed, the member not.
	global(nil)
	apply(nil)
	want("no access", true)
	if s.restarts.Load() != 0 || serverAddr(t, s) != addr || s.Allocations() != 1 {
		t.Fatalf("%d restarts, at %v (was %v), %d allocations", s.restarts.Load(), serverAddr(t, s), addr, s.Allocations())
	}

	// An access that does not compile fails the Apply, which keeps what
	// runs.
	if err := s.Apply(&config.TURNServer{Listen: m.Listen, Realm: realm, RelayIP: "127.0.0.1", Secret: secret,
		Access: &config.Access{DenyFiles: []string{"/nonexistent/fit-access-list"}}}); err == nil {
		t.Fatal("an access list file that is not there")
	}
	want("after a refused Apply", true)

	// The access's list files are watched, as certificate files are.
	m.Access = &config.Access{AllowFiles: []string{"a.txt"}, DenyFiles: []string{"d.txt"}}
	if got := app.CertFiles(&config.Config{WebRTC: &config.WebRTC{TURN: m}}); !slices.Contains(got, "a.txt") || !slices.Contains(got, "d.txt") {
		t.Fatalf("the files watched: %q", got)
	}
}
