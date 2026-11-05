// Package bodycrypt encrypts the bodies of link frames with AES-256-GCM
// under keys derived from a password both sides are configured with.
//
// The password's PBKDF2, the Coder, is derived once. Each link then opens
// with both sides sending a hello of RandomSize fresh random bytes, and a
// Session of its own: each direction has a key of its own, HKDF-SHA256 of
// the PBKDF2 under the dialer's random bytes followed by the listener's,
// with the direction, dialer to listener or listener to dialer, in the
// HKDF info; it numbers its frames from zero, the number being the GCM
// nonce. A sealed body is the body sealed, its 16-byte tag last; no nonce
// travels. A frame that does not open under the next number - forged,
// replayed, reordered, reflected back to its sender, carried over from
// another link in either direction, or sealed under another password -
// fails, and package link closes the link for it. That the direction is
// named, and not only the order of the random bytes, is what keeps a frame
// a dialer sealed from opening at another dialer whose link an attacker on
// the path gave the same two random bytes the other way round.
package bodycrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

const (
	salt = "fit-tunnel-link-body"
	iter = 10000
	// The HKDF info of each direction's key.
	infoToListener = "fit-tunnel-link v3 dialer to listener"
	infoToDialer   = "fit-tunnel-link v3 listener to dialer"
	nonceSize      = 12
	tagSize        = 16
)

// RandomSize is how many random bytes each side's hello carries, and
// Overhead how many more bytes a sealed body has than the plain one.
const (
	RandomSize = 16
	Overhead   = tagSize
)

var (
	// ErrOpen is the error of a body that does not open.
	ErrOpen = errors.New("bodycrypt: a frame failed to decrypt: forged, replayed, reordered, or sealed under another password")
	// ErrReflected is the error of a hello whose random bytes are the
	// side's own: its frames sent back to it would open.
	ErrReflected = errors.New("bodycrypt: the peer's hello is this side's own, sent back")
)

// Coder is a password's PBKDF2, from which every link's keys derive, for
// the side of the links that listened (New's) or dialed (Dialer's).
type Coder struct {
	secret []byte
	dialer bool
}

// Dialer is the Coder of the same password for the side that dialed the
// links. Both sides of a link must not be of the same side: their keys
// would not match, and the link fails at its first sealed frame.
func (cd *Coder) Dialer() *Coder { return &Coder{secret: cd.secret, dialer: true} }

// New is the Coder of password.
func New(password string) (*Coder, error) {
	if password == "" {
		return nil, errors.New("bodycrypt: empty password")
	}
	// SHA-256, a 20-byte salt and a 32-byte key are valid even in FIPS
	// 140-only mode: the derivation cannot fail.
	secret, _ := pbkdf2.Key(sha256.New, password, []byte(salt), iter, 32)
	return &Coder{secret: secret}, nil
}

// Random is the fresh random bytes of one side's hello.
func Random() []byte {
	b := make([]byte, RandomSize)
	_, _ = rand.Read(b)
	return b
}

// Session is the keys and counters of one link, as one side sees it. Its
// Seals must not run at once, nor its Opens; a Seal and an Open may.
type Session struct {
	send, recv   cipher.AEAD
	sendN, recvN uint64
	sendNonce    [nonceSize]byte
	recvNonce    [nonceSize]byte
}

// Session is the session of a link on which this side's hello carried own
// and the peer's carried peer.
func (cd *Coder) Session(own, peer []byte) (*Session, error) {
	if len(own) != RandomSize || len(peer) != RandomSize {
		return nil, errors.New("bodycrypt: a hello of the wrong length")
	}
	if bytes.Equal(own, peer) {
		return nil, ErrReflected
	}
	dialer, listener := peer, own
	sendInfo, recvInfo := infoToDialer, infoToListener
	if cd.dialer {
		dialer, listener = own, peer
		sendInfo, recvInfo = infoToListener, infoToDialer
	}
	send, err := cd.aead(dialer, listener, sendInfo)
	if err != nil {
		return nil, err
	}
	// The same cipher under another key: it is made where send's was.
	recv, _ := cd.aead(dialer, listener, recvInfo)
	return &Session{send: send, recv: recv}, nil
}

// aead is the cipher of one direction, info, of the link on which the
// dialer's hello carried dialer and the listener's listener. Only GCM
// fails, in FIPS 140-only mode, which refuses nonces it did not make;
// HKDF-SHA256 of a 32-byte secret to a 32-byte AES-256 key cannot.
func (cd *Coder) aead(dialer, listener []byte, info string) (cipher.AEAD, error) {
	s := make([]byte, 0, 2*RandomSize)
	s = append(append(s, dialer...), listener...)
	key, _ := hkdf.Key(sha256.New, cd.secret, s, info, 32)
	block, _ := aes.NewCipher(key)
	return cipher.NewGCM(block)
}

// SealInPlace seals body, the next frame's, with header as the additional
// data. body must have room for Overhead bytes more; the sealed body is
// returned in body's array.
func (s *Session) SealInPlace(body, header []byte) []byte {
	binary.BigEndian.PutUint64(s.sendNonce[4:], s.sendN)
	s.sendN++
	return s.send.Seal(body[:0], s.sendNonce[:], body, header)
}

// OpenInPlace opens body, which must be the next frame's sealed body, with
// header as the additional data, and returns the plain body in body's
// array.
func (s *Session) OpenInPlace(body, header []byte) ([]byte, error) {
	if len(body) < Overhead {
		return nil, ErrOpen
	}
	binary.BigEndian.PutUint64(s.recvNonce[4:], s.recvN)
	plain, err := s.recv.Open(body[:0], s.recvNonce[:], body, header)
	if err != nil {
		return nil, ErrOpen
	}
	s.recvN++
	return plain, nil
}
