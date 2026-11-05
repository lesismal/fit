package transport

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
	fibtls "github.com/lesismal/fib/tls"

	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// selfSigned is a self-signed certificate for hosts, and its fingerprint.
func selfSigned(t *testing.T, hosts ...string) (tls.Certificate, string) {
	t.Helper()
	c, k, err := NewSelfSigned(hosts...)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(c, k)
	if err != nil {
		t.Fatal(err)
	}
	return cert, Fingerprint(cert)
}

// tlsListen serves tls:// on 127.0.0.1 with cert, recording each link.
func tlsListen(t *testing.T, e *fib.Engine, cert tls.Certificate) (Listener, <-chan *recorder) {
	t.Helper()
	srv := make(chan *recorder, 8)
	l, err := Listen(e, "tls://127.0.0.1:0", ListenOptions{Cert: &cert}, func(c Conn) Handler {
		r := newRecorder()
		go func() { srv <- r }()
		return &echoer{c: c, r: r}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, srv
}

// echoer sends back what arrives, and records it.
type echoer struct {
	c Conn
	r *recorder
}

func (h *echoer) OnData(data []byte) {
	h.r.OnData(data)
	_ = h.c.Send(append([]byte(nil), data...))
}

func (h *echoer) OnClose(err error) { h.r.OnClose(err) }

// A tls:// link carries bytes both ways, a large transfer whole and in
// order, and closes at the other end.
func TestTLS(t *testing.T) {
	e := tunneltest.Engine(t)
	cert, fp := selfSigned(t, "127.0.0.1")
	l, srv := tlsListen(t, e, cert)
	if !strings.HasPrefix(l.URL(), "tls://127.0.0.1:") {
		t.Fatalf("URL %q", l.URL())
	}
	rc := newRecorder()
	c, err := Dial(e, l.URL()+"?"+ClientQuery(fp, "", false), 5*time.Second, func(Conn) Handler { return rc })
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 8<<20)
	_, _ = rand.Read(data)
	for off := 0; off < len(data); off += 256 << 10 {
		if err := c.Send(append([]byte(nil), data[off:off+256<<10]...)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the echo", func() bool { return len(rc.String()) >= len(data) })
	if !bytes.Equal([]byte(rc.String()), data) {
		t.Fatal("the echo differs")
	}
	// No handshake resumes a session: each checks the certificate.
	if st, ok := fibtls.ConnectionState(c.(*fibConn).c); !ok || st.DidResume || st.NegotiatedProtocol != TLSALPN {
		t.Fatalf("state %+v %v", st, ok)
	}
	rc2 := newRecorder()
	c2, err := Dial(e, l.URL()+"?pin="+fp, 5*time.Second, func(Conn) Handler { return rc2 })
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := fibtls.ConnectionState(c2.(*fibConn).c); st.DidResume {
		t.Fatal("a second dial resumed the first's session")
	}
	c2.CloseAfterSend()
	select {
	case <-rc2.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the link did not close")
	}
	// The server's end of the first link closes with it.
	r := <-srv
	_ = c.Close()
	select {
	case <-r.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the server's link did not close")
	}
}

// A certificate that is not the pinned one fails the dial, saying so; so
// does a pin that is not one, and insecure, which only quic takes.
func TestTLSPin(t *testing.T) {
	e := tunneltest.Engine(t)
	cert, _ := selfSigned(t, "127.0.0.1")
	_, other := selfSigned(t, "127.0.0.1")
	l, _ := tlsListen(t, e, cert)
	open := func(Conn) Handler { return newRecorder() }
	if _, err := Dial(e, l.URL()+"?pin="+other, 5*time.Second, open); err == nil || !strings.Contains(err.Error(), "does not match the pin") {
		t.Fatalf("another pin: %v", err)
	}
	if _, err := Dial(e, l.URL()+"?pin=abcd", 5*time.Second, open); err == nil || !strings.Contains(err.Error(), "not a certificate pin") {
		t.Fatalf("a short pin: %v", err)
	}
	if _, err := Dial(e, l.URL()+"?insecure=1", 5*time.Second, open); err == nil || !strings.Contains(err.Error(), "takes no insecure") {
		t.Fatalf("insecure: %v", err)
	}
	// Without a pin or CAs, the system's roots refuse a self-signed one.
	if _, err := Dial(e, l.URL(), 5*time.Second, open); err == nil {
		t.Fatal("the system's roots took a self-signed certificate")
	}
	for _, bad := range []string{"tls://127.0.0.1", "tls://%zz:1", "tls://127.0.0.1:1?%zz"} {
		if _, err := Dial(e, bad, time.Second, open); err == nil {
			t.Errorf("dialed %q", bad)
		}
	}
	if _, err := Listen(e, "tls://127.0.0.1:0", ListenOptions{}, func(Conn) Handler { return nil }); err == nil {
		t.Fatal("a tls listener without a certificate")
	}
}

// A pin is checked on a handshake that resumes a session too: a client
// given a session cache, which the links never are, still refuses a
// certificate that is not the pinned one.
func TestTLSPinOnResumption(t *testing.T) {
	e := tunneltest.Engine(t)
	cert, fp := selfSigned(t, "127.0.0.1")
	l, _ := tlsListen(t, e, cert)
	hostport := strings.TrimPrefix(l.URL(), "tls://")
	cache := fibtls.NewQUICSessionCache(4)
	dial := func(pin string) (*fib.Connection, error) {
		conf, err := ClientTLSConfig(ClientOptions{ServerName: "127.0.0.1", PinSHA256: pin}, TLSALPN)
		if err != nil {
			t.Fatal(err)
		}
		type result struct {
			c   *fib.Connection
			err error
		}
		ch := make(chan result, 1)
		w := &handshakeWait{inner: fib.HandlerFuncs{}, done: func(c *fib.Connection, err error) { ch <- result{c, err} }}
		if err := fibtls.DialWithOptions(e, "tcp", hostport, 5*time.Second, conf, fibtls.DialOptions{SessionCache: cache}, w,
			func(_ *fib.Connection, err error) {
				if err != nil {
					w.finish(nil, err)
				}
			}); err != nil {
			return nil, err
		}
		select {
		case r := <-ch:
			return r.c, r.err
		case <-time.After(10 * time.Second):
			t.Fatal("no handshake")
			return nil, nil
		}
	}
	c, err := dial(fp)
	if err != nil {
		t.Fatal(err)
	}
	// The ticket comes after the handshake.
	time.Sleep(200 * time.Millisecond)
	_ = c.Close()
	c, err = dial(fp)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := fibtls.ConnectionState(c)
	_ = c.Close()
	if !st.DidResume {
		t.Skip("the session was not resumed; nothing to check")
	}
	if _, err := dial(strings.Repeat("0", 64)); err == nil {
		t.Fatal("a resumed session skipped the pin")
	}
}

// writeCA makes a CA and a certificate it signs for hosts, writes the CA's
// PEM into a file, and returns the file and the certificate.
func writeCA(t *testing.T, hosts ...string) (string, tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: hosts[0]}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return file, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// With ca, a dial checks the chain to those CAs and the name it dials.
func TestTLSCA(t *testing.T) {
	e := tunneltest.Engine(t)
	caFile, cert := writeCA(t, "localhost")
	l, _ := tlsListen(t, e, cert)
	port := l.URL()[strings.LastIndex(l.URL(), ":")+1:]
	open := func(Conn) Handler { return newRecorder() }
	c, err := Dial(e, "tls://localhost:"+port+"?"+ClientQuery("", caFile, false), 5*time.Second, open)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	// The certificate is for localhost, not 127.0.0.1.
	if _, err := Dial(e, "tls://127.0.0.1:"+port+"?ca="+caFile, 5*time.Second, open); err == nil {
		t.Fatal("a certificate for another name was taken")
	}
	// Another CA does not sign it.
	otherCA, _ := writeCA(t, "localhost")
	if _, err := Dial(e, "tls://localhost:"+port+"?ca="+otherCA, 5*time.Second, open); err == nil {
		t.Fatal("a certificate another CA signed was taken")
	}
	if _, err := Dial(e, "tls://localhost:"+port+"?ca="+filepath.Join(t.TempDir(), "none.pem"), 5*time.Second, open); err == nil || !strings.Contains(err.Error(), "the hub's CAs") {
		t.Fatalf("a missing CA file: %v", err)
	}
	empty := filepath.Join(t.TempDir(), "empty.pem")
	_ = os.WriteFile(empty, []byte("nothing"), 0o600)
	if _, err := Dial(e, "tls://localhost:"+port+"?ca="+empty, 5*time.Second, open); err == nil || !strings.Contains(err.Error(), "no PEM certificate") {
		t.Fatalf("a CA file without certificates: %v", err)
	}
}

// A server that closes before the handshake is over fails the dial.
func TestTLSClosedInHandshake(t *testing.T) {
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
	e := tunneltest.Engine(t)
	if _, err := Dial(e, "tls://"+ln.Addr().String()+"?pin="+strings.Repeat("0", 64), 5*time.Second, func(Conn) Handler { return newRecorder() }); err == nil {
		t.Fatal("a dial to a server that closed worked")
	}
	if _, err := Dial(e, "tls://"+closedTCP(t)+"?pin="+strings.Repeat("0", 64), 2*time.Second, func(Conn) Handler { return newRecorder() }); err == nil {
		t.Fatal("a dial to a closed port worked")
	}
}

func closedTCP(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestClientQuery(t *testing.T) {
	for _, tc := range []struct {
		pin, ca  string
		insecure bool
		want     string
	}{
		{"ab", "c", true, "pin=ab"},
		{"", "/a b", true, "ca=%2Fa+b"},
		{"", "", true, "insecure=1"},
		{"", "", false, ""},
	} {
		q := ClientQuery(tc.pin, tc.ca, tc.insecure)
		if q != tc.want {
			t.Errorf("ClientQuery(%q, %q, %v) = %q", tc.pin, tc.ca, tc.insecure, q)
		}
		o, err := ParseClientQuery(q)
		if err != nil || o.PinSHA256 != tc.pin || tc.pin == "" && o.CAFile != tc.ca || tc.pin == "" && tc.ca == "" && o.InsecureSkipVerify != tc.insecure {
			t.Errorf("ParseClientQuery(%q) = %+v, %v", q, o, err)
		}
	}
	if _, err := ParseClientQuery("%zz"); err == nil {
		t.Error("a bad query parsed")
	}
	if Fingerprint(tls.Certificate{}) != "" {
		t.Error("the fingerprint of no certificate")
	}
}
