package quic

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

// NewSelfSigned makes a self-signed ECDSA certificate for hosts (names or
// IPs; "localhost" when none), valid for ten years, and returns it and its
// key as PEM. Clients pin it by its Fingerprint.
func NewSelfSigned(hosts ...string) (certPEM, keyPEM []byte, err error) {
	return selfSigned(hosts)
}

func selfSigned(hosts []string) (certPEM, keyPEM []byte, err error) {
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
		Subject:      pkix.Name{CommonName: hosts[0], Organization: []string{"fit tunnel"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
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

// Fingerprint is the SHA-256 of a certificate's leaf, in hex: what
// ClientOptions.PinSHA256 takes.
func Fingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(sum[:])
}

// ServerTLSConfig is a server's TLS config for cert.
func ServerTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{ALPN},
		MinVersion:   tls.VersionTLS13,
	}
}

// ClientOptions says how a client checks the server's certificate.
type ClientOptions struct {
	// ServerName is the name checked, and sent in SNI. Dial fills in the
	// host it dials when it is empty.
	ServerName string
	// PinSHA256, when set, accepts exactly the server certificate whose
	// Fingerprint it is (colons and case are ignored), in place of the
	// usual chain and name checks: for a self-signed server.
	PinSHA256 string
	// InsecureSkipVerify accepts any certificate. For testing only.
	InsecureSkipVerify bool
	// RootCAs, if set, replaces the system roots.
	RootCAs *x509.CertPool
}

// ClientTLSConfig is a client's TLS config for opts.
func ClientTLSConfig(opts ClientOptions) (*tls.Config, error) {
	conf := &tls.Config{
		ServerName:         opts.ServerName,
		NextProtos:         []string{ALPN},
		MinVersion:         tls.VersionTLS13,
		RootCAs:            opts.RootCAs,
		InsecureSkipVerify: opts.InsecureSkipVerify,
	}
	if opts.PinSHA256 != "" {
		pin, err := hex.DecodeString(strings.ToLower(strings.ReplaceAll(opts.PinSHA256, ":", "")))
		if err != nil || len(pin) != sha256.Size {
			return nil, fmt.Errorf("quic: bad certificate pin %q", opts.PinSHA256)
		}
		// The pin stands in for the chain and name checks.
		conf.InsecureSkipVerify = true
		conf.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("quic: server sent no certificate")
			}
			sum := sha256.Sum256(raw[0])
			if subtle.ConstantTimeCompare(sum[:], pin) != 1 {
				return fmt.Errorf("quic: server certificate %x does not match the pin", sum)
			}
			return nil
		}
	}
	return conf, nil
}
