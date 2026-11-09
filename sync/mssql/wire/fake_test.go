package mssqlwire

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fake is a TDS server for the tests: it negotiates encryption, checks the
// login, and hands each request to handle.
type fake struct {
	t  *testing.T
	ln net.Listener
	// enc is what the pre-login answers for encryption; noEnc leaves the
	// option out.
	enc    byte
	noEnc  bool
	strict bool
	cert   tls.Certificate
	user   string
	pass   string
	// packetSize is what the login's ENVCHANGE sets; 0 sets none.
	packetSize int
	// replySize splits the server's messages into packets of this size.
	replySize int
	handle    func(s *session, typ byte, body []byte)
	// login, when set, answers the login in place of the usual answer.
	login func(s *session, body []byte)

	mu       sync.Mutex
	sessions []*session
	wg       sync.WaitGroup
}

type session struct {
	f  *fake
	nc net.Conn
	rd io.Reader
	wmu sync.Mutex
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, enc: encryptNotSup, user: "sa", pass: "secret", packetSize: 4096, replySize: 4096}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.ln = ln
	t.Cleanup(f.close)
	return f
}

func (f *fake) start() {
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			nc, err := f.ln.Accept()
			if err != nil {
				return
			}
			s := &session{f: f, nc: nc, rd: nc}
			f.mu.Lock()
			f.sessions = append(f.sessions, s)
			f.mu.Unlock()
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				defer nc.Close()
				s.serve()
			}()
		}
	}()
}

func (f *fake) close() {
	f.ln.Close()
	f.mu.Lock()
	for _, s := range f.sessions {
		s.nc.Close()
	}
	f.mu.Unlock()
	f.wg.Wait()
}

// setHandle replaces handle while sessions run.
func (f *fake) setHandle(h func(s *session, typ byte, body []byte)) {
	f.mu.Lock()
	f.handle = h
	f.mu.Unlock()
}

func (f *fake) setLogin(l func(s *session, body []byte)) {
	f.mu.Lock()
	f.login = l
	f.mu.Unlock()
}

func (f *fake) addr() string { return f.ln.Addr().String() }

func (f *fake) config() Config {
	return Config{Addr: f.addr(), User: f.user, Password: f.pass, Database: "shop", Encrypt: EncryptOff, Timeout: 5 * time.Second}
}

// readPacket reads one packet.
func (s *session) readPacket() (typ, status byte, body []byte, err error) {
	var h [headerLen]byte
	if _, err = io.ReadFull(s.rd, h[:]); err != nil {
		return
	}
	n := int(binary.BigEndian.Uint16(h[2:]))
	body = make([]byte, n-headerLen)
	_, err = io.ReadFull(s.rd, body)
	return h[0], h[1], body, err
}

// readMsg reads a whole message.
func (s *session) readMsg() (byte, []byte, error) {
	var out []byte
	for {
		typ, status, body, err := s.readPacket()
		if err != nil {
			return 0, nil, err
		}
		out = append(out, body...)
		if status&statusEOM != 0 {
			return typ, out, nil
		}
	}
}

// send sends a message, in packets of replySize.
func (s *session) send(typ byte, b []byte) { s.sendPart(typ, b, true) }

// sendPart sends part of a message, its end when eom.
func (s *session) sendPart(typ byte, b []byte, eom bool) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	size := s.f.replySize - headerLen
	for first := true; first || len(b) > 0; first = false {
		n := min(size, len(b))
		h := []byte{typ, 0, 0, 0, 0, 0, 1, 0}
		if n == len(b) && eom {
			h[1] = statusEOM
		}
		binary.BigEndian.PutUint16(h[2:], uint16(n+headerLen))
		s.nc.Write(append(h, b[:n]...))
		b = b[n:]
	}
}

// reply sends tokens as a reply.
func (s *session) reply(tokens ...[]byte) {
	var b []byte
	for _, t := range tokens {
		b = append(b, t...)
	}
	s.send(pktReply, b)
}

// fakeHandshake carries the server's TLS handshake in packets.
type fakeHandshake struct {
	s    *session
	nc   net.Conn
	buf  []byte
	done bool
}

func (h *fakeHandshake) Read(p []byte) (int, error) {
	if h.done {
		return h.nc.Read(p)
	}
	if len(h.buf) == 0 {
		_, _, body, err := h.s.readPacket()
		if err != nil {
			return 0, err
		}
		h.buf = body
	}
	n := copy(p, h.buf)
	h.buf = h.buf[n:]
	return n, nil
}

func (h *fakeHandshake) Write(p []byte) (int, error) {
	if h.done {
		return h.nc.Write(p)
	}
	h.s.send(pktPrelogin, p)
	return len(p), nil
}

