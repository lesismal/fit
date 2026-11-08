package pgwire

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// A scripted PostgreSQL backend in the process: each connection runs the
// test's script, which reads the client's messages and writes the server's.
// TLS is offered when a certificate is set; a CancelRequest is counted.
type fakeConn struct {
	t        *testing.T
	nc       net.Conn
	br       *bufio.Reader
	cert     *tls.Certificate
	params   map[string]string
	lastBody []byte
}

type fakeOpts struct {
	cert    *tls.Certificate
	cancels chan [2]uint32
}

func fakeServer(t *testing.T, fn func(f *fakeConn)) string {
	return fakeServerOpts(t, fakeOpts{}, fn)
}

func fakeServerOpts(t *testing.T, o fakeOpts, fn func(f *fakeConn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer nc.Close()
				nc.SetDeadline(time.Now().Add(time.Minute)) // a hang guard
				br := bufio.NewReader(nc)
				head, _ := br.Peek(8)
				if len(head) == 8 && binary.BigEndian.Uint32(head[4:]) == cancelCode {
					var b [16]byte
					io.ReadFull(br, b[:])
					if o.cancels != nil {
						o.cancels <- [2]uint32{binary.BigEndian.Uint32(b[8:]), binary.BigEndian.Uint32(b[12:])}
					}
					return
				}
				fn(&fakeConn{t: t, nc: nc, br: br, cert: o.cert})
			}()
		}
	}()
	return ln.Addr().String()
}

// startup reads the startup message, answering an SSLRequest first.
func (f *fakeConn) startup() map[string]string {
	for {
		var n uint32
		if err := binary.Read(f.br, binary.BigEndian, &n); err != nil || n < 8 {
			return nil
		}
		b := make([]byte, n-4)
		if _, err := io.ReadFull(f.br, b); err != nil {
			return nil
		}
		if binary.BigEndian.Uint32(b) == sslRequestCode {
			if f.cert == nil {
				f.nc.Write([]byte{'N'})
				continue
			}
			f.nc.Write([]byte{'S'})
			tc := tls.Server(f.nc, &tls.Config{Certificates: []tls.Certificate{*f.cert}})
			if err := tc.Handshake(); err != nil {
				return nil
			}
			f.nc = tc
			f.br = bufio.NewReader(tc)
			continue
		}
		f.params = map[string]string{}
		parts := strings.Split(string(b[4:]), "\x00")
		for i := 0; i+1 < len(parts); i += 2 {
			f.params[parts[i]] = parts[i+1]
		}
		return f.params
	}
}

func (f *fakeConn) read() (byte, []byte) {
	var hdr [5]byte
	if _, err := io.ReadFull(f.br, hdr[:]); err != nil {
		return 0, nil
	}
	b := make([]byte, binary.BigEndian.Uint32(hdr[1:])-4)
	io.ReadFull(f.br, b)
	f.lastBody = b
	return hdr[0], b
}

// until reads messages up to one of type t, returning the types read.
func (f *fakeConn) until(t byte) string {
	var seen []byte
	for {
		typ, _ := f.read()
		if typ == 0 {
			return string(seen)
		}
		seen = append(seen, typ)
		if typ == t {
			return string(seen)
		}
	}
}

func (f *fakeConn) send(t byte, body ...[]byte) {
	var all []byte
	for _, b := range body {
		all = append(all, b...)
	}
	m := append([]byte{t, 0, 0, 0, 0}, all...)
	binary.BigEndian.PutUint32(m[1:], uint32(len(all)+4))
	f.nc.Write(m)
}

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }
func cs(s string) []byte  { return append([]byte(s), 0) }

func (f *fakeConn) ready() {
	f.send('R', u32(0))
	f.send('S', cs("server_version"), cs("16.4 (Debian)"))
	f.send('K', u32(42), u32(7))
	f.send('Z', []byte{'I'})
}

func (f *fakeConn) z() { f.send('Z', []byte{'I'}) }

func (f *fakeConn) errResp(code, msg string) {
	f.send('E', cs("SERROR"), cs("VERROR"), cs("C"+code), cs("M"+msg), []byte{0})
}

// rowDesc sends a RowDescription of columns named and typed by pairs.
func (f *fakeConn) rowDesc(cols ...any) {
	b := u16(uint16(len(cols) / 2))
	for i := 0; i < len(cols); i += 2 {
		b = append(b, cs(cols[i].(string))...)
		b = append(append(append(b, u32(0)...), u16(0)...), u32(uint32(cols[i+1].(int)))...)
		b = append(append(append(b, u16(0)...), u32(0xFFFFFFFF)...), u16(0)...)
	}
	f.send('T', b)
}

