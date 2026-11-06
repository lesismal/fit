package medialib

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lesismal/fit/config"
)

// Keys are a library's keys of encrypted HLS (I136): an AES-128 key for
// each resource, an item of vod or a channel, and each epoch of a channel's
// rotation, derived from the library's secret, so that nothing is stored
// and every handler of the library derives the same. A key is
//
//	HMAC-SHA256(K, "hls aes-128" 0 kind 0 resource 0 epoch)[:16]
//
// where K is HMAC-SHA256(secret, "fit media library" 0 name), the library's
// own: a library's keys open nothing of another's, nor any of media_auth's
// tokens, signed under keys derived apart. Keys are immutable; a reload that
// changes the secret makes new ones. The secret never leaves them, nor
// reaches a log.
type Keys struct {
	mac []byte
	id  string
}

// The kinds of resources keys are derived for.
const (
	// KeyItem is the kind of an item of vod: one key, epoch 0.
	KeyItem = "item"
	// KeyChannel is the kind of a channel's HLS, a key an epoch.
	KeyChannel = "channel"
)

// NewKeys returns the keys of library name derived from secret, or an error
// for a secret shorter than config.MinMediaSecret.
func NewKeys(name, secret string) (*Keys, error) {
	if len(secret) < config.MinMediaSecret {
		return nil, fmt.Errorf("the secret is %d bytes: use at least %d", len(secret), config.MinMediaSecret)
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("fit media library\x00" + name))
	k := &Keys{mac: m.Sum(nil)}
	// The ID tells keys apart, in ETags and logs, and says nothing of them.
	id := hmac.New(sha256.New, k.mac)
	id.Write([]byte("key id"))
	k.id = hex.EncodeToString(id.Sum(nil)[:6])
	return k, nil
}

// KeysOf returns the keys of library c, its secret_file read now; nil and
// no error when it has no secret.
func KeysOf(c *config.MediaLibrary) (*Keys, error) {
	switch {
	case c.Secret != "" && c.SecretFile != "":
		return nil, errors.New("set secret or secret_file, one of the two")
	case c.Secret != "":
		return NewKeys(c.Name, c.Secret)
	case c.SecretFile != "":
		data, err := os.ReadFile(c.SecretFile)
		if err != nil {
			return nil, fmt.Errorf("secret_file: %w", err)
		}
		k, err := NewKeys(c.Name, strings.TrimRight(string(data), "\r\n"))
		if err != nil {
			return nil, fmt.Errorf("secret_file %s: %w", c.SecretFile, err)
		}
		return k, nil
	}
	return nil, nil
}

// Key returns the AES-128 key of resource of kind at epoch: an item's ID
// with KeyItem, a channel's name with KeyChannel.
func (k *Keys) Key(kind, resource string, epoch uint64) [16]byte {
	m := hmac.New(sha256.New, k.mac)
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], epoch)
	m.Write([]byte("hls aes-128\x00" + kind + "\x00" + resource + "\x00"))
	m.Write(b[:])
	var out [16]byte
	copy(out[:], m.Sum(nil))
	return out
}

// ID returns a short ID of the keys, the same for the same library and
// secret: a fingerprint that tells keys apart without saying anything of
// them; "" for nil keys.
func (k *Keys) ID() string {
	if k == nil {
		return ""
	}
	return k.id
}

// String hides the keys from whatever prints them.
func (k *Keys) String() string { return "medialib.Keys(" + k.id + ")" }

// Encrypt encrypts segment, a media segment of HLS whose media sequence
// number is seq, in place with key, as RFC 8216 section 4.3.2.4 has
// METHOD=AES-128: AES-128 in CBC mode with PKCS#7 padding, its IV seq as a
// 128-bit big-endian number, as a player takes it when EXT-X-KEY names no
// IV. It returns segment with its padding, 1 to 16 bytes, appended, in its
// own capacity when it has room.
func Encrypt(key [16]byte, seq uint64, segment []byte) []byte {
	pad := aes.BlockSize - len(segment)%aes.BlockSize
	for range pad {
		segment = append(segment, byte(pad))
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err) // a 16-byte key always makes one
	}
	var iv [aes.BlockSize]byte
	binary.BigEndian.PutUint64(iv[8:], seq)
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(segment, segment)
	return segment
}
