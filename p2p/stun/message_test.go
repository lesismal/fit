package stun

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// unhex decodes the hex of an RFC's figure, ignoring spaces and newlines.
func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// RFC 5769's test vectors.
const (
	rfc5769Request = `
		00 01 00 58 21 12 a4 42 b7 e7 a7 01 bc 34 d6 86 fa 87 df ae
		80 22 00 10 53 54 55 4e 20 74 65 73 74 20 63 6c 69 65 6e 74
		00 24 00 04 6e 00 01 ff
		80 29 00 08 93 2f f9 b1 51 26 3b 36
		00 06 00 09 65 76 74 6a 3a 68 36 76 59 20 20 20
		00 08 00 14 9a ea a7 0c bf d8 cb 56 78 1e f2 b5 b2 d3 f2 49 c1 b5 71 a2
		80 28 00 04 e5 7a 3b cf`
	rfc5769ResponseIPv4 = `
		01 01 00 3c 21 12 a4 42 b7 e7 a7 01 bc 34 d6 86 fa 87 df ae
		80 22 00 0b 74 65 73 74 20 76 65 63 74 6f 72 20
		00 20 00 08 00 01 a1 47 e1 12 a6 43
		00 08 00 14 2b 91 f5 99 fd 9e 90 c3 8c 74 89 f9 2a f9 ba 53 f0 6b e7 d7
		80 28 00 04 c0 7d 4c 96`
	rfc5769ResponseIPv6 = `
		01 01 00 48 21 12 a4 42 b7 e7 a7 01 bc 34 d6 86 fa 87 df ae
		80 22 00 0b 74 65 73 74 20 76 65 63 74 6f 72 20
		00 20 00 14 00 02 a1 47 01 13 a9 fa a5 d3 f1 79 bc 25 f4 b5 be d2 b9 d9
		00 08 00 14 a3 82 95 4e 4b e6 7b f1 17 84 c9 7c 82 92 c2 75 bf e3 ed 41
		80 28 00 04 c8 fb 0b 4c`
	rfc5769LongTerm = `
		00 01 00 60 21 12 a4 42 78 ad 34 33 c6 ad 72 c0 29 da 41 2e
		00 06 00 12 e3 83 9e e3 83 88 e3 83 aa e3 83 83 e3 82 af e3 82 b9 00 00
		00 15 00 1c 66 2f 2f 34 39 39 6b 39 35 34 64 36 4f 4c 33 34 6f 4c 39 46 53 54 76 79 36 34 73 41
		00 14 00 0b 65 78 61 6d 70 6c 65 2e 6f 72 67 00
		00 08 00 14 f6 70 24 65 6d d6 4a 3e 02 b8 e0 71 2e 85 c9 a2 8c a8 96 66`
	rfc5769Password = "VOkJxbRl1RmTxUk/WvJxBt"
)

func TestRFC5769Request(t *testing.T) {
	var m Message
	if err := m.Parse(unhex(t, rfc5769Request)); err != nil {
		t.Fatal(err)
	}
	if m.Type != (Type{MethodBinding, ClassRequest}) {
		t.Fatalf("type %+v", m.Type)
	}
	if err := m.CheckFingerprint(); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckIntegrity([]byte(rfc5769Password)); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckIntegrity([]byte("wrong")); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("a wrong password: %v", err)
	}
	if v, _ := m.Get(AttrUsername); string(v) != "evtj:h6vY" {
		t.Fatalf("USERNAME %q", v)
	}
	if v, _ := m.Get(AttrSoftware); string(v) != "STUN test client" {
		t.Fatalf("SOFTWARE %q", v)
	}
	if p, err := m.Uint32(AttrPriority); err != nil || p != 0x6e0001ff {
		t.Fatalf("PRIORITY %x, %v", p, err)
	}
	if v, err := m.Uint64(AttrICEControlled); err != nil || v != 0x932ff9b151263b36 {
		t.Fatalf("ICE-CONTROLLED %x, %v", v, err)
	}
}

