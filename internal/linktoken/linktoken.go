// Package linktoken is the tokens a listener issues for the links of the
// tunnel and of the LLM mesh, in place of a password every member shares
// (decision 035): the tokens' secrets and what derives from them, the
// handshake's proofs, the listener's registry of its tokens and the links
// that use them, and the management of tokens that the pages, the Admin API
// and the CLI share.
//
// # Keys
//
// A token is LinkTokenPrefix, its ID (16 hex digits) and a 32-byte random
// secret. From the secret:
//
//	client = HMAC-SHA256(secret, "fit link token client key")
//	stored = SHA-256(client)                 the token's Hash, all the listener keeps
//	server = HKDF-SHA256(stored, info "fit link token server key")
//	body   = HKDF-SHA256(stored, info "fit link token body key")
//
// # Handshake
//
// The dialer and the listener each pick a fresh 32-byte nonce. The
// transcript is a label naming the protocol, the token's ID, the dialer's
// nonce, the listener's nonce and what else the protocol binds (a tunnel
// peer's type and name), each after its length. The dialer proves the token
// with client XOR HMAC-SHA256(stored, "dialer to listener" | transcript):
// the listener recovers client from it and checks its SHA-256 against
// stored, so that what the listener keeps is not enough to link. The
// listener proves the token back with HMAC-SHA256(server, "listener to
// dialer" | transcript | its answer). The proofs name their direction and
// both nonces, so a proof sent back to its sender, replayed on another link
// or answered to another nonce does not check. The secret is 256 bits, so an
// observed proof leaks nothing worth guessing.
package linktoken

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/lesismal/fit/config"
)

// Sizes of a token's parts and of the handshake's nonces.
const (
	SecretSize = 32
	NonceSize  = 32
	ProofSize  = sha256.Size
)

const (
	labelClient   = "fit link token client key"
	labelServer   = "fit link token server key"
	labelBody     = "fit link token body key"
	labelDialer   = "fit link token v1 dialer to listener"
	labelListener = "fit link token v1 listener to dialer"
)

// ErrProof is the error of a proof that does not check.
var ErrProof = errors.New("linktoken: the proof does not check")

// Secret is a token as its holder has it.
type Secret struct {
	ID     string
	secret []byte
}

// New makes a token: a random ID and a random secret.
func New() Secret {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	s := make([]byte, SecretSize)
	_, _ = rand.Read(s)
	return Secret{ID: hex.EncodeToString(id), secret: s}
}

// Parse reads a token, as String writes it.
func Parse(tok string) (Secret, error) {
	if msg := config.CheckLinkTokenString(tok); msg != "" {
		return Secret{}, errors.New(msg)
	}
	id, enc, _ := strings.Cut(strings.TrimPrefix(tok, config.LinkTokenPrefix), ".")
	s, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(s) < 16 {
		return Secret{}, errors.New("linktoken: the token's secret is not 16 or more bytes of base64url")
	}
	return Secret{ID: id, secret: s}, nil
}

// String is the token, which its holder configures.
func (s Secret) String() string {
	return config.LinkTokenPrefix + s.ID + "." + base64.RawURLEncoding.EncodeToString(s.secret)
}

// StringAs is the token as String writes it, with prefix in place of
// config.LinkTokenPrefix: for the tokens of other uses, as a cluster's
// enrollment tokens.
func (s Secret) StringAs(prefix string) string {
	return prefix + s.ID + "." + base64.RawURLEncoding.EncodeToString(s.secret)
}

// ParseAs reads a token StringAs wrote with prefix.
func ParseAs(tok, prefix string) (Secret, error) {
	rest, ok := strings.CutPrefix(tok, prefix)
	id, enc, dot := strings.Cut(rest, ".")
	if !ok || !dot || !config.ValidLinkTokenID(id) {
		return Secret{}, errors.New("linktoken: not a token of " + prefix + "<16 hex digits>.<secret>")
	}
	s, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(s) < 16 || len(s) > 64 {
		return Secret{}, errors.New("linktoken: the token's secret is not 16 to 64 bytes of base64url")
	}
	return Secret{ID: id, secret: s}, nil
}

