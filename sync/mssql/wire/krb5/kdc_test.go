package krb5

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/cryptobyte"
	"golang.org/x/crypto/cryptobyte/asn1"
)

// fakeKDC is a KDC of the realm FIT.TEST over TCP for the tests: the user
// fituser of a password, the service MSSQLSvc/db.fit.test:1433 of a key,
// and krbtgt's key.
type fakeKDC struct {
	t        *testing.T
	ln       net.Listener
	userKey  Key
	svcKey   Key
	tgtKey   Key
	etype    int32
	salt     string
	preauth  bool
	saltInAS bool
	// answer, when set, answers a request in place of the KDC, by its
	// application tag.
	answer func(tag int, req []byte) []byte
	mu     sync.Mutex
	reqs   []int
}

const fakeRealm = "FIT.TEST"

func newKDC(t *testing.T, etype int32) *fakeKDC {
	k := &fakeKDC{t: t, etype: etype, salt: "FIT.TESTfituser", preauth: true}
	k.userKey, _ = StringToKey(etype, "pw", k.salt, 0)
	k.svcKey, _ = StringToKey(etype, "svc", "x", 0)
	k.tgtKey, _ = StringToKey(etype, "tgt", "y", 0)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	k.ln = ln
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go k.serve(nc)
		}
	}()
	return k
}

func (k *fakeKDC) addr() string { return k.ln.Addr().String() }

func (k *fakeKDC) client() *Client {
	return &Client{Realm: fakeRealm, User: "fituser", Password: "pw", KDCs: []string{k.addr()}}
}

func (k *fakeKDC) serve(nc net.Conn) {
	defer nc.Close()
	var h [4]byte
	if _, err := io.ReadFull(nc, h[:]); err != nil {
		return
	}
	req := make([]byte, binary.BigEndian.Uint32(h[:]))
	if _, err := io.ReadFull(nc, req); err != nil {
		return
	}
	tag := tagOf(req)
	k.mu.Lock()
	k.reqs = append(k.reqs, tag)
	answer := k.answer
	k.mu.Unlock()
	var resp []byte
	if answer != nil {
		resp = answer(tag, req)
	}
	if resp == nil {
		resp = k.handle(tag, req)
	}
	nc.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(resp))), resp...))
}

func krbError(code int64, edata []byte) []byte {
	var b cryptobyte.Builder
	b.AddASN1(appTag(appKRBError), func(b *cryptobyte.Builder) {
		b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
			addInt(b, 0, 5)
			addInt(b, 1, appKRBError)
			addTime(b, 4, time.Now())
			addInt(b, 5, 0)
			addInt(b, 6, code)
			addString(b, 9, fakeRealm)
			addPrincipal(b, 10, Principal{Type: 2, Parts: []string{"krbtgt", fakeRealm}})
			addString(b, 11, "fake error")
			if edata != nil {
				addOctets(b, 12, edata)
			}
		})
	})
	return b.BytesOrPanic()
}

// etypeInfo2 is METHOD-DATA with an ETYPE-INFO2 of the KDC's type.
func (k *fakeKDC) etypeInfo2() []byte {
	var info cryptobyte.Builder
	info.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
		b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
			addInt(b, 0, int64(k.etype))
			addString(b, 1, k.salt)
			addOctets(b, 2, []byte{0, 0, 0x10, 0})
		})
	})
	return paSeq([]paData{{typ: paETypeInfo2, value: info.BytesOrPanic()}})
}

func paSeq(pa []paData) []byte {
	var b cryptobyte.Builder
	b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
		for _, p := range pa {
			b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
				addInt(b, 1, p.typ)
				addOctets(b, 2, p.value)
			})
		}
	})
	return b.BytesOrPanic()
}

