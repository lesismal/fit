package krb5

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/cryptobyte"
	"golang.org/x/crypto/cryptobyte/asn1"
)

// mutateTicket and mutateRep change the fake KDC's plaintexts before they
// are encrypted; the identity but in TestMutations.
var (
	mutateTicket = func(b []byte) []byte { return b }
	mutateRep    = func(b []byte) []byte { return b }
)

// mutations calls f with b changed at each byte to each of a few values.
func mutations(b []byte, f func([]byte)) {
	for i := range b {
		for _, v := range []byte{0x00, 0x01, 0x30, 0x7f, 0xff} {
			if b[i] == v {
				continue
			}
			m := append([]byte(nil), b...)
			m[i] = v
			f(m)
		}
	}
}

// at returns a mutate func changing byte i to v.
func at(i int, v byte) func([]byte) []byte {
	return func(b []byte) []byte {
		if i < len(b) {
			b = append([]byte(nil), b...)
			b[i] = v
		}
		return b
	}
}

// TestMutations changes every byte of the messages, outer and encrypted,
// and reads them: errors, never a panic, and never a ticket of a part
// that lost a field the client needs.
func TestMutations(t *testing.T) {
	defer func() {
		mutateTicket, mutateRep = func(b []byte) []byte { return b }, func(b []byte) []byte { return b }
	}()
	k := newKDC(t, ETypeAES256)
	c := k.client()
	cname := Principal{Type: 1, Parts: []string{"fituser"}}
	tgsName := Principal{Type: 2, Parts: []string{"krbtgt", fakeRealm}}
	svc := Principal{Type: 2, Parts: []string{"MSSQLSvc", "db.fit.test:1433"}}
	sk := k.sessionKey()
	end := time.Now().Add(time.Hour)
	tkt := ticketFor(k.tgtKey, tgsName, cname, sk, end)

	// The AS-REP, outside and in its encrypted part.
	good := rep(appASRep, appEncASRepPart, usageASRepPart, k.userKey, cname, tgsName, tkt, sk, 7, end, k.etypeInfo2())
	mutations(good, func(m []byte) { c.asRep(m, 7, nil) })
	plainLen := 200
	for i := 0; i < plainLen; i++ {
		for _, v := range []byte{0x00, 0x05, 0xff} {
			mutateRep = at(i, v)
			c.asRep(rep(appASRep, appEncASRepPart, usageASRepPart, k.userKey, cname, tgsName, tkt, sk, 7, end, nil), 7, nil)
		}
	}
	mutateRep = func(b []byte) []byte { return b }

	// The AP-REQ the acceptor reads, outside and in the ticket.
	in := &Initiator{Client: c}
	tok, err := in.Start(context.Background(), "MSSQLSvc/db.fit.test:1433")
	if err != nil {
		t.Fatal(err)
	}
	acc := &Acceptor{Keytab: k.svcKeytab()}
	mutations(tok, func(m []byte) { acc.Accept(m) })
	for i := 0; i < plainLen; i++ {
		for _, v := range []byte{0x00, 0x05, 0xff} {
			mutateTicket = at(i, v)
			st := ticketFor(k.svcKey, svc, cname, sk, end)
			mutateTicket = func(b []byte) []byte { return b }
			ap, err := c.apReq(&Ticket{raw: st, Key: sk}, usageAPReqAuth, apOptionMutual, 0x8003, make([]byte, 24), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			acc.Accept(gssToken(oidKRB5, append([]byte{1, 0}, ap...)))
		}
	}

	// The AP-REP, outside and in its encrypted part.
	resp, _, err := acc.Accept(tok)
	if err != nil {
		t.Fatal(err)
	}
	mutations(resp, func(m []byte) {
		in.done = false
		in.Next(context.Background(), m)
	})
	apRep := func(plain []byte) []byte {
		ct, _ := encrypt(in.ticket.Key, usageAPRepPart, plain)
		var r cryptobyte.Builder
		r.AddASN1(appTag(appAPRep), func(r *cryptobyte.Builder) {
			r.AddASN1(asn1.SEQUENCE, func(r *cryptobyte.Builder) {
				addInt(r, 0, 5)
				addInt(r, 1, appAPRep)
				addEncrypted(r, 2, encrypted{etype: in.ticket.Key.EType, cipher: ct})
			})
		})
		return r.BytesOrPanic()
	}
	var p cryptobyte.Builder
	p.AddASN1(appTag(appEncAPRepPart), func(p *cryptobyte.Builder) {
		p.AddASN1(asn1.SEQUENCE, func(p *cryptobyte.Builder) {
			addTime(p, 0, in.ctime)
			addInt(p, 1, int64(in.ctime.Nanosecond()/1000))
		})
	})
	part := p.BytesOrPanic()
	if err := in.checkAPRep(apRep(part)); err != nil {
		t.Fatal(err)
	}
	mutations(part, func(m []byte) { in.checkAPRep(apRep(m)) })

	// KRB-ERROR and ETYPE-INFO2.
	mutations(krbError(25, k.etypeInfo2()), func(m []byte) { readError(m) })
	pa, _ := readPAData(cryptobyte.String(k.etypeInfo2()))
	mutations(pa[0].value, func(m []byte) { readETypeInfo2(m) })
	mutations(k.etypeInfo2(), func(m []byte) { readPAData(cryptobyte.String(m)) })
}
