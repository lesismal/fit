package tlsmgr

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"github.com/lesismal/fit/config"
)

// ClientAuth is how a listener authenticates its clients by their
// certificates: what crypto/tls's Config.ClientAuth and ClientCAs are set to.
type ClientAuth struct {
	Type tls.ClientAuthType
	CAs  *x509.CertPool
	// Sum identifies the mode and the authorities, so that a listener can
	// tell whether a reload changed them.
	Sum [sha256.Size]byte

	files map[string][sha256.Size]byte
}

// LoadClientAuth loads the authorities of c's files. Its errors carry the
// path and position of the file that failed.
func LoadClientAuth(c *config.ClientAuth) (*ClientAuth, error) {
	a := &ClientAuth{Type: tls.RequireAndVerifyClientCert, CAs: x509.NewCertPool(), files: map[string][sha256.Size]byte{}}
	if c.Mode == config.ClientAuthVerifyIfGiven {
		a.Type = tls.VerifyClientCertIfGiven
	}
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00", a.Type)
	for i, path := range c.CAFiles {
		data, err := readFile(path)
		if err == nil {
			err = addCerts(a.CAs, data)
		}
		if err != nil {
			return nil, c.At.Field("ca_files").Index(i).Errorf("%v", err)
		}
		sum := sha256.Sum256(data)
		a.files[path] = sum
		h.Write(sum[:])
	}
	h.Sum(a.Sum[:0])
	return a, nil
}

// addCerts adds the certificates of a PEM file to pool, and fails for a file
// with none, or with one that does not parse.
func addCerts(pool *x509.CertPool, data []byte) error {
	n := 0
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return err
		}
		pool.AddCert(cert)
		n++
	}
	if n == 0 {
		return fmt.Errorf("no PEM certificate found")
	}
	return nil
}

// Files returns the SHA-256 of each file a was loaded from, by path, which
// the caller must not change.
func (a *ClientAuth) Files() map[string][sha256.Size]byte { return a.files }
