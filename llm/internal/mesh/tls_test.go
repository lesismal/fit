package mesh

import (
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/internal/certpin"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/llm/internal/model"
)

// selfSigned makes a self-signed certificate for 127.0.0.1, as PEM.
func selfSigned(t *testing.T) (cert, key string) {
	t.Helper()
	c, k, err := certpin.SelfSigned("test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return string(c), string(k)
}

func fingerprint(t *testing.T, cert, key string) string {
	t.Helper()
	c, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	return certpin.Fingerprint(c)
}

func targetErr(m *Mesh, addr string) string {
	for _, x := range m.Targets() {
		if x.Addr == addr {
			return x.Error
		}
	}
	return ""
}

// A listener with a self-signed certificate and a dialer pinned to it: the
// link is made over wss://, both ways of calling work, and the pages see
// the TLS and the fingerprint. A dialer with another pin gets no link.
func TestMeshTLSPinned(t *testing.T) {
	addr := freeAddr(t)
	cert, key := selfSigned(t)
	fp := fingerprint(t, cert, key)
	node, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	if err := node.Configure(model.ListenConfig{Addr: addr, Password: "pw", SelfSigned: true, Cert: cert, Key: key}, nil); err != nil {
		t.Fatal(err)
	}
	if ls := node.Listen(); ls.Error != "" || !ls.TLS || !ls.SelfSigned || ls.Fingerprint != fp {
		t.Fatalf("listen state %+v", ls)
	}
	hub, _ := newTestMesh(t, model.RoleHub, "h1", allPeers)
	start(t, hub, "", model.DialReq{Addr: addr, Password: "pw", Role: model.RoleNode, Pin: strings.ToUpper(fp)})
	waitFor(t, "hello over TLS", func() bool { return hub.Online("n1") && node.Online("h1") })
	ctx := context.Background()
	if got, err := Call[map[string]string](ctx, hub, "n1", "/echo", map[string]string{"msg": "hi"}); err != nil || got["msg"] != "hi" {
		t.Fatalf("hub → node: %v %v", got, err)
	}
	if got, err := Call[map[string]string](ctx, node, "h1", "/echo", map[string]string{"msg": "back"}); err != nil || got["from"] != "n1" {
		t.Fatalf("node → hub: %v %v", got, err)
	}
	if ts := hub.Targets(); len(ts) != 1 || !ts[0].TLS || ts[0].Pin != strings.ToUpper(fp) {
		t.Fatalf("targets %+v", ts)
	}
	if rs := hub.DialReqs(); len(rs) != 1 || rs[0].Pin != strings.ToUpper(fp) {
		t.Fatalf("dial reqs %+v", rs)
	}

	other, _ := newTestMesh(t, model.RoleProxy, "p1", allPeers)
	wrong := strings.Repeat("ab", 32)
	start(t, other, "", model.DialReq{Addr: addr, Password: "pw", Role: model.RoleNode, Pin: wrong})
	waitFor(t, "the pin mismatch", func() bool { return strings.Contains(targetErr(other, addr), "does not match the pin") })
	if node.Online("p1") || other.Online("n1") {
		t.Fatal("a dialer with another pin linked")
	}
	// A plain dialer gets nowhere either.
	plain, _ := newTestMesh(t, model.RoleProxy, "p2", allPeers)
	start(t, plain, "", toNode(addr))
	waitFor(t, "the plain dial to fail", func() bool { return targetErr(plain, addr) != "" })
	if node.Online("p2") {
		t.Fatal("a plain dialer linked to a TLS listener")
	}
}

// A new certificate at the same address is checked on the next dial: the
// pin of the old one does not reach it, as no session is resumed.
func TestMeshTLSNewCertificate(t *testing.T) {
	addr := freeAddr(t)
	cert, key := selfSigned(t)
	fp := fingerprint(t, cert, key)
	node, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	if err := node.Configure(model.ListenConfig{Addr: addr, Password: "pw", Cert: cert, Key: key}, nil); err != nil {
		t.Fatal(err)
	}
	hub, _ := newTestMesh(t, model.RoleHub, "h1", allPeers)
	start(t, hub, "", model.DialReq{Addr: addr, Password: "pw", Role: model.RoleNode, Pin: fp})
	waitFor(t, "the link", func() bool { return hub.Online("n1") })

	c2, k2 := selfSigned(t)
	if err := node.Configure(model.ListenConfig{Addr: addr, Password: "pw", Cert: c2, Key: k2}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the redial to fail on the pin", func() bool {
		return !hub.Online("n1") && strings.Contains(targetErr(hub, addr), "does not match the pin")
	})
	if c := clientConfigOf(t, hub, addr); c.ClientSessionCache != nil || !c.SessionTicketsDisabled || !c.InsecureSkipVerify || c.VerifyPeerCertificate == nil {
		t.Fatalf("the pinned dialer's TLS %+v", c)
	}
}

