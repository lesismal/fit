package stun

import (
	"bytes"
	"testing"
)

// FuzzParse checks that Parse never panics nor reads outside what it is
// given, and that what it takes keeps its attributes when built again.
func FuzzParse(f *testing.F) {
	for _, s := range []string{rfc5769Request, rfc5769ResponseIPv4, rfc5769ResponseIPv6, rfc5769LongTerm} {
		f.Add(unhex(f, s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var m Message
		if m.Parse(b) != nil {
			return
		}
		var again Message
		again.Build(m.Type, m.ID)
		for _, a := range m.Attrs {
			v := m.Value(a)
			if len(v) > len(b) {
				t.Fatalf("attribute %#04x longer than the message", a.Type)
			}
			again.Add(a.Type, v)
		}
		_, _ = m.XORAddr(AttrXORMappedAddress)
		_, _ = m.Addr(AttrMappedAddress)
		_, _, _ = m.ErrorCode()
		_, _ = m.UnknownAttributes(nil)
		_ = m.CheckIntegrity([]byte("k"))
		_ = m.CheckIntegritySHA256([]byte("k"))
		_ = m.CheckFingerprint()
		var back Message
		if err := back.Parse(again.Raw); err != nil {
			t.Fatalf("built from a parsed message, it does not parse: %v", err)
		}
		if len(back.Attrs) != len(m.Attrs) {
			t.Fatalf("%d attributes, then %d", len(m.Attrs), len(back.Attrs))
		}
		for i, a := range back.Attrs {
			if a.Type != m.Attrs[i].Type || !bytes.Equal(back.Value(a), m.Value(m.Attrs[i])) {
				t.Fatalf("attribute %d changed", i)
			}
		}
	})
}
