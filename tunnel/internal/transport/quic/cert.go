package quic

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"github.com/lesismal/fit/internal/certpin"
)

// NewSelfSigned makes a self-signed ECDSA certificate for hosts (names or
// IPs; "localhost" when none), valid for ten years, and returns it and its
// key as PEM. Clients pin it by its Fingerprint.
func NewSelfSigned(hosts ...string) (certPEM, keyPEM []byte, err error) {
	return certpin.SelfSigned("fit tunnel", hosts...)
}

// Fingerprint is the SHA-256 of a certificate's leaf, in hex: what
// ClientOptions.PinSHA256 takes.
func Fingerprint(cert tls.Certificate) string { return certpin.Fingerprint(cert) }

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
		pin, err := certpin.Parse(opts.PinSHA256)
		if err != nil {
			return nil, fmt.Errorf("quic: %w", err)
		}
		// The pin stands in for the chain and name checks.
		conf.InsecureSkipVerify = true
		conf.VerifyPeerCertificate = certpin.Verify(pin, "quic: server")
	}
	return conf, nil
}
