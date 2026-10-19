package tlsmgr

import (
	"crypto/tls"
	"errors"
	"strings"

	"github.com/lesismal/fit/config"
)

// Static is a Manager of certificates loaded from files, indexed by the
// names they are for, so that a handshake finds its certificate without
// looking at the others. It does not change once loaded; a reload loads
// another.
type Static struct {
	certs []*tls.Certificate
	// exact holds, for each name a certificate is for (a host name in lower
	// case, or an IP address), the certificates for it in the order given;
	// wildcard holds the same for the parent of each "*." name.
	exact    map[string][]int
	wildcard map[string][]int
}

// LoadStatic loads c's certificates. Its errors carry the path and position
// of the certificate that failed.
func LoadStatic(c *config.TLS) (*Static, error) {
	s := &Static{exact: map[string][]int{}, wildcard: map[string][]int{}}
	for i, cc := range c.Certificates {
		cert, err := tls.LoadX509KeyPair(cc.CertFile, cc.KeyFile)
		if err != nil {
			return nil, c.At.Field("certificates").Index(i).Errorf("%v", err)
		}
		s.add(&cert)
	}
	return s, nil
}

// NewStatic returns a Static of certs, which must have their Leaf set.
func NewStatic(certs ...*tls.Certificate) *Static {
	s := &Static{exact: map[string][]int{}, wildcard: map[string][]int{}}
	for _, c := range certs {
		s.add(c)
	}
	return s
}

func (s *Static) add(cert *tls.Certificate) {
	i := len(s.certs)
	s.certs = append(s.certs, cert)
	leaf := cert.Leaf
	if leaf == nil {
		return
	}
	for _, name := range leaf.DNSNames {
		name = strings.ToLower(name)
		if parent, ok := strings.CutPrefix(name, "*."); ok {
			s.wildcard[parent] = appendOnce(s.wildcard[parent], i)
		} else {
			s.exact[name] = appendOnce(s.exact[name], i)
		}
	}
	for _, ip := range leaf.IPAddresses {
		s.exact[ip.String()] = appendOnce(s.exact[ip.String()], i)
	}
}

func appendOnce(list []int, i int) []int {
	if len(list) > 0 && list[len(list)-1] == i {
		return list
	}
	return append(list, i)
}

// Certificates returns s's certificates, which the caller must not change.
func (s *Static) Certificates() []*tls.Certificate { return s.certs }

// errNoCertificates is what a Static with no certificates answers.
var errNoCertificates = errors.New("tlsmgr: no certificates")

// GetCertificate returns the first of the certificates for the name the
// client asks for that the client supports, or the first for that name if it
// supports none; the first of all when none is for that name, or the client
// names none.
func (s *Static) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if len(s.certs) == 0 {
		return nil, errNoCertificates
	}
	if len(s.certs) == 1 || hello.ServerName == "" {
		return s.certs[0], nil
	}
	name := strings.ToLower(strings.TrimSuffix(hello.ServerName, "."))
	exact := s.exact[name]
	var wild []int
	// An IP address has no wildcard: no "*." name's parent looks like the
	// last parts of one.
	if _, parent, ok := strings.Cut(name, "."); ok {
		wild = s.wildcard[parent]
	}
	// The only certificate for the name is served whatever the client
	// supports, so there is nothing to check.
	switch {
	case len(exact) == 1 && len(wild) == 0:
		return s.certs[exact[0]], nil
	case len(exact) == 0 && len(wild) == 1:
		return s.certs[wild[0]], nil
	}
	// The two lists are each in the order given; walk them as one.
	first := -1
	for len(exact) > 0 || len(wild) > 0 {
		var i int
		if len(wild) == 0 || len(exact) > 0 && exact[0] < wild[0] {
			i, exact = exact[0], exact[1:]
		} else {
			i, wild = wild[0], wild[1:]
		}
		if first < 0 {
			first = i
		}
		if hello.SupportsCertificate(s.certs[i]) == nil {
			return s.certs[i], nil
		}
	}
	if first >= 0 {
		return s.certs[first], nil
	}
	return s.certs[0], nil
}
