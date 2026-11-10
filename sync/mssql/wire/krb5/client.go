// Package krb5 is a Kerberos 5 client written in fit (RFC 4120), enough
// for a SQL Server login: a ticket-granting ticket from the KDC (AS-REQ,
// with a password or a keytab, encrypted-timestamp pre-authentication), a
// service ticket (TGS-REQ), and the AP-REQ a service takes, as a GSS-API
// token in SPNEGO (RFC 4121, RFC 4178), the reply's AP-REP checked. Its
// encryption types are AES's, aes256- and aes128-cts-hmac-sha1-96 (RFC
// 3962). It talks to the KDC over TCP. Left out: RC4 and DES, cross-realm
// referrals, FAST, PKINIT, UDP, DNS lookups of KDCs, renewals (a ticket
// that expires is got again).
package krb5

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/cryptobyte"
	"golang.org/x/crypto/cryptobyte/asn1"
)

// Client gets tickets for one principal.
type Client struct {
	// Realm is the principal's realm.
	Realm string
	// User is the principal's name without its realm: user, or
	// service/host.
	User string
	// Password is the principal's password; its key is made with the
	// salt and iterations the KDC gives.
	Password string
	// Keytab holds the principal's keys, in place of a password.
	Keytab *Keytab
	// KDCs are the KDCs' host:port (port 88 when left out), tried in turn.
	KDCs []string
	// Timeout bounds each exchange with a KDC; 10s when 0.
	Timeout time.Duration
	// Dial opens the TCP connection to a KDC in place of net.Dialer.
	Dial func(ctx context.Context, addr string) (net.Conn, error)

	mu      sync.Mutex
	tgt     *Ticket
	tickets map[string]*Ticket
	// now is the clock; the tests set it.
	now func() time.Time
}

// Ticket is a ticket and its session key.
type Ticket struct {
	// raw is the Ticket, DER with its APPLICATION 1 tag.
	raw   []byte
	Key   Key
	End   time.Time
	Realm string
	Name  Principal
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Client) client() Principal {
	return Principal{Type: nameTypePrincipal, Parts: strings.Split(c.User, "/")}
}

// ParseSPN reads a service principal name, service/host[:port][@REALM],
// into its principal (of the type NT-SRV-INST, as Active Directory and
// SQL Server write it: the host and port one part) and its realm.
func ParseSPN(spn string) (Principal, string, error) {
	name, realm, _ := strings.Cut(spn, "@")
	svc, host, ok := strings.Cut(name, "/")
	if !ok || svc == "" || host == "" {
		return Principal{}, "", fmt.Errorf("krb5: the SPN %q is not service/host", spn)
	}
	return Principal{Type: nameTypeSrvInst, Parts: []string{svc, host}}, realm, nil
}

// ServiceTicket returns a ticket for the service spn, getting a
// ticket-granting ticket first when there is none good.
func (c *Client) ServiceTicket(ctx context.Context, spn string) (*Ticket, error) {
	sname, realm, err := ParseSPN(spn)
	if err != nil {
		return nil, err
	}
	if realm == "" {
		realm = c.Realm
	}
	if realm != c.Realm {
		return nil, fmt.Errorf("krb5: the service's realm %s is not the client's %s: cross-realm tickets are not supported", realm, c.Realm)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	if t := c.tickets[spn]; t != nil && now.Add(time.Minute).Before(t.End) {
		return t, nil
	}
	if c.tgt == nil || !now.Add(time.Minute).Before(c.tgt.End) {
		if c.tgt, err = c.login(ctx); err != nil {
			return nil, err
		}
	}
	t, err := c.tgs(ctx, sname)
	if err != nil {
		return nil, err
	}
	if c.tickets == nil {
		c.tickets = map[string]*Ticket{}
	}
	c.tickets[spn] = t
	return t, nil
}

// nonce is a random 31-bit number.
func nonce() int64 {
	n, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
	return n.Int64()
}

// etypes are the encryption types asked for, the strongest first.
var etypes = []int32{ETypeAES256, ETypeAES128}

// reqBody is a KDC-REQ-BODY.
func (c *Client) reqBody(cname *Principal, sname Principal, n int64) []byte {
	var b cryptobyte.Builder
	b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
		addBits(b, 0, 0)
		if cname != nil {
			addPrincipal(b, 1, *cname)
		}
		addString(b, 2, c.Realm)
		addPrincipal(b, 3, sname)
		addTime(b, 5, c.clock().Add(24*time.Hour))
		addInt(b, 7, n)
		b.AddASN1(ctxTag(8), func(b *cryptobyte.Builder) {
			b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
				for _, e := range etypes {
					b.AddASN1Int64(int64(e))
				}
			})
		})
	})
	return b.BytesOrPanic()
}