func (h *fakeHandshake) Close() error                       { return h.nc.Close() }
func (h *fakeHandshake) LocalAddr() net.Addr                { return h.nc.LocalAddr() }
func (h *fakeHandshake) RemoteAddr() net.Addr               { return h.nc.RemoteAddr() }
func (h *fakeHandshake) SetDeadline(t time.Time) error      { return h.nc.SetDeadline(t) }
func (h *fakeHandshake) SetReadDeadline(t time.Time) error  { return h.nc.SetReadDeadline(t) }
func (h *fakeHandshake) SetWriteDeadline(t time.Time) error { return h.nc.SetWriteDeadline(t) }

func (s *session) serve() {
	f := s.f
	if f.strict {
		tc := tls.Server(s.nc, &tls.Config{Certificates: []tls.Certificate{f.cert}, NextProtos: []string{"tds/8.0"}})
		if tc.Handshake() != nil {
			return
		}
		s.nc, s.rd = tc, tc
	}
	if _, _, err := s.readMsg(); err != nil {
		return
	}
	var pre []byte
	if f.noEnc {
		pre = []byte{0, 0, 6, 0, 6, 0xff, 16, 0, 0x10, 0x2d, 0, 0}
		pre[1], pre[2] = 0, 6
	} else {
		pre = []byte{0, 0, 11, 0, 6, 1, 0, 17, 0, 1, 0xff, 16, 0, 0x10, 0x2d, 0, 0, f.enc}
	}
	s.send(pktReply, pre)
	if !f.strict && !f.noEnc && (f.enc == encryptOn || f.enc == encryptReq) {
		h := &fakeHandshake{s: s, nc: s.nc}
		tc := tls.Server(h, &tls.Config{Certificates: []tls.Certificate{f.cert}})
		if tc.Handshake() != nil {
			return
		}
		h.done = true
		s.nc, s.rd = tc, tc
	}
	typ, body, err := s.readMsg()
	if err != nil || typ != pktLogin7 {
		return
	}
	f.mu.Lock()
	login := f.login
	f.mu.Unlock()
	if login != nil {
		login(s, body)
	} else {
		user, pass := loginField(body, 40), loginField(body, 44)
		for i := range pass {
			pass[i] ^= 0xa5
			pass[i] = pass[i]<<4 | pass[i]>>4
		}
		if ucs2String(user) != f.user || ucs2String(pass) != f.pass {
			s.reply(tError(tokError, 18456, 14, "Login failed for user '"+ucs2String(user)+"'."), tDone(tokDone, doneError, 0))
			return
		}
		toks := [][]byte{tEnv(envDatabase, bvarchar("shop")), tEnv(envCollation, []byte{5, 0x09, 0x04, 0xd0, 0x00, 0x34})}
		if f.packetSize > 0 {
			toks = append(toks, tEnv(envPacketSize, bvarchar(strconv.Itoa(f.packetSize))))
		}
		toks = append(toks, tInfo(5701, "Changed database context to 'shop'."), tLoginAck(), tFeatureAck(), tDone(tokDone, 0, 0))
		s.reply(toks...)
	}
	for {
		typ, body, err := s.readMsg()
		if err != nil {
			return
		}
		if typ == pktAttention {
			s.reply(tDone(tokDone, doneAttn, 0))
			continue
		}
		f.mu.Lock()
		handle := f.handle
		f.mu.Unlock()
		if handle == nil {
			s.reply(tDone(tokDone, 0, 0))
			continue
		}
		handle(s, typ, body)
	}
}

// loginField returns a LOGIN7 field by its offset's place.
func loginField(b []byte, at int) []byte {
	off, n := int(binary.LittleEndian.Uint16(b[at:])), int(binary.LittleEndian.Uint16(b[at+2:]))
	return append([]byte(nil), b[off:off+2*n]...)
}

func bvarchar(s string) []byte { return appendBVarchar(nil, s) }

func tDone(tok byte, status uint16, count uint64) []byte {
	b := []byte{tok}
	b = binary.LittleEndian.AppendUint16(b, status)
	b = binary.LittleEndian.AppendUint16(b, 0xc1)
	return binary.LittleEndian.AppendUint64(b, count)
}

func tColMeta(cols ...Column) []byte {
	b := binary.LittleEndian.AppendUint16([]byte{tokColMetadata}, uint16(len(cols)))
	for i := range cols {
		b = binary.LittleEndian.AppendUint32(b, cols[i].UserType)
		b = binary.LittleEndian.AppendUint16(b, cols[i].Flags)
		b = appendTypeInfo(b, &cols[i], true)
		b = appendBVarchar(b, cols[i].Name)
	}
	return b
}

