package mysqlwire

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

// fake is the server's end of a connection in the tests: it reads and
// writes packets as a server does, and a test's script says what it
// answers.
type fake struct {
	t    *testing.T
	nc   net.Conn
	br   *bufio.Reader
	seq  byte
	caps uint32

	scramble []byte
	user     string
	auth     []byte
	db       string
	plugin   string
}

// serve listens and runs script for the first connection; the address is
// for Dial. The listener and the connection close when the test ends.
func serve(t *testing.T, script func(f *fake)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan net.Conn, 1)
	go func() {
		defer wg.Done()
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		done <- nc
		f := &fake{t: t, nc: nc, br: bufio.NewReader(nc), scramble: []byte("abcdefghij0123456789")}
		defer func() {
			_ = recover() // a read after the client left ends the script
			f.nc.Close()
		}()
		script(f)
	}()
	t.Cleanup(func() {
		ln.Close()
		select {
		case nc := <-done:
			nc.Close()
		default:
		}
		wg.Wait()
	})
	return ln.Addr().String()
}

type fakeEnd struct{}

// read reads a logical packet; at the stream's end the script stops.
func (f *fake) read() []byte {
	var out []byte
	for {
		var h [4]byte
		if _, err := io.ReadFull(f.br, h[:]); err != nil {
			panic(fakeEnd{})
		}
		n := int(h[0]) | int(h[1])<<8 | int(h[2])<<16
		if h[3] != f.seq {
			f.t.Errorf("fake: packet %d, expected %d", h[3], f.seq)
		}
		f.seq = h[3] + 1
		b := make([]byte, n)
		if _, err := io.ReadFull(f.br, b); err != nil {
			panic(fakeEnd{})
		}
		out = append(out, b...)
		if n < maxPayload {
			return out
		}
	}
}

// cmd reads a command: its byte and its body.
func (f *fake) cmd() (byte, []byte) {
	f.seq = 0
	p := f.read()
	if len(p) == 0 {
		f.t.Error("fake: an empty command")
		return 0, nil
	}
	return p[0], p[1:]
}

// write writes a logical packet, split as the protocol splits it.
func (f *fake) write(p []byte) {
	for {
		n := min(len(p), maxPayload)
		var h [4]byte
		putHeader(h[:], n, f.seq)
		f.seq++
		if _, err := f.nc.Write(append(h[:], p[:n]...)); err != nil {
			panic(fakeEnd{})
		}
		p = p[n:]
		if n < maxPayload {
			return
		}
	}
}

const fakeCaps = capLongPassword | capLongFlag | capConnectWithDB | capProtocol41 | capTransactions |
	capSecureConnection | capMultiResults | capPSMultiResults | capPluginAuth | capPluginAuthLenEnc

// greet sends the handshake with plugin and the server's caps.
func (f *fake) greet(version string, caps uint32, plugin string) {
	b := []byte{10}
	b = append(b, version...)
	b = append(b, 0)
	b = binary.LittleEndian.AppendUint32(b, 42)
	b = append(b, f.scramble[:8]...)
	b = append(b, 0)
	b = binary.LittleEndian.AppendUint16(b, uint16(caps))
	b = append(b, 45)
	b = binary.LittleEndian.AppendUint16(b, StatusAutocommit)
	b = binary.LittleEndian.AppendUint16(b, uint16(caps>>16))
	b = append(b, byte(len(f.scramble)+1))
	b = append(b, make([]byte, 10)...)
	b = append(b, f.scramble[8:]...)
	b = append(b, 0)
	b = append(b, plugin...)
	b = append(b, 0)
	f.write(b)
}

// response reads the handshake response, through TLS when the client asks
// for it.
func (f *fake) response() {
	p := f.read()
	r := rd{b: p}
	f.caps = r.u32()
	r.take(4 + 1 + 23)
	if len(p) == 32 {
		if f.caps&capSSL == 0 {
			f.t.Error("fake: an SSL request without capSSL")
		}
		tc := tls.Server(f.nc, &tls.Config{Certificates: []tls.Certificate{testCert(f.t)}})
		if err := tc.Handshake(); err != nil {
			panic(fakeEnd{})
		}
		f.nc = tc
		f.br = bufio.NewReader(tc)
		p = f.read()
		r = rd{b: p[32:]}
	}
	f.user = string(r.nul())
	if f.caps&capPluginAuthLenEnc != 0 {
		f.auth = r.lenEncBytes()
	} else {
		f.auth = r.take(int(r.u8()))
	}
	if f.caps&capConnectWithDB != 0 {
		f.db = string(r.nul())
	}
	f.plugin = string(r.nul())
	if r.bad {
		f.t.Error("fake: a malformed handshake response")
	}
}

