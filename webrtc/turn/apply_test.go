package turn

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// A role not yet applied listens nowhere, holds nothing and has no
// certificate; a member that is not one, a peer rule, a user key or
// certificate files that are not what they claim are refused before
// anything starts; and once stopped it does not start again.
func TestApplyRefusals(t *testing.T) {
	s := New(&app.Host{ID: ID, Engine: startEngine(t)})
	tcp, tls := s.StreamAddrs()
	if s.Addrs() != nil || tcp != nil || tls != nil || s.Allocations() != 0 {
		t.Fatal("a role not applied listens")
	}
	if _, err := s.getCertificate(nil); err == nil {
		t.Fatal("a certificate with no listen_tls")
	}
	for name, edit := range map[string]func(m *config.TURNServer){
		"a peer rule":  func(m *config.TURNServer) { m.DeniedPeers = []string{"not a peer"} },
		"a user's key": func(m *config.TURNServer) { m.Users = []config.TURNUser{{Name: "bob", Key: "short"}} },
		"certificate files": func(m *config.TURNServer) {
			missing := filepath.Join(t.TempDir(), "missing.pem")
			m.TLS = &config.TURNTLS{Certificates: []config.Certificate{{CertFile: missing, KeyFile: missing}}}
		},
	} {
		m := member()
		edit(m)
		if err := s.Apply(m); err == nil {
			t.Fatalf("%s that is not one was applied", name)
		}
	}
	if err := s.Apply((*config.TURNServer)(nil)); err == nil {
		t.Fatal("applied no member")
	}
	if s.Addrs() != nil {
		t.Fatal("a refused member started")
	}
	s.Stop(t.Context())
	if err := s.Apply(member()); !errors.Is(err, app.ErrStopped) {
		t.Fatalf("applied after Stop: %v", err)
	}
}

// The credentials of one realm are no other realm's.
func TestAuthRealm(t *testing.T) {
	auth, err := newAuth(member())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := auth("alice", realm); !ok {
		t.Fatal("alice is not known in her realm")
	}
	if _, ok := auth("alice", "other.test"); ok {
		t.Fatal("alice is known in another realm")
	}
}

// A restart on the same address that fib's server refuses brings the
// server as it was back, and a move elsewhere that it refuses keeps it and
// its peer rules; relay_ip's log line names public_ip and the port range.
func TestRestartFallsBack(t *testing.T) {
	e := startEngine(t)
	m := member()
	m.PublicIP = "192.0.2.1"
	m.PortMin, m.PortMax = 49152, 65535
	// A port of its own: the server as it was starts again on its listen.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m.Listen = []string{pc.LocalAddr().String()}
	_ = pc.Close()
	if got := relayFrom(m); !strings.Contains(got, "192.0.2.1 to clients") || !strings.Contains(got, "ports 49152-65535") {
		t.Fatal(got)
	}
	s := startRole(t, e, m)
	addr := serverAddr(t, s)
	rules := s.rules.Load()
	for _, listen := range []string{addr.String(), "127.0.0.1:0"} {
		bad := *m
		bad.Realm = "other.test"
		bad.Listen = []string{listen}
		bad.PortMin, bad.PortMax = 50000, 40000
		bad.AllowedPeers = nil
		if err := s.Apply(&bad); err == nil {
			t.Fatalf("on %s: started with port_min past port_max", listen)
		}
		if serverAddr(t, s) != addr || s.restarts.Load() != 0 || s.rules.Load() != rules {
			t.Fatalf("on %s: at %v after a failed restart, %d restarts", listen, serverAddr(t, s), s.restarts.Load())
		}
	}
	newClient(t, addr, "alice", "pw", false).mustAllocate(t)
}
