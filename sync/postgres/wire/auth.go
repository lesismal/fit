package pgwire

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
)

// maxSCRAMIterations bounds the iterations a server may ask for, so that a
// hostile server cannot hold the client in PBKDF2; PostgreSQL's default is
// 4096.
const maxSCRAMIterations = 1 << 22

// authenticate answers the server's authentication requests until
// AuthenticationOk.
func (c *Conn) authenticate() error {
	var sc *scram
	for {
		t, body, err := c.recv()
		if err != nil {
			return err
		}
		if t == 'E' {
			return parseError(body)
		}
		if t == 'v' { // NegotiateProtocolVersion: no options were asked for
			continue
		}
		if t != 'R' || len(body) < 4 {
			return c.unexpected(t, "authentication")
		}
		code := binary.BigEndian.Uint32(body)
		body = body[4:]
		switch code {
		case 0: // AuthenticationOk
			if c.cfg.ChannelBinding == ChannelBindingRequire && !c.bound {
				return ErrChannelBinding
			}
			if sc != nil && !sc.done {
				return errors.New("postgres: the server ended SCRAM without proving it knows the password")
			}
			return nil
		case 3: // cleartext
			if !c.cfg.AllowCleartext {
				return ErrCleartextRefused
			}
			if err := c.sendPassword(c.cfg.Password); err != nil {
				return err
			}
		case 5: // MD5
			if !c.cfg.AllowMD5 {
				return ErrMD5Refused
			}
			if len(body) < 4 {
				return c.protoErr("AuthenticationMD5Password")
			}
			if err := c.sendPassword(md5Password(c.cfg.User, c.cfg.Password, body[:4])); err != nil {
				return err
			}
		case 10: // SASL: the mechanisms offered
			var mechs []string
			for len(body) > 0 && body[0] != 0 {
				m, rest, ok := cstring(body)
				if !ok {
					return c.protoErr("AuthenticationSASL")
				}
				mechs = append(mechs, m)
				body = rest
			}
			sc, err = c.startSCRAM(mechs)
			if err != nil {
				return err
			}
		case 11: // SASLContinue
			if sc == nil {
				return c.unexpected(t, "authentication")
			}
			final, err := sc.serverFirst(body)
			if err != nil {
				return err
			}
			i := c.begin('p')
			c.wbuf = append(c.wbuf, final...)
			c.end(i)
			if err := c.flush(); err != nil {
				return err
			}
		case 12: // SASLFinal
			if sc == nil {
				return c.unexpected(t, "authentication")
			}
			if err := sc.serverFinal(body); err != nil {
				return err
			}
			c.bound = sc.plus
		default:
			return fmt.Errorf("postgres: authentication method %d is not supported", code)
		}
	}
}

func (c *Conn) sendPassword(p string) error {
	i := c.begin('p')
	c.wbuf = append(append(c.wbuf, p...), 0)
	c.end(i)
	return c.flush()
}

func md5Password(user, password string, salt []byte) string {
	h := md5.Sum([]byte(password + user))
	inner := hex.EncodeToString(h[:])
	h = md5.Sum(append([]byte(inner), salt...))
	return "md5" + hex.EncodeToString(h[:])
}

// startSCRAM picks SCRAM-SHA-256 or its -PLUS form and sends the client's
// first message.
func (c *Conn) startSCRAM(mechs []string) (*scram, error) {
	var plain, plus bool
	for _, m := range mechs {
		switch m {
		case "SCRAM-SHA-256":
			plain = true
		case "SCRAM-SHA-256-PLUS":
			plus = true
		}
	}
	ts := c.TLS()
	sc := &scram{password: c.cfg.Password}
	switch {
	case c.cfg.ChannelBinding != ChannelBindingDisable && plus && ts != nil:
		cb, err := tlsServerEndPoint(ts.PeerCertificates[0])
		if err != nil && c.cfg.ChannelBinding == ChannelBindingRequire {
			return nil, err
		}
		if err == nil {
			sc.plus = true
			sc.gs2 = "p=tls-server-end-point,,"
			sc.cbData = cb
		} else {
			sc.gs2 = "n,,"
		}
	case c.cfg.ChannelBinding == ChannelBindingRequire:
		return nil, ErrChannelBinding
	case plain && ts != nil && c.cfg.ChannelBinding != ChannelBindingDisable:
		// The client could bind and believes the server cannot: a server
		// that did offer -PLUS (stripped by a man in the middle) refuses.
		sc.gs2 = "y,,"
	case plain:
		sc.gs2 = "n,,"
	default:
		return nil, fmt.Errorf("postgres: no SASL mechanism supported among %v", mechs)
	}
	if !plain && !sc.plus {
		return nil, fmt.Errorf("postgres: no SASL mechanism supported among %v", mechs)
	}
	var nonce [18]byte
	rand.Read(nonce[:]) // never fails (crypto/rand)
	first := sc.clientFirst("", base64.RawStdEncoding.EncodeToString(nonce[:]))
	mech := "SCRAM-SHA-256"
	if sc.plus {
		mech = "SCRAM-SHA-256-PLUS"
	}
	i := c.begin('p')
	c.wbuf = append(append(c.wbuf, mech...), 0)
	c.wbuf = binary.BigEndian.AppendUint32(c.wbuf, uint32(len(first)))
	c.wbuf = append(c.wbuf, first...)
	c.end(i)
	return sc, c.flush()
}

