package krb5

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/cryptobyte"
	"golang.org/x/crypto/cryptobyte/asn1"
)

// Kerberos's messages (RFC 4120), in DER: SEQUENCEs of explicitly tagged
// fields, wrapped in APPLICATION tags.

// The application tags of the messages.
const (
	appTicket        = 1
	appAuthenticator = 2
	appASReq         = 10
	appASRep         = 11
	appTGSReq        = 12
	appTGSRep        = 13
	appAPReq         = 14
	appAPRep         = 15
	appEncASRepPart  = 25
	appEncTGSRepPart = 26
	appEncAPRepPart  = 27
	appKRBError      = 30
)

// The pre-authentication data types the client knows.
const (
	paTGSReq     = 1
	paEncTS      = 2
	paETypeInfo2 = 19
)

// The key usages (RFC 4120 7.5.1).
const (
	usagePAEncTS        = 1
	usageASRepPart      = 3
	usageTGSReqChecksum = 6
	usageTGSReqAuth     = 7
	usageTGSRepPart     = 8
	usageAPReqAuth      = 11
	usageAPRepPart      = 12
)

// The principal name types.
const (
	nameTypePrincipal = 1
	nameTypeSrvInst   = 2
)

// The KRB-ERROR codes the client tells apart.
const (
	errPreauthRequired = 25
)

var (
	tagGeneralString = asn1.Tag(27)
	errMalformed     = errors.New("krb5: a malformed message")
)

func ctxTag(n int) asn1.Tag { return asn1.Tag(n).ContextSpecific().Constructed() }
func appTag(n int) asn1.Tag { return asn1.Tag(0x60 | n) }

// Builders of fields.

func addInt(b *cryptobyte.Builder, n int, v int64) {
	b.AddASN1(ctxTag(n), func(b *cryptobyte.Builder) { b.AddASN1Int64(v) })
}

func addString(b *cryptobyte.Builder, n int, s string) {
	b.AddASN1(ctxTag(n), func(b *cryptobyte.Builder) {
		b.AddASN1(tagGeneralString, func(b *cryptobyte.Builder) { b.AddBytes([]byte(s)) })
	})
}

func addTime(b *cryptobyte.Builder, n int, t time.Time) {
	b.AddASN1(ctxTag(n), func(b *cryptobyte.Builder) { b.AddASN1GeneralizedTime(t.UTC().Truncate(time.Second)) })
}

func addOctets(b *cryptobyte.Builder, n int, data []byte) {
	b.AddASN1(ctxTag(n), func(b *cryptobyte.Builder) { b.AddASN1OctetString(data) })
}

func addBits(b *cryptobyte.Builder, n int, flags uint32) {
	b.AddASN1(ctxTag(n), func(b *cryptobyte.Builder) {
		b.AddASN1BitString([]byte{byte(flags >> 24), byte(flags >> 16), byte(flags >> 8), byte(flags)})
	})
}

// Principal is a principal's name: its type and parts.
type Principal struct {
	Type  int32
	Parts []string
}

func (p Principal) String() string {
	s := ""
	for i, x := range p.Parts {
		if i > 0 {
			s += "/"
		}
		s += x
	}
	return s
}

func addPrincipal(b *cryptobyte.Builder, n int, p Principal) {
	b.AddASN1(ctxTag(n), func(b *cryptobyte.Builder) {
		b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
			addInt(b, 0, int64(p.Type))
			b.AddASN1(ctxTag(1), func(b *cryptobyte.Builder) {
				b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
					for _, s := range p.Parts {
						b.AddASN1(tagGeneralString, func(b *cryptobyte.Builder) { b.AddBytes([]byte(s)) })
					}
				})
			})
		})
	})
}

// encrypted is an EncryptedData.
type encrypted struct {
	etype  int32
	kvno   int64
	cipher []byte
}

func addEncrypted(b *cryptobyte.Builder, n int, e encrypted) {
	b.AddASN1(ctxTag(n), func(b *cryptobyte.Builder) {
		b.AddASN1(asn1.SEQUENCE, func(b *cryptobyte.Builder) {
			addInt(b, 0, int64(e.etype))
			addOctets(b, 2, e.cipher)
		})
	})
}

