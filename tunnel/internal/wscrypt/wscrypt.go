// Package wscrypt is the encryption of the WebSocket a tunnel server's
// console pages can be reached through, fibhub's: every binary message is sealed with
// AES-256-GCM under a key both sides derive from the password they share.
//
// The server opens the session with a message of 16 random bytes, and the
// client answers with 16 of its own. Each direction then has a key of its
// own, HKDF-SHA256 of the password's PBKDF2 under both sets of bytes, and
// numbers its messages from zero, the number being the GCM nonce. A message
// that does not open under the next number - forged, replayed, reordered, or
// sealed under another password - ends the session.
package wscrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"
)

// HelloLen is the length of the random bytes each side opens with.
const HelloLen = 16

const (
	pbkdf2Salt = "fibhub-ws"
	pbkdf2Iter = 10000
)

// Secret is a password's PBKDF2, derived once for every session: a role
// derives it when its config is applied, not as a connection opens.
type Secret []byte

// NewSecret is the secret of password.
func NewSecret(password string) (Secret, error) {
	b, err := pbkdf2.Key(sha256.New, password, []byte(pbkdf2Salt), pbkdf2Iter, 32)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Hello is the random bytes one side opens with.
func Hello() []byte {
	b := make([]byte, HelloLen)
	_, _ = rand.Read(b)
	return b
}

// Session seals what one side sends and opens what it receives.
type Session struct {
	sendMu sync.Mutex
	send   cipher.AEAD
	sendN  uint64
	recvMu sync.Mutex
	recv   cipher.AEAD
	recvN  uint64
}

// NewSession is the session of the side that is the server, when server is
// true, once serverHello and clientHello are exchanged.
func NewSession(secret Secret, serverHello, clientHello []byte, server bool) (*Session, error) {
	if len(serverHello) != HelloLen || len(clientHello) != HelloLen {
		return nil, errors.New("wscrypt: bad hello")
	}
	salt := append(append([]byte(nil), serverHello...), clientHello...)
	c2s, err := aead(secret, salt, "fibhub-ws c2s")
	if err != nil {
		return nil, err
	}
	s2c, err := aead(secret, salt, "fibhub-ws s2c")
	if err != nil {
		return nil, err
	}
	if server {
		return &Session{send: s2c, recv: c2s}, nil
	}
	return &Session{send: c2s, recv: s2c}, nil
}

func aead(secret Secret, salt []byte, info string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, secret, salt, info, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func nonce(n uint64) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint64(b[4:], n)
	return b
}

// Seal is the message carrying plaintext.
func (s *Session) Seal(plaintext []byte) []byte {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	out := s.send.Seal(nil, nonce(s.sendN), plaintext, nil)
	s.sendN++
	return out
}

// ErrOpen is the error of a message that does not open.
var ErrOpen = errors.New("wscrypt: message failed to decrypt")

// Open is the plaintext of the next message the other side sent.
func (s *Session) Open(message []byte) ([]byte, error) {
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	out, err := s.recv.Open(nil, nonce(s.recvN), message, nil)
	if err != nil {
		return nil, ErrOpen
	}
	s.recvN++
	return out, nil
}