// ticketFor is a ticket of sname with session key sk for cname,
// encrypted with key.
func ticketFor(key Key, sname, cname Principal, sk Key, end time.Time) []byte {
	var p cryptobyte.Builder
	p.AddASN1(appTag(appEncTicketPart), func(p *cryptobyte.Builder) {
		p.AddASN1(asn1.SEQUENCE, func(p *cryptobyte.Builder) {
			addBits(p, 0, 0)
			p.AddASN1(ctxTag(1), func(p *cryptobyte.Builder) {
				p.AddASN1(asn1.SEQUENCE, func(p *cryptobyte.Builder) {
					addInt(p, 0, int64(sk.EType))
					addOctets(p, 1, sk.Value)
				})
			})
			addString(p, 2, fakeRealm)
			addPrincipal(p, 3, cname)
			addTime(p, 5, time.Now())
			addTime(p, 7, end)
		})
	})
	ct, _ := encrypt(key, usageTicket, mutateTicket(p.BytesOrPanic()))
	var t cryptobyte.Builder
	t.AddASN1(appTag(appTicket), func(t *cryptobyte.Builder) {
		t.AddASN1(asn1.SEQUENCE, func(t *cryptobyte.Builder) {
			addInt(t, 0, 5)
			addString(t, 1, fakeRealm)
			addPrincipal(t, 2, sname)
			addEncrypted(t, 3, encrypted{etype: key.EType, cipher: ct})
		})
	})
	return t.BytesOrPanic()
}

// rep is an AS-REP or TGS-REP.
func rep(tag, partTag int, usage uint32, replyKey Key, cname, sname Principal, ticket []byte, sk Key, n int64, end time.Time, pa []byte) []byte {
	var p cryptobyte.Builder
	p.AddASN1(appTag(partTag), func(p *cryptobyte.Builder) {
		p.AddASN1(asn1.SEQUENCE, func(p *cryptobyte.Builder) {
			p.AddASN1(ctxTag(0), func(p *cryptobyte.Builder) {
				p.AddASN1(asn1.SEQUENCE, func(p *cryptobyte.Builder) {
					addInt(p, 0, int64(sk.EType))
					addOctets(p, 1, sk.Value)
				})
			})
			p.AddASN1(ctxTag(1), func(p *cryptobyte.Builder) { p.AddASN1(asn1.SEQUENCE, func(*cryptobyte.Builder) {}) })
			addInt(p, 2, n)
			addBits(p, 4, 0)
			addTime(p, 5, time.Now())
			addTime(p, 7, end)
			addString(p, 9, fakeRealm)
			addPrincipal(p, 10, sname)
		})
	})
	ct, _ := encrypt(replyKey, usage, mutateRep(p.BytesOrPanic()))
	var b cryptobyte.Builder
	b.AddASN1(appTag(tag), func(b *cryptobyte.Builder) {
		b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
			addInt(b, 0, 5)
			addInt(b, 1, int64(tag))
			if pa != nil {
				b.AddASN1(ctxTag(2), func(b *cryptobyte.Builder) { b.AddBytes(pa) })
			}
			addString(b, 3, fakeRealm)
			addPrincipal(b, 4, cname)
			b.AddASN1(ctxTag(5), func(b *cryptobyte.Builder) { b.AddBytes(ticket) })
			addEncrypted(b, 6, encrypted{etype: replyKey.EType, cipher: ct})
		})
	})
	return b.BytesOrPanic()
}

// sessionKey is a fresh session key of the KDC's type.
func (k *fakeKDC) sessionKey() Key {
	sk, _ := StringToKey(k.etype, time.Now().String(), "s", 1)
	return sk
}

