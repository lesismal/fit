package wire

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certpin"
)

// Over TLS a forced close still drops the link at once.
func TestTLSAbortFound(t *testing.T) {
	c, k, err := certpin.SelfSigned("test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(c, k)
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := certpin.Parse(certpin.Fingerprint(cert))
	client := &tls.Config{ServerName: "127.0.0.1", MinVersion: tls.VersionTLS13}
	certpin.Pinned(client, pin, "test")
	opened := make(chan *Conn, 1)
	addr := Listen(t, NewHandler(ConnConfig{Auth: Password("pw"), OnOpen: func(c *Conn) { opened <- c },
		TLS: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d, err := Dial(ctx, "wss://"+addr+Path, DialConfig{ConnConfig: ConnConfig{Auth: Password("pw"), TLS: client}, Engine: SharedEngine(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var l *Conn
	select {
	case l = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the listener did not open the link")
	}
	if d.RemoteAddr() != addr || l.RemoteAddr() == "" {
		t.Fatalf("addresses: dialer %q, listener %q", d.RemoteAddr(), l.RemoteAddr())
	}
	l.forceClose(ErrHeartbeat)
	select {
	case <-d.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the dialer did not see the link dropped")
	}
}
