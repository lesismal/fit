package krb5

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/pbkdf2"
)

// The encryption types the client speaks: AES in CTS mode with HMAC-SHA1
// (RFC 3962), what MIT's and Active Directory's KDCs use by default. RC4
// and DES, broken, are not.
const (
	ETypeAES128 = 17 // aes128-cts-hmac-sha1-96
	ETypeAES256 = 18 // aes256-cts-hmac-sha1-96
)

// The checksum types of the encryption types.
const (
	cksumAES128 = 15 // hmac-sha1-96-aes128
	cksumAES256 = 16 // hmac-sha1-96-aes256
)

// Key is an encryption key of an encryption type.
type Key struct {
	EType int32
	Value []byte
}

// keySize is an encryption type's key length, 0 for one not spoken.
func keySize(etype int32) int {
	switch etype {
	case ETypeAES128:
		return 16
	case ETypeAES256:
		return 32
	}
	return 0
}

func checksumType(etype int32) int32 {
	if etype == ETypeAES128 {
		return cksumAES128
	}
	return cksumAES256
}

// errEType is a key or message of an encryption type the client does not
// speak.
var errEType = errors.New("krb5: an encryption type other than aes128-cts-hmac-sha1-96 and aes256-cts-hmac-sha1-96")

// defaultIterations is PBKDF2's count when the KDC's s2kparams give none.
const defaultIterations = 4096

// maxIterations bounds the count a KDC (or what poses as one) may ask
// for, which would otherwise hold the client for as long as it likes.
const maxIterations = 1 << 20

// StringToKey makes a key from a password and its salt (RFC 3962):
// PBKDF2-HMAC-SHA1 of iterations, then DK with "kerberos".
func StringToKey(etype int32, password, salt string, iterations int) (Key, error) {
	n := keySize(etype)
	if n == 0 {
		return Key{}, errEType
	}
	if iterations <= 0 {
		iterations = defaultIterations
	}
	if iterations > maxIterations {
		return Key{}, fmt.Errorf("krb5: the KDC asks for %d string-to-key iterations, more than %d", iterations, maxIterations)
	}
	tkey := pbkdf2.Key([]byte(password), []byte(salt), iterations, n, sha1.New)
	k, err := derive(tkey, []byte("kerberos"))
	return Key{EType: etype, Value: k}, err
}