func (k *fakeKDC) handle(tag int, req []byte) []byte {
	f, err := app(req, tag)
	if err != nil {
		return krbError(40, nil)
	}
	bf, ok := fields(f[4])
	if !ok {
		return krbError(40, nil)
	}
	n, _ := readInt(bf, 7)
	sname, _ := readPrincipal(bf, 3)
	var pa []paData
	if s, ok := f[3]; ok {
		pa, _ = readPAData(s)
	}
	end := time.Now().Add(time.Hour)
	switch tag {
	case appASReq:
		cname, _ := readPrincipal(bf, 1)
		if cname.String() != "fituser" {
			return krbError(6, nil)
		}
		var ts []byte
		for _, p := range pa {
			if p.typ == paEncTS {
				ts = p.value
			}
		}
		if k.preauth && ts == nil {
			return krbError(errPreauthRequired, k.etypeInfo2())
		}
		if ts != nil {
			ef, _ := fields(cryptobyte.String(ts))
			e, _ := readEncrypted(map[int]cryptobyte.String{0: wrapSeq(ef)}, 0)
			_ = e
			c, _ := readOctets(ef, 2)
			if _, err := decrypt(k.userKey, usagePAEncTS, c); err != nil {
				return krbError(24, nil)
			}
		}
		sk := k.sessionKey()
		tgt := ticketFor(k.tgtKey, sname, cname, sk, end)
		var padata []byte
		if k.saltInAS {
			padata = k.etypeInfo2()
		}
		return rep(appASRep, appEncASRepPart, usageASRepPart, k.userKey, cname, sname, tgt, sk, n, end, padata)
	case appTGSReq:
		if len(pa) != 1 || pa[0].typ != paTGSReq {
			return krbError(40, nil)
		}
		af, err := app(pa[0].value, appAPReq)
		if err != nil {
			return krbError(40, nil)
		}
		var tin cryptobyte.String
		ts := af[3]
		ts.ReadASN1(&tin, appTag(appTicket))
		tf, _ := fields(tin)
		tenc, _ := readEncrypted(tf, 3)
		plain, err := decrypt(k.tgtKey, usageTicket, tenc.cipher)
		if err != nil {
			return krbError(31, nil)
		}
		pf, _ := app(plain, appEncTicketPart)
		tgtSK, _ := readKey(pf, 1)
		cname, _ := readPrincipal(pf, 3)
		aenc, _ := readEncrypted(af, 4)
		if _, err := decrypt(tgtSK, usageTGSReqAuth, aenc.cipher); err != nil {
			return krbError(31, nil)
		}
		if sname.String() != "MSSQLSvc/db.fit.test:1433" {
			return krbError(7, nil)
		}
		sk := k.sessionKey()
		st := ticketFor(k.svcKey, sname, cname, sk, end)
		return rep(appTGSRep, appEncTGSRepPart, usageTGSRepPart, tgtSK, cname, sname, st, sk, n, end, nil)
	}
	return krbError(40, nil)
}

func wrapSeq(map[int]cryptobyte.String) cryptobyte.String { return nil }

func (k *fakeKDC) svcKeytab() *Keytab {
	return &Keytab{Entries: []KeytabEntry{{Realm: fakeRealm, Parts: []string{"MSSQLSvc", "db.fit.test:1433"}, KVNO: 2, Key: k.svcKey}}}
}

// TestKerberos: a password (pre-authentication asked for, the salt from
// ETYPE-INFO2) and a keytab (no pre-authentication) each get a service
// ticket, which the acceptor takes, and whose AP-REP the initiator
// checks; tickets are kept.
func TestKerberos(t *testing.T) {
	for _, et := range []int32{ETypeAES256, ETypeAES128} {
		k := newKDC(t, et)
		c := k.client()
		in := &Initiator{Client: c}
		tok, err := in.Start(context.Background(), "MSSQLSvc/db.fit.test:1433")
		if err != nil {
			t.Fatal(err)
		}
		resp, who, err := (&Acceptor{Keytab: k.svcKeytab()}).Accept(tok)
		if err != nil || who != "fituser@FIT.TEST" {
			t.Fatal(who, err)
		}
		if out, err := in.Next(context.Background(), resp); err != nil || out != nil {
			t.Fatal(out, err)
		}
		// Done: a second answer is not looked at.
		if _, err := in.Next(context.Background(), []byte("x")); err != nil {
			t.Fatal(err)
		}
		if strings.Join(intsOf(k.reqs), ",") != "10,10,12" {
			t.Fatal(k.reqs)
		}
		// The ticket is kept.
		if _, err := in.Start(context.Background(), "MSSQLSvc/db.fit.test:1433"); err != nil || len(k.reqs) != 3 {
			t.Fatal(err, k.reqs)
		}

		// A keytab, without pre-authentication, the salt in the AS-REP.
		k2 := newKDC(t, et)
		k2.preauth, k2.saltInAS = false, true
		c2 := k2.client()
		c2.Password, c2.Keytab = "", &Keytab{Entries: []KeytabEntry{{Realm: fakeRealm, Parts: []string{"fituser"}, KVNO: 1, Key: k2.userKey}}}
		if _, err := c2.ServiceTicket(context.Background(), "MSSQLSvc/db.fit.test:1433@FIT.TEST"); err != nil {
			t.Fatal(err)
		}
		// A password and no pre-authentication: the default salt.
		k3 := newKDC(t, et)
		k3.preauth = false
		if _, err := k3.client().ServiceTicket(context.Background(), "MSSQLSvc/db.fit.test:1433"); err != nil {
			t.Fatal(err)
		}
	}
}

