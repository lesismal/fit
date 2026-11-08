package kafkawire

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
	"time"
)

// The SASL mechanisms the client speaks.
const (
	SASLPlain       = "PLAIN"
	SASLScramSHA256 = "SCRAM-SHA-256"
	SASLScramSHA512 = "SCRAM-SHA-512"
)

// SASL is the client's authentication: a mechanism and its credentials.
// Over a connection without TLS, PLAIN sends the password in the clear.
type SASL struct {
	Mechanism          string
	Username, Password string
}

// maxScramIterations bounds the work a server can ask of the client.
const maxScramIterations = 1 << 20

// authenticate runs SaslHandshake and then SaslAuthenticate with the
// mechanism's messages.
func (bc *brokerConn) authenticate(ctx context.Context, cfg *Config) error {
	s := cfg.SASL
	mech := s.Mechanism
	var e encoder
	e.string(mech)
	resp, err := bc.roundTrip(ctx, apiSaslHandshake, 1, e.b, cfg.RequestTimeout)
	if err != nil {
		return err
	}
	d := decoder{b: resp}
	code := Error(d.int16())
	var mechs []string
	for range d.arrayLen() {
		mechs = append(mechs, d.string())
	}
	if d.err != nil {
		return &ConnError{Addr: bc.addr, Err: d.err}
	}
	if code != 0 {
		return fmt.Errorf("kafka: broker %s: SASL %s (it offers %s): %w", bc.addr, mech, strings.Join(mechs, ", "), code)
	}
	exchange := func(msg []byte) ([]byte, error) {
		v, err := bc.version(apiSaslAuthenticate)
		if err != nil {
			return nil, err
		}
		var e encoder
		e.bytes(msg)
		resp, err := bc.roundTrip(ctx, apiSaslAuthenticate, v, e.b, cfg.RequestTimeout)
		if err != nil {
			return nil, err
		}
		d := decoder{b: resp}
		code := Error(d.int16())
		emsg := d.nullableString()
		out := d.bytes()
		lifetime := d.int64()
		if d.err != nil {
			return nil, &ConnError{Addr: bc.addr, Err: d.err}
		}
		if code != 0 {
			if emsg != nil && *emsg != "" {
				return nil, fmt.Errorf("kafka: broker %s: SASL %s: %s: %w", bc.addr, mech, *emsg, code)
			}
			return nil, fmt.Errorf("kafka: broker %s: SASL %s: %w", bc.addr, mech, code)
		}
		if lifetime > 0 {
			// Reconnect a little before the broker ends the session.
			bc.expires = time.Now().Add(time.Duration(lifetime) * time.Millisecond * 9 / 10)
		}
		return out, nil
	}
	return bc.authenticateMech(s, exchange)
}

// authenticateMech runs the mechanism's messages through exchange.
func (bc *brokerConn) authenticateMech(s *SASL, exchange func([]byte) ([]byte, error)) error {
	switch s.Mechanism {
	case SASLPlain:
		_, err := exchange([]byte("\x00" + s.Username + "\x00" + s.Password))
		return err
	case SASLScramSHA256:
		return scram(sha256.New, s.Username, s.Password, "", exchange)
	case SASLScramSHA512:
		return scram(sha512.New, s.Username, s.Password, "", exchange)
	}
	return fmt.Errorf("kafka: SASL mechanism %q (PLAIN, SCRAM-SHA-256, SCRAM-SHA-512)", s.Mechanism)
}

// scram runs SCRAM (RFC 5802) without channel binding: the client's first
// message, the proof from the server's salt and iterations, and the
// server's signature checked. nonce is random when empty (tests set it).
func scram(h func() hash.Hash, user, password, nonce string, exchange func([]byte) ([]byte, error)) error {
	if nonce == "" {
		var b [24]byte
		rand.Read(b[:])
		nonce = base64.RawStdEncoding.EncodeToString(b[:])
	}
	first := "n=" + strings.NewReplacer("=", "=3D", ",", "=2C").Replace(user) + ",r=" + nonce
	sf, err := exchange([]byte("n,," + first))
	if err != nil {
		return err
	}
	serverFirst := string(sf)
	attrs := scramAttrs(serverFirst)
	if m, ok := attrs["e"]; ok {
		return fmt.Errorf("kafka: SCRAM: server: %s", m)
	}
	r, s := attrs["r"], attrs["s"]
	iter, err := strconv.Atoi(attrs["i"])
	if err != nil || iter < 1 || iter > maxScramIterations || !strings.HasPrefix(r, nonce) || len(r) == len(nonce) {
		return errors.New("kafka: SCRAM: malformed server-first message")
	}
	salt, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return errors.New("kafka: SCRAM: malformed salt")
	}
	// An error is only FIPS mode refusing a short key, not this one.
	salted, _ := pbkdf2.Key(h, password, salt, iter, h().Size())
	mac := func(key []byte, msg string) []byte {
		m := hmac.New(h, key)
		m.Write([]byte(msg))
		return m.Sum(nil)
	}
	clientKey := mac(salted, "Client Key")
	sh := h()
	sh.Write(clientKey)
	storedKey := sh.Sum(nil)
	final := "c=biws,r=" + r
	authMessage := first + "," + serverFirst + "," + final
	proof := mac(storedKey, authMessage)
	for i := range proof {
		proof[i] ^= clientKey[i]
	}
	sfin, err := exchange([]byte(final + ",p=" + base64.StdEncoding.EncodeToString(proof)))
	if err != nil {
		return err
	}
	fattrs := scramAttrs(string(sfin))
	if m, ok := fattrs["e"]; ok {
		return fmt.Errorf("kafka: SCRAM: server: %s", m)
	}
	v, err := base64.StdEncoding.DecodeString(fattrs["v"])
	want := mac(mac(salted, "Server Key"), authMessage)
	if err != nil || subtle.ConstantTimeCompare(v, want) != 1 {
		return errors.New("kafka: SCRAM: the server's signature does not match")
	}
	return nil
}

func scramAttrs(msg string) map[string]string {
	m := make(map[string]string)
	for _, kv := range strings.Split(msg, ",") {
		if len(kv) >= 2 && kv[1] == '=' {
			m[kv[:1]] = kv[2:]
		}
	}
	return m
}
