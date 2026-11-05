package quic

import (
	"crypto/tls"

	"github.com/lesismal/fit/tunnel/internal/transport"
)

// NewSelfSigned makes a self-signed ECDSA certificate for hosts (names or
// IPs; "localhost" when none), valid for ten years, and returns it and its
// key as PEM. Clients pin it by its Fingerprint.
func NewSelfSigned(hosts ...string) (certPEM, keyPEM []byte, err error) {
	return transport.NewSelfSigned(hosts...)
}

// Fingerprint is the SHA-256 of a certificate's leaf, in hex: what
// ClientOptions.PinSHA256 takes.
func Fingerprint(cert tls.Certificate) string { return transport.Fingerprint(cert) }

// ServerTLSConfig is a server's TLS config for cert.
func ServerTLSConfig(cert tls.Certificate) *tls.Config { return transport.ServerTLSConfig(cert, ALPN) }

// ClientOptions says how a client checks the server's certificate.
type ClientOptions = transport.ClientOptions

// ClientTLSConfig is a client's TLS config for opts.
func ClientTLSConfig(opts ClientOptions) (*tls.Config, error) {
	return transport.ClientTLSConfig(opts, ALPN)
}
