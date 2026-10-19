// Package tlsmgr builds the TLS configurations listeners serve. For now
// they come from certificate files; certificate storage, ACME and OCSP come
// with M2.
package tlsmgr

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"slices"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
)

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

// ServerConfig loads c's certificates and returns the TLS configuration a
// listener serving protocols uses, and a fingerprint of the certificates, by
// which a reload tells whether they changed. Its errors carry the path and
// position of the file that failed.
//
// The certificates are handed to fib's TLS as Config.Certificates, from which
// a handshake takes the first that suits the client's hello, its server name
// above all, without a goroutine: fib runs such handshakes as state machines.
func ServerConfig(c *config.TLS, protocols []string) (*tls.Config, string, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, CipherSuites: cipherSuites}
	h := sha256.New()
	for i, cc := range c.Certificates {
		cert, err := tls.LoadX509KeyPair(cc.CertFile, cc.KeyFile)
		if err != nil {
			return nil, "", c.At.Field("certificates").Index(i).Errorf("%v", err)
		}
		cfg.Certificates = append(cfg.Certificates, cert)
		for _, der := range cert.Certificate {
			h.Write(der)
		}
	}
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
	for _, p := range cfg.NextProtos {
		h.Write([]byte(p + "\x00"))
	}
	return cfg, hex.EncodeToString(h.Sum(nil)), nil
}
