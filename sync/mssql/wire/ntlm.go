package mssqlwire

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/md4" //lint:ignore SA1019 NTLM is defined on MD4
)

// NTLM is integrated authentication with NTLMv2 (MS-NLMP): a Windows
// account's name and password, for a server outside a domain (or a
// client that has no Kerberos). Weaker than Kerberos: the server is not
// authenticated, and a captured exchange can be attacked offline.
// NTLMv1 and LM are not offered. Its methods are SSPI's.
type NTLM struct {
	// Domain is the account's domain, or the server's name for a local
	// account.
	Domain string
	// User and Password are the account's.
	User, Password string
	// Workstation is the client's name; the host's when empty.
	Workstation string

	// now and random are the clock and the client challenge's source;
	// the tests set them.
	now    func() time.Time
	random func([]byte)
}

// ParseNTLMUser splits DOMAIN\user (or user@domain) into its domain and
// user.
func ParseNTLMUser(s string) (domain, user string) {
	if d, u, ok := strings.Cut(s, `\`); ok {
		return d, u
	}
	if u, d, ok := strings.Cut(s, "@"); ok {
		return d, u
	}
	return "", s
}

// The negotiate flags the client asks for: Unicode, the target's name and
// information, NTLM with extended session security, always sign, 128 and
// 56 bit.
const ntlmFlags = 0x00000001 | 0x00000004 | 0x00000200 | 0x00008000 | 0x00080000 | 0x00800000 | 0x20000000 | 0x80000000

var ntlmSignature = []byte("NTLMSSP\x00")

// Start returns the NEGOTIATE message.
func (n *NTLM) Start(context.Context, string) ([]byte, error) {
	b := append([]byte(nil), ntlmSignature...)
	b = binary.LittleEndian.AppendUint32(b, 1)
	b = binary.LittleEndian.AppendUint32(b, ntlmFlags)
	// No domain or workstation given here: their fields empty, at the
	// end of the message.
	for range 2 {
		b = binary.LittleEndian.AppendUint16(b, 0)
		b = binary.LittleEndian.AppendUint16(b, 0)
		b = binary.LittleEndian.AppendUint32(b, 32)
	}
	return b, nil
}

// Next answers the server's CHALLENGE with the AUTHENTICATE message.
func (n *NTLM) Next(_ context.Context, in []byte) ([]byte, error) {
	if len(in) < 48 || !bytes.Equal(in[:8], ntlmSignature) || binary.LittleEndian.Uint32(in[8:]) != 2 {
		return nil, errors.New("ntlm: the server's token is not a CHALLENGE")
	}
	flags := binary.LittleEndian.Uint32(in[20:])
	challenge := in[24:32]
	tl, to := int(binary.LittleEndian.Uint16(in[40:])), int(binary.LittleEndian.Uint32(in[44:]))
	if to+tl > len(in) {
		return nil, errors.New("ntlm: a malformed CHALLENGE")
	}
	info := in[to : to+tl]
	now := time.Now()
	if n.now != nil {
		now = n.now()
	}
	stamp, hasStamp := avTimestamp(info)
	if !hasStamp {
		stamp = filetime(now)
	}
	client := make([]byte, 8)
	if n.random != nil {
		n.random(client)
	} else {
		rand.Read(client)
	}
	key := ntowfv2(n.User, n.Password, n.Domain)
	nt, lm := ntlmv2Response(key, challenge, client, stamp, info)
	if hasStamp {
		// With the server's time in its information, the LM response is
		// zeros (MS-NLMP 3.1.5.1.2).
		lm = make([]byte, 24)
	}
	ws := n.Workstation
	if ws == "" {
		ws = hostname()
	}
	payload := [][]byte{lm, nt, ucs2(n.Domain), ucs2(n.User), ucs2(ws), nil}
	b := append([]byte(nil), ntlmSignature...)
	b = binary.LittleEndian.AppendUint32(b, 3)
	off := 64
	for _, p := range payload {
		b = binary.LittleEndian.AppendUint16(b, uint16(len(p)))
		b = binary.LittleEndian.AppendUint16(b, uint16(len(p)))
		b = binary.LittleEndian.AppendUint32(b, uint32(off))
		off += len(p)
	}
	b = binary.LittleEndian.AppendUint32(b, flags&ntlmFlags)
	for _, p := range payload {
		b = append(b, p...)
	}
	return b, nil
}

// ntowfv2 is NTOWFv2: HMAC-MD5, keyed by the MD4 of the password in
// UTF-16LE, of the user in upper case and the domain.
func ntowfv2(user, password, domain string) []byte {
	h := md4.New()
	h.Write(ucs2(password))
	m := hmac.New(md5.New, h.Sum(nil))
	m.Write(ucs2(strings.ToUpper(user) + domain))
	return m.Sum(nil)
}

// ntlmv2Response is the NTLMv2 and LMv2 responses.
func ntlmv2Response(key, server, client []byte, stamp uint64, info []byte) (nt, lm []byte) {
	temp := []byte{1, 1, 0, 0, 0, 0, 0, 0}
	temp = binary.LittleEndian.AppendUint64(temp, stamp)
	temp = append(temp, client...)
	temp = append(temp, 0, 0, 0, 0)
	temp = append(temp, info...)
	temp = append(temp, 0, 0, 0, 0)
	m := hmac.New(md5.New, key)
	m.Write(server)
	m.Write(temp)
	nt = append(m.Sum(nil), temp...)
	m = hmac.New(md5.New, key)
	m.Write(server)
	m.Write(client)
	lm = append(m.Sum(nil), client...)
	return nt, lm
}

// avTimestamp finds MsvAvTimestamp in a target information's AV pairs.
func avTimestamp(info []byte) (uint64, bool) {
	for len(info) >= 4 {
		id, n := binary.LittleEndian.Uint16(info), int(binary.LittleEndian.Uint16(info[2:]))
		if id == 0 || 4+n > len(info) {
			break
		}
		if id == 7 && n == 8 {
			return binary.LittleEndian.Uint64(info[4:]), true
		}
		info = info[4+n:]
	}
	return 0, false
}

// filetime is t as Windows' FILETIME: 100 ns since 1601.
func filetime(t time.Time) uint64 {
	return uint64(t.UnixNano()/100) + 116444736000000000
}

func ucs2(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, x := range u {
		binary.LittleEndian.PutUint16(b[2*i:], x)
	}
	return b
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