// tlsServerEndPoint is RFC 5929's tls-server-end-point: a hash of the
// server's certificate, with SHA-256 for MD5 and SHA-1 signatures.
func tlsServerEndPoint(cert *x509.Certificate) ([]byte, error) {
	var h hash.Hash
	switch cert.SignatureAlgorithm {
	case x509.MD5WithRSA, x509.SHA1WithRSA, x509.ECDSAWithSHA1, x509.DSAWithSHA1,
		x509.SHA256WithRSA, x509.SHA256WithRSAPSS, x509.ECDSAWithSHA256, x509.DSAWithSHA256:
		h = sha256.New()
	case x509.SHA384WithRSA, x509.SHA384WithRSAPSS, x509.ECDSAWithSHA384:
		h = sha512.New384()
	case x509.SHA512WithRSA, x509.SHA512WithRSAPSS, x509.ECDSAWithSHA512:
		h = sha512.New()
	default:
		return nil, fmt.Errorf("postgres: channel binding: no hash for a certificate signed with %v", cert.SignatureAlgorithm)
	}
	h.Write(cert.Raw)
	return h.Sum(nil), nil
}

// scram is one SCRAM-SHA-256 exchange (RFC 5802, RFC 7677). The password
// is used as its bytes, without SASLprep, as PostgreSQL itself does with a
// password SASLprep refuses; ASCII passwords are the same either way.
type scram struct {
	password  string
	gs2       string
	cbData    []byte
	plus      bool
	nonce     string
	firstBare string
	serverSig []byte
	done      bool
}

func (s *scram) clientFirst(user, nonce string) []byte {
	s.nonce = nonce
	s.firstBare = "n=" + user + ",r=" + nonce
	return []byte(s.gs2 + s.firstBare)
}

// serverFirst reads the server's first message and returns the client's
// final one.
func (s *scram) serverFirst(msg []byte) ([]byte, error) {
	var nonce, salt string
	iter := 0
	for _, attr := range strings.Split(string(msg), ",") {
		k, v, _ := strings.Cut(attr, "=")
		switch k {
		case "r":
			nonce = v
		case "s":
			salt = v
		case "i":
			iter, _ = strconv.Atoi(v)
		case "e":
			return nil, fmt.Errorf("postgres: SCRAM: %s", v)
		}
	}
	if !strings.HasPrefix(nonce, s.nonce) || len(nonce) == len(s.nonce) {
		return nil, errors.New("postgres: SCRAM: the server's nonce does not extend the client's")
	}
	if iter < 1 || iter > maxSCRAMIterations {
		return nil, fmt.Errorf("postgres: SCRAM: %d iterations, outside 1..%d", iter, maxSCRAMIterations)
	}
	saltb, err := base64.StdEncoding.DecodeString(salt)
	if err != nil || len(saltb) == 0 {
		return nil, errors.New("postgres: SCRAM: a bad salt")
	}
	salted, err := pbkdf2.Key(sha256.New, s.password, saltb, iter, 32)
	if err != nil {
		return nil, fmt.Errorf("postgres: SCRAM: %w", err)
	}
	clientKey := hmacSum(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	cb := append([]byte(s.gs2), s.cbData...)
	finalNoProof := "c=" + base64.StdEncoding.EncodeToString(cb) + ",r=" + nonce
	authMsg := s.firstBare + "," + string(msg) + "," + finalNoProof
	sig := hmacSum(storedKey[:], authMsg)
	proof := make([]byte, len(clientKey))
	for i := range proof {
		proof[i] = clientKey[i] ^ sig[i]
	}
	s.serverSig = hmacSum(hmacSum(salted, "Server Key"), authMsg)
	return []byte(finalNoProof + ",p=" + base64.StdEncoding.EncodeToString(proof)), nil
}

// serverFinal checks the server's signature.
func (s *scram) serverFinal(msg []byte) error {
	m := string(msg)
	if e, ok := strings.CutPrefix(m, "e="); ok {
		return fmt.Errorf("postgres: SCRAM: %s", e)
	}
	v, ok := strings.CutPrefix(m, "v=")
	if !ok {
		return errors.New("postgres: SCRAM: a bad final message")
	}
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	sig, err := base64.StdEncoding.DecodeString(v)
	if err != nil || s.serverSig == nil || !hmac.Equal(sig, s.serverSig) {
		return errors.New("postgres: SCRAM: the server's signature is wrong")
	}
	s.done = true
	return nil
}

func hmacSum(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}