func TestRFC5769Responses(t *testing.T) {
	for _, c := range []struct {
		name, hex string
		want      netip.AddrPort
	}{
		{"ipv4", rfc5769ResponseIPv4, netip.MustParseAddrPort("192.0.2.1:32853")},
		{"ipv6", rfc5769ResponseIPv6, netip.MustParseAddrPort("[2001:db8:1234:5678:11:2233:4455:6677]:32853")},
	} {
		t.Run(c.name, func(t *testing.T) {
			var m Message
			if err := m.Parse(unhex(t, c.hex)); err != nil {
				t.Fatal(err)
			}
			if m.Type != (Type{MethodBinding, ClassSuccess}) {
				t.Fatalf("type %+v", m.Type)
			}
			if err := m.CheckFingerprint(); err != nil {
				t.Fatal(err)
			}
			if err := m.CheckIntegrity([]byte(rfc5769Password)); err != nil {
				t.Fatal(err)
			}
			got, err := m.XORAddr(AttrXORMappedAddress)
			if err != nil || got != c.want {
				t.Fatalf("XOR-MAPPED-ADDRESS %v, %v", got, err)
			}
			// Built again from what was parsed, it is the same message.
			var b Message
			b.Build(m.Type, m.ID)
			v, _ := m.Get(AttrSoftware)
			b.Add(AttrSoftware, v)
			b.AddXORAddr(AttrXORMappedAddress, got)
			b.AddIntegrity([]byte(rfc5769Password))
			b.AddFingerprint()
			// The vector pads SOFTWARE with spaces, which a builder need
			// not do; the rest must match.
			want := unhex(t, c.hex)
			copy(b.Raw[20+4+11:20+4+12], want[20+4+11:20+4+12])
			if !bytes.Equal(b.Raw[:36], want[:36]) || len(b.Raw) != len(want) {
				t.Fatalf("built\n%x\nwant\n%x", b.Raw, want)
			}
		})
	}
}

func TestRFC5769LongTerm(t *testing.T) {
	var m Message
	if err := m.Parse(unhex(t, rfc5769LongTerm)); err != nil {
		t.Fatal(err)
	}
	user, _ := m.Get(AttrUsername)
	realm, _ := m.Get(AttrRealm)
	nonce, _ := m.Get(AttrNonce)
	if string(user) != "マトリックス" || string(realm) != "example.org" ||
		string(nonce) != "f//499k954d6OL34oL9FSTvy64sA" {
		t.Fatalf("USERNAME %q, REALM %q, NONCE %q", user, realm, nonce)
	}
	// The password is "The<U+00AD>M<U+00AA>tr<U+2168>", which OpaqueString
	// makes "TheMatrIX".
	if err := m.CheckIntegrity(LongTermKey(string(user), string(realm), "TheMatrIX")); err != nil {
		t.Fatal(err)
	}
}