func tRow(t *testing.T, cols []Column, vals ...any) []byte {
	t.Helper()
	b := []byte{tokRow}
	for i := range cols {
		var err error
		if b, err = appendValue(b, &cols[i], vals[i]); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func tNBCRow(t *testing.T, cols []Column, vals ...any) []byte {
	t.Helper()
	bitmap := make([]byte, (len(cols)+7)/8)
	b := []byte{tokNBCRow}
	var rest []byte
	for i := range cols {
		if vals[i] == nil {
			bitmap[i/8] |= 1 << (i % 8)
			continue
		}
		var err error
		if rest, err = appendValue(rest, &cols[i], vals[i]); err != nil {
			t.Fatal(err)
		}
	}
	return append(append(b, bitmap...), rest...)
}

func tMessage(tok byte, num int32, class byte, msg string) []byte {
	var m []byte
	m = binary.LittleEndian.AppendUint32(m, uint32(num))
	m = append(m, 1, class)
	m = appendUSVarchar(m, msg)
	m = appendBVarchar(m, "fake")
	m = appendBVarchar(m, "proc")
	m = binary.LittleEndian.AppendUint32(m, 7)
	b := binary.LittleEndian.AppendUint16([]byte{tok}, uint16(len(m)))
	return append(b, m...)
}

func tError(tok byte, num int32, class byte, msg string) []byte { return tMessage(tok, num, class, msg) }

func tInfo(num int32, msg string) []byte { return tMessage(tokInfo, num, 0, msg) }

// tEnv is an ENVCHANGE of typ to newValue (with its length prefix), the
// old value empty.
func tEnv(typ byte, newValue []byte) []byte {
	body := append(append([]byte{typ}, newValue...), 0)
	b := binary.LittleEndian.AppendUint16([]byte{tokEnvChange}, uint16(len(body)))
	return append(b, body...)
}

func tLoginAck() []byte {
	body := []byte{1}
	body = binary.BigEndian.AppendUint32(body, tdsVersion)
	body = appendBVarchar(body, "Microsoft SQL Server")
	body = append(body, 16, 0, 0x10, 0x2d)
	b := binary.LittleEndian.AppendUint16([]byte{tokLoginAck}, uint16(len(body)))
	return append(b, body...)
}

func tFeatureAck() []byte {
	b := []byte{tokFeatureExtAck, 0x0a}
	b = binary.LittleEndian.AppendUint32(b, 1)
	return append(b, 1, 0xff)
}

func tReturnStatus(v int32) []byte {
	return binary.LittleEndian.AppendUint32([]byte{tokReturnStatus}, uint32(v))
}

func tReturnValue(t *testing.T, name string, c Column, v any) []byte {
	t.Helper()
	b := binary.LittleEndian.AppendUint16([]byte{tokReturnValue}, 1)
	b = appendBVarchar(b, name)
	b = append(b, 1)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = appendTypeInfo(b, &c, false)
	b, err := appendValue(b, &c, v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// bytesReader reads a message held whole.
func bytesReader(b []byte) *msgReader { return &msgReader{buf: b, eom: true, started: true} }

// rpcParam is a parameter of an RPC request, as the fake reads it.
type rpcParam struct {
	name   string
	output bool
	col    Column
	value  any
}

// parseRPC reads an RPC request: its procedure (a name, or an id's text)
// and parameters; tx is its transaction descriptor.
func parseRPC(t *testing.T, body []byte) (proc string, tx [8]byte, params []rpcParam) {
	t.Helper()
	total := binary.LittleEndian.Uint32(body)
	copy(tx[:], body[10:18])
	r := bytesReader(body[total:])
	id, _ := r.u16()
	if id == 0xffff {
		n, _ := r.u16()
		proc = strconv.Itoa(int(n))
	} else {
		b, _ := r.next(int(id) * 2)
		proc = ucs2String(b)
	}
	r.u16()
	for len(r.buf) > 0 {
		var p rpcParam
		var err error
		if p.name, err = r.bVarchar(); err != nil {
			t.Fatal(err)
		}
		st, _ := r.u8()
		p.output = st&1 != 0
		if err := r.readTypeInfo(&p.col, false); err != nil {
			t.Fatal(err)
		}
		if p.value, err = r.readValue(&p.col, 1<<30); err != nil {
			t.Fatal(err)
		}
		params = append(params, p)
	}
	return
}

// batchSQL reads a SQL batch's text and transaction descriptor.
func batchSQL(body []byte) (string, [8]byte) {
	var tx [8]byte
	total := binary.LittleEndian.Uint32(body)
	copy(tx[:], body[10:18])
	return ucs2String(body[total:]), tx
}

// testCert makes a self-signed certificate for 127.0.0.1 and its pool.
func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}, IsCA: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, BasicConstraintsValid: true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}
