// Package tlsmgr gives listeners their TLS: the configuration they serve,
// and a Manager that picks each handshake's certificate, from files (Static)
// or obtained with ACME and kept in a Storage (ACME), or both (Certs).
package tlsmgr

import (
	"crypto/tls"
	"slices"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
)

// A Manager gives a handshake its certificate. A listener's TLS
// configuration asks it through tls.Config.GetCertificate, which fib calls on
// its handshake pool, never on a worker; a Manager still answers from memory,
// and leaves what may block (obtaining a certificate) to pools of its own.
type Manager interface {
	GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
}

// ACMEALPN is the protocol a CA asks for through ALPN when it checks a
// TLS-ALPN-01 challenge.
const ACMEALPN = "acme-tls/1"

// cipherSuites are the TLS 1.2 suites a listener allows: ECDHE for forward
// secrecy and AEAD ciphers only, as Mozilla's intermediate profile has them.
// TLS 1.3's suites are not configurable, and all of them qualify.
var cipherSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

// ServerConfig returns the TLS configuration of a listener serving
// protocols, whose certificates get picks. get is called for every full
// handshake, so the listener may change what it answers, on a reload or when
// a certificate is renewed, without the configuration changing: fib's TLS
// copies a configuration when it first uses it, and sees no change made to it
// after.
func ServerConfig(protocols []string, get func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, CipherSuites: cipherSuites, GetCertificate: get}
	h1 := slices.Contains(protocols, config.ProtocolHTTP1)
	h2 := slices.Contains(protocols, config.ProtocolH2)
	switch {
	case h1 && h2:
		cfg = fibhttp.ConfigureTLS(cfg)
	case h2:
		cfg.NextProtos = []string{"h2"}
	default:
		cfg.NextProtos = []string{"http/1.1"}
	}
	// Last, so that only a client that offers it alone, as a CA checking a
	// TLS-ALPN-01 challenge does, gets it.
	cfg.NextProtos = append(cfg.NextProtos, ACMEALPN)
	return cfg
}
