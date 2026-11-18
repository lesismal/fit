// Package bodycrypt seals the bodies of a link's frames with AES-256-GCM,
// under keys of the link's own, one for each direction, that its handshake
// (package auth) derives from the password and random bytes of both sides.
//
// Each direction numbers its frames from zero, and a frame's number is its
// GCM nonce, which is not sent: a sealed body is the body sealed, its
// 16-byte tag last. The frame's header is the sealing's additional data, so
// it cannot be changed either. A frame that does not open under the next
// number - forged, replayed, reordered, reflected back to its sender, or
// sealed under another link's keys - does not open at all, and package link
// drops the connection.
package bodycrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"math"
)

// Overhead is how many more bytes a sealed body has than the plain one.
const Overhead = 16

// KeySize is the size of a direction's key.
const KeySize = 32

// ErrOpen is the error of a body that does not open.
var ErrOpen = errors.New("bodycrypt: a frame failed to decrypt; are the passwords the same?")

// errExhausted is the error of a direction that sealed or opened as many
// frames as its nonces number.
var errExhausted = errors.New("bodycrypt: the link's frame counter is exhausted")

// Direction seals, or opens, the frames of one direction of a link, in
// order. It is not safe for concurrent use: package link seals under its
// send lock, and opens where the link is read.
type Direction struct {
	aead  cipher.AEAD
	n     uint64
	nonce [12]byte
}

// NewDirection is a direction under key, KeySize bytes.
func NewDirection(key []byte) (*Direction, error) {
	if len(key) != KeySize {
		return nil, errors.New("bodycrypt: a key of the wrong size")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Direction{aead: aead}, nil
}

// next is the nonce of the next frame.
func (d *Direction) next() ([]byte, error) {
	if d.n == math.MaxUint64 {
		return nil, errExhausted
	}
	binary.BigEndian.PutUint64(d.nonce[4:], d.n)
	d.n++
	return d.nonce[:], nil
}

// SealInPlace seals the plain body in buf, with header as the additional
// data. buf must have room for Overhead bytes more; the sealed body is
// returned in buf's array.
func (d *Direction) SealInPlace(buf, header []byte) ([]byte, error) {
	nonce, err := d.next()
	if err != nil {
		return nil, err
	}
	return d.aead.Seal(buf[:0], nonce, buf, header), nil
}

// OpenInPlace opens body, the next sealed body of the direction, with
// header as the additional data, and returns the plain body in body's
// array.
func (d *Direction) OpenInPlace(body, header []byte) ([]byte, error) {
	if len(body) < Overhead {
		return nil, ErrOpen
	}
	nonce, err := d.next()
	if err != nil {
		return nil, err
	}
	plain, err := d.aead.Open(body[:0], nonce, body, header)
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}

// Session is both directions of a link: what it sends, and what it
// receives.
type Session struct {
	Send, Recv *Direction
}

// NewSession is the session that seals with sendKey and opens with
// recvKey.
func NewSession(sendKey, recvKey []byte) (*Session, error) {
	send, err := NewDirection(sendKey)
	if err != nil {
		return nil, err
	}
	recv, err := NewDirection(recvKey)
	if err != nil {
		return nil, err
	}
	return &Session{Send: send, Recv: recv}, nil
}
