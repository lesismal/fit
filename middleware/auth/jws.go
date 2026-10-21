package auth

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rsa"
	_ "crypto/sha256" // the hashes the algorithms name
	_ "crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
)

// A JSON Web Token, as jwt verifies it: a JWS in compact serialization
// (RFC 7515) whose payload is a JSON object of claims (RFC 7519), signed
// with RSA (RS256, RS384, RS512, PS256, PS384, PS512), ECDSA (ES256,
// ES384, ES512), Ed25519 (EdDSA) or HMAC (HS256, HS384, HS512). "none" is
// never accepted, nor a header with crit, nor an algorithm that does not fit
// the key: an HMAC key is a secret of the config's, never a public key.

// maxTokenBytes bounds the token jwt reads.
const maxTokenBytes = 16 << 10

// key is a key tokens are verified with.
type key struct {
	id  string // kid, or "" for none
	alg string // the one algorithm it is for, or "" for any of its type's
	pub crypto.PublicKey
	// secret is an HMAC key's, with pub nil.
	secret []byte
}

// algorithm is a signing algorithm.
type algorithm struct {
	hash crypto.Hash
	kind byte // 'R' RSA PKCS#1 v1.5, 'P' RSA-PSS, 'E' ECDSA, 'O' Ed25519, 'H' HMAC
	size int  // ECDSA: the bytes of each of r and s
}

var algorithms = map[string]algorithm{
	"RS256": {crypto.SHA256, 'R', 0}, "RS384": {crypto.SHA384, 'R', 0}, "RS512": {crypto.SHA512, 'R', 0},
	"PS256": {crypto.SHA256, 'P', 0}, "PS384": {crypto.SHA384, 'P', 0}, "PS512": {crypto.SHA512, 'P', 0},
	"ES256": {crypto.SHA256, 'E', 32}, "ES384": {crypto.SHA384, 'E', 48}, "ES512": {crypto.SHA512, 'E', 66},
	"EdDSA": {0, 'O', 0},
	"HS256": {crypto.SHA256, 'H', 0}, "HS384": {crypto.SHA384, 'H', 0}, "HS512": {crypto.SHA512, 'H', 0},
}

// fits reports whether k may verify a signature of alg.
func (k *key) fits(name string, alg algorithm) bool {
	if k.alg != "" && k.alg != name {
		return false
	}
	switch pub := k.pub.(type) {
	case *rsa.PublicKey:
		return alg.kind == 'R' || alg.kind == 'P'
	case *ecdsa.PublicKey:
		return alg.kind == 'E' && (pub.Curve.Params().BitSize+7)/8 == alg.size
	case ed25519.PublicKey:
		return alg.kind == 'O'
	case nil:
		return alg.kind == 'H' && k.secret != nil
	}
	return false
}

// verify reports whether sig is k's signature of signed by alg, which fits.
func (k *key) verify(alg algorithm, signed, sig []byte) bool {
	var digest []byte
	if alg.hash != 0 && alg.kind != 'H' {
		h := alg.hash.New()
		h.Write(signed)
		digest = h.Sum(nil)
	}
	switch alg.kind {
	case 'R':
		return rsa.VerifyPKCS1v15(k.pub.(*rsa.PublicKey), alg.hash, digest, sig) == nil
	case 'P':
		return rsa.VerifyPSS(k.pub.(*rsa.PublicKey), alg.hash, digest, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}) == nil
	case 'E':
		if len(sig) != 2*alg.size {
			return false
		}
		r := new(big.Int).SetBytes(sig[:alg.size])
		s := new(big.Int).SetBytes(sig[alg.size:])
		return ecdsa.Verify(k.pub.(*ecdsa.PublicKey), digest, r, s)
	case 'O':
		return ed25519.Verify(k.pub.(ed25519.PublicKey), signed, sig)
	case 'H':
		m := hmac.New(alg.hash.New, k.secret)
		m.Write(signed)
		return hmac.Equal(m.Sum(nil), sig)
	}
	return false
}

// header is a JWS's protected header, as far as jwt reads it.
type header struct {
	Alg  string          `json:"alg"`
	Kid  string          `json:"kid"`
	Crit json.RawMessage `json:"crit"`
}

// token is a JWT taken apart, its signature not yet verified.
type token struct {
	header  header
	signed  []byte // the header and payload, as signed
	payload []byte
	sig     []byte
}

var (
	errMalformed = errors.New("the token is malformed")
	errAlgorithm = errors.New("the token's algorithm is not allowed")
)

// parseToken takes a compact JWS apart.
func parseToken(s string) (*token, error) {
	if len(s) > maxTokenBytes {
		return nil, errMalformed
	}
	first := strings.IndexByte(s, '.')
	last := strings.LastIndexByte(s, '.')
	if first < 0 || first == last || strings.IndexByte(s[first+1:last], '.') >= 0 {
		return nil, errMalformed
	}
	enc := base64.RawURLEncoding
	t := &token{signed: []byte(s[:last])}
	h, err := enc.DecodeString(s[:first])
	if err != nil {
		return nil, errMalformed
	}
	if t.payload, err = enc.DecodeString(s[first+1 : last]); err != nil {
		return nil, errMalformed
	}
	if t.sig, err = enc.DecodeString(s[last+1:]); err != nil {
		return nil, errMalformed
	}
	if err := json.Unmarshal(h, &t.header); err != nil {
		return nil, errMalformed
	}
	if t.header.Crit != nil {
		// No extension is understood, so none may be critical.
		return nil, errMalformed
	}
	return t, nil
}

// claims decodes the payload, keeping numbers as json.Number so that no
// precision is lost.
func (t *token) claims() (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(t.payload))
	d.UseNumber()
	var claims map[string]any
	if err := d.Decode(&claims); err != nil || claims == nil || d.More() {
		return nil, errMalformed
	}
	return claims, nil
}
