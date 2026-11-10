package mssqlwire

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// preAnswer is a pre-login answer of options, each a token and its data.
func preAnswer(opts ...[]byte) []byte {
	off := len(opts)*5 + 1
	var head, data []byte
	for _, o := range opts {
		head = append(head, o[0])
		head = binary.BigEndian.AppendUint16(head, uint16(off+len(data)))
		head = binary.BigEndian.AppendUint16(head, uint16(len(o)-1))
		data = append(data, o[1:]...)
	}
	return append(append(head, 0xff), data...)
}

// preOption finds a pre-login option's data.
func preOption(b []byte, token byte) ([]byte, bool) {
	for i := 0; i+5 <= len(b) && b[i] != 0xff; i += 5 {
		o, n := int(binary.BigEndian.Uint16(b[i+1:])), int(binary.BigEndian.Uint16(b[i+3:]))
		if b[i] == token {
			return b[o : o+n], true
		}
	}
	return nil, false
}

// loginAck answers a login.
func loginAck(s *session) {
	s.reply(tEnv(envDatabase, bvarchar("shop")), tLoginAck(), tDone(tokDone, 0, 0))
}

// TestFedAuth logs in with an access token: FEDAUTHREQUIRED in the
// pre-login, and LOGIN7 with no user or password, fExtension, and the
// FEDAUTH feature extension of the token, the echo of the server's
// FEDAUTHREQUIRED and its nonce.
func TestFedAuth(t *testing.T) {
	cert, pool := testCert(t)
	nonce := bytes.Repeat([]byte{7}, 32)
	for _, echo := range []bool{true, false} {
		f := newFake(t)
		f.cert, f.enc = cert, encryptOn
		req := byte(0)
		if echo {
			req = 1
		}
		f.pre = preAnswer([]byte{0, 16, 0, 0x10, 0x2d, 0, 0}, []byte{1, encryptOn}, []byte{6, req}, append([]byte{7}, nonce...))
		got := make(chan []byte, 1)
		f.login = func(s *session, body []byte) {
			got <- body
			loginAck(s)
		}
		f.start()
		cfg := f.config()
		cfg.Encrypt, cfg.TLS = EncryptOn, &tls.Config{RootCAs: pool}
		calls := 0
		cfg.AccessToken = func(context.Context) (string, error) { calls++; return "eyJ0b2tlbg", nil }
		c, err := Dial(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		if st, ok := c.TLS(); !ok || st.Version != tls.VersionTLS12 {
			t.Fatal("TLS", st.Version)
		}
		body := <-got
		if v, ok := preOption(f.preIn, 6); !ok || !bytes.Equal(v, []byte{1}) {
			t.Fatalf("FEDAUTHREQUIRED %x %v", v, ok)
		}
		if len(loginField(body, 40)) != 0 || len(loginField(body, 44)) != 0 || body[27]&0x10 == 0 || calls != 1 {
			t.Fatalf("user %q, password %q, flags %x, calls %d", loginField(body, 40), loginField(body, 44), body[27], calls)
		}
		ib, cb := binary.LittleEndian.Uint16(body[56:]), binary.LittleEndian.Uint16(body[58:])
		if cb != 4 {
			t.Fatal(cb)
		}
		ext := body[binary.LittleEndian.Uint32(body[ib:]):]
		tok := ucs2("eyJ0b2tlbg")
		want := []byte{0x02}
		want = binary.LittleEndian.AppendUint32(want, uint32(1+4+len(tok)+32))
		opt := byte(0x02)
		if echo {
			opt = 0x03
		}
		want = append(want, opt)
		want = binary.LittleEndian.AppendUint32(want, uint32(len(tok)))
		want = append(append(append(want, tok...), nonce...), 0xff)
		if !bytes.Equal(ext, want) {
			t.Fatalf("feature extension\n%x\nwant\n%x", ext, want)
		}
		if int(binary.LittleEndian.Uint32(body)) != len(body) {
			t.Fatal("LOGIN7's length")
		}
	}
}

// TestFedAuthRefusals: a token's error, an empty token, a token with
// encrypt off, a token and SSPI at once.
func TestFedAuthRefusals(t *testing.T) {
	f := newFake(t)
	f.start()
	cfg := f.config()
	cfg.AccessToken = func(context.Context) (string, error) { return "t", nil }
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "needs encrypt on or strict") {
		t.Fatal(err)
	}
	cfg.SSPI = sspiOf(&NTLM{})
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "SSPI at once") {
		t.Fatal(err)
	}
	cert, pool := testCert(t)
	f = newFake(t)
	f.cert, f.enc = cert, encryptOn
	f.start()
	cfg = f.config()
	cfg.Encrypt, cfg.TLS = EncryptOn, &tls.Config{RootCAs: pool}
	cfg.AccessToken = func(context.Context) (string, error) { return "", errors.New("no token today") }
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "getting an access token: no token today") {
		t.Fatal(err)
	}
	cfg.AccessToken = func(context.Context) (string, error) { return "", nil }
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "the access token is empty") {
		t.Fatal(err)
	}
}

