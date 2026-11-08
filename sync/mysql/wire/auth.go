package mysqlwire

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Capability flags.
const (
	capLongPassword     = 1 << 0
	capLongFlag         = 1 << 2
	capConnectWithDB    = 1 << 3
	capProtocol41       = 1 << 9
	capSSL              = 1 << 11
	capTransactions     = 1 << 13
	capSecureConnection = 1 << 15
	capMultiResults     = 1 << 17
	capPSMultiResults   = 1 << 18
	capPluginAuth       = 1 << 19
	capPluginAuthLenEnc = 1 << 21
	capSessionTrack     = 1 << 23
	capDeprecateEOF     = 1 << 24
)

// The password plugins.
const (
	pluginNative      = "mysql_native_password"
	pluginCachingSHA2 = "caching_sha2_password"
	pluginSHA256      = "sha256_password"
	pluginClear       = "mysql_clear_password"
	cachingFastOK     = 3
	cachingFullAuth   = 4
	cachingRequestKey = 2
	sha256RequestKey  = 1
)

// handshake reads the server's greeting, answers it (through TLS when
// configured) and authenticates.
func (c *Conn) handshake(ctx context.Context) error {
	c.seq = 0
	p, err := c.readPacket()
	if err != nil {
		return err
	}
	if len(p) > 0 && p[0] == 0xff {
		return c.parseErr(p)
	}
	r := rd{b: p}
	if v := r.u8(); v != 10 {
		return fmt.Errorf("%w: protocol version %d, not 10", ErrMalformed, v)
	}
	c.version = string(r.nul())
	c.connID = r.u32()
	scramble := append([]byte(nil), r.take(8)...)
	r.u8()
	caps := uint32(r.u16())
	r.u8() // the server's collation
	c.status = r.u16()
	caps |= uint32(r.u16()) << 16
	authLen := int(r.u8())
	r.take(10)
	if caps&capSecureConnection != 0 {
		n := max(13, authLen-8)
		part := r.take(min(n, len(r.b)))
		part = bytes.TrimRight(part, "\x00")
		scramble = append(scramble, part...)
	}
	plugin := ""
	if caps&capPluginAuth != 0 {
		plugin = string(r.nul())
	}
	if r.bad {
		return fmt.Errorf("%w: handshake", ErrMalformed)
	}
	if strings.Contains(c.version, "MariaDB") {
		c.mariadb = true
		c.version = strings.TrimPrefix(c.version, "5.5.5-")
	}
	if caps&capProtocol41 == 0 || caps&capSecureConnection == 0 {
		return fmt.Errorf("mysql: server %s does not speak protocol 4.1", c.version)
	}

	c.caps = capLongPassword | capLongFlag | capProtocol41 | capTransactions | capSecureConnection |
		capMultiResults | capPSMultiResults
	c.caps |= caps & (capPluginAuth | capPluginAuthLenEnc | capDeprecateEOF)
	if c.cfg.Database != "" {
		c.caps |= capConnectWithDB
	}
	if c.cfg.TLS != nil {
		if caps&capSSL == 0 {
			return fmt.Errorf("mysql: server %s does not offer TLS", c.version)
		}
		c.caps |= capSSL
		b := c.appendClientHead(c.startPacket())
		if err := c.writePacket(b); err != nil {
			return err
		}
		if c.br.Buffered() > 0 {
			// Bytes past the greeting would be lost under TLS.
			return fmt.Errorf("%w: data after the greeting", ErrMalformed)
		}
		tc := c.cfg.TLS.Clone()
		if tc.ServerName == "" && !tc.InsecureSkipVerify {
			host, _, err := net.SplitHostPort(c.cfg.Addr)
			if err != nil {
				host = c.cfg.Addr
			}
			tc.ServerName = host
		}
		conn := tls.Client(c.raw, tc)
		if err := conn.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("TLS: %w", err)
		}
		c.nc = conn
		c.br.Reset(conn)
		c.secure = true
	}

	if plugin == "" {
		plugin = pluginNative
	}
	auth, err := c.authData(plugin, scramble)
	if err != nil {
		if !errors.Is(err, errUnknownPlugin) {
			return err
		}
		// The server switches to the user's plugin.
		plugin = pluginNative
		auth, _ = c.authData(plugin, scramble)
	}
	b := c.appendClientHead(c.startPacket())
	b = append(b, c.cfg.User...)
	b = append(b, 0)
	if c.caps&capPluginAuthLenEnc != 0 {
		b = appendLenEncBytes(b, auth)
	} else {
		b = append(b, byte(len(auth)))
		b = append(b, auth...)
	}
	if c.caps&capConnectWithDB != 0 {
		b = append(b, c.cfg.Database...)
		b = append(b, 0)
	}
	if c.caps&capPluginAuth != 0 {
		b = append(b, plugin...)
		b = append(b, 0)
	}
	if err := c.writePacket(b); err != nil {
		return err
	}
	return c.authenticate(plugin, scramble)
}

