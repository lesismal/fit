package mongowire

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMongo is an in-process server speaking OP_MSG: it answers hello as
// its hello func says, runs SCRAM-SHA-256 for user and pass, and hands
// every other command to handle.
type fakeMongo struct {
	t    testing.TB
	ln   net.Listener
	addr string

	mu     sync.Mutex
	hello  func() D
	handle func(name string, cmd Raw) any
	user   string
	pass   string
	iter   int
	// scramMode makes SCRAM go wrong: "badfirst", "badsig", "neverdone",
	// "continuefail", "lastfail".
	scramMode string
	conns     []net.Conn
	cmds      []string
}

// The things handle can answer besides a D, the answer's fields.
type (
	noAnswer  struct{}           // wait until the connection is closed
	closeConn struct{}           // close the connection
	rawAnswer struct{ b []byte } // these bytes, as they are
)

func newFake(t testing.TB) *fakeMongo {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return newFakeOn(t, ln)
}

func newFakeOn(t testing.TB, ln net.Listener) *fakeMongo {
	f := &fakeMongo{t: t, ln: ln, addr: ln.Addr().String(), iter: 4096}
	f.hello = func() D { return D{{"isWritablePrimary", true}} }
	f.handle = func(string, Raw) any { return D{} }
	go f.serve()
	t.Cleanup(f.close)
	return f
}

func (f *fakeMongo) close() {
	f.ln.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		c.Close()
	}
}

func (f *fakeMongo) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *fakeMongo) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func (f *fakeMongo) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, c)
		f.mu.Unlock()
		go f.conn(c)
	}
}

func (f *fakeMongo) conn(c net.Conn) {
	defer c.Close()
	rd := bufio.NewReader(c)
	var scr *scramServer
	for {
		var hdr [headerSize]byte
		if _, err := io.ReadFull(rd, hdr[:]); err != nil {
			return
		}
		msg := make([]byte, binary.LittleEndian.Uint32(hdr[:]))
		copy(msg, hdr[:])
		if _, err := io.ReadFull(rd, msg[headerSize:]); err != nil {
			return
		}
		id := int32(binary.LittleEndian.Uint32(hdr[4:]))
		_, body, seqs, err := parseMsg(msg)
		if err != nil {
			f.t.Errorf("fake: %v", err)
			return
		}
		name := body.Keys()[0]
		f.mu.Lock()
		f.cmds = append(f.cmds, name)
		hello, handle, user, pass, iter, mode := f.hello, f.handle, f.user, f.pass, f.iter, f.scramMode
		f.mu.Unlock()
		var ans any
		switch name {
		case "hello":
			d := hello()
			if !hasKey(d, "maxWireVersion") {
				d = append(d, E{"maxWireVersion", int32(21)})
			}
			if v, ok := body.Lookup("saslSupportedMechs"); ok {
				if s, _ := v.StringValue(); s == "admin."+user {
					d = append(d, E{"saslSupportedMechs", A{"SCRAM-SHA-256"}})
				}
			}
			ans = d
		case "saslStart":
			scr = &scramServer{user: user, pass: pass, iter: iter, mode: mode}
			ans = scr.start(body)
		case "saslContinue":
			ans = scr.next(body)
		default:
			if len(seqs) > 0 {
				body = append(Raw(nil), body...)
			}
			ans = handle(name, body)
		}
		var out []byte
		switch a := ans.(type) {
		case noAnswer:
			_, _ = io.Copy(io.Discard, rd)
			return
		case closeConn:
			return
		case rawAnswer:
			out = a.b
		case D:
			if !hasKey(a, "ok") {
				a = append(a, E{"ok", 1.0})
			}
			r, err := Marshal(a)
			if err != nil {
				f.t.Errorf("fake: %v", err)
				return
			}
			out = appendMsg(nil, 0, r, nil)
			binary.LittleEndian.PutUint32(out[8:], uint32(id))
		}
		if _, err := c.Write(out); err != nil {
			return
		}
	}
}

func hasKey(d D, k string) bool {
	for _, e := range d {
		if e.Key == k {
			return true
		}
	}
	return false
}

// scramServer is the server's side of SCRAM-SHA-256.
type scramServer struct {
	user, pass  string
	iter        int
	mode        string
	clientFirst string
	serverFirst string
}

var testSalt = []byte("fit-mongowire-salt")

func (s *scramServer) start(body Raw) D {
	v, _ := body.Lookup("payload")
	b, _ := v.Binary()
	s.clientFirst = strings.TrimPrefix(string(b.Data), "n,,")
	a := scramAttrs(s.clientFirst)
	if a["n"] != strings.NewReplacer("=", "=3D", ",", "=2C").Replace(s.user) {
		return D{{"ok", 0.0}, {"code", int32(18)}, {"codeName", "AuthenticationFailed"}, {"errmsg", "Authentication failed."}}
	}
	if s.mode == "badfirst" {
		return D{{"conversationId", int32(1)}, {"done", false}, {"payload", []byte("r=x,s=AA==,i=4096")}}
	}
	s.serverFirst = "r=" + a["r"] + "srv,s=" + base64.StdEncoding.EncodeToString(testSalt) + ",i=" + itoa(s.iter)
	return D{{"conversationId", int32(1)}, {"done", false}, {"payload", []byte(s.serverFirst)}}
}

func (s *scramServer) next(body Raw) D {
	v, _ := body.Lookup("payload")
	b, _ := v.Binary()
	if len(b.Data) == 0 {
		switch s.mode {
		case "neverdone":
			return D{{"conversationId", int32(1)}, {"done", false}, {"payload", []byte{}}}
		case "lastfail":
			return D{{"ok", 0.0}, {"code", int32(18)}, {"errmsg", "no"}}
		}
		return D{{"conversationId", int32(1)}, {"done", true}, {"payload", []byte{}}}
	}
	if s.mode == "continuefail" {
		return D{{"ok", 0.0}, {"code", int32(18)}, {"errmsg", "no"}}
	}
	final := string(b.Data)
	i := strings.LastIndex(final, ",p=")
	salted, _ := pbkdf2.Key(sha256.New, s.pass, testSalt, s.iter, 32)
	clientKey := hmacSHA256(salted, "Client Key")
	stored := sha256.Sum256(clientKey)
	authMsg := s.clientFirst + "," + s.serverFirst + "," + final[:i]
	sig := hmacSHA256(stored[:], authMsg)
	proof, _ := base64.StdEncoding.DecodeString(final[i+3:])
	for j := range proof {
		proof[j] ^= sig[j]
	}
	if !hmac.Equal(proof, clientKey) {
		return D{{"ok", 0.0}, {"code", int32(18)}, {"codeName", "AuthenticationFailed"}, {"errmsg", "Authentication failed."}}
	}
	v2 := base64.StdEncoding.EncodeToString(hmacSHA256(hmacSHA256(salted, "Server Key"), authMsg))
	if s.mode == "badsig" {
		v2 = base64.StdEncoding.EncodeToString([]byte("wrong"))
	}
	return D{{"conversationId", int32(1)}, {"done", false}, {"payload", []byte("v=" + v2)}}
}

// testCert returns a self-signed certificate for 127.0.0.1, and its key.
func testCert(t testing.TB) (certPEM, keyPEM []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fit test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd})
}