// scriptedSSPI answers each server token with the next of out, and
// records what it was given.
type scriptedSSPI struct {
	first   []byte
	out     [][]byte
	in      [][]byte
	spn     string
	failAt  int
	started bool
}

func (s *scriptedSSPI) Start(_ context.Context, spn string) ([]byte, error) {
	s.spn, s.started = spn, true
	if s.failAt == 0 && s.first == nil {
		return nil, errors.New("no ticket")
	}
	return s.first, nil
}

func (s *scriptedSSPI) Next(_ context.Context, in []byte) ([]byte, error) {
	s.in = append(s.in, append([]byte(nil), in...))
	if len(s.in) == s.failAt {
		return nil, errors.New("a bad token")
	}
	if len(s.out) == 0 {
		return nil, nil
	}
	o := s.out[0]
	s.out = s.out[1:]
	return o, nil
}

func tSSPI(b []byte) []byte {
	return append(binary.LittleEndian.AppendUint16([]byte{tokSSPI}, uint16(len(b))), b...)
}

// TestSSPI logs in with integrated authentication: fIntSecurity, the first
// token in LOGIN7 (and cbSSPILong for a long one), the server's tokens in
// answers that end without a DONE, the client's in SSPI messages, until
// the LOGINACK; a last token beside the LOGINACK is checked too.
func TestSSPI(t *testing.T) {
	for _, long := range []bool{false, true} {
		f := newFake(t)
		first := []byte("negotiate")
		if long {
			first = bytes.Repeat([]byte{'n'}, 70000)
		}
		var spnego scriptedSSPI
		spnego.first, spnego.out, spnego.failAt = first, [][]byte{[]byte("authenticate")}, -1
		got := make(chan []byte, 2)
		f.login = func(s *session, body []byte) {
			if body[25]&0x80 == 0 || len(loginField(body, 40)) != 0 {
				t.Error("no fIntSecurity, or a user")
			}
			ib, cb := int(binary.LittleEndian.Uint16(body[78:])), int(binary.LittleEndian.Uint16(body[80:]))
			if cb == 0xffff {
				cb = int(binary.LittleEndian.Uint32(body[90:]))
			}
			got <- body[ib : ib+cb]
			s.reply(tSSPI([]byte("challenge")))
			typ, b, err := s.readMsg()
			if err != nil || typ != pktSSPI {
				t.Error(typ, err)
				return
			}
			got <- b
			s.reply(tSSPI([]byte("done")), tEnv(envDatabase, bvarchar("shop")), tLoginAck(), tDone(tokDone, 0, 0))
		}
		f.start()
		cfg := f.config()
		cfg.SSPI = sspiOf(&spnego)
		c, err := Dial(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		if !bytes.Equal(<-got, first) || string(<-got) != "authenticate" {
			t.Fatal("tokens")
		}
		host, port, _ := net.SplitHostPort(f.addr())
		if spnego.spn != "MSSQLSvc/"+host+":"+port || len(spnego.in) != 2 || string(spnego.in[0]) != "challenge" || string(spnego.in[1]) != "done" {
			t.Fatal(spnego.spn, spnego.in)
		}
	}
}

// TestSSPIFailures: a first token not made, a server token refused, a
// login refused after the exchange, an SSPI token to a login without
// SSPI, an answer ending without a DONE, the SSPI message's write failing.
func TestSSPIFailures(t *testing.T) {
	f := newFake(t)
	f.login = func(s *session, body []byte) {
		s.reply(tSSPI([]byte("challenge")))
		if _, _, err := s.readMsg(); err != nil {
			return
		}
		s.reply(tError(tokError, 18452, 14, "Login failed. The login is from an untrusted domain."), tDone(tokDone, doneError, 0))
	}
	f.start()
	cfg := f.config()
	cfg.SPN = "MSSQLSvc/db.example.com:1433"
	if _, err := Dial(context.Background(), cfg); err == nil {
		t.Fatal("logged in")
	}
	s := &scriptedSSPI{failAt: 0}
	cfg.SSPI = sspiOf(s)
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "SSPI: no ticket") {
		t.Fatal(err)
	}
	cfg.SSPI = sspiOf(&scriptedSSPI{first: []byte("x"), failAt: 1})
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "SSPI: a bad token") {
		t.Fatal(err)
	}
	s = &scriptedSSPI{first: []byte("x"), out: [][]byte{[]byte("y")}, failAt: -1}
	cfg.SSPI = sspiOf(s)
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "18452") {
		t.Fatal(err)
	}
	if s.spn != "MSSQLSvc/db.example.com:1433" {
		t.Fatal(s.spn)
	}

	// An SSPI token to SQL authentication; an answer ending without a
	// DONE and nothing to answer.
	f = newFake(t)
	f.login = func(s *session, body []byte) { s.reply(tSSPI([]byte("?"))) }
	f.start()
	cfg = f.config()
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "to a login without SSPI") {
		t.Fatal(err)
	}
	f.setLogin(func(s *session, body []byte) { s.reply(tLoginAck()) })
	if _, err := Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "ended without a DONE") {
		t.Fatal(err)
	}
	// An SSPI token after the login.
	f.setLogin(func(s *session, body []byte) { loginAck(s) })
	f.setHandle(func(s *session, typ byte, body []byte) { s.reply(tSSPI([]byte("late")), tDone(tokDone, 0, 0)) })
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(context.Background(), "SELECT 1"); err == nil || !strings.Contains(err.Error(), "outside the login") {
		t.Fatal(err)
	}
	// SSPI tokens cut short.
	for _, b := range [][]byte{{tokSSPI, 5}, {tokSSPI, 5, 0, 1}} {
		f.setLogin(func(s *session, body []byte) { s.reply(b) })
		cfg.SSPI = sspiOf(&scriptedSSPI{first: []byte("x"), failAt: -1})
		if _, err := Dial(context.Background(), cfg); err == nil {
			t.Fatal("logged in")
		}
	}
	// The SSPI message cannot be sent: the server is gone.
	f.setLogin(func(s *session, body []byte) {
		s.reply(tSSPI([]byte("challenge")))
		s.nc.Close()
	})
	cfg.SSPI = sspiOf(&scriptedSSPI{first: []byte("x"), out: [][]byte{bytes.Repeat([]byte{1}, 1<<16)}, failAt: -1})
	if _, err := Dial(context.Background(), cfg); err == nil {
		t.Fatal("logged in")
	}
}

