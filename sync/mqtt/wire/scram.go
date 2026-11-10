package mqttwire

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
)

// Authenticator is a method of MQTT 5's enhanced authentication: CONNECT
// names it and carries its first data, the server and the client trade
// AUTH packets, and the server's success (CONNACK, or AUTH when
// re-authenticating) carries its last.
type Authenticator interface {
	// Method is the authentication method's name, as SCRAM-SHA-256.
	Method() string
	// Start begins an exchange: its state, and the data CONNECT, or the
	// AUTH that re-authenticates, carries.
	Start() (AuthExchange, []byte, error)
}

// AuthExchange is one exchange of an Authenticator.
type AuthExchange interface {
	// Next answers the data of the server's AUTH, continue
	// authentication, with the data of the client's.
	Next(data []byte) ([]byte, error)
	// Done takes the data of the server's success, and checks it.
	Done(data []byte) error
}

// The SCRAM methods (RFC 5802, 7677) NewSCRAM takes.
const (
	SCRAMSHA1   = "SCRAM-SHA-1"
	SCRAMSHA256 = "SCRAM-SHA-256"
	SCRAMSHA512 = "SCRAM-SHA-512"
)

// maxSCRAMIterations bounds the iterations a server may ask for, so that a
// hostile one cannot hold the client for long.
const maxSCRAMIterations = 1 << 20

// errSCRAM says a SCRAM exchange went wrong.
var errSCRAM = errors.New("mqtt: SCRAM")

type scram struct {
	method, user, password string
	hash                   func() hash.Hash
	nonce                  func() string
}

// NewSCRAM is the SCRAM method of SCRAMSHA1, SCRAMSHA256 or SCRAMSHA512
// for user and password, without channel binding. The password is used as
// it is, not normalized by SASLprep: a password of ASCII is the same
// either way.
func NewSCRAM(method, user, password string) (Authenticator, error) {
	s := &scram{method: method, user: user, password: password, nonce: scramNonce}
	switch method {
	case SCRAMSHA1:
		s.hash = sha1.New
	case SCRAMSHA256:
		s.hash = sha256.New
	case SCRAMSHA512:
		s.hash = sha512.New
	default:
		return nil, fmt.Errorf("mqtt: %q is not a SCRAM method: SCRAM-SHA-1, SCRAM-SHA-256 or SCRAM-SHA-512", method)
	}
	if user == "" {
		return nil, errors.New("mqtt: SCRAM needs a user name")
	}
	return s, nil
}

func scramNonce() string {
	var b [18]byte
	_, _ = rand.Read(b[:])
	return base64.RawStdEncoding.EncodeToString(b[:])
}

func (s *scram) Method() string { return s.method }

func (s *scram) Start() (AuthExchange, []byte, error) {
	name := strings.NewReplacer("=", "=3D", ",", "=2C").Replace(s.user)
	x := &scramExchange{s: s, nonce: s.nonce()}
	x.first = "n=" + name + ",r=" + x.nonce
	return x, []byte("n,," + x.first), nil
}

type scramExchange struct {
	s            *scram
	nonce, first string
	// serverSig is the server's signature to expect, once the client's
	// final message is sent.
	serverSig []byte
}

func (x *scramExchange) hmac(key []byte, msg string) []byte {
	h := hmac.New(x.s.hash, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

// Next answers the server's first message with the client's final one.
func (x *scramExchange) Next(data []byte) ([]byte, error) {
	if x.serverSig != nil {
		return nil, fmt.Errorf("%w: the server continued after the client's final message", errSCRAM)
	}
	msg := string(data)
	attrs := map[byte]string{}
	for _, a := range strings.Split(msg, ",") {
		if len(a) < 2 || a[1] != '=' {
			return nil, fmt.Errorf("%w: a malformed server-first-message", errSCRAM)
		}
		if _, ok := attrs[a[0]]; !ok {
			attrs[a[0]] = a[2:]
		}
	}
	if e, ok := attrs['e']; ok {
		return nil, fmt.Errorf("%w: the server refused: %s", errSCRAM, e)
	}
	if _, ok := attrs['m']; ok {
		return nil, fmt.Errorf("%w: the server asks for an extension", errSCRAM)
	}
	nonce := attrs['r']
	salt, err := base64.StdEncoding.DecodeString(attrs['s'])
	iter, ierr := strconv.Atoi(attrs['i'])
	switch {
	case !strings.HasPrefix(nonce, x.nonce) || len(nonce) == len(x.nonce):
		return nil, fmt.Errorf("%w: the server's nonce does not extend the client's", errSCRAM)
	case err != nil || len(salt) == 0:
		return nil, fmt.Errorf("%w: no salt, or not base64", errSCRAM)
	case ierr != nil || iter < 1 || iter > maxSCRAMIterations:
		return nil, fmt.Errorf("%w: an iteration count of %q", errSCRAM, attrs['i'])
	}
	salted, err := pbkdf2.Key(x.s.hash, x.s.password, salt, iter, x.s.hash().Size())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errSCRAM, err)
	}
	clientKey := x.hmac(salted, "Client Key")
	h := x.s.hash()
	h.Write(clientKey)
	stored := h.Sum(nil)
	final := "c=biws,r=" + nonce
	auth := x.first + "," + msg + "," + final
	proof := x.hmac(stored, auth)
	for i := range proof {
		proof[i] ^= clientKey[i]
	}
	x.serverSig = x.hmac(x.hmac(salted, "Server Key"), auth)
	return []byte(final + ",p=" + base64.StdEncoding.EncodeToString(proof)), nil
}

// Done checks the server's final message, its signature.
func (x *scramExchange) Done(data []byte) error {
	msg := string(data)
	switch {
	case x.serverSig == nil:
		return fmt.Errorf("%w: the server ended the exchange before the client's proof", errSCRAM)
	case strings.HasPrefix(msg, "e="):
		return fmt.Errorf("%w: the server refused: %s", errSCRAM, msg[2:])
	case !strings.HasPrefix(msg, "v="):
		return fmt.Errorf("%w: no server signature", errSCRAM)
	}
	v, _, _ := strings.Cut(msg[2:], ",")
	sig, err := base64.StdEncoding.DecodeString(v)
	if err != nil || subtle.ConstantTimeCompare(sig, x.serverSig) != 1 {
		return fmt.Errorf("%w: the server's signature is wrong: it does not know the password", errSCRAM)
	}
	return nil
}
