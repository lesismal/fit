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
	acme   *ACME   // nil when it lists no names and has no on-demand TLS
	names  map[string]bool
	first  string // the first name listed, for a client that names none
	// onDemand has a obtain a certificate during the handshake for a name
	// neither listed nor in the files.
	onDemand bool
}

// errNoChallenge is what a handshake for ACMEALPN gets with no TLS-ALPN-01
// challenge under way for its name.
var errNoChallenge = errors.New("tlsmgr: no tls-alpn-01 challenge under way for this name")

// isACMETLS reports whether hello offers ACMEALPN alone, as a CA checking a
// TLS-ALPN-01 challenge does (RFC 8737).
func isACMETLS(hello *tls.ClientHelloInfo) bool {
	return len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == ACMEALPN
}

// errNoCertificate is what a handshake gets when there is no certificate to
// give it: a listed name ACME has none for yet, and no files.
var errNoCertificate = errors.New("tlsmgr: no certificate yet for this name")

// NewCerts returns the Manager of a listener with the certificates from files
// static, which may be nil, and names, which a obtains certificates for, and,
// with onDemand, any other name a client asks for that a allows (see
// ACME.OnDemand).
func NewCerts(static *Static, a *ACME, names []string, onDemand bool) *Certs {
	c := &Certs{static: static, names: map[string]bool{}}
	if a == nil {
		return c
	}
	if len(names) > 0 {
		c.acme, c.first = a, normalizeName(names[0])
		for _, n := range names {
			c.names[normalizeName(n)] = true
		}
	}
	if onDemand {
		c.acme, c.onDemand = a, true
	}
	return c
}

// Release gives back what Staple took for the certificates from files.
func (c *Certs) Release() {
	if c.static != nil {
		c.static.Release()
	}
}

// Static returns the certificates from files, or nil.
func (c *Certs) Static() *Static { return c.static }

// Certificates returns the certificates c serves: those from files, then
// those obtained so far for its names, in no order. With on-demand TLS, a
// handshake gets any certificate ACME has for a name the files do not
// cover, so those are listed too.
func (c *Certs) Certificates() []*tls.Certificate {
	var certs []*tls.Certificate
	if c.static != nil {
		certs = append(certs, c.static.Certificates()...)
	}
	if c.acme != nil {
		for name, cert := range *c.acme.certs.Load() {
			if c.names[name] || c.onDemand && (c.static == nil || !c.static.Has(name)) {
				certs = append(certs, cert)
			}
		}
	}
	return certs
}

func (c *Certs) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if c.acme != nil && isACMETLS(hello) {
		// A CA checking a TLS-ALPN-01 challenge, which gets the challenge's
		// certificate or nothing.
		if cert := c.acme.ALPNChallenge(hello.ServerName); cert != nil && (c.names[normalizeName(hello.ServerName)] || c.onDemand) {
			return cert, nil
		}
		return nil, errNoChallenge
	}
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
	if c.onDemand && hello.ServerName != "" && (c.static == nil || !c.static.Has(hello.ServerName)) {
		// Blocks, on fib's handshake pool, for at most on_demand's
		// timeout.
		return c.acme.OnDemand(hello.ServerName)
	}
	if c.static != nil {
		return c.static.GetCertificate(hello)
	}
	return nil, errNoCertificate
}