// kdcReq is a KDC-REQ: an AS-REQ or TGS-REQ of its pre-authentication
// data and body.
func kdcReq(tag, msgType int, pa []paData, body []byte) []byte {
	var b cryptobyte.Builder
	b.AddASN1(appTag(tag), func(b *cryptobyte.Builder) {
		b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
			addInt(b, 1, 5)
			addInt(b, 2, int64(msgType))
			if len(pa) > 0 {
				b.AddASN1(ctxTag(3), func(b *cryptobyte.Builder) {
					b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
						for _, p := range pa {
							b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
								addInt(b, 1, p.typ)
								addOctets(b, 2, p.value)
							})
						}
					})
				})
			}
			b.AddASN1(ctxTag(4), func(b *cryptobyte.Builder) { b.AddBytes(body) })
		})
	})
	return b.BytesOrPanic()
}

// login gets a ticket-granting ticket: an AS-REQ without
// pre-authentication, which tells the key's salt, then one with the
// encrypted timestamp when the KDC asks for it.
func (c *Client) login(ctx context.Context) (*Ticket, error) {
	if c.Realm == "" || c.User == "" {
		return nil, errors.New("krb5: no realm or no principal")
	}
	if c.Password == "" && c.Keytab == nil {
		return nil, errors.New("krb5: neither a password nor a keytab")
	}
	cname := c.client()
	tgs := Principal{Type: nameTypeSrvInst, Parts: []string{"krbtgt", c.Realm}}
	var info []etypeInfo
	var pa []paData
	for try := 0; ; try++ {
		n := nonce()
		resp, err := c.exchange(ctx, kdcReq(appASReq, appASReq, pa, c.reqBody(&cname, tgs, n)))
		if err != nil {
			return nil, err
		}
		if tagOf(resp) == appKRBError {
			e, err := readError(resp)
			if err != nil {
				return nil, err
			}
			if e.Code != errPreauthRequired || try > 0 {
				return nil, e
			}
			for _, p := range e.paData {
				if p.typ == paETypeInfo2 {
					info, _ = readETypeInfo2(p.value)
				}
			}
			key, err := c.key(info)
			if err != nil {
				return nil, err
			}
			ts, err := c.encTimestamp(key)
			if err != nil {
				return nil, err
			}
			pa = []paData{{typ: paEncTS, value: ts}}
			continue
		}
		return c.asRep(resp, n, info)
	}
}

// key is the client's key of the first encryption type the KDC's info (or
// the client's list, without info) names that the client has: from the
// keytab, or the password with the info's salt, the realm and the name's
// parts by default.
func (c *Client) key(info []etypeInfo) (Key, error) {
	if len(info) == 0 {
		for _, e := range etypes {
			info = append(info, etypeInfo{etype: e})
		}
	}
	for _, in := range info {
		if keySize(in.etype) == 0 {
			continue
		}
		if c.Keytab != nil {
			if k, ok := c.Keytab.Key(c.Realm, c.client().Parts, in.etype); ok {
				return k, nil
			}
			continue
		}
		salt := in.salt
		if !in.hasSalt {
			salt = c.Realm + strings.Join(c.client().Parts, "")
		}
		return StringToKey(in.etype, c.Password, salt, in.iterations)
	}
	if c.Keytab != nil {
		return Key{}, fmt.Errorf("krb5: the keytab has no AES key of %s@%s", c.User, c.Realm)
	}
	return Key{}, errors.New("krb5: the KDC offers no AES encryption type for the principal")
}