// TestRouting follows Azure's routing ENVCHANGE once, to the server it
// names, with the SPN of the new host; a second routing is not followed.
func TestRouting(t *testing.T) {
	to := newFake(t)
	var spn string
	to.login = func(s *session, body []byte) {
		s.reply(tSSPI([]byte("ok")), tEnv(envDatabase, bvarchar("shop")), tLoginAck(), tDone(tokDone, 0, 0))
	}
	to.start()
	host, port, _ := net.SplitHostPort(to.addr())
	p, _ := strconv.Atoi(port)
	route := func(h string, p int) []byte {
		v := []byte{0}
		v = binary.LittleEndian.AppendUint16(v, uint16(p))
		v = appendUSVarchar(v, h)
		b := binary.LittleEndian.AppendUint16([]byte{envRouting}, uint16(len(v)))
		b = append(b, v...)
		b = append(b, 0, 0)
		return append(binary.LittleEndian.AppendUint16([]byte{tokEnvChange}, uint16(len(b))), b...)
	}
	from := newFake(t)
	from.login = func(s *session, body []byte) {
		s.reply(route(host, p), tLoginAck(), tDone(tokDone, 0, 0))
	}
	from.start()
	cfg := from.config()
	sspi := &recordSPN{spn: &spn}
	cfg.SSPI = sspiOf(sspi)
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.RemoteAddr() != to.addr() || spn != "MSSQLSvc/"+host+":"+port {
		t.Fatal(c.RemoteAddr(), spn)
	}
	// The routed server routes again: not followed.
	to.setLogin(func(s *session, body []byte) { s.reply(route("elsewhere", 1), tLoginAck(), tDone(tokDone, 0, 0)) })
	c2, err := Dial(context.Background(), from.config())
	if err != nil || c2.RemoteAddr() != to.addr() {
		t.Fatal(err)
	}
	c2.Close()
	// Malformed routings.
	for _, b := range [][]byte{
		{tokEnvChange, 3, 0, envRouting, 0, 0},
		{tokEnvChange, 9, 0, envRouting, 7, 0, 1, 0x99, 0x05, 0, 0, 0},
		{tokEnvChange, 9, 0, envRouting, 7, 0, 0, 0x99, 0x05, 5, 0, 0},
	} {
		from.setLogin(func(s *session, body []byte) { s.reply(b, tLoginAck(), tDone(tokDone, 0, 0)) })
		if _, err := Dial(context.Background(), from.config()); err == nil || !strings.Contains(err.Error(), "routing") {
			t.Fatal(err)
		}
	}
}