// ok sends an OK packet.
func (f *fake) ok(affected, insertID uint64, status uint16) {
	b := []byte{0}
	b = appendLenEnc(b, affected)
	b = appendLenEnc(b, insertID)
	b = binary.LittleEndian.AppendUint16(b, status)
	b = binary.LittleEndian.AppendUint16(b, 0)
	f.write(b)
}

// end sends what ends a result set: EOF, or OK with 0xfe with
// DEPRECATE_EOF.
func (f *fake) end(status uint16) {
	if f.caps&capDeprecateEOF != 0 {
		b := []byte{0xfe, 0, 0}
		b = binary.LittleEndian.AppendUint16(b, status)
		f.write(append(b, 0, 0))
		return
	}
	b := []byte{0xfe, 0, 0}
	f.write(binary.LittleEndian.AppendUint16(b, status))
}

func (f *fake) eof() {
	if f.caps&capDeprecateEOF == 0 {
		f.write([]byte{0xfe, 0, 0, StatusAutocommit, 0})
	}
}

// fail sends an ERR packet.
func (f *fake) fail(code uint16, state, msg string) {
	b := []byte{0xff}
	b = binary.LittleEndian.AppendUint16(b, code)
	if state != "" {
		b = append(b, '#')
		b = append(b, state...)
	}
	f.write(append(b, msg...))
}

// login answers a native-password client: the greeting, its response, OK.
func (f *fake) login(caps uint32) {
	f.greet("8.0.40", caps, pluginNative)
	f.response()
	f.ok(0, 0, StatusAutocommit)
}

func colDef(c Column) []byte {
	b := appendLenEncBytes(nil, "def")
	for _, s := range []string{c.Schema, c.Table, c.OrgTable, c.Name, c.OrgName} {
		b = appendLenEncBytes(b, s)
	}
	b = append(b, 0x0c)
	b = binary.LittleEndian.AppendUint16(b, c.Collation)
	b = binary.LittleEndian.AppendUint32(b, c.Length)
	b = append(b, c.Type)
	b = binary.LittleEndian.AppendUint16(b, c.Flags)
	return append(b, c.Decimals, 0, 0)
}

// columns sends a column count and the columns.
func (f *fake) columns(cols []Column) {
	f.write(appendLenEnc(nil, uint64(len(cols))))
	for _, c := range cols {
		f.write(colDef(c))
	}
	f.eof()
}

// textRow sends a text row; nil is NULL.
func (f *fake) textRow(vals ...[]byte) {
	var b []byte
	for _, v := range vals {
		if v == nil {
			b = append(b, 0xfb)
		} else {
			b = appendLenEncBytes(b, v)
		}
	}
	f.write(b)
}

var (
	certOnce sync.Once
	cert     tls.Certificate
	certPool *x509.CertPool
)

// testCert is a self-signed certificate for 127.0.0.1.
func testCert(t *testing.T) tls.Certificate {
	certOnce.Do(func() {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, IsCA: true, BasicConstraintsValid: true,
			KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		cert = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
		c, _ := x509.ParseCertificate(der)
		certPool = x509.NewCertPool()
		certPool.AddCert(c)
	})
	return cert
}

func clientTLS(t *testing.T) *tls.Config {
	testCert(t)
	return &tls.Config{RootCAs: certPool}
}

var (
	rsaOnce sync.Once
	rsaKey  *rsa.PrivateKey
)

func testRSA(t *testing.T) *rsa.PrivateKey {
	rsaOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		rsaKey = k
	})
	return rsaKey
}

func rsaPEM(t *testing.T) []byte {
	der, err := x509.MarshalPKIXPublicKey(&testRSA(t).PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// decrypt reads a password encrypted as encryptPassword does.
func (f *fake) decrypt(b []byte) string {
	plain, err := rsa.DecryptOAEP(sha1.New(), nil, testRSA(f.t), b, nil)
	if err != nil {
		f.t.Errorf("fake: decrypting: %v", err)
		return ""
	}
	for i := range plain {
		plain[i] ^= f.scramble[i%len(f.scramble)]
	}
	return string(bytes.TrimSuffix(plain, []byte{0}))
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func dialT(t *testing.T, cfg Config) *Conn {
	t.Helper()
	c, err := Dial(ctxT(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