// encTimestamp is PA-ENC-TIMESTAMP's value: the time, encrypted.
func (c *Client) encTimestamp(k Key) ([]byte, error) {
	now := c.clock()
	var b cryptobyte.Builder
	b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
		addTime(b, 0, now)
		addInt(b, 1, int64(now.Nanosecond()/1000))
	})
	ct, err := encrypt(k, usagePAEncTS, b.BytesOrPanic())
	if err != nil {
		return nil, err
	}
	var e cryptobyte.Builder
	e.AddASN1(asn1.SEQUENCE, func(e *cryptobyte.Builder) {
		addInt(e, 0, int64(k.EType))
		addOctets(e, 2, ct)
	})
	return e.BytesOrPanic(), nil
}

// asRep reads an AS-REP, its part decrypted with the client's key.
func (c *Client) asRep(b []byte, n int64, info []etypeInfo) (*Ticket, error) {
	f, err := app(b, appASRep)
	if err != nil {
		return nil, fmt.Errorf("krb5: the KDC's answer to the AS-REQ: %w", err)
	}
	// The AS-REP's own ETYPE-INFO2, when the KDC asked for no
	// pre-authentication, has the salt.
	if pa, ok := f[2]; ok {
		if list, ok := readPAData(pa); ok {
			for _, p := range list {
				if p.typ == paETypeInfo2 {
					info, _ = readETypeInfo2(p.value)
				}
			}
		}
	}
	enc, ok := readEncrypted(f, 6)
	if !ok {
		return nil, errMalformed
	}
	var key Key
	for _, in := range info {
		if in.etype == enc.etype {
			info = []etypeInfo{in}
		}
	}
	if len(info) == 0 || info[0].etype != enc.etype {
		info = []etypeInfo{{etype: enc.etype}}
	}
	if key, err = c.key(info); err != nil {
		return nil, err
	}
	return readRep(f, enc, key, usageASRepPart, n)
}

// readRep reads a KDC-REP's ticket and decrypted part: the session key,
// checked against the nonce asked with.
func readRep(f map[int]cryptobyte.String, enc encrypted, key Key, usage uint32, n int64) (*Ticket, error) {
	ticket, ok := f[5]
	if !ok {
		return nil, errMalformed
	}
	var raw cryptobyte.String
	if !ticket.ReadASN1Element(&raw, appTag(appTicket)) {
		return nil, errMalformed
	}
	if enc.etype != key.EType {
		return nil, errEType
	}
	plain, err := decrypt(key, usage, enc.cipher)
	if err != nil {
		return nil, err
	}
	// Some KDCs tag an AS-REP's part as a TGS-REP's.
	tag := tagOf(plain)
	if tag != appEncASRepPart && tag != appEncTGSRepPart {
		return nil, errMalformed
	}
	pf, err := app(plain, tag)
	if err != nil {
		return nil, err
	}
	sk, ok1 := readKey(pf, 0)
	got, ok2 := readInt(pf, 2)
	end, ok3 := readTime(pf, 7)
	realm, _ := readString(pf, 9)
	sname, _ := readPrincipal(pf, 10)
	if !ok1 {
		return nil, fmt.Errorf("krb5: the session key is missing or not AES: %w", errEType)
	}
	if !ok2 || !ok3 {
		return nil, errMalformed
	}
	if got != n {
		return nil, errors.New("krb5: the KDC's answer is not to this request (another nonce)")
	}
	return &Ticket{raw: []byte(raw), Key: sk, End: end, Realm: realm, Name: sname}, nil
}