// dataRow sends a DataRow; a nil value is NULL.
func (f *fakeConn) dataRow(vals ...[]byte) {
	b := u16(uint16(len(vals)))
	for _, v := range vals {
		if v == nil {
			b = append(b, u32(0xFFFFFFFF)...)
			continue
		}
		b = append(append(b, u32(uint32(len(v)))...), v...)
	}
	f.send('D', b)
}

// prepared answers a Parse, Describe and Sync with the parameter types and
// the columns given (RowDescription when there are columns, else NoData).
func (f *fakeConn) prepared(params []uint32, cols ...any) {
	f.until('S')
	f.send('1')
	b := u16(uint16(len(params)))
	for _, p := range params {
		b = append(b, u32(p)...)
	}
	f.send('t', b)
	if len(cols) > 0 {
		f.rowDesc(cols...)
	} else {
		f.send('n')
	}
	f.z()
}

// scram runs the server's side of SCRAM-SHA-256 for password, with the
// mechanisms offered, and returns the client's gs2 header; cb is the
// channel binding data a -PLUS exchange must carry.
func (f *fakeConn) scram(password string, mechs []string, cb []byte) string {
	b := u32(10)
	for _, m := range mechs {
		b = append(b, cs(m)...)
	}
	f.send('R', append(b, 0))
	typ, init := f.read()
	if typ != 'p' {
		return "" // the client gave up
	}
	mech, rest, _ := cstring(init)
	first := string(rest[4:])
	gs2end := strings.Index(first, ",,") + 2
	gs2, bare := first[:gs2end], first[gs2end:]
	if (mech == "SCRAM-SHA-256-PLUS") != strings.HasPrefix(gs2, "p=") {
		f.t.Errorf("mechanism %s with gs2 %q", mech, gs2)
	}
	cnonce := bare[strings.Index(bare, "r=")+2:]
	salt := []byte("salt-of-the-fake")
	serverFirst := "r=" + cnonce + "srv,s=" + base64.StdEncoding.EncodeToString(salt) + ",i=4096"
	f.send('R', u32(11), []byte(serverFirst))
	if typ, _ := f.read(); typ != 'p' {
		return gs2
	}
	final := string(f.lastBody)
	noProof := final[:strings.Index(final, ",p=")]
	proof, _ := base64.StdEncoding.DecodeString(final[strings.Index(final, ",p=")+3:])
	wantC := "c=" + base64.StdEncoding.EncodeToString(append([]byte(gs2), cbFor(gs2, cb)...))
	if !strings.HasPrefix(noProof, wantC+",") {
		f.t.Errorf("client final %q, want %q", noProof, wantC)
	}
	salted, _ := pbkdf2.Key(sha256.New, password, salt, 4096, 32)
	stored := sha256.Sum256(hmacSum(salted, "Client Key"))
	authMsg := bare + "," + serverFirst + "," + noProof
	sig := hmacSum(stored[:], authMsg)
	ck := make([]byte, len(proof))
	for i := range ck {
		ck[i] = proof[i] ^ sig[i%len(sig)]
	}
	if got := sha256.Sum256(ck); !hmac.Equal(got[:], stored[:]) {
		f.errResp("28P01", "password authentication failed")
		return gs2
	}
	f.send('R', u32(12), []byte("v="+base64.StdEncoding.EncodeToString(hmacSum(hmacSum(salted, "Server Key"), authMsg))))
	f.ready()
	return gs2
}

func cbFor(gs2 string, cb []byte) []byte {
	if strings.HasPrefix(gs2, "p=") {
		return cb
	}
	return nil
}

// testCert makes a self-signed certificate for 127.0.0.1, ECDSA P-256 with
// SHA-256 or Ed25519.
func testCert(t *testing.T, ed bool) (*tls.Certificate, *x509.CertPool, *x509.Certificate) {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	var pub, priv any
	if ed {
		pk, sk, _ := ed25519.GenerateKey(rand.Reader)
		pub, priv = pk, sk
	} else {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		pub, priv = &k.PublicKey, k
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: leaf}, pool, leaf
}

func ctx(t *testing.T) context.Context {
	if t == nil {
		return context.Background()
	}
	c, cancel := context.WithTimeout(context.Background(), time.Minute) // a hang guard
	t.Cleanup(cancel)
	return c
}

// session connects to a fake that completes the startup without
// authentication and then runs script.
func session(t *testing.T, script func(f *fakeConn)) *Conn {
	t.Helper()
	addr := fakeServer(t, func(f *fakeConn) {
		f.startup()
		f.ready()
		script(f)
	})
	c, err := Connect(ctx(t), Config{Addr: addr, User: "u", SSLMode: SSLDisable})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