type recordSPN struct{ spn *string }

func (r *recordSPN) Start(_ context.Context, spn string) ([]byte, error) {
	*r.spn = spn
	return []byte("t"), nil
}
func (r *recordSPN) Next(context.Context, []byte) ([]byte, error) { return nil, nil }

// RemoteAddr is the server's address, for the routing test.
func (c *Conn) RemoteAddr() string { return c.raw.RemoteAddr().String() }

// TestNTLM checks NTLMv2 against MS-NLMP's vectors (4.2.4) and the
// messages' layout.
func TestNTLM(t *testing.T) {
	key := ntowfv2("User", "Password", "Domain")
	if hex.EncodeToString(key) != "0c868a403bfd7a93a3001ef22ef02e3f" {
		t.Fatalf("NTOWFv2 %x", key)
	}
	server, _ := hex.DecodeString("0123456789abcdef")
	client := bytes.Repeat([]byte{0xaa}, 8)
	info, _ := hex.DecodeString("02000c0044006f006d00610069006e0001000c0053006500720076006500720000000000")
	nt, lm := ntlmv2Response(key, server, client, 0, info)
	if hex.EncodeToString(nt[:16]) != "68cd0ab851e51c96aabc927bebef6a1c" {
		t.Fatalf("NTProofStr %x", nt[:16])
	}
	if hex.EncodeToString(lm) != "86c35097ac9cec102554764a57cccc19aaaaaaaaaaaaaaaa" {
		t.Fatalf("LMv2 %x", lm)
	}

	n := &NTLM{Domain: "Domain", User: "User", Password: "Password", Workstation: "COMPUTER",
		now: func() time.Time { return time.Unix(0, 0) }, random: func(b []byte) { copy(b, client) }}
	neg, _ := n.Start(context.Background(), "")
	if !bytes.Equal(neg[:8], ntlmSignature) || binary.LittleEndian.Uint32(neg[8:]) != 1 || len(neg) != 32 {
		t.Fatalf("%x", neg)
	}
	challenge := func(info []byte) []byte {
		b := append([]byte(nil), ntlmSignature...)
		b = binary.LittleEndian.AppendUint32(b, 2)
		b = append(b, 0, 0, 0, 0, 48, 0, 0, 0)
		b = binary.LittleEndian.AppendUint32(b, ntlmFlags)
		b = append(b, server...)
		b = append(b, make([]byte, 8)...)
		b = binary.LittleEndian.AppendUint16(b, uint16(len(info)))
		b = binary.LittleEndian.AppendUint16(b, uint16(len(info)))
		b = binary.LittleEndian.AppendUint32(b, 48)
		return append(b, info...)
	}
	auth, err := n.Next(context.Background(), challenge(info))
	if err != nil {
		t.Fatal(err)
	}
	field := func(at int) []byte {
		l, o := binary.LittleEndian.Uint16(auth[at:]), binary.LittleEndian.Uint32(auth[at+4:])
		return auth[o : o+uint32(l)]
	}
	if binary.LittleEndian.Uint32(auth[8:]) != 3 || !bytes.Equal(field(12), lm) || !bytes.Equal(field(36), ucs2("User")) ||
		!bytes.Equal(field(28), ucs2("Domain")) || !bytes.Equal(field(44), ucs2("COMPUTER")) {
		t.Fatalf("%x", auth)
	}
	stamp := filetime(time.Unix(0, 0))
	want, _ := ntlmv2Response(key, server, client, stamp, info)
	if !bytes.Equal(field(20), want) {
		t.Fatal("NTLMv2 response")
	}
	// The server's time in its information: used, and the LM response
	// zeros.
	withTime := append(binary.LittleEndian.AppendUint64([]byte{7, 0, 8, 0}, 42), info...)
	auth, _ = n.Next(context.Background(), challenge(withTime))
	if !bytes.Equal(field(12), make([]byte, 24)) || binary.LittleEndian.Uint64(field(20)[24:]) != 42 {
		t.Fatalf("%x", auth)
	}
	for _, bad := range [][]byte{nil, challenge(info)[:47], append([]byte("NTLMSSP\x00\x03\x00\x00\x00"), make([]byte, 40)...)} {
		if _, err := n.Next(context.Background(), bad); err == nil {
			t.Fatal("not a challenge")
		}
	}
	cut := challenge(info)
	binary.LittleEndian.PutUint32(cut[44:], 1000)
	if _, err := n.Next(context.Background(), cut); err == nil {
		t.Fatal("a target info past the end")
	}
	// Defaults: the host's name, a random challenge, the clock.
	d := &NTLM{User: "u", Password: "p"}
	if _, err := d.Next(context.Background(), challenge(info)); err != nil {
		t.Fatal(err)
	}
	if _, ok := avTimestamp([]byte{7, 0, 8}); ok {
		t.Fatal("a short pair")
	}
	for _, tc := range [][3]string{{`DOM\u`, "DOM", "u"}, {"u@dom.example", "dom.example", "u"}, {"u", "", "u"}} {
		if dm, u := ParseNTLMUser(tc[0]); dm != tc[1] || u != tc[2] {
			t.Fatal(tc, dm, u)
		}
	}
}

