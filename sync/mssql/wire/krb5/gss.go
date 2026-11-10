package krb5

import (
	"context"
	encasn1 "encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/cryptobyte"
	"golang.org/x/crypto/cryptobyte/asn1"
)

// The mechanisms' object identifiers.
var (
	oidKRB5   = encasn1.ObjectIdentifier{1, 2, 840, 113554, 1, 2, 2}
	oidMSKRB5 = encasn1.ObjectIdentifier{1, 2, 840, 48018, 1, 2, 2}
	oidSPNEGO = encasn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 2}
)

// The GSS-API context flags the AP-REQ's checksum asks for: mutual
// authentication, integrity and confidentiality.
const gssFlags = 0x02 | 0x10 | 0x20

// apOptionMutual is the AP-REQ's mutual-required option.
const apOptionMutual = 0x20000000

// Initiator is the initiator of a GSS-API security context in SPNEGO with
// Kerberos 5, as Windows' Negotiate package takes it: Start's token is a
// NegTokenInit with the AP-REQ, mutual authentication required; Next
// checks the server's NegTokenResp, its AP-REP. Its methods are TDS's SSPI
// exchange (mssqlwire.SSPI).
type Initiator struct {
	Client *Client

	ticket *Ticket
	ctime  time.Time
	done   bool
}

// Start returns the NegTokenInit for the service spn.
func (in *Initiator) Start(ctx context.Context, spn string) ([]byte, error) {
	t, err := in.Client.ServiceTicket(ctx, spn)
	if err != nil {
		return nil, err
	}
	// RFC 4121's checksum: the length of the channel bindings (none),
	// their MD5 (zeros), the flags.
	ck := binary.LittleEndian.AppendUint32(nil, 16)
	ck = append(ck, make([]byte, 16)...)
	ck = binary.LittleEndian.AppendUint32(ck, gssFlags)
	now := in.Client.clock()
	ap, err := in.Client.apReq(t, usageAPReqAuth, apOptionMutual, 0x8003, ck, now)
	if err != nil {
		return nil, err
	}
	in.ticket, in.ctime, in.done = t, now, false
	mech := gssToken(oidKRB5, append([]byte{1, 0}, ap...))
	var b cryptobyte.Builder
	b.AddASN1(asn1.Tag(0).ContextSpecific().Constructed(), func(b *cryptobyte.Builder) {
		b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
			b.AddASN1(ctxTag(0), func(b *cryptobyte.Builder) {
				b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) { b.AddASN1ObjectIdentifier(oidKRB5) })
			})
			addOctets(b, 2, mech)
		})
	})
	return gssToken(oidSPNEGO, b.BytesOrPanic()), nil
}

// gssToken frames a token as RFC 2743's InitialContextToken: [APPLICATION
// 0] of the mechanism's OID and the token.
func gssToken(oid encasn1.ObjectIdentifier, inner []byte) []byte {
	var b cryptobyte.Builder
	b.AddASN1(asn1.Tag(0x60), func(b *cryptobyte.Builder) {
		b.AddASN1ObjectIdentifier(oid)
		b.AddBytes(inner)
	})
	return b.BytesOrPanic()
}

// unframe reads a GSS-API framed token's OID and the rest.
func unframe(b []byte) (encasn1.ObjectIdentifier, []byte, bool) {
	s := cryptobyte.String(b)
	var in cryptobyte.String
	var oid encasn1.ObjectIdentifier
	if !s.ReadASN1(&in, asn1.Tag(0x60)) || !in.ReadASN1ObjectIdentifier(&oid) {
		return nil, nil, false
	}
	return oid, []byte(in), true
}

// The SPNEGO negotiation states.
const (
	negAcceptCompleted  = 0
	negAcceptIncomplete = 1
	negReject           = 2
)

// Next checks the server's answer: a NegTokenResp holding the AP-REP (or a
// bare Kerberos token). It has nothing to send back.
func (in *Initiator) Next(ctx context.Context, tok []byte) ([]byte, error) {
	if in.ticket == nil {
		return nil, errors.New("krb5: an answer before the AP-REQ")
	}
	if in.done {
		return nil, nil
	}
	resp := tok
	if len(tok) > 0 && tok[0] == 0xa1 {
		s := cryptobyte.String(tok)
		var r cryptobyte.String
		if !s.ReadASN1(&r, asn1.Tag(1).ContextSpecific().Constructed()) {
			return nil, errMalformed
		}
		f, ok := fields(r)
		if !ok {
			return nil, errMalformed
		}
		if st, ok := f[0]; ok {
			var state int
			if !st.ReadASN1Enum(&state) {
				return nil, errMalformed
			}
			if state == negReject {
				return nil, errors.New("krb5: the server rejected the SPNEGO negotiation")
			}
		}
		if mech, ok := f[1]; ok {
			var oid encasn1.ObjectIdentifier
			if !mech.ReadASN1ObjectIdentifier(&oid) || (!oid.Equal(oidKRB5) && !oid.Equal(oidMSKRB5)) {
				return nil, fmt.Errorf("krb5: the server chose the mechanism %v, not Kerberos", oid)
			}
		}
		rt, ok := readOctets(f, 2)
		if !ok {
			// accept-completed without a token: nothing to check.
			in.done = true
			return nil, nil
		}
		resp = rt
	}
	if oid, inner, ok := unframe(resp); ok {
		if !oid.Equal(oidKRB5) && !oid.Equal(oidMSKRB5) {
			return nil, fmt.Errorf("krb5: a token of the mechanism %v", oid)
		}
		if len(inner) < 2 {
			return nil, errMalformed
		}
		switch {
		case inner[0] == 3 && inner[1] == 0:
			e, err := readError(inner[2:])
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("the server refused the ticket: %w", e)
		case inner[0] != 2 || inner[1] != 0:
			return nil, errMalformed
		}
		resp = inner[2:]
	}
	if err := in.checkAPRep(resp); err != nil {
		return nil, err
	}
	in.done = true
	return nil, nil
}

// checkAPRep checks the AP-REP: its part decrypted with the session key
// holds the authenticator's time.
func (in *Initiator) checkAPRep(b []byte) error {
	if tagOf(b) == appKRBError {
		e, err := readError(b)
		if err != nil {
			return err
		}
		return fmt.Errorf("the server refused the ticket: %w", e)
	}
	f, err := app(b, appAPRep)
	if err != nil {
		return err
	}
	enc, ok := readEncrypted(f, 2)
	if !ok {
		return errMalformed
	}
	plain, err := decrypt(in.ticket.Key, usageAPRepPart, enc.cipher)
	if err != nil {
		return err
	}
	pf, err := app(plain, appEncAPRepPart)
	if err != nil {
		return err
	}
	ctime, ok1 := readTime(pf, 0)
	cusec, ok2 := readInt(pf, 1)
	if !ok1 || !ok2 || !ctime.Equal(in.ctime.UTC().Truncate(time.Second)) || cusec != int64(in.ctime.Nanosecond()/1000) {
		return errors.New("krb5: the server's AP-REP does not answer the AP-REQ: mutual authentication failed")
	}
	return nil
}