// appendClientHead appends what the SSL request and the handshake
// response start with.
func (c *Conn) appendClientHead(b []byte) []byte {
	b = binary.LittleEndian.AppendUint32(b, c.caps)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = append(b, c.cfg.Collation)
	return append(b, make([]byte, 23)...)
}

var errUnknownPlugin = errors.New("mysql: unknown authentication plugin")

// authData is the first answer to plugin's scramble.
func (c *Conn) authData(plugin string, scramble []byte) ([]byte, error) {
	pw := c.cfg.Password
	switch plugin {
	case pluginNative:
		return scrambleNative(pw, scramble), nil
	case pluginCachingSHA2:
		return scrambleSHA256(pw, scramble), nil
	case pluginSHA256:
		switch {
		case pw == "":
			return []byte{0}, nil
		case c.secure:
			return append([]byte(pw), 0), nil
		case c.cfg.ServerPublicKey != nil:
			return encryptPassword(pw, scramble, c.cfg.ServerPublicKey)
		case c.cfg.AllowPublicKeyRetrieval:
			return []byte{sha256RequestKey}, nil
		}
		return nil, fmt.Errorf("mysql: %s without TLS needs ServerPublicKey or AllowPublicKeyRetrieval", plugin)
	case pluginClear:
		if !c.secure && !c.cfg.AllowCleartextPassword {
			return nil, fmt.Errorf("mysql: %s needs TLS, a Unix socket or AllowCleartextPassword", plugin)
		}
		return append([]byte(pw), 0), nil
	}
	return nil, fmt.Errorf("%w %q", errUnknownPlugin, plugin)
}

