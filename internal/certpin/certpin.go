// Package certpin makes self-signed certificates and checks a peer's
// certificate against a pin, the SHA-256 of its leaf: what a listener that
// has no certificate from an authority presents, and what its dialers take
// from it in place of the chain and name checks.
package certpin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

// SelfSigned makes a self-signed ECDSA P-256 certificate for hosts (names
// or IP addresses; "localhost" when none), of the organization org, valid
// for ten years, and returns it and its key as PEM. Peers pin it by its
// Fingerprint.
func SelfSigned(org string, hosts ...string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	if len(hosts) == 0 {
		hosts = []string{"localhost"}
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hosts[0], Organization: []string{org}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// Fingerprint is the SHA-256 of a certificate's leaf, in lower-case hex, or
// "" for a certificate without one: what Parse takes.
func Fingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(sum[:])
}

// Parse reads a pin: 64 hex digits, in either case, with or without colons
// between the bytes.
func Parse(pin string) ([]byte, error) {
	b, err := hex.DecodeString(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(pin), ":", "")))
	if err != nil || len(b) != sha256.Size {
		return nil, fmt.Errorf("bad certificate pin %q: it is the SHA-256 of the certificate, 64 hex digits", pin)
	}
	return b, nil
}

// Verify returns a tls.Config VerifyPeerCertificate that takes exactly the
// leaf certificate whose SHA-256 is pin, compared in constant time; what
// names the peer in its errors, as "quic: server".
func Verify(pin []byte, what string) func(raw [][]byte, _ [][]*x509.Certificate) error {
	pin = append([]byte(nil), pin...)
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return errors.New(what + " sent no certificate")
		}
		sum := sha256.Sum256(raw[0])
		if subtle.ConstantTimeCompare(sum[:], pin) != 1 {
			return fmt.Errorf("%s certificate %x does not match the pin", what, sum)
		}
		return nil
	}
}

// Pinned sets conf to take exactly the certificate whose SHA-256 is pin, in
// place of the chain and name checks: InsecureSkipVerify, which only turns
// those off, with VerifyPeerCertificate checking the pin. A session cache
// is removed, so that no handshake resumes a session, which would skip
// VerifyPeerCertificate; and the pin is checked again in VerifyConnection,
// which a resumed handshake runs too, so that a cache set later cannot skip
// it. what names the peer in errors.
func Pinned(conf *tls.Config, pin []byte, what string) {
	verify := Verify(pin, what)
	conf.InsecureSkipVerify = true
	conf.VerifyPeerCertificate = verify
	conf.VerifyConnection = func(cs tls.ConnectionState) error {
		raw := make([][]byte, len(cs.PeerCertificates))
		for i, c := range cs.PeerCertificates {
			raw[i] = c.Raw
		}
		return verify(raw, nil)
	}
	conf.ClientSessionCache = nil
	conf.SessionTicketsDisabled = true
}
