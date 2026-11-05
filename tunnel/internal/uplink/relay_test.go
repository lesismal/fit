package uplink

import (
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
)

// certPair is a self-signed certificate for 127.0.0.1, and its fingerprint.
func certPair(t *testing.T) (tls.Certificate, string) {
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

// tlsHub is a fakeHub on a listener of scheme, tls or wss, with a
// certificate of its own: its URL and the certificate's fingerprint.
func tlsHub(t *testing.T, scheme string) (*fakeHub, string) {
	t.Helper()
	cert, fp := certPair(t)
	addr := "tls://127.0.0.1:0"
	if scheme == "wss" {
		addr = "wss://127.0.0.1:0/tunnel"
	}
	return newFakeHubOn(t, addr, transport.ListenOptions{Cert: &cert}), fp
}

// hostport is the host and port of a listener's URL.
func hostport(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

// relayed is url with its host and port replaced by relay's.
func relayed(t *testing.T, raw, relay string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = relay
	return u.String()
}

// kickFrame is a link frame that kicks the node with reason: what an
// attacker who could write into a link would send.
func kickFrame(reason string) []byte {
	f := make([]byte, 4+6+len(reason))
	binary.BigEndian.PutUint32(f, uint32(6+len(reason)))
	f[4], f[5] = 4, byte(proto.MethodKick) // notify kick, id 0
	copy(f[10:], reason)
	return f
}

// The attack of the review, on tls and wss: a relay at a taken-over hub
// address, knowing no password and not the hub's key, passes the node's
// bytes to the real hub and back, which lets the node register, and then
// writes a frame of its own. Over TLS the frame is not a record the node's
// TLS opens: the link fails, and the node serves nothing of the relay's.
func TestRelayCannotInject(t *testing.T) {
	for _, scheme := range []string{"tls", "wss"} {
		t.Run(scheme, func(t *testing.T) {
			s, fp := tlsHub(t, scheme)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			var wmu sync.Mutex
			var toNode net.Conn
			go func() {
				d, err := ln.Accept()
				if err != nil {
					return
				}
				defer d.Close()
				u, err := net.Dial("tcp", hostport(t, s.addr))
				if err != nil {
					return
				}
				defer u.Close()
				wmu.Lock()
				toNode = d
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
			cfg := &config.TunnelUplink{Name: "srv", Password: fakePassword, PoolSize: 1, Pin: fp,
				RetryInterval: config.Duration(time.Hour), PingInterval: config.Duration(time.Hour),
				Endpoints: []config.TunnelEndpoint{{Address: relayed(t, s.addr, ln.Addr().String())}}}
			u := newUplink(t, cfg)
			u.Start()
			waitFor(t, "registered through the relay", func() bool { return u.Status().State == StateRegistered })
			// The frame, and enough after it that any record its first
			// bytes could start is whole.
			wmu.Lock()
			_, _ = toNode.Write(append(kickFrame("injected by a relay"), make([]byte, 20<<10)...))
			wmu.Unlock()
			waitFor(t, "the link to fail", func() bool { return u.Status().State != StateRegistered })
			if st := u.Status(); strings.Contains(st.Error, "injected") {
				t.Fatalf("the node served the relay's frame: %q", st.Error)
			}
		})
	}
}

// A relay that ends the node's TLS itself, with a certificate of its own,
// and opens its own to the hub, could read and write every frame; the node
// pins the hub's certificate, and does not complete the handshake with it.
func TestRelayWithItsOwnCertificate(t *testing.T) {
	for _, scheme := range []string{"tls", "wss"} {
		t.Run(scheme, func(t *testing.T) {
			s, fp := tlsHub(t, scheme)
			mine, _ := certPair(t)
			alpn := transport.TLSALPN
			if scheme == "wss" {
				alpn = "http/1.1"
			}
			ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{mine}, NextProtos: []string{alpn}})
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			handshakes := make(chan error, 4)
			go func() {
				for {
					d, err := ln.Accept()
					if err != nil {
						return
					}
					go func() {
						defer d.Close()
						handshakes <- d.(*tls.Conn).Handshake()
						u, err := tls.Dial("tcp", hostport(t, s.addr), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{alpn}})
						if err != nil {
							return
						}
						defer u.Close()
						go func() { _, _ = io.Copy(u, d) }()
						_, _ = io.Copy(d, u)
					}()
				}
			}()
			cfg := &config.TunnelUplink{Name: "srv", Password: fakePassword, PoolSize: 1, Pin: fp,
				RetryInterval: config.Duration(50 * time.Millisecond), PingInterval: config.Duration(time.Hour),
				Endpoints: []config.TunnelEndpoint{{Address: relayed(t, s.addr, ln.Addr().String())}}}
			u := newUplink(t, cfg)
			u.Start()
			waitFor(t, "the pin refused", func() bool { return strings.Contains(u.Status().Error, "does not match the pin") })
			select {
			case err := <-handshakes:
				if err == nil {
					t.Fatal("the node completed a handshake with the relay")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the relay saw no handshake")
			}
			time.Sleep(200 * time.Millisecond)
			if st := u.Status(); st.State == StateRegistered || s.conns.Load() != 0 {
				t.Fatalf("registered through the relay: %+v, %d at the hub", st, s.conns.Load())
			}
		})
	}
}

// Pinned to the hub's own certificate, tls and wss register directly.
func TestTLSRegisters(t *testing.T) {
	for _, scheme := range []string{"tls", "wss"} {
		t.Run(scheme, func(t *testing.T) {
			s, fp := tlsHub(t, scheme)
			cfg := &config.TunnelUplink{Name: "srv", Password: fakePassword, PoolSize: 2, Pin: fp,
				RetryInterval: config.Duration(20 * time.Millisecond), PingInterval: config.Duration(20 * time.Millisecond),
				Endpoints: []config.TunnelEndpoint{{Address: s.addr}}}
			u := newUplink(t, cfg)
			u.Start()
			waitFor(t, "the pool registered", func() bool { st := u.Status(); return st.Pool == 2 })
			if st := u.Status(); st.Links[0].Proto != scheme || st.Pin != fp || strings.Contains(st.Addr, "pin=") {
				t.Fatalf("status %+v", st)
			}
		})
	}
}