// authenticate follows the server's answers to the handshake response:
// switches of plugin, caching_sha2_password's fast path or full
// authentication, the RSA key asked for, until OK or ERR.
func (c *Conn) authenticate(plugin string, scramble []byte) error {
	wantKey := false
	for {
		p, err := c.readPacket()
		if err != nil {
			return err
		}
		if len(p) == 0 {
			return fmt.Errorf("%w: empty packet in authentication", ErrMalformed)
		}
		switch p[0] {
		case 0x00:
			_, err := c.parseOK(p)
			return err
		case 0xff:
			return c.parseErr(p)
		case 0xfe:
			if len(p) == 1 {
				return errors.New("mysql: the server asks for the old (pre-4.1) password hash, which is not supported")
			}
			r := rd{b: p[1:]}
			plugin = string(r.nul())
			scramble = bytes.TrimRight(append([]byte(nil), r.rest()...), "\x00")
			auth, err := c.authData(plugin, scramble)
			if err != nil {
				return err
			}
			wantKey = plugin == pluginSHA256 && len(auth) == 1 && auth[0] == sha256RequestKey
			if err := c.writePacket(append(c.startPacket(), auth...)); err != nil {
				return err
			}
		case 0x01:
			data := p[1:]
			var answer []byte
			switch {
			case wantKey || plugin == pluginSHA256:
				pub, err := parsePublicKey(data)
				if err != nil {
					return err
				}
				if answer, err = encryptPassword(c.cfg.Password, scramble, pub); err != nil {
					return err
				}
				wantKey = false
			case plugin == pluginCachingSHA2 && len(data) == 1 && data[0] == cachingFastOK:
				continue
			case plugin == pluginCachingSHA2 && len(data) == 1 && data[0] == cachingFullAuth:
				switch {
				case c.secure:
					answer = append([]byte(c.cfg.Password), 0)
				case c.cfg.ServerPublicKey != nil:
					if answer, err = encryptPassword(c.cfg.Password, scramble, c.cfg.ServerPublicKey); err != nil {
						return err
					}
				case c.cfg.AllowPublicKeyRetrieval:
					answer = []byte{cachingRequestKey}
					wantKey = true
				default:
					return errors.New("mysql: caching_sha2_password's full authentication needs TLS, ServerPublicKey or AllowPublicKeyRetrieval")
				}
			default:
				return fmt.Errorf("%w: unexpected authentication data for %s", ErrMalformed, plugin)
			}
			if err := c.writePacket(append(c.startPacket(), answer...)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unexpected answer 0x%x in authentication", ErrMalformed, p[0])
		}
	}
}

// scrambleNative is SHA1(password) XOR SHA1(scramble, SHA1(SHA1(password))).
func scrambleNative(pw string, scramble []byte) []byte {
	if pw == "" {
		return nil
	}
	s1 := sha1.Sum([]byte(pw))
	s2 := sha1.Sum(s1[:])
	h := sha1.New()
	h.Write(scramble[:min(20, len(scramble))])
	h.Write(s2[:])
	s3 := h.Sum(nil)
	for i := range s3 {
		s3[i] ^= s1[i]
	}
	return s3
}

// scrambleSHA256 is SHA256(password) XOR SHA256(SHA256(SHA256(password)), scramble).
func scrambleSHA256(pw string, scramble []byte) []byte {
	if pw == "" {
		return nil
	}
	s1 := sha256.Sum256([]byte(pw))
	s2 := sha256.Sum256(s1[:])
	h := sha256.New()
	h.Write(s2[:])
	h.Write(scramble[:min(20, len(scramble))])
	s3 := h.Sum(nil)
	for i := range s3 {
		s3[i] ^= s1[i]
	}
	return s3
}

// encryptPassword is the password and a NUL, XORed with the scramble,
// encrypted with RSA-OAEP (SHA-1).
func encryptPassword(pw string, scramble []byte, pub *rsa.PublicKey) ([]byte, error) {
	if len(scramble) == 0 {
		return nil, fmt.Errorf("%w: no scramble", ErrMalformed)
	}
	plain := append([]byte(pw), 0)
	for i := range plain {
		plain[i] ^= scramble[i%len(scramble)]
	}
	out, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, pub, plain, nil)
	if err != nil {
		return nil, fmt.Errorf("mysql: encrypting the password: %w", err)
	}
	return out, nil
}

// ParsePublicKey reads an RSA public key in PEM, as the server's status
// variable Caching_sha2_password_rsa_public_key shows it.
func ParsePublicKey(pemText []byte) (*rsa.PublicKey, error) { return parsePublicKey(pemText) }

func parsePublicKey(data []byte) (*rsa.PublicKey, error) {
	blk, _ := pem.Decode(data)
	if blk == nil {
		return nil, errors.New("mysql: the server's public key is not PEM")
	}
	if k, err := x509.ParsePKIXPublicKey(blk.Bytes); err == nil {
		if pub, ok := k.(*rsa.PublicKey); ok {
			return pub, nil
		}
		return nil, errors.New("mysql: the server's public key is not RSA")
	}
	pub, err := x509.ParsePKCS1PublicKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("mysql: the server's public key: %w", err)
	}
	return pub, nil
}
