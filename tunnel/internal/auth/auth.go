// Package auth is the handshake a node or proxy opens each link to a hub
// with: both sides prove they know the password, without sending it, and
// derive the keys that seal the link's frames from then on (package
// bodycrypt).
//
// # The handshake
//
//  1. The peer calls proto.MethodHello, unsealed, with a Hello: the
//     protocol's version, its type and name, and 32 random bytes.
//  2. The hub answers, unsealed, with a HelloRsp: 32 random bytes of its
//     own, and its salt. From here on every frame either way is sealed.
//  3. The peer calls proto.MethodRegister, sealed, with the MAC of the
//     transcript; the hub answers, sealed, with its own proof of it.
//
// The password's key is PBKDF2-HMAC-SHA256 of it under the hub's salt,
// 16 random bytes the hub picks when it starts, with Iterations rounds; the
// body password's, when one is set, is derived alike, and both together
// are the secret. The transcript is the version, the peer's type and name,
// both sides' random bytes and the salt; the session's secret is HKDF of
// the secret, salted with both sides' random bytes, and the MACs and the
// keys of each direction are HKDF of that, each for its own purpose, with
// the transcript as their info. So:
//
//   - a peer proves the password to the hub, and the hub proves it to the
//     peer, for this link alone: a fake hub, or one between the two, that
//     does not know the password cannot open the peer's frames, nor seal
//     one the peer opens;
//   - a recorded handshake or frame is worth nothing on another link, as
//     both sides' random bytes differ, and a frame sent back to its sender
//     does not open, as each direction has its own key;
//   - a guess of the password costs whoever overheard a handshake
//     Iterations rounds of PBKDF2, under a salt no other hub has.
package auth

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"

	"github.com/lesismal/fit/tunnel/internal/bodycrypt"
)

// Version is the version of the handshake, and so of the protocol: a hub
// refuses a peer of another.
const Version = 2

// The sizes of the random bytes each side sends, and of the hub's salt.
const (
	NonceLen = 32
	SaltLen  = 16
)

// Iterations is the rounds of PBKDF2 a password's key takes. Tests lower
// it before anything derives a key; nothing else changes it, as the two
// sides of a link must agree on it.
var Iterations = 600_000

// Key is a password's key under a hub's salt.
type Key []byte

// Derive is the key of password under salt.
func Derive(password string, salt []byte) (Key, error) {
	if password == "" {
		return nil, errors.New("auth: empty password")
	}
	return pbkdf2.Key(sha256.New, password, salt, Iterations, 32)
}

// Random is n random bytes.
func Random(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// Hello opens a link's handshake.
type Hello struct {
	Version int    `json:"v"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Nonce   []byte `json:"nonce"`
}

// HelloRsp answers a Hello.
type HelloRsp struct {
	Version int    `json:"v"`
	Nonce   []byte `json:"nonce"`
	Salt    []byte `json:"salt"`
}

// Handshake is what both sides of a link compute of its handshake.
type Handshake struct {
	transcript []byte
	secret     []byte
}

// NewHandshake is the handshake of hello and rsp, with key the password's
// and body the body password's, nil without one.
func NewHandshake(hello *Hello, rsp *HelloRsp, key, body Key) (*Handshake, error) {
	switch {
	case hello.Version != Version || rsp.Version != Version:
		return nil, errors.New("auth: the other side speaks another version of the tunnel's protocol; update both to the same fit")
	case len(hello.Nonce) != NonceLen || len(rsp.Nonce) != NonceLen || len(rsp.Salt) != SaltLen:
		return nil, errors.New("auth: a handshake with random bytes of the wrong size")
	case len(key) == 0:
		return nil, errors.New("auth: no key")
	}
	var t []byte
	t = append(t, "fit-tunnel"...)
	t = binary.BigEndian.AppendUint32(t, Version)
	for _, s := range [][]byte{[]byte(hello.Type), []byte(hello.Name), hello.Nonce, rsp.Nonce, rsp.Salt} {
		t = binary.BigEndian.AppendUint32(t, uint32(len(s)))
		t = append(t, s...)
	}
	ikm := append(append([]byte(nil), key...), body...)
	salt := append(append([]byte(nil), hello.Nonce...), rsp.Nonce...)
	secret, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	return &Handshake{transcript: t, secret: secret}, nil
}

func (h *Handshake) expand(purpose string, n int) []byte {
	b, err := hkdf.Expand(sha256.New, h.secret, purpose+"\x00"+string(h.transcript), n)
	if err != nil {
		// Only a length past HKDF's bound fails, which n never is.
		panic(err)
	}
	return b
}

func (h *Handshake) mac(purpose string) []byte {
	m := hmac.New(sha256.New, h.expand(purpose, 32))
	m.Write(h.transcript)
	return m.Sum(nil)
}

// PeerMAC is what the peer proves the password with.
func (h *Handshake) PeerMAC() []byte { return h.mac("fit-tunnel peer mac") }

// HubProof is what the hub proves the password with.
func (h *Handshake) HubProof() []byte { return h.mac("fit-tunnel hub mac") }

// Equal reports, in constant time, whether two MACs are the same.
func Equal(a, b []byte) bool { return hmac.Equal(a, b) }

// Session is the hub's side of the link's keys, when hub is true, or
// the peer's.
func (h *Handshake) Session(hub bool) (*bodycrypt.Session, error) {
	up := h.expand("fit-tunnel peer to hub", bodycrypt.KeySize)
	down := h.expand("fit-tunnel hub to peer", bodycrypt.KeySize)
	if hub {
		return bodycrypt.NewSession(down, up)
	}
	return bodycrypt.NewSession(up, down)
}

// Answer is the hub's answer to hello, with salt its salt and key and body
// the keys of the peer's passwords under it: the HelloRsp, the handshake,
// and the hub's side of the link's keys.
func Answer(hello *Hello, salt []byte, key, body Key) (*HelloRsp, *Handshake, *bodycrypt.Session, error) {
	rsp := &HelloRsp{Version: Version, Nonce: Random(NonceLen), Salt: salt}
	hs, err := NewHandshake(hello, rsp, key, body)
	if err != nil {
		return nil, nil, nil, err
	}
	sess, err := hs.Session(true)
	if err != nil {
		return nil, nil, nil, err
	}
	return rsp, hs, sess, nil
}
