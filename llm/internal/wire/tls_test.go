package wire_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certpin"
	"github.com/lesismal/fit/llm/internal/wire"
)

// tlsPair is a listener's TLS and the TLS of a dialer pinned to its certificate.
func tlsPair(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	c, k, err := certpin.SelfSigned("test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(c, k)
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := certpin.Parse(certpin.Fingerprint(cert))
	client = &tls.Config{ServerName: "127.0.0.1", MinVersion: tls.VersionTLS13}
	certpin.Pinned(client, pin, "test: the listener's")
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, SessionTicketsDisabled: true}, client
}

// Over wss://, the handshake runs inside TLS: calls and streams work, a
// wrong password still fails, a plain dialer gets nowhere, and a wss:// URL
// without a TLS configuration is refused before it dials.
func TestTLSLink(t *testing.T) {
	stc, ctc := tlsPair(t)
	ts := startServer(t, wire.ConnConfig{Auth: wire.Password("pw"), TLS: stc})
	c, err := dial(t, "wss://"+ts.addr+wire.Path, wire.DialConfig{ConnConfig: wire.ConnConfig{Auth: wire.Password("pw"), TLS: ctc}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctxT(t)
	if got, err := c.Call(ctx, wire.Head{Method: "/echo"}, []byte("hi")); err != nil || string(got) != "hi" {
		t.Fatalf("call: %q %v", got, err)
	}
	st, err := c.OpenStream(ctx, wire.Head{Method: "/echo"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Send(ctx, []byte("chunk")); err != nil {
		t.Fatal(err)
	}
	if p, err := st.Recv(ctx); err != nil || string(p) != "chunk" {
		t.Fatalf("stream: %q %v", p, err)
	}
	_ = st.CloseSend()
	if _, err := dial(t, "wss://"+ts.addr+wire.Path, wire.DialConfig{ConnConfig: wire.ConnConfig{Auth: wire.Password("no"), TLS: ctc}}); err == nil {
		t.Fatal("a wrong password linked over TLS")
	}
	if _, err := dial(t, "ws://"+ts.addr+wire.Path, wire.DialConfig{ConnConfig: wire.ConnConfig{Auth: wire.Password("pw")}}); err == nil {
		t.Fatal("a plain dialer linked to a TLS listener")
	}
	if _, err := dial(t, "wss://"+ts.addr+wire.Path, wire.DialConfig{ConnConfig: wire.ConnConfig{Auth: wire.Password("pw")}}); err == nil ||
		!strings.Contains(err.Error(), "needs a TLS configuration") {
		t.Fatalf("wss:// without TLS: %v", err)
	}
}

// A dialer pinned to one certificate takes no listener with another, and
// checks the pin on every dial: no session is resumed past it.
func TestTLSPinEveryDial(t *testing.T) {
	stc, ctc := tlsPair(t)
	other, _ := tlsPair(t)
	good := startServer(t, wire.ConnConfig{Auth: wire.Password("pw"), TLS: stc})
	bad := startServer(t, wire.ConnConfig{Auth: wire.Password("pw"), TLS: other})
	calls := 0
	var mu sync.Mutex
	verify := ctc.VerifyPeerCertificate
	ctc.VerifyPeerCertificate = func(raw [][]byte, chains [][]*x509.Certificate) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return verify(raw, chains)
	}
	for i := 0; i < 3; i++ {
		c, err := dial(t, "wss://"+good.addr+wire.Path, wire.DialConfig{ConnConfig: wire.ConnConfig{Auth: wire.Password("pw"), TLS: ctc}})
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 3 {
		t.Fatalf("the pin was checked %d times in 3 dials", n)
	}
	_, err := dial(t, "wss://"+bad.addr+wire.Path, wire.DialConfig{ConnConfig: wire.ConnConfig{Auth: wire.Password("pw"), TLS: ctc}})
	if err == nil || !strings.Contains(err.Error(), "does not match the pin") {
		t.Fatalf("another certificate: %v", err)
	}
}

// The relay of the review: an attacker at a taken-over address passes a
// pinned dialer through to the real listener, then injects a request. Over
// TLS the dialer's records are sealed with keys the relay does not have, so
// what it injects is not a record of the link: the link fails, and nothing
// is served. A relay that answers TLS itself has no certificate that
// matches the pin.
func TestTLSRelayCannotInject(t *testing.T) {
	stc, ctc := tlsPair(t)
	real := startServer(t, wire.ConnConfig{Credentials: []wire.Credential{{Password: "np", Roles: []string{"node"}}}, TLS: stc})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var wmu sync.Mutex
	var toDialer net.Conn
	go func() {
		d, err := ln.Accept()
		if err != nil {
			return
		}
		u, err := net.Dial("tcp", real.addr)
		if err != nil {
			return
		}
		wmu.Lock()
		toDialer = d
		wmu.Unlock()
		go func() { _, _ = io.Copy(u, d) }()
		buf := make([]byte, 64<<10)
		for {
			n, err := u.Read(buf)
			if err != nil {
				return
			}
			wmu.Lock()
			_, _ = d.Write(buf[:n])
			wmu.Unlock()
		}
	}()
	got := make(chan string, 1)
	cfg := wire.DialConfig{ConnConfig: wire.ConnConfig{Auth: wire.Password("np"), TLS: ctc, Handlers: func(*wire.Conn) wire.Handlers {
		return wire.Handlers{Request: func(_ context.Context, r *wire.Request) ([]byte, error) {
			got <- r.Method
			return nil, nil
		}}
	}}}
	c, err := dial(t, "wss://"+ln.Addr().String()+wire.Path, cfg)
	if err != nil {
		t.Fatalf("dial through the relay: %v", err)
	}
	f := &wire.Frame{Type: wire.TypeRequest, ID: 1000, Head: wire.Head{Method: "node.injected"}, Payload: []byte("{}")}
	b, err := f.Marshal(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ws := append([]byte{0x82, byte(len(b))}, b...) // a server-to-client binary frame, unmasked
	wmu.Lock()
	_, _ = toDialer.Write(ws)
	wmu.Unlock()
	select {
	case m := <-got:
		t.Fatalf("the dialer served %q injected by a relay", m)
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the link outlived the injected bytes")
	}

	// A relay that answers TLS with a certificate of its own.
	fake, _ := tlsPair(t)
	mitm := startServer(t, wire.ConnConfig{Credentials: []wire.Credential{{Password: "np", Roles: []string{"node"}}}, TLS: fake})
	if _, err := dial(t, "wss://"+mitm.addr+wire.Path, cfg); err == nil || !strings.Contains(err.Error(), "does not match the pin") {
		t.Fatalf("a relay with its own certificate: %v", err)
	}
}
