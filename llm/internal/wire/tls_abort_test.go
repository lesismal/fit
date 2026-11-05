package wire

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certpin"
)

// Over TLS both ends still find the fib connection under a link, so that a
// dead or slow peer's socket is dropped at once.
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
	if d.abort == nil || l.abort == nil {
		t.Fatalf("abort: dialer %v, listener %v", d.abort != nil, l.abort != nil)
	}
	l.forceClose(closeHeartbeat, "test", ErrHeartbeat)
	select {
	case <-d.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the dialer did not see the link dropped")
	}
}
