//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd || windows

package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	fib "github.com/lesismal/fib"
	fibtls "github.com/lesismal/fib/tls"
)

// The TLS of the links: tls://host:port is the framing of tcp over TLS, and
// wss:// (package transport/ws) and quic:// run TLS too. A listener presents
// the certificate ListenOptions gives it; a dialer checks the hub's by what
// the address's query says:
//
//	pin=SHA-256 of the certificate (see Fingerprint), which stands in for
//	    the chain and name checks: for a self-signed hub
//	ca=a PEM file of the CAs that sign the hub's certificate, in place of
//	    the system's roots; the name is checked
//	insecure=1, quic only: any certificate, for tests
//
// and without any of them by the system's roots and the name. A client keeps
// no session tickets, so every handshake is a full one that checks the
// certificate; the pin is checked again in VerifyConnection, which a resumed
// handshake runs too, so a session cache added later cannot skip it.

// TLSALPN is the TLS application protocol of tls:// links.
const TLSALPN = "fit-tunnel"

// NewSelfSigned makes a self-signed ECDSA certificate for hosts (names or
// IPs; "localhost" when none), valid for ten years, and returns it and its
// key as PEM. Clients pin it by its Fingerprint.
func NewSelfSigned(hosts ...string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	if len(hosts) == 0 {
		hosts = []string{"localhost"}
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hosts[0], Organization: []string{"fit tunnel"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// Fingerprint is the SHA-256 of a certificate's leaf, in hex: what a pin
// is.
func Fingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(sum[:])
}

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
	b, err := hex.DecodeString(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(pin), ":", "")))
	if err != nil || len(b) != sha256.Size {
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
	check := func(leaf []byte) error {
		sum := sha256.Sum256(leaf)
		if subtle.ConstantTimeCompare(sum[:], pin) != 1 {
			return fmt.Errorf("tunnel: the hub's certificate %x does not match the pin", sum)
		}
		return nil
	}
	// The pin stands in for the chain and name checks.
	conf.InsecureSkipVerify = true
	conf.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return errors.New("tunnel: the hub sent no certificate")
		}
		return check(raw[0])
	}
	// A resumed handshake skips VerifyPeerCertificate but not this.
	conf.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("tunnel: the hub sent no certificate")
		}
		return check(cs.PeerCertificates[0].Raw)
	}
	return conf, nil
}

// ClientQuery is the query of a dial's address that says how it checks the
// hub's certificate: pinned to pin, or by the CAs of caFile, or, if
// insecure, not at all; "" for the system's roots.
func ClientQuery(pin, caFile string, insecure bool) string {
	switch {
	case pin != "":
		return "pin=" + url.QueryEscape(pin)
	case caFile != "":
		return "ca=" + url.QueryEscape(caFile)
	case insecure:
		return "insecure=1"
	}
	return ""
}

// ParseClientQuery is the ClientOptions of a dial address's query.
func ParseClientQuery(query string) (ClientOptions, error) {
	q, err := url.ParseQuery(query)
	if err != nil {
		return ClientOptions{}, err
	}
	insecure, _ := strconv.ParseBool(q.Get("insecure"))
	return ClientOptions{PinSHA256: q.Get("pin"), CAFile: q.Get("ca"), InsecureSkipVerify: insecure}, nil
}

// TLSClientConfig is the client TLS config of a dial to addr, a URL whose
// query says how the hub's certificate is checked, offering alpn; insecure
// says whether addr may take any certificate. It returns the host and port
// to dial.
func TLSClientConfig(addr, alpn string, insecure bool) (string, *tls.Config, error) {
	base, query, _ := strings.Cut(addr, "?")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.Port() == "" {
		return "", nil, fmt.Errorf("tunnel: %q is not a URL with a host and port", base)
	}
	opts, err := ParseClientQuery(query)
	if err != nil {
		return "", nil, err
	}
	if opts.InsecureSkipVerify && !insecure {
		return "", nil, fmt.Errorf("tunnel: %s:// takes no insecure: pin the hub's certificate, or name its CAs", u.Scheme)
	}
	opts.ServerName = u.Hostname()
	conf, err := ClientTLSConfig(opts, alpn)
	if err != nil {
		return "", nil, err
	}
	return u.Host, conf, nil
}

// tlsTransport is the framing of tcp over TLS: tls://host:port.
type tlsTransport struct{}

func (tlsTransport) Dial(engine *fib.Engine, addr string, timeout time.Duration, open OpenFunc) (Conn, error) {
	hostport, conf, err := TLSClientConfig(addr, TLSALPN, false)
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
	hostport, _, _ := strings.Cut(strings.TrimPrefix(addr, "tls://"), "?")
	h := fibtls.NewServer(ServerTLSConfig(*o.Cert, TLSALPN), FibHandler(open))
	return ListenFib(engine, hostport, h, func(hp string) string { return "tls://" + hp })
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