// Readers of fields.

// fields reads a SEQUENCE's explicitly tagged fields, by tag, each its
// contents.
func fields(s cryptobyte.String) (map[int]cryptobyte.String, bool) {
	var seq cryptobyte.String
	if !s.ReadASN1(&seq, asn1.SEQUENCE) {
		return nil, false
	}
	out := map[int]cryptobyte.String{}
	for !seq.Empty() {
		var v cryptobyte.String
		var tag asn1.Tag
		if !seq.ReadAnyASN1(&v, &tag) || tag&0xc0 != 0x80 {
			return nil, false
		}
		out[int(tag&0x1f)] = v
	}
	return out, true
}

// app reads a message of an application tag: its SEQUENCE's fields.
func app(b []byte, tag int) (map[int]cryptobyte.String, error) {
	s := cryptobyte.String(b)
	var in cryptobyte.String
	if !s.ReadASN1(&in, appTag(tag)) {
		return nil, errMalformed
	}
	f, ok := fields(in)
	if !ok {
		return nil, errMalformed
	}
	return f, nil
}

func readInt(f map[int]cryptobyte.String, n int) (int64, bool) {
	s, ok := f[n]
	var v int64
	return v, ok && s.ReadASN1Integer(&v)
}

func readString(f map[int]cryptobyte.String, n int) (string, bool) {
	s, ok := f[n]
	var v cryptobyte.String
	return string(v), ok && s.ReadASN1(&v, tagGeneralString)
}

func readOctets(f map[int]cryptobyte.String, n int) ([]byte, bool) {
	s, ok := f[n]
	var v []byte
	return v, ok && s.ReadASN1Bytes(&v, asn1.OCTET_STRING)
}

func readTime(f map[int]cryptobyte.String, n int) (time.Time, bool) {
	s, ok := f[n]
	var v time.Time
	return v, ok && s.ReadASN1GeneralizedTime(&v)
}

func readPrincipal(f map[int]cryptobyte.String, n int) (Principal, bool) {
	s, ok := f[n]
	if !ok {
		return Principal{}, false
	}
	pf, ok := fields(s)
	if !ok {
		return Principal{}, false
	}
	t, ok := readInt(pf, 0)
	if !ok {
		return Principal{}, false
	}
	p := Principal{Type: int32(t)}
	names, ok := pf[1]
	var seq cryptobyte.String
	if !ok || !names.ReadASN1(&seq, asn1.SEQUENCE) {
		return Principal{}, false
	}
	for !seq.Empty() {
		var v cryptobyte.String
		if !seq.ReadASN1(&v, tagGeneralString) {
			return Principal{}, false
		}
		p.Parts = append(p.Parts, string(v))
	}
	return p, true
}

func readEncrypted(f map[int]cryptobyte.String, n int) (encrypted, bool) {
	s, ok := f[n]
	if !ok {
		return encrypted{}, false
	}
	ef, ok := fields(s)
	if !ok {
		return encrypted{}, false
	}
	et, ok1 := readInt(ef, 0)
	c, ok2 := readOctets(ef, 2)
	kvno, _ := readInt(ef, 1)
	return encrypted{etype: int32(et), kvno: kvno, cipher: c}, ok1 && ok2
}

func readKey(f map[int]cryptobyte.String, n int) (Key, bool) {
	s, ok := f[n]
	if !ok {
		return Key{}, false
	}
	kf, ok := fields(s)
	if !ok {
		return Key{}, false
	}
	t, ok1 := readInt(kf, 0)
	v, ok2 := readOctets(kf, 1)
	return Key{EType: int32(t), Value: v}, ok1 && ok2 && len(v) == keySize(int32(t))
}

// paData is a PA-DATA.
type paData struct {
	typ   int64
	value []byte
}