// A listener's cert_file and key_file rotated on disk are read again when
// the same listener is set again, as -watch-certs has the role applied:
// links from then on are taken with the new certificate, the link open
// stays, and files that do not load leave the listener as it is.
func TestMeshTLSRotatedFiles(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "l.crt"), filepath.Join(dir, "l.key")
	write := func(cert, key string) {
		t.Helper()
		if err := os.WriteFile(certFile, []byte(cert), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyFile, []byte(key), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c1, k1 := selfSigned(t)
	c2, k2 := selfSigned(t)
	fp1, fp2 := fingerprint(t, c1, k1), fingerprint(t, c2, k2)
	write(c1, k1)
	addr := freeAddr(t)
	lc := model.ListenConfig{Addr: addr, Password: "pw", CertFile: certFile, KeyFile: keyFile}
	node, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	if err := node.Configure(lc, nil); err != nil {
		t.Fatal(err)
	}
	hub, _ := newTestMesh(t, model.RoleHub, "h1", allPeers)
	start(t, hub, "", model.DialReq{Addr: addr, Password: "pw", Role: model.RoleNode, Pin: fp1})
	waitFor(t, "the link", func() bool { return hub.Online("n1") })

	// Set again with the files as they are: nothing changes.
	if err := node.SetListen(lc); err != nil || node.Listen().Fingerprint != fp1 {
		t.Fatalf("unchanged: %v %s", err, node.Listen().Fingerprint)
	}
	// Rotated: the new certificate, and the link open stays.
	write(c2, k2)
	if err := node.SetListen(lc); err != nil || node.Listen().Fingerprint != fp2 {
		t.Fatalf("rotated: %v %s", err, node.Listen().Fingerprint)
	}
	if !hub.Online("n1") {
		t.Fatal("the open link closed")
	}
	newer, _ := newTestMesh(t, model.RoleProxy, "p1", allPeers)
	start(t, newer, "", model.DialReq{Addr: addr, Password: "pw", Role: model.RoleNode, Pin: fp2})
	older, _ := newTestMesh(t, model.RoleProxy, "p2", allPeers)
	start(t, older, "", model.DialReq{Addr: addr, Password: "pw", Role: model.RoleNode, Pin: fp1})
	waitFor(t, "the new pin to link and the old to fail", func() bool {
		return newer.Online("n1") && strings.Contains(targetErr(older, addr), "does not match the pin")
	})
	// Half written: the certificate stays.
	write(c1, k2)
	if err := node.SetListen(lc); err != nil || node.Listen().Fingerprint != fp2 || node.Listen().Error != "" {
		t.Fatalf("half written: %v %+v", err, node.Listen())
	}
}

func clientConfigOf(t *testing.T, m *Mesh, addr string) *tls.Config {
	t.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()
	if tg := m.targets[addr]; tg != nil && tg.tls != nil {
		return tg.tls
	}
	t.Fatalf("no TLS target %s", addr)
	return nil
}

// A listener with a certificate an authority issued, from files, and dialers
// that check it with ca_file: the name must match, and another authority's
// root takes nothing.
func TestMeshTLSCA(t *testing.T) {
	dir := t.TempDir()
	ca, err := certtest.NewCA("mesh test CA")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.Issue(dir, "", "node.test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(dir, "ca.pem")
	if err := ca.WriteCert(caFile); err != nil {
		t.Fatal(err)
	}
	ca2, err := certtest.NewCA("another CA")
	if err != nil {
		t.Fatal(err)
	}
	ca2File := filepath.Join(dir, "ca2.pem")
	if err := ca2.WriteCert(ca2File); err != nil {
		t.Fatal(err)
	}
	addr := freeAddr(t)
	node, _ := newTestMesh(t, model.RoleNode, "n1", allPeers)
	if err := node.Configure(model.ListenConfig{Addr: addr, Password: "pw", CertFile: leaf.CertFile, KeyFile: leaf.KeyFile}, nil); err != nil {
		t.Fatal(err)
	}
	if ls := node.Listen(); !ls.TLS || ls.CertFile != leaf.CertFile || ls.Fingerprint == "" {
		t.Fatalf("listen state %+v", ls)
	}
	hub, _ := newTestMesh(t, model.RoleHub, "h1", allPeers)
	start(t, hub, "", model.DialReq{Addr: addr, Password: "pw", Role: model.RoleNode, CAFile: caFile})
	named, _ := newTestMesh(t, model.RoleProxy, "p1", allPeers)
	start(t, named, "", model.DialReq{Addr: addr, Password: "pw", Role: model.RoleNode, CAFile: caFile, ServerName: "node.test"})
	waitFor(t, "the links", func() bool { return hub.Online("n1") && named.Online("n1") })

	wrongName, _ := newTestMesh(t, model.RoleHub, "h2", allPeers)
	start(t, wrongName, "", model.DialReq{Addr: addr, Password: "pw", Role: model.RoleNode, CAFile: caFile, ServerName: "other.test"})
	wrongCA, _ := newTestMesh(t, model.RoleProxy, "p2", allPeers)
	start(t, wrongCA, "", model.DialReq{Addr: addr, Password: "pw", Role: model.RoleNode, CAFile: ca2File})
	waitFor(t, "both refused", func() bool {
		return strings.Contains(targetErr(wrongName, addr), "certificate") && strings.Contains(targetErr(wrongCA, addr), "certificate")
	})
	if node.Online("h2") || node.Online("p2") {
		t.Fatal("a dialer linked with the wrong name or authority")
	}
}

// What is refused before anything listens or dials: off loopback, a listener
// needs TLS and a dial target a pin or a CA file; a pin is 64 hex digits; a
// certificate must load. Loopback and localhost keep plain ws://.
func TestMeshTLSValidation(t *testing.T) {
	m, _ := newTestMesh(t, model.RoleHub, "h1", allPeers)
	bad := func(what string, err error, want string) {
		t.Helper()
		if codeOf(err) != model.CodeBadRequest || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", what, err, want)
		}
	}
	bad("listen off loopback", m.SetListen(model.ListenConfig{Addr: "0.0.0.0:0", Password: "pw"}), "set cert_file and key_file, or self_signed")
	bad("listen on every address", m.SetListen(model.ListenConfig{Addr: ":0", Password: "pw"}), "is not a loopback address")
	bad("dial off loopback", m.Dial(model.DialReq{Addr: "192.0.2.1:9", Password: "pw", Role: model.RoleNode}), "set pin to the fingerprint")
	bad("dial by name", m.Dial(model.DialReq{Addr: "node.example.net:9", Password: "pw", Role: model.RoleNode}), "set pin")
	bad("a short pin", m.Dial(model.DialReq{Addr: "192.0.2.1:9", Password: "pw", Role: model.RoleNode, Pin: "abcd"}), "bad certificate pin")
	bad("pin and CA", m.Dial(model.DialReq{Addr: "192.0.2.1:9", Password: "pw", Role: model.RoleNode, Pin: strings.Repeat("a", 64), CAFile: "x"}), "one of the two")
	bad("a CA file that is not there", m.Dial(model.DialReq{Addr: "192.0.2.1:9", Password: "pw", Role: model.RoleNode, CAFile: filepath.Join(t.TempDir(), "none.pem")}), "ca_file")
	junk := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(junk, []byte("not PEM"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad("a CA file without PEM", m.Dial(model.DialReq{Addr: "192.0.2.1:9", Password: "pw", Role: model.RoleNode, CAFile: junk}), "holds no PEM certificate")
	bad("a certificate that does not load", m.SetListen(model.ListenConfig{Addr: "127.0.0.1:0", Password: "pw", Cert: "x", Key: "y"}), "certificate")
	if e := m.Listen().Error; !strings.Contains(e, "certificate") {
		t.Errorf("the pages see %q", e)
	}
	bad("self-signed with none made", m.SetListen(model.ListenConfig{Addr: "127.0.0.1:0", Password: "pw", SelfSigned: true}), "no certificate")

	for _, a := range []string{"127.0.0.1:9", "127.1.2.3:9", "[::1]:9", "localhost:9"} {
		if err := m.Dial(model.DialReq{Addr: a, Password: "pw", Role: model.RoleNode}); err != nil {
			t.Errorf("a plain dial to %s: %v", a, err)
		}
	}
	if err := m.SetListen(model.ListenConfig{Addr: "127.0.0.1:0", Password: "pw"}); err != nil || m.Listen().TLS {
		t.Errorf("a plain listener on loopback: %v %+v", err, m.Listen())
	}
	// A target set again with another pin is dialed anew; with the same, it is left alone.
	pin := strings.Repeat("a", 64)
	if err := m.Dial(model.DialReq{Addr: "192.0.2.1:9", Password: "pw", Role: model.RoleNode, Pin: pin}); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	first := m.targets["192.0.2.1:9"]
	m.mu.RUnlock()
	_ = m.Dial(model.DialReq{Addr: "192.0.2.1:9", Password: "pw", Role: model.RoleNode, Pin: pin})
	_ = m.Dial(model.DialReq{Addr: "192.0.2.1:9", Password: "pw", Role: model.RoleNode, Pin: strings.Repeat("b", 64)})
	m.mu.RLock()
	second := m.targets["192.0.2.1:9"]
	m.mu.RUnlock()
	if second == first || second.req.Pin != strings.Repeat("b", 64) {
		t.Fatal("a new pin did not replace the target")
	}
}