// tgs gets a service ticket with the ticket-granting ticket.
func (c *Client) tgs(ctx context.Context, sname Principal) (*Ticket, error) {
	n := nonce()
	body := c.reqBody(nil, sname, n)
	ck, err := checksum(c.tgt.Key, usageTGSReqChecksum, body)
	if err != nil {
		return nil, err
	}
	ap, err := c.apReq(c.tgt, usageTGSReqAuth, 0, checksumType(c.tgt.Key.EType), ck, c.clock())
	if err != nil {
		return nil, err
	}
	resp, err := c.exchange(ctx, kdcReq(appTGSReq, appTGSReq, []paData{{typ: paTGSReq, value: ap}}, body))
	if err != nil {
		return nil, err
	}
	if tagOf(resp) == appKRBError {
		e, err := readError(resp)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("asking for a ticket to %s: %w", sname, e)
	}
	f, err := app(resp, appTGSRep)
	if err != nil {
		return nil, fmt.Errorf("krb5: the KDC's answer to the TGS-REQ: %w", err)
	}
	enc, ok := readEncrypted(f, 6)
	if !ok {
		return nil, errMalformed
	}
	return readRep(f, enc, c.tgt.Key, usageTGSRepPart, n)
}

// apReq is an AP-REQ of a ticket, its authenticator encrypted with the
// ticket's session key, with a checksum of a type (none when 0) and the
// time.
func (c *Client) apReq(t *Ticket, usage uint32, options uint32, cksumType int32, cksum []byte, now time.Time) ([]byte, error) {
	var a cryptobyte.Builder
	a.AddASN1(appTag(appAuthenticator), func(a *cryptobyte.Builder) {
		a.AddASN1(asn1.SEQUENCE, func(a *cryptobyte.Builder) {
			addInt(a, 0, 5)
			addString(a, 1, c.Realm)
			addPrincipal(a, 2, c.client())
			if cksum != nil {
				a.AddASN1(ctxTag(3), func(a *cryptobyte.Builder) {
					a.AddASN1(asn1.SEQUENCE, func(a *cryptobyte.Builder) {
						addInt(a, 0, int64(cksumType))
						addOctets(a, 1, cksum)
					})
				})
			}
			addInt(a, 4, int64(now.Nanosecond()/1000))
			addTime(a, 5, now)
		})
	})
	ct, err := encrypt(t.Key, usage, a.BytesOrPanic())
	if err != nil {
		return nil, err
	}
	var b cryptobyte.Builder
	b.AddASN1(appTag(appAPReq), func(b *cryptobyte.Builder) {
		b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
			addInt(b, 0, 5)
			addInt(b, 1, appAPReq)
			addBits(b, 2, options)
			b.AddASN1(ctxTag(3), func(b *cryptobyte.Builder) { b.AddBytes(t.raw) })
			addEncrypted(b, 4, encrypted{etype: t.Key.EType, cipher: ct})
		})
	})
	return b.BytesOrPanic(), nil
}

// exchange sends a message to the KDCs in turn, over TCP (a 4-byte length
// before each message), and returns the first answer.
func (c *Client) exchange(ctx context.Context, msg []byte) ([]byte, error) {
	if len(c.KDCs) == 0 {
		return nil, fmt.Errorf("krb5: no KDC for the realm %s", c.Realm)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	var errs []error
	for _, kdc := range c.KDCs {
		if _, _, err := net.SplitHostPort(kdc); err != nil {
			kdc = net.JoinHostPort(kdc, "88")
		}
		b, err := c.exchangeWith(ctx, kdc, msg, timeout)
		if err == nil {
			return b, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", kdc, err))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("krb5: no KDC answered: %w", errors.Join(errs...))
}

func (c *Client) exchangeWith(ctx context.Context, kdc string, msg []byte, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var nc net.Conn
	var err error
	if c.Dial != nil {
		nc, err = c.Dial(ctx, kdc)
	} else {
		var d net.Dialer
		nc, err = d.DialContext(ctx, "tcp", kdc)
	}
	if err != nil {
		return nil, err
	}
	defer nc.Close()
	stop := context.AfterFunc(ctx, func() { nc.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if _, err := nc.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(msg))), msg...)); err != nil {
		return nil, err
	}
	var h [4]byte
	if _, err := io.ReadFull(nc, h[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(h[:])
	if n > 1<<20 {
		return nil, fmt.Errorf("an answer of %d bytes", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(nc, b); err != nil {
		return nil, err
	}
	return b, nil
}