func (s Secret) client() []byte {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(labelClient))
	return m.Sum(nil)
}

// Stored is what the listener keeps of the token: the SHA-256 of its client
// key.
func (s Secret) Stored() []byte {
	h := sha256.Sum256(s.client())
	return h[:]
}

// Hash is Stored in hex, as a config's token has it.
func (s Secret) Hash() string { return hex.EncodeToString(s.Stored()) }

// StoredOf decodes a config token's Hash.
func StoredOf(hash string) ([]byte, error) {
	b, err := hex.DecodeString(hash)
	if err != nil || len(b) != sha256.Size {
		return nil, errors.New("linktoken: a hash is 64 hex digits")
	}
	return b, nil
}

func derive(stored []byte, info string) []byte {
	// HKDF-SHA256 of a 32-byte key to 32 bytes cannot fail.
	k, _ := hkdf.Key(sha256.New, stored, nil, info, 32)
	return k
}

// BodyKey is the key a link's bodies are sealed under derives from, for
// the token whose stored key is stored (see package bodycrypt).
func BodyKey(stored []byte) []byte { return derive(stored, labelBody) }

// Transcript is what a handshake's proofs bind: Label names the protocol,
// Token the token's ID, Dialer and Listener the two sides' nonces, and
// Extra what else the protocol binds.
type Transcript struct {
	Label            string
	Token            string
	Dialer, Listener []byte
	Extra            []string
}

// bytes is the transcript, each part after its length, so that no two
// transcripts' parts run together into the same bytes.
func (t *Transcript) bytes(direction string) []byte {
	var b []byte
	put := func(p []byte) {
		b = binary.BigEndian.AppendUint32(b, uint32(len(p)))
		b = append(b, p...)
	}
	put([]byte(direction))
	put([]byte(t.Label))
	put([]byte(t.Token))
	put(t.Dialer)
	put(t.Listener)
	for _, e := range t.Extra {
		put([]byte(e))
	}
	return b
}

func mac(key []byte, parts ...[]byte) []byte {
	m := hmac.New(sha256.New, key)
	for _, p := range parts {
		m.Write(p)
	}
	return m.Sum(nil)
}

// Prove is the dialer's proof of the token over t.
func (s Secret) Prove(t Transcript) []byte {
	sig := mac(s.Stored(), t.bytes(labelDialer))
	c := s.client()
	out := make([]byte, ProofSize)
	subtle.XORBytes(out, c, sig)
	return out
}

// Verify checks a dialer's proof over t of the token whose stored key is
// stored.
func Verify(stored []byte, t Transcript, proof []byte) error {
	if len(proof) != ProofSize || len(stored) != sha256.Size {
		return ErrProof
	}
	sig := mac(stored, t.bytes(labelDialer))
	c := make([]byte, ProofSize)
	subtle.XORBytes(c, proof, sig)
	got := sha256.Sum256(c)
	if subtle.ConstantTimeCompare(got[:], stored) != 1 {
		return ErrProof
	}
	return nil
}

// ListenerProof is the listener's proof, over t and its answer, of the
// token whose stored key is stored.
func ListenerProof(stored []byte, t Transcript, answer ...string) []byte {
	parts := [][]byte{t.bytes(labelListener)}
	for _, a := range answer {
		parts = append(parts, binary.BigEndian.AppendUint32(nil, uint32(len(a))), []byte(a))
	}
	return mac(derive(stored, labelServer), parts...)
}

// VerifyListener checks the listener's proof over t and its answer.
func (s Secret) VerifyListener(t Transcript, proof []byte, answer ...string) error {
	if !hmac.Equal(proof, ListenerProof(s.Stored(), t, answer...)) {
		return ErrProof
	}
	return nil
}

// Nonce is a fresh nonce.
func Nonce() []byte {
	b := make([]byte, NonceSize)
	_, _ = rand.Read(b)
	return b
}
