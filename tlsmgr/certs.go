package tlsmgr

import (
	"crypto/tls"
	"errors"
)

// Certs is a listener's Manager: the certificates obtained with ACME for the
// names it lists, and its certificates from files for every other name, and
// for a listed name until ACME has one.
type Certs struct {
	static *Static // nil when the listener has no files
	acme   *ACME   // nil when it lists no names
	names  map[string]bool
	first  string // the first name listed, for a client that names none
}

// errNoCertificate is what a handshake gets when there is no certificate to
// give it: a listed name ACME has none for yet, and no files.
var errNoCertificate = errors.New("tlsmgr: no certificate yet for this name")

// NewCerts returns the Manager of a listener with the certificates from files
// static, which may be nil, and names, which a obtains certificates for.
func NewCerts(static *Static, a *ACME, names []string) *Certs {
	c := &Certs{static: static}
	if a != nil && len(names) > 0 {
		c.acme, c.names, c.first = a, make(map[string]bool, len(names)), normalizeName(names[0])
		for _, n := range names {
			c.names[normalizeName(n)] = true
		}
	}
	return c
}

// Static returns the certificates from files, or nil.
func (c *Certs) Static() *Static { return c.static }

func (c *Certs) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if c.acme != nil {
		name := normalizeName(hello.ServerName)
		if name == "" && c.static == nil {
			name = c.first
		}
		if c.names[name] {
			if cert := c.acme.Certificate(name); cert != nil {
				return cert, nil
			}
		}
	}
	if c.static != nil {
		return c.static.GetCertificate(hello)
	}
	return nil, errNoCertificate
}
