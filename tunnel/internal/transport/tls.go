//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd || windows

package transport

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	fib "github.com/lesismal/fib"
	fibtls "github.com/lesismal/fib/tls"

	"github.com/lesismal/fit/internal/certpin"
)

// The TLS of the links: tls://host:port is the framing of tcp over TLS, and
// wss:// (package transport/ws) and quic:// run TLS too. A listener presents
// the certificate ListenOptions gives it; a dialer checks the hub's as the
// ClientOptions given to Dial say:
//
//	PinSHA256, the SHA-256 of the certificate (see Fingerprint), which
//	    stands in for the chain and name checks: for a self-signed hub
//	CAFile, a PEM file of the CAs that sign the hub's certificate, in place
//	    of the system's roots; the name is checked
//	InsecureSkipVerify, quic only: any certificate, for tests
//
// and without any of them by the system's roots and the name. The options
// are never read from the address, which ParseURL refuses with a query or a
// fragment, so that no address can change how a certificate is checked: one
// a page or an operator was handed included. A client keeps
// no session tickets, so every handshake is a full one that checks the
// certificate; the pin is checked again in VerifyConnection, which a resumed
// handshake runs too, so a session cache added later cannot skip it.

// TLSALPN is the TLS application protocol of tls:// links.
const TLSALPN = "fit-tunnel"

// NewSelfSigned makes a self-signed ECDSA certificate for hosts (names or
// IPs; "localhost" when none), valid for ten years, and returns it and its
// key as PEM. Clients pin it by its Fingerprint.
func NewSelfSigned(hosts ...string) (certPEM, keyPEM []byte, err error) {
	return certpin.SelfSigned("fit tunnel", hosts...)
}

// Fingerprint is the SHA-256 of a certificate's leaf, in hex: what a pin
// is.
func Fingerprint(cert tls.Certificate) string { return certpin.Fingerprint(cert) }

// ServerTLSConfig is a listener's TLS config for cert, offering alpn.
func ServerTLSConfig(cert tls.Certificate, alpn string) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{alpn},
		MinVersion:   tls.VersionTLS13,
	}
}

// ClientOptions says how a client checks the hub's certificate.
type ClientOptions struct {
	// ServerName is the name checked, and sent in SNI. The dials fill in
	// the host they dial when it is empty.
	ServerName string
	// PinSHA256, when set, accepts exactly the certificate whose
	// Fingerprint it is (colons and case are ignored), in place of the
	// chain and name checks: for a self-signed hub.
	PinSHA256 string
	// CAFile, when set, is a PEM file of the CAs that replace the system's
	// roots. It is read at each dial.
	CAFile string
	// RootCAs, when set, replaces the system's roots.
	RootCAs *x509.CertPool
	// InsecureSkipVerify accepts any certificate. For tests only.
	InsecureSkipVerify bool
}

// ParsePin is the SHA-256 a pin names, or an error.
func ParsePin(pin string) ([]byte, error) {
	b, err := certpin.Parse(pin)
	if err != nil {
		return nil, fmt.Errorf("tunnel: %q is not a certificate pin: the SHA-256 of the certificate in hex, 64 digits", pin)
	}
	return b, nil
}

// ClientTLSConfig is a client's TLS config for opts, offering alpn.
func ClientTLSConfig(opts ClientOptions, alpn string) (*tls.Config, error) {
	conf := &tls.Config{
		ServerName:         opts.ServerName,
		NextProtos:         []string{alpn},
		MinVersion:         tls.VersionTLS13,
		RootCAs:            opts.RootCAs,
		InsecureSkipVerify: opts.InsecureSkipVerify,
		// No ClientSessionCache: no handshake resumes a session, and each
		// checks the certificate in full.
	}
	if opts.CAFile != "" {
		pemBytes, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("tunnel: the hub's CAs: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("tunnel: the hub's CAs: %s has no PEM certificate", opts.CAFile)
		}
		conf.RootCAs = pool
	}
	if opts.PinSHA256 == "" {
		return conf, nil
	}
	pin, err := ParsePin(opts.PinSHA256)
	if err != nil {
		return nil, err
	}
	// The pin stands in for the chain and name checks, and is checked on a
	// resumed handshake too.
	certpin.Pinned(conf, pin, "tunnel: the hub")
	return conf, nil
}

