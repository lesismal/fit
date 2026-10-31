package stun

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"hash/crc32"
)

// fingerprintXOR is what a FINGERPRINT's CRC-32 is XORed with (RFC 8489
// section 14.7), so that it differs from the CRC of another protocol
// sharing the port.
const fingerprintXOR = 0x5354554e

// LongTermKey returns the key of long-term credentials (RFC 8489 section
// 9.2.2) for MESSAGE-INTEGRITY: MD5 of username, realm and password. The
// password is taken as it is given, with no OpaqueString processing (see
// decision 025).
func LongTermKey(username, realm, password string) []byte {
	h := md5.New()
	writeCredentials(h, username, realm, password)
	return h.Sum(nil)
}

// LongTermKeySHA256 is LongTermKey for the SHA-256 password algorithm.
func LongTermKeySHA256(username, realm, password string) []byte {
	h := sha256.New()
	writeCredentials(h, username, realm, password)
	return h.Sum(nil)
}

func writeCredentials(h hash.Hash, username, realm, password string) {
	h.Write([]byte(username))
	h.Write([]byte{':'})
	h.Write([]byte(realm))
	h.Write([]byte{':'})
	h.Write([]byte(password))
}

// AddIntegrity appends MESSAGE-INTEGRITY, an HMAC-SHA1 with key of the
// message so far. Short-term credentials' key is the password itself;
// long-term credentials' is LongTermKey. Only FINGERPRINT and
// MESSAGE-INTEGRITY-SHA256 may follow it.
func (m *Message) AddIntegrity(key []byte) {
	m.addIntegrity(AttrMessageIntegrity, key, sha1.New, sha1.Size)
}

// AddIntegritySHA256 appends MESSAGE-INTEGRITY-SHA256, an HMAC-SHA256 with
// key of the message so far. Only FINGERPRINT may follow it.
func (m *Message) AddIntegritySHA256(key []byte) {
	m.addIntegrity(AttrMessageIntegritySHA256, key, sha256.New, sha256.Size)
}

var zeros [sha256.Size]byte

func (m *Message) addIntegrity(t AttrType, key []byte, h func() hash.Hash, size int) {
	off := len(m.Raw)
	m.Raw = append(m.grow(t, size), zeros[:size]...)
	// The header's length counts the attribute, as the HMAC covers it.
	m.pad()
	mac := hmac.New(h, key)
	mac.Write(m.Raw[:off])
	mac.Sum(m.Raw[off+4 : off+4])
}

// CheckIntegrity checks m's MESSAGE-INTEGRITY against key. It returns
// ErrNoAttr if m has none.
func (m *Message) CheckIntegrity(key []byte) error {
	return m.checkIntegrity(AttrMessageIntegrity, key, sha1.New, sha1.Size, sha1.Size)
}

// CheckIntegritySHA256 checks m's MESSAGE-INTEGRITY-SHA256 against key,
// which may be cut to as few as 16 bytes. It returns ErrNoAttr if m has
// none.
func (m *Message) CheckIntegritySHA256(key []byte) error {
	return m.checkIntegrity(AttrMessageIntegritySHA256, key, sha256.New, 16, sha256.Size)
}

func (m *Message) checkIntegrity(t AttrType, key []byte, h func() hash.Hash, minLen, maxLen int) error {
	for _, a := range m.Attrs {
		if a.Type != t {
			continue
		}
		v := m.Value(a)
		if len(v) < minLen || len(v) > maxLen || len(v)%4 != 0 {
			return ErrBadAttr
		}
		// The HMAC covers the header with its length ending at this
		// attribute, and every attribute before it.
		start := int(a.off) - 4
		var header [headerSize]byte
		copy(header[:], m.Raw)
		binary.BigEndian.PutUint16(header[2:], uint16(start+4+len(v)-headerSize))
		mac := hmac.New(h, key)
		mac.Write(header[:])
		mac.Write(m.Raw[headerSize:start])
		var sum [sha256.Size]byte
		if !hmac.Equal(mac.Sum(sum[:0])[:len(v)], v) {
			return ErrIntegrity
		}
		return nil
	}
	return ErrNoAttr
}

// AddFingerprint appends FINGERPRINT, the CRC-32 of the message so far,
// which lets a message be told from another protocol's on a shared port. It
// is always the last attribute.
func (m *Message) AddFingerprint() {
	off := len(m.Raw)
	m.Raw = append(m.grow(AttrFingerprint, 4), 0, 0, 0, 0)
	m.pad()
	binary.BigEndian.PutUint32(m.Raw[off+4:], crc32.ChecksumIEEE(m.Raw[:off])^fingerprintXOR)
}

// CheckFingerprint checks m's FINGERPRINT. It returns ErrNoAttr if m has
// none.
func (m *Message) CheckFingerprint() error {
	n := len(m.Attrs)
	if n == 0 || m.Attrs[n-1].Type != AttrFingerprint {
		return ErrNoAttr
	}
	a := m.Attrs[n-1]
	// Parse has made sure it is the last attribute, so the header's length
	// already ends with it.
	if crc32.ChecksumIEEE(m.Raw[:a.off-4])^fingerprintXOR != binary.BigEndian.Uint32(m.Value(a)) {
		return ErrFingerprint
	}
	return nil
}