func TestTypeEncoding(t *testing.T) {
	for _, c := range []struct {
		t Type
		v uint16
	}{
		{Type{MethodBinding, ClassRequest}, 0x0001},
		{Type{MethodBinding, ClassIndication}, 0x0011},
		{Type{MethodBinding, ClassSuccess}, 0x0101},
		{Type{MethodBinding, ClassError}, 0x0111},
		{Type{MethodAllocate, ClassRequest}, 0x0003},
		{Type{MethodAllocate, ClassError}, 0x0113},
		{Type{MethodSend, ClassIndication}, 0x0016},
		{Type{MethodData, ClassIndication}, 0x0017},
		{Type{0xfff, ClassError}, 0x3fff},
	} {
		if got := c.t.value(); got != c.v {
			t.Errorf("%+v: %#04x, want %#04x", c.t, got, c.v)
		}
		if got := typeOf(c.v); got != c.t {
			t.Errorf("%#04x: %+v, want %+v", c.v, got, c.t)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	id := NewTransactionID()
	var b Message
	b.Build(Type{MethodAllocate, ClassError}, id)
	b.AddErrorCode(CodeUnknownAttribute, "Unknown Attribute")
	b.AddUnknownAttributes([]AttrType{0x0042, 0x0043, 0x0044})
	b.AddAddr(AttrMappedAddress, netip.MustParseAddrPort("10.0.0.1:3478"))
	b.AddXORAddr(AttrXORPeerAddress, netip.MustParseAddrPort("[::ffff:10.0.0.2]:9"))
	b.AddXORAddr(AttrXORRelayedAddress, netip.MustParseAddrPort("[2001:db8::1]:65535"))
	b.AddString(AttrRealm, "realm")
	b.AddUint32(AttrLifetime, 600)
	key := LongTermKeySHA256("u", "realm", "p")
	b.AddIntegrity([]byte("k"))
	b.AddIntegritySHA256(key)
	b.AddFingerprint()

	var m Message
	if err := m.Parse(bytes.Clone(b.Raw)); err != nil {
		t.Fatal(err)
	}
	if m.Type != b.Type || m.ID != id {
		t.Fatalf("type %+v id %x", m.Type, m.ID)
	}
	if code, reason, err := m.ErrorCode(); err != nil || code != 420 || string(reason) != "Unknown Attribute" {
		t.Fatalf("ERROR-CODE %d %q %v", code, reason, err)
	}
	if u, err := m.UnknownAttributes(nil); err != nil || len(u) != 3 || u[2] != 0x0044 {
		t.Fatalf("UNKNOWN-ATTRIBUTES %v %v", u, err)
	}
	if a, err := m.Addr(AttrMappedAddress); err != nil || a.String() != "10.0.0.1:3478" {
		t.Fatalf("MAPPED-ADDRESS %v %v", a, err)
	}
	if a, err := m.XORAddr(AttrXORPeerAddress); err != nil || a.String() != "10.0.0.2:9" {
		t.Fatalf("XOR-PEER-ADDRESS %v %v", a, err)
	}
	if a, err := m.XORAddr(AttrXORRelayedAddress); err != nil || a.String() != "[2001:db8::1]:65535" {
		t.Fatalf("XOR-RELAYED-ADDRESS %v %v", a, err)
	}
	if l, err := m.Uint32(AttrLifetime); err != nil || l != 600 {
		t.Fatalf("LIFETIME %d %v", l, err)
	}
	if err := m.CheckIntegrity([]byte("k")); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckIntegritySHA256(key); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckFingerprint(); err != nil {
		t.Fatal(err)
	}
	// A flipped bit anywhere before them fails all three.
	m.Raw[30] ^= 1
	if m.CheckIntegrity([]byte("k")) == nil || m.CheckIntegritySHA256(key) == nil || m.CheckFingerprint() == nil {
		t.Fatal("a changed message passed its checks")
	}
	if _, err := m.Uint64(AttrPriority); !errors.Is(err, ErrNoAttr) {
		t.Fatalf("a missing attribute: %v", err)
	}
}

func TestAfterIntegrityIgnored(t *testing.T) {
	var b Message
	b.Build(Type{MethodBinding, ClassRequest}, NewTransactionID())
	b.AddIntegrity([]byte("k"))
	b.AddString(AttrSoftware, "late")
	b.AddFingerprint()
	var m Message
	if err := m.Parse(b.Raw); err != nil {
		t.Fatal(err)
	}
	if m.Has(AttrSoftware) {
		t.Fatal("an attribute after MESSAGE-INTEGRITY counted")
	}
	if !m.Has(AttrFingerprint) || m.CheckIntegrity([]byte("k")) != nil {
		t.Fatal("integrity or fingerprint lost")
	}
}

func TestMalformed(t *testing.T) {
	valid := func() []byte {
		var b Message
		b.Build(Type{MethodBinding, ClassRequest}, NewTransactionID())
		b.AddString(AttrSoftware, "x")
		return bytes.Clone(b.Raw)
	}
	for _, c := range []struct {
		name string
		edit func([]byte) []byte
		want error
	}{
		{"short", func(b []byte) []byte { return b[:19] }, ErrNotSTUN},
		{"top bits", func(b []byte) []byte { b[0] |= 0x40; return b }, ErrNotSTUN},
		{"cookie", func(b []byte) []byte { b[4] ^= 1; return b }, ErrNotSTUN},
		{"length past the end", func(b []byte) []byte { b[3] += 4; return b }, ErrMalformed},
		{"length not a multiple of 4", func(b []byte) []byte { b[3]--; return b[:len(b)-1] }, ErrMalformed},
		{"trailing bytes", func(b []byte) []byte { return append(b, 0, 0, 0, 0) }, ErrMalformed},
		{"attribute past the end", func(b []byte) []byte { b[23] = 9; return b }, ErrMalformed},
		{"fingerprint not last", func(b []byte) []byte {
			var m Message
			m.Build(Type{MethodBinding, ClassRequest}, NewTransactionID())
			m.AddFingerprint()
			m.AddString(AttrSoftware, "x")
			return m.Raw
		}, ErrMalformed},
		{"too many attributes", func(b []byte) []byte {
			var m Message
			m.Build(Type{MethodBinding, ClassRequest}, NewTransactionID())
			for range MaxAttrs + 1 {
				m.Add(AttrPadding, nil)
			}
			return m.Raw
		}, ErrTooManyAttrs},
	} {
		var m Message
		if err := m.Parse(c.edit(valid())); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	var m Message
	m.Build(Type{MethodBinding, ClassSuccess}, NewTransactionID())
	m.Add(AttrXORMappedAddress, []byte{0, 3, 0, 0, 1, 2, 3, 4})
	m.Add(AttrErrorCode, []byte{0, 0, 7, 0})
	m.Add(AttrLifetime, []byte{1})
	if _, err := m.XORAddr(AttrXORMappedAddress); !errors.Is(err, ErrBadAttr) {
		t.Errorf("a bad family: %v", err)
	}
	if _, _, err := m.ErrorCode(); !errors.Is(err, ErrBadAttr) {
		t.Errorf("a bad error class: %v", err)
	}
	if _, err := m.Uint32(AttrLifetime); !errors.Is(err, ErrBadAttr) {
		t.Errorf("a short LIFETIME: %v", err)
	}
	if MessageLen([]byte{1}) != 0 || MessageLen(make([]byte, 20)) != -1 {
		t.Error("MessageLen of a short or foreign header")
	}
}

func TestUnknownRequired(t *testing.T) {
	var m Message
	m.Build(Type{MethodBinding, ClassRequest}, NewTransactionID())
	m.Add(0x0042, nil)
	m.Add(0x8042, nil)
	m.AddString(AttrUsername, "u")
	got := m.UnknownRequired(nil, func(t AttrType) bool { return t == AttrUsername })
	if len(got) != 1 || got[0] != 0x0042 {
		t.Fatalf("unknown %v", got)
	}
}

func TestNoAllocs(t *testing.T) {
	raw := unhex(t, rfc5769ResponseIPv4)
	var m, b Message
	addr := netip.MustParseAddrPort("192.0.2.1:32853")
	build := func() {
		b.Build(Type{MethodBinding, ClassSuccess}, m.ID)
		b.AddXORAddr(AttrXORMappedAddress, addr)
		b.AddString(AttrSoftware, "fit")
		b.AddFingerprint()
	}
	build()
	if n := testing.AllocsPerRun(100, func() {
		if m.Parse(raw) != nil || m.CheckFingerprint() != nil {
			t.Fatal("parse")
		}
		if _, err := m.XORAddr(AttrXORMappedAddress); err != nil {
			t.Fatal(err)
		}
		build()
	}); n != 0 {
		t.Fatalf("%v allocations parsing and building", n)
	}
}
