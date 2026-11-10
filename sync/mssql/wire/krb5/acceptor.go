package krb5

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/cryptobyte"
	"golang.org/x/crypto/cryptobyte/asn1"
)

// appEncTicketPart is the EncTicketPart's application tag.
const appEncTicketPart = 3

// usageTicket is the key usage of a ticket's encrypted part.
const usageTicket = 2

// Acceptor is the accepting side of Initiator's exchange, a service
// checking a client's AP-REQ with the keys of its keytab and answering
// the AP-REP: what a server does, here for tests of clients (a fake TDS
// server's SSPI against a real KDC's tickets).
type Acceptor struct {
	Keytab *Keytab
	// Skew is the clock difference allowed; 5 minutes when 0.
	Skew time.Duration
	// now is the clock; the tests set it.
	now func() time.Time
}

// Accept checks a NegTokenInit (or a bare Kerberos GSS-API token) and
// returns the NegTokenResp to send back and the client's principal,
// name@REALM.
func (a *Acceptor) Accept(tok []byte) ([]byte, string, error) {
	oid, inner, ok := unframe(tok)
	if !ok {
		return nil, "", errMalformed
	}
	if oid.Equal(oidSPNEGO) {
		s := cryptobyte.String(inner)
		var init cryptobyte.String
		if !s.ReadASN1(&init, asn1.Tag(0).ContextSpecific().Constructed()) {
			return nil, "", errMalformed
		}
		f, ok := fields(init)
		if !ok {
			return nil, "", errMalformed
		}
		mech, ok := readOctets(f, 2)
		if !ok {
			return nil, "", errors.New("krb5: a NegTokenInit without a mechanism token")
		}
		if oid, inner, ok = unframe(mech); !ok {
			return nil, "", errMalformed
		}
	}
	if !oid.Equal(oidKRB5) && !oid.Equal(oidMSKRB5) || len(inner) < 2 || inner[0] != 1 || inner[1] != 0 {
		return nil, "", errors.New("krb5: not a Kerberos AP-REQ")
	}
	rep, client, err := a.apReq(inner[2:])
	if err != nil {
		return nil, "", err
	}
	mech := gssToken(oidKRB5, append([]byte{2, 0}, rep...))
	var b cryptobyte.Builder
	b.AddASN1(asn1.Tag(1).ContextSpecific().Constructed(), func(b *cryptobyte.Builder) {
		b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
			b.AddASN1(ctxTag(0), func(b *cryptobyte.Builder) { b.AddASN1Enum(negAcceptCompleted) })
			b.AddASN1(ctxTag(1), func(b *cryptobyte.Builder) { b.AddASN1ObjectIdentifier(oidKRB5) })
			addOctets(b, 2, mech)
		})
	})
	return b.BytesOrPanic(), client, nil
}

// apReq checks an AP-REQ and returns the AP-REP and the client.
func (a *Acceptor) apReq(b []byte) ([]byte, string, error) {
	f, err := app(b, appAPReq)
	if err != nil {
		return nil, "", err
	}
	ts, ok := f[3]
	var tf map[int]cryptobyte.String
	var tin cryptobyte.String
	if ok {
		if ok = ts.ReadASN1(&tin, appTag(appTicket)); ok {
			tf, ok = fields(tin)
		}
	}
	if !ok {
		return nil, "", errMalformed
	}
	realm, _ := readString(tf, 1)
	sname, ok1 := readPrincipal(tf, 2)
	tenc, ok2 := readEncrypted(tf, 3)
	if !ok1 || !ok2 {
		return nil, "", errMalformed
	}
	key, ok := a.Keytab.Key(realm, sname.Parts, tenc.etype)
	if !ok {
		return nil, "", fmt.Errorf("krb5: the keytab has no key of %s@%s of encryption type %d", sname, realm, tenc.etype)
	}
	plain, err := decrypt(key, usageTicket, tenc.cipher)
	if err != nil {
		return nil, "", err
	}
	pf, err := app(plain, appEncTicketPart)
	if err != nil {
		return nil, "", err
	}
	sk, ok1 := readKey(pf, 1)
	crealm, ok2 := readString(pf, 2)
	cname, ok3 := readPrincipal(pf, 3)
	end, ok4 := readTime(pf, 7)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, "", errMalformed
	}
	now := time.Now()
	if a.now != nil {
		now = a.now()
	}
	skew := a.Skew
	if skew <= 0 {
		skew = 5 * time.Minute
	}
	if now.After(end.Add(skew)) {
		return nil, "", errors.New("krb5: the ticket has expired")
	}
	aenc, ok := readEncrypted(f, 4)
	if !ok {
		return nil, "", errMalformed
	}
	auth, err := decrypt(sk, usageAPReqAuth, aenc.cipher)
	if err != nil {
		return nil, "", err
	}
	af, err := app(auth, appAuthenticator)
	if err != nil {
		return nil, "", err
	}
	arealm, _ := readString(af, 1)
	aname, _ := readPrincipal(af, 2)
	cusec, ok1 := readInt(af, 4)
	ctime, ok2 := readTime(af, 5)
	if !ok1 || !ok2 {
		return nil, "", errMalformed
	}
	if arealm != crealm || aname.String() != cname.String() {
		return nil, "", errors.New("krb5: the authenticator's client is not the ticket's")
	}
	if d := now.Sub(ctime); d > skew || d < -skew {
		return nil, "", errors.New("krb5: the authenticator's time is outside the allowed skew")
	}
	var p cryptobyte.Builder
	p.AddASN1(appTag(appEncAPRepPart), func(p *cryptobyte.Builder) {
		p.AddASN1(asn1.SEQUENCE, func(p *cryptobyte.Builder) {
			addTime(p, 0, ctime)
			addInt(p, 1, cusec)
		})
	})
	ct, err := encrypt(sk, usageAPRepPart, p.BytesOrPanic())
	if err != nil {
		return nil, "", err
	}
	var r cryptobyte.Builder
	r.AddASN1(appTag(appAPRep), func(r *cryptobyte.Builder) {
		r.AddASN1(asn1.SEQUENCE, func(r *cryptobyte.Builder) {
			addInt(r, 0, 5)
			addInt(r, 1, appAPRep)
			addEncrypted(r, 2, encrypted{etype: sk.EType, cipher: ct})
		})
	})
	return r.BytesOrPanic(), cname.String() + "@" + crealm, nil
}