// TestNTLMLogin logs in with NTLM through the SSPI exchange.
func TestNTLMLogin(t *testing.T) {
	f := newFake(t)
	f.login = func(s *session, body []byte) {
		ib, cb := int(binary.LittleEndian.Uint16(body[78:])), int(binary.LittleEndian.Uint16(body[80:]))
		if !bytes.Equal(body[ib:ib+8], ntlmSignature) || cb != 32 {
			t.Error("no NEGOTIATE")
		}
		ch := append([]byte(nil), ntlmSignature...)
		ch = binary.LittleEndian.AppendUint32(ch, 2)
		ch = append(ch, make([]byte, 36)...)
		s.reply(tSSPI(ch))
		typ, b, err := s.readMsg()
		if err != nil || typ != pktSSPI || binary.LittleEndian.Uint32(b[8:]) != 3 {
			t.Error("no AUTHENTICATE", err)
			return
		}
		loginAck(s)
	}
	f.start()
	cfg := f.config()
	cfg.SSPI = sspiOf(&NTLM{Domain: "D", User: "u", Password: "p"})
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.TLS(); ok {
		t.Fatal("TLS without encryption")
	}
	c.Close()
}

var _ = time.Second

// sspiOf makes a's exchange every login's.
func sspiOf(a SSPI) func() SSPI { return func() SSPI { return a } }
