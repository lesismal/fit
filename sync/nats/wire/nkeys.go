package natswire

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// NKeys are ed25519 keys written in base32 with a prefix byte, saying what
// the key is, and a CRC-16 (XMODEM) after it, little-endian.
const (
	prefixSeed byte = 18 << 3 // 'S'
	prefixUser byte = 20 << 3 // 'U'
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// crc16 is CRC-16/XMODEM, as NKeys check themselves.
func crc16(b []byte) uint16 {
	var crc uint16
	for _, c := range b {
		crc ^= uint16(c) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// KeyPair is an NKey: the private key of a seed, and its public key as the
// server knows it.
type KeyPair struct {
	priv   ed25519.PrivateKey
	public string
}

// ParseSeed reads an NKey seed, as nsc writes it ("SU..." for a user):
// base32 of two prefix bytes, the 32-byte ed25519 seed and the CRC.
func ParseSeed(seed string) (*KeyPair, error) {
	raw, err := b32.DecodeString(strings.TrimSpace(seed))
	if err != nil || len(raw) != 36 {
		return nil, errors.New("nats: the nkey seed is not base32 of 36 bytes")
	}
	if crc16(raw[:34]) != binary.LittleEndian.Uint16(raw[34:]) {
		return nil, errors.New("nats: the nkey seed's checksum is wrong")
	}
	if raw[0]&0xf8 != prefixSeed {
		return nil, errors.New("nats: the nkey is not a seed")
	}
	kind := (raw[0]&7)<<5 | (raw[1]&0xf8)>>3
	priv := ed25519.NewKeyFromSeed(raw[2:34])
	return &KeyPair{priv: priv, public: encodeKey(kind, priv.Public().(ed25519.PublicKey))}, nil
}

// encodeKey writes a public key with its prefix.
func encodeKey(kind byte, key []byte) string {
	raw := append([]byte{kind}, key...)
	raw = binary.LittleEndian.AppendUint16(raw, crc16(raw))
	return b32.EncodeToString(raw)
}

// SeedOf writes an ed25519 seed as a user's NKey seed, the inverse of
// ParseSeed: for tests and tools.
func SeedOf(seed []byte) string {
	raw := []byte{prefixSeed | prefixUser>>5, (prefixUser & 31) << 3}
	raw = append(raw, seed...)
	raw = binary.LittleEndian.AppendUint16(raw, crc16(raw))
	return b32.EncodeToString(raw)
}

// Public is the key's public key, "U..." for a user.
func (k *KeyPair) Public() string { return k.public }

// Sign signs the server's nonce, base64url without padding, as CONNECT's
// sig is written.
func (k *KeyPair) Sign(nonce []byte) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(k.priv, nonce))
}

// ParseCreds reads a creds file, as nsc writes it: the user's JWT and NKey
// seed, each between a BEGIN and an END line.
func ParseCreds(data []byte) (jwt, seed string, err error) {
	if jwt, err = credsBlock(data, "JWT"); err != nil {
		return "", "", err
	}
	if seed, err = credsBlock(data, "SEED"); err != nil {
		return "", "", err
	}
	return jwt, seed, nil
}

// credsBlock returns the first line after the BEGIN line naming what.
func credsBlock(data []byte, what string) (string, error) {
	lines := bytes.Split(data, []byte("\n"))
	for i, l := range lines {
		l = bytes.TrimSpace(l)
		if !bytes.HasPrefix(l, []byte("-----BEGIN")) || !bytes.Contains(l, []byte(what)) {
			continue
		}
		for _, v := range lines[i+1:] {
			if v = bytes.TrimSpace(v); len(v) > 0 {
				if bytes.HasPrefix(v, []byte("--")) {
					break
				}
				return string(v), nil
			}
		}
	}
	return "", fmt.Errorf("nats: the creds hold no %s", strings.ToLower(what))
}
