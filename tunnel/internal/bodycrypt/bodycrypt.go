// Package bodycrypt encrypts the bodies of link frames with AES-256-GCM
// under a key derived from a password both sides are configured with, as
// fibhub does.
//
// A sealed body is a random 12-byte nonce followed by the body sealed, its
// 16-byte tag last. The frame's header is the sealing's additional data, so
// it cannot be changed either. Package link marks a frame whose body is
// sealed, and drops the connection of a frame that arrives unsealed or does
// not open: a peer configured with another password, or none, gets nowhere.
package bodycrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

const (
	salt      = "fibhub-link-body"
	iter      = 10000
	nonceSize = 12
	tagSize   = 16
)

// NonceSize is how many bytes come before a sealed body, and Overhead how
// many more a sealed body has than the plain one.
const (
	NonceSize = nonceSize
	Overhead  = nonceSize + tagSize
)

// ErrOpen is the error of a body that does not open.
var ErrOpen = errors.New("bodycrypt: message failed to decrypt; are the passwords the same?")

// Coder seals and opens bodies.
type Coder struct {
	aead cipher.AEAD
}

// New is a Coder whose key is derived from password.
func New(password string) (*Coder, error) {
	if password == "" {
		return nil, errors.New("bodycrypt: empty password")
	}
	key, err := pbkdf2.Key(sha256.New, password, []byte(salt), iter, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Coder{aead: aead}, nil
}

// SealInPlace seals the body in buf, whose first NonceSize bytes are room
// for the nonce and the rest the plain body, with header as the additional
// data. buf must have room for Overhead-NonceSize bytes more; the sealed
// body is returned in buf's array.
func (cd *Coder) SealInPlace(buf, header []byte) []byte {
	nonce := buf[:nonceSize]
	_, _ = rand.Read(nonce)
	sealed := cd.aead.Seal(buf[nonceSize:nonceSize], nonce, buf[nonceSize:], header)
	return buf[:nonceSize+len(sealed)]
}

// OpenInPlace opens body, a sealed body, with header as the additional
// data, and returns the plain body in body's array.
func (cd *Coder) OpenInPlace(body, header []byte) ([]byte, error) {
	if len(body) < Overhead {
		return nil, ErrOpen
	}
	plain, err := cd.aead.Open(body[nonceSize:nonceSize], body[:nonceSize], body[nonceSize:], header)
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}