func intsOf(v []int) []string {
	var s []string
	for _, x := range v {
		s = append(s, string(rune('0'+x/10))+string(rune('0'+x%10)))
	}
	return s
}

// TestKerberosFailures: the KDC's errors, a wrong password, an unknown
// service, a nonce of another request, no KDC, settings missing.
func TestKerberosFailures(t *testing.T) {
	k := newKDC(t, ETypeAES256)
	c := k.client()
	c.Password = "wrong"
	if _, err := c.ServiceTicket(context.Background(), "MSSQLSvc/db.fit.test:1433"); err == nil || !strings.Contains(err.Error(), "PREAUTH_FAILED") {
		t.Fatal(err)
	}
	c = k.client()
	if _, err := c.ServiceTicket(context.Background(), "MSSQLSvc/other.fit.test:1433"); err == nil || !strings.Contains(err.Error(), "S_PRINCIPAL_UNKNOWN") {
		t.Fatal(err)
	}
	c = k.client()
	c.User = "nobody"
	var ke *KDCError
	if _, err := c.ServiceTicket(context.Background(), "MSSQLSvc/db.fit.test:1433"); !errors.As(err, &ke) || ke.Code != 6 || ke.Text != "fake error" {
		t.Fatal(err)
	}
	for _, spn := range []string{"nohost", "MSSQLSvc/", "MSSQLSvc/h@OTHER.REALM"} {
		if _, err := k.client().ServiceTicket(context.Background(), spn); err == nil {
			t.Fatal(spn)
		}
	}
	for _, c := range []*Client{{User: "u", Password: "p", KDCs: []string{k.addr()}}, {Realm: fakeRealm, User: "u", KDCs: []string{k.addr()}}, {Realm: fakeRealm, User: "u", Password: "p"}} {
		if _, err := c.ServiceTicket(context.Background(), "MSSQLSvc/h:1"); err == nil {
			t.Fatal(c)
		}
	}
	// A KDC that is not there, then one that is: the second answers.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	c = k.client()
	c.KDCs = []string{dead, k.addr()}
	if _, err := c.ServiceTicket(context.Background(), "MSSQLSvc/db.fit.test:1433"); err != nil {
		t.Fatal(err)
	}
	c = k.client()
	c.KDCs = []string{"127.0.0.1"}
	c.Dial = func(ctx context.Context, addr string) (net.Conn, error) {
		if addr != "127.0.0.1:88" {
			t.Error(addr)
		}
		return nil, errors.New("no route")
	}
	if _, err := c.ServiceTicket(context.Background(), "MSSQLSvc/db.fit.test:1433"); err == nil || !strings.Contains(err.Error(), "no route") {
		t.Fatal(err)
	}
	// Answers the client cannot take.
	for _, tc := range []struct {
		name   string
		answer func(tag int, req []byte) []byte
		want   string
	}{
		{"garbage", func(int, []byte) []byte { return []byte{0x30, 0} }, "malformed"},
		{"a bad error", func(int, []byte) []byte { return []byte{0x7e, 0} }, "malformed"},
		{"preauth twice", func(int, []byte) []byte { return krbError(errPreauthRequired, k.etypeInfo2()) }, "PREAUTH_REQUIRED"},
		{"no AES", func(int, []byte) []byte {
			var info cryptobyte.Builder
			info.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
				b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) { addInt(b, 0, 23) })
			})
			return krbError(errPreauthRequired, paSeq([]paData{{typ: paETypeInfo2, value: info.BytesOrPanic()}}))
		}, "no AES"},
		{"a TGS error", func(tag int, req []byte) []byte {
			if tag == appTGSReq {
				return krbError(37, nil)
			}
			return nil
		}, "SKEW"},
		{"a TGS-REP of garbage", func(tag int, req []byte) []byte {
			if tag == appTGSReq {
				return []byte{0x6d, 0}
			}
			return nil
		}, "malformed"},
		{"another nonce", func(tag int, req []byte) []byte {
			if tag != appASReq || !strings.Contains(string(req), "\x02\x01\x02") {
				return nil
			}
			cname := Principal{Type: 1, Parts: []string{"fituser"}}
			sname := Principal{Type: 2, Parts: []string{"krbtgt", fakeRealm}}
			sk := k.sessionKey()
			return rep(appASRep, appEncASRepPart, usageASRepPart, k.userKey, cname, sname, ticketFor(k.tgtKey, sname, cname, sk, time.Now().Add(time.Hour)), sk, -1, time.Now().Add(time.Hour), nil)
		}, "another nonce"},
		{"an answer too long", func(int, []byte) []byte { return make([]byte, 2<<20) }, "bytes"},
	} {
		k.answer = tc.answer
		if _, err := k.client().ServiceTicket(context.Background(), "MSSQLSvc/db.fit.test:1433"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	k.answer = nil
	// A keytab without the key.
	c = k.client()
	c.Password, c.Keytab = "", &Keytab{}
	if _, err := c.ServiceTicket(context.Background(), "MSSQLSvc/db.fit.test:1433"); err == nil || !strings.Contains(err.Error(), "keytab has no AES key") {
		t.Fatal(err)
	}
	// A context ended.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c = k.client()
	c.KDCs = []string{dead, k.addr()}
	if _, err := c.ServiceTicket(ctx, "MSSQLSvc/db.fit.test:1433"); err == nil {
		t.Fatal("a ticket")
	}
}

// TestAcceptorAndInitiatorFailures: tokens that are not AP-REQs, tickets
// of keys not in the keytab, expired, a server's rejection, error, or
// AP-REP of another time.
func TestAcceptorAndInitiatorFailures(t *testing.T) {
	k := newKDC(t, ETypeAES256)
	in := &Initiator{Client: k.client()}
	if _, err := in.Next(context.Background(), nil); err == nil {
		t.Fatal("an answer before the AP-REQ")
	}
	if _, err := in.Start(context.Background(), "bad"); err == nil {
		t.Fatal("a bad SPN")
	}
	tok, err := in.Start(context.Background(), "MSSQLSvc/db.fit.test:1433")
	if err != nil {
		t.Fatal(err)
	}
	acc := &Acceptor{Keytab: k.svcKeytab()}
	for _, bad := range [][]byte{nil, gssToken(oidSPNEGO, []byte{0xa0, 0}), gssToken(oidSPNEGO, []byte{0xa0, 2, 0x30, 0}), gssToken(oidKRB5, []byte{1, 0, 0x30, 0}), gssToken(oidKRB5, []byte{2, 0})} {
		if _, _, err := acc.Accept(bad); err == nil {
			t.Fatalf("%x accepted", bad)
		}
	}
	if _, _, err := (&Acceptor{Keytab: &Keytab{}}).Accept(tok); err == nil || !strings.Contains(err.Error(), "keytab has no key") {
		t.Fatal(err)
	}
	if _, _, err := (&Acceptor{Keytab: k.svcKeytab(), now: func() time.Time { return time.Now().Add(3 * time.Hour) }}).Accept(tok); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatal(err)
	}
	if _, _, err := (&Acceptor{Keytab: k.svcKeytab(), now: func() time.Time { return time.Now().Add(30 * time.Minute) }}).Accept(tok); err == nil || !strings.Contains(err.Error(), "skew") {
		t.Fatal(err)
	}
	wrong := k.svcKeytab()
	wrong.Entries[0].Key = k.tgtKey
	if _, _, err := (&Acceptor{Keytab: wrong}).Accept(tok); err == nil {
		t.Fatal("a wrong key")
	}
	resp, _, err := acc.Accept(tok)
	if err != nil {
		t.Fatal(err)
	}
	// A bare Kerberos token, unwrapped from SPNEGO, is taken too.
	_, inner, _ := unframe(tok)
	s := cryptobyte.String(inner)
	var init cryptobyte.String
	s.ReadASN1(&init, asn1.Tag(0).ContextSpecific().Constructed())
	f, _ := fields(init)
	mech, _ := readOctets(f, 2)
	if _, _, err := acc.Accept(mech); err != nil {
		t.Fatal(err)
	}
	// The AP-REP bare, framed, and within SPNEGO.
	_, rf := unwrapResp(t, resp)
	for _, ok := range [][]byte{resp, rf} {
		in.done = false
		if _, err := in.Next(context.Background(), ok); err != nil {
			t.Fatal(err)
		}
	}
	_, apRep, _ := unframe(rf)
	in.done = false
	if _, err := in.Next(context.Background(), apRep[2:]); err != nil {
		t.Fatal(err)
	}
	negResp := func(state int, oid []int, token []byte) []byte {
		var b cryptobyte.Builder
		b.AddASN1(asn1.Tag(1).ContextSpecific().Constructed(), func(b *cryptobyte.Builder) {
			b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
				b.AddASN1(ctxTag(0), func(b *cryptobyte.Builder) { b.AddASN1Enum(int64(state)) })
				if oid != nil {
					b.AddASN1(ctxTag(1), func(b *cryptobyte.Builder) { b.AddASN1ObjectIdentifier(oid) })
				}
				if token != nil {
					addOctets(b, 2, token)
				}
			})
		})
		return b.BytesOrPanic()
	}
	in.done = false
	if _, err := in.Next(context.Background(), negResp(negAcceptCompleted, nil, nil)); err != nil {
		t.Fatal(err)
	}
	// Another AP-REQ's time.
	in2 := &Initiator{Client: in.Client}
	in2.Start(context.Background(), "MSSQLSvc/db.fit.test:1433")
	in2.ctime = in2.ctime.Add(time.Second)
	if _, err := in2.Next(context.Background(), resp); err == nil || !strings.Contains(err.Error(), "mutual authentication failed") {
		t.Fatal(err)
	}
	errTok := gssToken(oidKRB5, append([]byte{3, 0}, krbError(41, nil)...))
	for _, tc := range []struct {
		tok  []byte
		want string
	}{
		{negResp(negReject, nil, nil), "rejected"},
		{negResp(negAcceptCompleted, []int{1, 2, 3}, nil), "chose the mechanism"},
		{[]byte{0xa1, 0}, "malformed"},
		{[]byte{0xa1, 5, 0x30, 3, 0xa0, 1, 0}, "malformed"},
		{errTok, "MODIFIED"},
		{krbError(41, nil), "MODIFIED"},
		{gssToken(oidSPNEGO, []byte{2, 0}), "mechanism"},
		{gssToken(oidKRB5, []byte{2}), "malformed"},
		{gssToken(oidKRB5, []byte{4, 0}), "malformed"},
		{gssToken(oidKRB5, []byte{3, 0, 0x30}), "malformed"},
		{[]byte{0x6f, 0}, "malformed"},
		{[]byte{0x30, 0}, "malformed"},
		{[]byte{0x7e, 0}, "malformed"},
	} {
		in.done = false
		if _, err := in.Next(context.Background(), tc.tok); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%x: %v", tc.tok, err)
		}
	}
	if (&KDCError{Code: 999}).Error() != "krb5: the KDC answered error 999" {
		t.Fatal((&KDCError{Code: 999}).Error())
	}
	if (Principal{Parts: []string{"a", "b"}}).String() != "a/b" {
		t.Fatal("principal")
	}
}

// unwrapResp returns a NegTokenResp's response token.
func unwrapResp(t *testing.T, resp []byte) (int, []byte) {
	t.Helper()
	s := cryptobyte.String(resp)
	var r cryptobyte.String
	if !s.ReadASN1(&r, asn1.Tag(1).ContextSpecific().Constructed()) {
		t.Fatal("not a NegTokenResp")
	}
	f, _ := fields(r)
	tok, _ := readOctets(f, 2)
	return 0, tok
}