// readPAData reads a SEQUENCE OF PA-DATA.
func readPAData(s cryptobyte.String) ([]paData, bool) {
	var seq cryptobyte.String
	if !s.ReadASN1(&seq, asn1.SEQUENCE) {
		return nil, false
	}
	var out []paData
	for !seq.Empty() {
		var el cryptobyte.String
		if !seq.ReadASN1Element(&el, asn1.SEQUENCE) {
			return nil, false
		}
		f, ok := fields(el)
		if !ok {
			return nil, false
		}
		t, ok1 := readInt(f, 1)
		v, ok2 := readOctets(f, 2)
		if !ok1 || !ok2 {
			return nil, false
		}
		out = append(out, paData{typ: t, value: v})
	}
	return out, true
}

// etypeInfo is an ETYPE-INFO2-ENTRY: an encryption type, its salt and
// its string-to-key iterations.
type etypeInfo struct {
	etype      int32
	salt       string
	hasSalt    bool
	iterations int
}

// readETypeInfo2 reads an ETYPE-INFO2.
func readETypeInfo2(b []byte) ([]etypeInfo, bool) {
	s := cryptobyte.String(b)
	var seq cryptobyte.String
	if !s.ReadASN1(&seq, asn1.SEQUENCE) {
		return nil, false
	}
	var out []etypeInfo
	for !seq.Empty() {
		var el cryptobyte.String
		if !seq.ReadASN1Element(&el, asn1.SEQUENCE) {
			return nil, false
		}
		f, ok := fields(el)
		if !ok {
			return nil, false
		}
		et, ok := readInt(f, 0)
		if !ok {
			return nil, false
		}
		e := etypeInfo{etype: int32(et)}
		e.salt, e.hasSalt = readString(f, 1)
		if p, ok := readOctets(f, 2); ok && len(p) == 4 {
			e.iterations = int(uint32(p[0])<<24 | uint32(p[1])<<16 | uint32(p[2])<<8 | uint32(p[3]))
		}
		out = append(out, e)
	}
	return out, true
}

// KDCError is a KRB-ERROR.
type KDCError struct {
	Code  int64
	Text  string
	Realm string
	// paData is e-data's METHOD-DATA, when there is.
	paData []paData
}

// The names of the commonest KRB-ERROR codes.
var errNames = map[int64]string{
	6:  "KDC_ERR_C_PRINCIPAL_UNKNOWN (the client is not in the KDC's database)",
	7:  "KDC_ERR_S_PRINCIPAL_UNKNOWN (no such service principal: check the SPN)",
	14: "KDC_ERR_ETYPE_NOSUPP (no AES key for the principal)",
	18: "KDC_ERR_CLIENT_REVOKED",
	23: "KDC_ERR_KEY_EXPIRED (the password has expired)",
	24: "KDC_ERR_PREAUTH_FAILED (a wrong password or key)",
	25: "KDC_ERR_PREAUTH_REQUIRED",
	31: "KRB_AP_ERR_BAD_INTEGRITY",
	32: "KRB_AP_ERR_TKT_EXPIRED",
	37: "KRB_AP_ERR_SKEW (the clocks differ by more than the KDC allows)",
	41: "KRB_AP_ERR_MODIFIED",
	68: "KDC_ERR_WRONG_REALM",
}

func (e *KDCError) Error() string {
	s := fmt.Sprintf("krb5: the KDC answered error %d", e.Code)
	if n := errNames[e.Code]; n != "" {
		s += ", " + n
	}
	if e.Text != "" {
		s += ": " + e.Text
	}
	return s
}

// readError reads a KRB-ERROR.
func readError(b []byte) (*KDCError, error) {
	f, err := app(b, appKRBError)
	if err != nil {
		return nil, err
	}
	code, ok := readInt(f, 6)
	if !ok {
		return nil, errMalformed
	}
	e := &KDCError{Code: code}
	e.Text, _ = readString(f, 11)
	e.Realm, _ = readString(f, 9)
	if d, ok := readOctets(f, 12); ok {
		e.paData, _ = readPAData(cryptobyte.String(d))
	}
	return e, nil
}

// tagOf is a message's application tag, -1 when it has none.
func tagOf(b []byte) int {
	if len(b) == 0 || b[0]&0xe0 != 0x60 {
		return -1
	}
	return int(b[0] & 0x1f)
}