// nfold folds in to n bytes (RFC 3961's n-fold): copies of in, each
// rotated 13 bits right of the one before, to the least common multiple
// of the lengths, added in ones' complement in n-byte pieces.
func nfold(in []byte, n int) []byte {
	k := len(in)
	lcm := n * k / gcd(n, k)
	big := make([]byte, lcm)
	for i := 0; i < lcm/k; i++ {
		rotateRight(big[i*k:(i+1)*k], in, 13*i)
	}
	// Ones' complement addition of the n-byte pieces: their bytes summed
	// column by column, then the carries moved up from the last byte,
	// the one out of the first wrapping round to the last.
	out := make([]byte, n)
	sum := make([]int, n)
	for p := 0; p < lcm/n; p++ {
		for j := 0; j < n; j++ {
			sum[j] += int(big[p*n+j])
		}
	}
	for {
		carry := 0
		for j := n - 1; j >= 0; j-- {
			sum[j] += carry
			carry = sum[j] >> 8
			sum[j] &= 0xff
		}
		if carry == 0 {
			break
		}
		sum[n-1] += carry
	}
	for j := range out {
		out[j] = byte(sum[j])
	}
	return out
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// rotateRight writes in, as a bit string rotated r bits right, to out.
func rotateRight(out, in []byte, r int) {
	bits := len(in) * 8
	r %= bits
	for i := range out {
		out[i] = 0
	}
	for i := 0; i < bits; i++ {
		src := (i - r + bits) % bits
		if in[src/8]&(0x80>>(src%8)) != 0 {
			out[i/8] |= 0x80 >> (i % 8)
		}
	}
}

// derive is DK(key, constant) of RFC 3961 for AES: the n-fold of the
// constant to a block, encrypted again and again to the key's length;
// random-to-key is the identity.
func derive(key, constant []byte) ([]byte, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	block := nfold(constant, aes.BlockSize)
	out := make([]byte, 0, len(key))
	for len(out) < len(key) {
		next := make([]byte, aes.BlockSize)
		b.Encrypt(next, block)
		out = append(out, next...)
		block = next
	}
	return out[:len(key)], nil
}

// usageKey derives a key usage's key: kind 0x99 checksums, 0xaa
// encryption, 0x55 integrity.
func usageKey(k Key, usage uint32, kind byte) ([]byte, error) {
	c := binary.BigEndian.AppendUint32(nil, usage)
	return derive(k.Value, append(c, kind))
}

// ctsEncrypt is AES in CBC mode with ciphertext stealing, IV zero, as
// Kerberos uses it: the last two blocks swapped, the last one cut short.
func ctsEncrypt(key, plain []byte) ([]byte, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	bs := aes.BlockSize
	if len(plain) < bs {
		return nil, errors.New("krb5: less than a block to encrypt")
	}
	iv := make([]byte, bs)
	if len(plain) == bs {
		out := make([]byte, bs)
		cipher.NewCBCEncrypter(b, iv).CryptBlocks(out, plain)
		return out, nil
	}
	padded := append([]byte(nil), plain...)
	if r := len(padded) % bs; r != 0 {
		padded = append(padded, make([]byte, bs-r)...)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(b, iv).CryptBlocks(out, padded)
	n := len(out)
	// Swap the last two blocks; the last is cut to the plaintext's tail.
	last := len(plain) - (n - bs)
	res := make([]byte, 0, len(plain))
	res = append(res, out[:n-2*bs]...)
	res = append(res, out[n-bs:]...)
	res = append(res, out[n-2*bs:n-2*bs+last]...)
	return res, nil
}

// ctsDecrypt undoes ctsEncrypt.
func ctsDecrypt(key, ct []byte) ([]byte, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	bs := aes.BlockSize
	if len(ct) < bs {
		return nil, errors.New("krb5: a ciphertext shorter than a block")
	}
	iv := make([]byte, bs)
	if len(ct) == bs {
		out := make([]byte, bs)
		cipher.NewCBCDecrypter(b, iv).CryptBlocks(out, ct)
		return out, nil
	}
	nb := (len(ct) + bs - 1) / bs
	last := len(ct) - (nb-1)*bs // the last block's length, 1 to 16
	// cn1 is the second to last block as sent (the CBC ciphertext's
	// last), cn its last, cut short.
	head := ct[:(nb-2)*bs]
	cn1 := ct[(nb-2)*bs : (nb-1)*bs]
	cn := ct[(nb-1)*bs:]
	// Decrypting cn1 gives the last plaintext block XOR the CBC
	// ciphertext's second to last block, whose tail is cn's missing part.
	d := make([]byte, bs)
	b.Decrypt(d, cn1)
	full := append(append([]byte(nil), cn...), d[last:]...)
	cbc := append(append(append([]byte(nil), head...), full...), cn1...)
	out := make([]byte, len(cbc))
	cipher.NewCBCDecrypter(b, iv).CryptBlocks(out, cbc)
	return out[:len(ct)], nil
}

// encrypt encrypts plain with a key usage: a random confounder before it,
// AES-CTS with the usage's encryption key, and the first 12 bytes of
// HMAC-SHA1 with its integrity key after.
func encrypt(k Key, usage uint32, plain []byte) ([]byte, error) {
	if keySize(k.EType) == 0 {
		return nil, errEType
	}
	ke, err := usageKey(k, usage, 0xaa)
	if err != nil {
		return nil, err
	}
	ki, _ := usageKey(k, usage, 0x55)
	conf := make([]byte, aes.BlockSize)
	rand.Read(conf)
	data := append(conf, plain...)
	ct, err := ctsEncrypt(ke, data)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha1.New, ki)
	m.Write(data)
	return append(ct, m.Sum(nil)[:12]...), nil
}

// decrypt undoes encrypt, checking the HMAC.
func decrypt(k Key, usage uint32, ct []byte) ([]byte, error) {
	if keySize(k.EType) == 0 {
		return nil, errEType
	}
	if len(ct) < aes.BlockSize+12 {
		return nil, errors.New("krb5: a ciphertext too short")
	}
	ke, err := usageKey(k, usage, 0xaa)
	if err != nil {
		return nil, err
	}
	ki, _ := usageKey(k, usage, 0x55)
	data, err := ctsDecrypt(ke, ct[:len(ct)-12])
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha1.New, ki)
	m.Write(data)
	if subtle.ConstantTimeCompare(m.Sum(nil)[:12], ct[len(ct)-12:]) != 1 {
		return nil, fmt.Errorf("krb5: the integrity check of a message of key usage %d failed (a wrong key or password?)", usage)
	}
	return data[aes.BlockSize:], nil
}

// checksum is the hmac-sha1-96 checksum of data with a key usage.
func checksum(k Key, usage uint32, data []byte) ([]byte, error) {
	if keySize(k.EType) == 0 {
		return nil, errEType
	}
	kc, err := usageKey(k, usage, 0x99)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha1.New, kc)
	m.Write(data)
	return m.Sum(nil)[:12], nil
}
