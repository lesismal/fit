package mongowire

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// SCRAM-SHA-256 (RFC 5802, RFC 7677) as MongoDB runs it, over saslStart
// and saslContinue. The password is used as given: SASLprep is not
// applied, which changes nothing for printable ASCII.

const minIterations = 4096

type scram struct {
	user, password, nonce string
	clientFirstBare       string
	serverSignature       []byte
	cache                 *saltCache
}

func newScram(user, password string, cache *saltCache) *scram {
	var b [24]byte
	_, _ = rand.Read(b[:]) // crypto/rand does not fail
	return &scram{user: user, password: password, nonce: base64.StdEncoding.EncodeToString(b[:]), cache: cache}
}

// first is the client's first message.
func (s *scram) first() string {
	u := strings.NewReplacer("=", "=3D", ",", "=2C").Replace(s.user)
	s.clientFirstBare = "n=" + u + ",r=" + s.nonce
	return "n,," + s.clientFirstBare
}

// final reads the server's first message and returns the client's final.
func (s *scram) final(serverFirst string) (string, error) {
	attrs := scramAttrs(serverFirst)
	if e, ok := attrs["e"]; ok {
		return "", fmt.Errorf("mongowire: SCRAM: the server says %s", e)
	}
	nonce, salt64, iter64 := attrs["r"], attrs["s"], attrs["i"]
	if !strings.HasPrefix(nonce, s.nonce) || len(nonce) == len(s.nonce) {
		return "", errors.New("mongowire: SCRAM: the server's nonce does not extend ours")
	}
	salt, err := base64.StdEncoding.DecodeString(salt64)
	if err != nil || len(salt) == 0 {
		return "", errors.New("mongowire: SCRAM: a bad salt")
	}
	iter, err := strconv.Atoi(iter64)
	if err != nil || iter < minIterations || iter > 10_000_000 {
		return "", fmt.Errorf("mongowire: SCRAM: %q iterations", iter64)
	}
	salted, err := s.cache.salted(s.password, salt, iter)
	if err != nil {
		return "", err
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	withoutProof := "c=biws,r=" + nonce
	authMessage := s.clientFirstBare + "," + serverFirst + "," + withoutProof
	proof := hmacSHA256(storedKey[:], authMessage)
	for i := range proof {
		proof[i] ^= clientKey[i]
	}
	s.serverSignature = hmacSHA256(hmacSHA256(salted, "Server Key"), authMessage)
	return withoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof), nil
}

// verify checks the server's final message.
func (s *scram) verify(serverFinal string) error {
	attrs := scramAttrs(serverFinal)
	if e, ok := attrs["e"]; ok {
		return fmt.Errorf("mongowire: SCRAM: the server says %s", e)
	}
	v, err := base64.StdEncoding.DecodeString(attrs["v"])
	if err != nil || subtle.ConstantTimeCompare(v, s.serverSignature) != 1 {
		return errors.New("mongowire: SCRAM: the server's signature does not match: it does not know the password")
	}
	return nil
}

func scramAttrs(msg string) map[string]string {
	m := map[string]string{}
	for _, p := range strings.Split(msg, ",") {
		if len(p) >= 2 && p[1] == '=' {
			m[p[:1]] = p[2:]
		}
	}
	return m
}

func hmacSHA256(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

// saltCache keeps salted passwords, so that a pool's connections do not
// each run PBKDF2's thousands of rounds; it holds a few, keyed by a hash.
type saltCache struct {
	mu sync.Mutex
	m  map[[32]byte][]byte
}

func (c *saltCache) salted(password string, salt []byte, iter int) ([]byte, error) {
	if c == nil {
		return pbkdf2.Key(sha256.New, password, salt, iter, sha256.Size)
	}
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00%d\x00%s\x00", iter, len(salt), salt)
	h.Write([]byte(password))
	var k [32]byte
	h.Sum(k[:0])
	c.mu.Lock()
	v, ok := c.m[k]
	c.mu.Unlock()
	if ok {
		return v, nil
	}
	v, err := pbkdf2.Key(sha256.New, password, salt, iter, sha256.Size)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.m == nil || len(c.m) >= 8 {
		c.m = map[[32]byte][]byte{}
	}
	c.m[k] = v
	c.mu.Unlock()
	return v, nil
}

// scramAuth authenticates c as user against the database source.
func scramAuth(ctx context.Context, c *Conn, source, user, password string, cache *saltCache) error {
	s := newScram(user, password, cache)
	reply, err := c.Run(ctx, source, D{
		{"saslStart", int32(1)}, {"mechanism", "SCRAM-SHA-256"},
		{"payload", []byte(s.first())}, {"autoAuthorize", int32(1)},
		{"options", D{{"skipEmptyExchange", true}}},
	})
	if err != nil {
		return fmt.Errorf("mongowire: authenticating %s: %w", user, err)
	}
	conv, _ := reply.Lookup("conversationId")
	payload := func(r Raw) string {
		v, _ := r.Lookup("payload")
		b, _ := v.Binary()
		return string(b.Data)
	}
	final, err := s.final(payload(reply))
	if err != nil {
		return err
	}
	reply, err = c.Run(ctx, source, D{{"saslContinue", int32(1)}, {"conversationId", conv}, {"payload", []byte(final)}})
	if err != nil {
		return fmt.Errorf("mongowire: authenticating %s: %w", user, err)
	}
	if err := s.verify(payload(reply)); err != nil {
		return err
	}
	for i := 0; ; i++ {
		if done, _ := reply.Lookup("done"); done.Truthy() {
			return nil
		}
		if i == 2 {
			return errors.New("mongowire: SCRAM: the server does not end the conversation")
		}
		reply, err = c.Run(ctx, source, D{{"saslContinue", int32(1)}, {"conversationId", conv}, {"payload", []byte{}}})
		if err != nil {
			return fmt.Errorf("mongowire: authenticating %s: %w", user, err)
		}
	}
}
