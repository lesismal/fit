package tlsmgr

import (
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lesismal/fit/config"
)

// Static is a Manager of certificates loaded from files, indexed by the
// names they are for, so that a handshake finds its certificate without
// looking at the others. It does not change once loaded; a reload loads
// another, as a FileWatcher has done when the files change.
type Static struct {
	certs []*tls.Certificate
	// exact holds, for each name a certificate is for (a host name in lower
	// case, or an IP address), the certificates for it in the order given;
	// wildcard holds the same for the parent of each "*." name.
	exact    map[string][]int
	wildcard map[string][]int
	// ocsp and staples, set by Staple, keep each certificate's OCSP
	// response, by index: nil for one with none to keep.
	ocsp    *OCSP
	staples []*Staple
	// files holds the SHA-256 of each certificate and key file loaded, by
	// path, so that a watcher can tell when one changes; nil for a Static
	// not loaded from files.
	files map[string][sha256.Size]byte
}

// MaxCertFileBytes is the largest certificate or key file LoadStatic reads.
const MaxCertFileBytes = 1 << 20

// LoadStatic loads c's certificates. Its errors carry the path and position
// of the certificate that failed.
func LoadStatic(c *config.TLS) (*Static, error) {
	s := &Static{exact: map[string][]int{}, wildcard: map[string][]int{}, files: map[string][sha256.Size]byte{}}
	for i, cc := range c.Certificates {
		certPEM, err := s.read(cc.CertFile)
		var keyPEM []byte
		if err == nil {
			keyPEM, err = s.read(cc.KeyFile)
		}
		var cert tls.Certificate
		if err == nil {
			cert, err = tls.X509KeyPair(certPEM, keyPEM)
		}
		if err != nil {
			return nil, c.At.Field("certificates").Index(i).Errorf("%v", err)
		}
		s.add(&cert)
	}
	return s, nil
}

// read reads the file at path, up to MaxCertFileBytes, and keeps its sum.
func (s *Static) read(path string) ([]byte, error) {
	data, err := readFile(path)
	if err == nil {
		s.files[path] = sha256.Sum256(data)
	}
	return data, err
}

// readFile reads the file at path, refusing one past MaxCertFileBytes.
func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxCertFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxCertFileBytes {
		return nil, fmt.Errorf("%s: larger than %d bytes", path, MaxCertFileBytes)
	}
	return data, nil
}

// Files returns the SHA-256 of each file s was loaded from, by path, which
// the caller must not change; nil for a Static made by NewStatic.
func (s *Static) Files() map[string][sha256.Size]byte { return s.files }

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

// cert returns the ith certificate, with its OCSP response stapled if it
// has one.
func (s *Static) cert(i int) *tls.Certificate {
	if s.staples != nil {
		if st := s.staples[i]; st != nil {
			return st.Certificate()
		}
	}
	return s.certs[i]
}

// Staple has o keep the OCSP responses of s's certificates, which s's
// handshakes then get, until Release. It is called once, before s serves.
func (s *Static) Staple(o *OCSP) {
	s.ocsp = o
	s.staples = make([]*Staple, len(s.certs))
	for i, c := range s.certs {
		s.staples[i] = o.Add(c, nil)
	}
}

// Release gives back what Staple took.
func (s *Static) Release() {
	if s.ocsp == nil {
		return
	}
	for _, st := range s.staples {
		s.ocsp.Release(st)
	}
}

// Certificates returns s's certificates, which the caller must not change.
func (s *Static) Certificates() []*tls.Certificate { return s.certs }

// Has reports whether s has a certificate for name, exactly or by a
// wildcard.
func (s *Static) Has(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if len(s.exact[name]) > 0 {
		return true
	}
	_, parent, ok := strings.Cut(name, ".")
	return ok && len(s.wildcard[parent]) > 0
}

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
		return s.cert(0), nil
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
		return s.cert(exact[0]), nil
	case len(exact) == 0 && len(wild) == 1:
		return s.cert(wild[0]), nil
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
			return s.cert(i), nil
		}
	}
	if first >= 0 {
		return s.cert(first), nil
	}
	return s.cert(0), nil
}
