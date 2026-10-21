package auth

import (
	"crypto/x509"
	stdhttp "net/http"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
)

// ClientCert lets a request through only if its client sent a certificate
// that the listener verified, which a listener does whose tls.client_auth
// is set: with mode require every client has one, and with verify_if_given
// this middleware is what makes a route need one. The certificate's subject
// becomes who the request was authenticated as, unless basic_auth said so
// before it.
type ClientCert struct {
	Optional      bool   `json:"optional,omitempty" doc:"Let a request without a certificate through as well, unauthenticated; refused with 403 Forbidden when left out."`
	SubjectHeader string `json:"subject_header,omitempty" doc:"A request field to set to the certificate's subject, as in \"CN=alice,O=Example\", for the handler and the upstream a proxy sends the request to; any the client sent is removed, with or without a certificate."`

	subjectHeader string // canonical
}

// Provision checks the field name.
func (cc *ClientCert) Provision(ctx *module.Context) error {
	if cc.SubjectHeader != "" {
		if !validName(cc.SubjectHeader) {
			return ctx.Errorf("subject_header", "%q is not a field name", cc.SubjectHeader)
		}
		cc.subjectHeader = stdhttp.CanonicalHeaderKey(cc.SubjectHeader)
	}
	return nil
}

// Wrap returns next behind the check.
func (cc *ClientCert) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		if cc.subjectHeader != "" {
			delete(c.Request.Header, cc.subjectHeader)
		}
		cert := ClientCertificate(c.Request)
		if cert == nil {
			if !cc.Optional {
				_ = c.Respond(stdhttp.StatusForbidden, "text/plain; charset=utf-8", forbidden)
				return
			}
			next.ServeHTTP(c)
			return
		}
		subject := cert.Subject.String()
		if cc.subjectHeader != "" {
			c.Request.Header[cc.subjectHeader] = []string{headerSafe(subject)}
		}
		if UserOf(c) == "" {
			setUser(c, subject)
		}
		next.ServeHTTP(c)
	})
}

var forbidden = []byte("Forbidden\n")

// ClientCertificate returns the certificate the client of r sent and the
// listener verified, or nil when there is none.
func ClientCertificate(r *stdhttp.Request) *x509.Certificate {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return nil
	}
	return r.TLS.PeerCertificates[0]
}

// headerSafe replaces the bytes a field value may not hold, the control
// characters but tab, with '?'.
func headerSafe(s string) string {
	for i := range len(s) {
		if c := s[i]; c < 0x20 && c != '\t' || c == 0x7f {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if c := b[j]; c < 0x20 && c != '\t' || c == 0x7f {
					b[j] = '?'
				}
			}
			return string(b)
		}
	}
	return s
}