// CheckBy is how a link checks the hub's certificate, of a hub's settings:
// pinned to pin, or else by the CAs of caFile, or else, if insecure, not at
// all; the zero ClientOptions, the system's roots, without any of them.
func CheckBy(pin, caFile string, insecure bool) ClientOptions {
	switch {
	case pin != "":
		return ClientOptions{PinSHA256: pin}
	case caFile != "":
		return ClientOptions{CAFile: caFile}
	}
	return ClientOptions{InsecureSkipVerify: insecure}
}

// TLSClientConfig is the client TLS config of a dial to u, an address
// ParseURL took, checking the hub's certificate as opts says, offering
// alpn; insecure says whether u's scheme may take any certificate. The name
// checked is opts.ServerName, or u's host.
func TLSClientConfig(u *url.URL, opts ClientOptions, alpn string, insecure bool) (*tls.Config, error) {
	if opts.InsecureSkipVerify && !insecure {
		return nil, fmt.Errorf("tunnel: %s:// takes no insecure: pin the hub's certificate, or name its CAs", u.Scheme)
	}
	if opts.ServerName == "" {
		opts.ServerName = u.Hostname()
	}
	return ClientTLSConfig(opts, alpn)
}

// tlsTransport is the framing of tcp over TLS: tls://host:port.
type tlsTransport struct{}

func (tlsTransport) Dial(engine *fib.Engine, addr string, timeout time.Duration, opts ClientOptions, open OpenFunc) (Conn, error) {
	u, err := HostURL(addr, "tls")
	if err != nil {
		return nil, err
	}
	hostport := u.Host
	conf, err := TLSClientConfig(u, opts, TLSALPN, false)
	if err != nil {
		return nil, err
	}
	return DialFib(func(done func(*fib.Connection, error)) error {
		w := &handshakeWait{inner: FibHandler(open), done: done}
		return fibtls.DialWithOptions(engine, "tcp", hostport, timeout, conf, fibtls.DialOptions{HandshakeTimeout: timeout}, w,
			func(_ *fib.Connection, err error) {
				if err != nil {
					w.finish(nil, err)
				}
			})
	})
}

// Listen serves tls://host:port with the certificate o gives.
func (tlsTransport) Listen(engine *fib.Engine, addr string, o ListenOptions, open OpenFunc) (Listener, error) {
	if o.Cert == nil {
		return nil, errors.New("tls: a listener needs a certificate")
	}
	hostport := strings.TrimPrefix(addr, "tls://")
	h := fibtls.NewServer(ServerTLSConfig(*o.Cert, TLSALPN), FibHandler(open))
	return ListenFib(engine, hostport, h, o.Filter, func(hp string) string { return "tls://" + hp })
}

// handshakeWait is the handler of a dial over TLS until, and around, the
// one it wraps, which opens once the handshake is over: a dial then fails
// with why the handshake did, a certificate that does not match among them,
// and the link's first frames go to a hub already checked.
type handshakeWait struct {
	inner fib.Handler
	done  func(*fib.Connection, error)
	once  sync.Once
	mu    sync.Mutex
	open  bool
}

func (w *handshakeWait) finish(c *fib.Connection, err error) {
	w.once.Do(func() { w.done(c, err) })
}

func (w *handshakeWait) OnOpen(*fib.Connection) {}

// OnHandshake opens the wrapped handler, before any of the hub's
// plaintext reaches it.
func (w *handshakeWait) OnHandshake(c *fib.Connection, _ tls.ConnectionState) {
	w.inner.OnOpen(c)
	w.mu.Lock()
	w.open = true
	w.mu.Unlock()
	w.finish(c, nil)
}

func (w *handshakeWait) OnData(c *fib.Connection, data []byte) { w.inner.OnData(c, data) }

func (w *handshakeWait) OnPriorityData(*fib.Connection, []byte) {}

func (w *handshakeWait) OnClose(c *fib.Connection, err error) {
	w.mu.Lock()
	open := w.open
	w.mu.Unlock()
	if open {
		w.inner.OnClose(c, err)
		return
	}
	if err == nil {
		err = ErrClosedOpening
	}
	w.finish(nil, err)
}

var _ fibtls.HandshakeHandler = (*handshakeWait)(nil)
