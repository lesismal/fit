package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	stdhttp "net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/certpin"
)

// The bounds of a member's connections: its calls go one at a time, a
// poll's after each other, so two connections and a few waiting are room
// enough for a poll and a logout.
const (
	maxConnsPerMember   = 2
	maxPendingPerMember = 4
	maxHeaderBytes      = 64 << 10
	// maxDialTimeout bounds a connect, below the call's own timeout.
	maxDialTimeout = 10 * time.Second
	// idleConnTimeout closes a connection kept between polls; the
	// member's own closes it first when shorter.
	idleConnTimeout = time.Minute
)

// limits are what bound a member's calls.
type limits struct {
	timeout  time.Duration
	maxBytes int64
}

// CertInfo is the certificate a member presented: its fingerprint, which a
// person confirms before it is pinned, and what it says. Verified is how it
// was trusted, "pin", "ca" or "system", or "" when it was not.
type CertInfo struct {
	Fingerprint string    `json:"fingerprint"`
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	Names       []string  `json:"names"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	SelfSigned  bool      `json:"self_signed"`
	Verified    string    `json:"verified,omitempty"`
	// at is when the handshake that saw it ran, and err what refused it.
	at  time.Time
	err *certError
}

// certError is a member's certificate refused: kind is KindTLSUntrusted or
// KindTLSPin.
type certError struct {
	kind string
	msg  string
}

func (e *certError) Error() string { return e.msg }

// transport is a member's connections: a fib client whose TLS checks the
// member's certificate as its TLS says, keeping what it saw.
type transport struct {
	client *fibhttp.Client
	base   *url.URL
	lim    limits
	// seen is the certificate of the latest handshake.
	seen atomic.Pointer[CertInfo]

	mu     sync.Mutex
	calls  map[*call]struct{}
	closed bool
}

// newTransport returns m's connections on engine. m was checked: its URL
// parses, and its CAs.
func newTransport(engine *fib.Engine, m *Member, lim limits) *transport {
	u, _ := url.Parse(strings.TrimSuffix(m.URL, "/"))
	t := &transport{base: u, lim: lim, calls: map[*call]struct{}{}}
	cc := fibhttp.DefaultClientConfig()
	cc.Timeout = lim.timeout
	cc.ResponseHeaderTimeout = lim.timeout
	cc.DialTimeout = min(lim.timeout, maxDialTimeout)
	cc.MaxConnsPerHost, cc.MaxPendingPerHost, cc.MaxIdleConnsPerHost = maxConnsPerMember, maxPendingPerMember, 1
	cc.IdleConnTimeout = idleConnTimeout
	cc.MaxResponseHeaderBytes = maxHeaderBytes
	cc.MaxResponseBodyBytes = lim.maxBytes
	cc.ExpectContinueTimeout = 0
	if u.Scheme == "https" {
		cc.TLSConfig = t.tlsConfig(m.TLS, u.Hostname())
	}
	t.client = fibhttp.NewClient(engine, cc)
	return t
}

// tlsConfig checks the member's certificate itself, in VerifyConnection,
// which every handshake runs, resumed or not: against the pin, the CAs, or
// the system's, keeping the certificate whatever comes of it, so that one
// not trusted can be shown to a person.
func (t *transport) tlsConfig(trust TLS, serverName string) *tls.Config {
	var pin []byte
	var pool *x509.CertPool
	switch {
	case trust.Fingerprint != "":
		pin, _ = certpin.Parse(trust.Fingerprint) // checked
	case trust.CA != "":
		pool, _ = caPool(trust.CA) // checked
	}
	verified := trust.Verified()
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
		// The chain and the name are checked below, as trust says, in
		// place of crypto/tls's own checks, so that a certificate refused
		// is still seen.
		InsecureSkipVerify:     true, //nolint:gosec
		SessionTicketsDisabled: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return &certError{kind: KindTLSUntrusted, msg: "the member sent no certificate"}
			}
			info := certInfo(cs.PeerCertificates[0])
			info.at = time.Now()
			var err *certError
			switch {
			case pin != nil:
				sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
				if !bytes.Equal(sum[:], pin) {
					err = &certError{kind: KindTLSPin, msg: "the member's certificate " + info.Fingerprint + " is not the one pinned"}
				}
			default:
				opts := x509.VerifyOptions{Roots: pool, DNSName: serverName, Intermediates: x509.NewCertPool()}
				for _, c := range cs.PeerCertificates[1:] {
					opts.Intermediates.AddCert(c)
				}
				if _, verr := cs.PeerCertificates[0].Verify(opts); verr != nil {
					err = &certError{kind: KindTLSUntrusted, msg: "the member's certificate " + info.Fingerprint + " is not trusted: " + verr.Error()}
				}
			}
			if err == nil {
				info.Verified = verified
			}
			info.err = err
			t.seen.Store(info)
			if err != nil {
				return err
			}
			return nil
		},
	}
}

// certInfo is what c says.
func certInfo(c *x509.Certificate) *CertInfo {
	sum := sha256.Sum256(c.Raw)
	names := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	return &CertInfo{Fingerprint: formatFingerprint(sum[:]), Subject: c.Subject.String(), Issuer: c.Issuer.String(),
		Names: names, NotBefore: c.NotBefore.UTC(), NotAfter: c.NotAfter.UTC(),
		SelfSigned: bytes.Equal(c.RawIssuer, c.RawSubject) && c.CheckSignatureFrom(c) == nil}
}

// reply is a member's answer, whole: at most the transport's maxBytes.
type reply struct {
	status int
	header stdhttp.Header
	body   []byte
}

// call is a call in flight, which the transport's close cancels.
type call struct {
	fc fibhttp.Call
}

// errClosed is a call to a member removed, or of a manager closing.
var errClosed = errors.New("the member's connections were closed")

// do sends method path, with header and body, to the member, and calls done
// once with its answer or why there is none, from a fib worker or the
// caller's goroutine. It never blocks.
func (t *transport) do(method, path string, header stdhttp.Header, body []byte, done func(*reply, error)) {
	u := *t.base
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	// The URL parsed when the member was checked, and the method is the
	// manager's own: the request is made.
	req, _ := stdhttp.NewRequest(method, u.String(), nil)
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "fit-cluster-manager")
	if body != nil {
		req.Body, req.ContentLength = fibhttp.NewBufferedBody(body), int64(len(body))
		req.Header.Set("Content-Type", "application/json")
	}
	k := &call{}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		done(nil, errClosed)
		return
	}
	t.calls[k] = struct{}{}
	t.mu.Unlock()
	// fib calls the callback once.
	fc := t.client.Start(req, func(resp *fibhttp.ClientResponse, err error) {
		t.mu.Lock()
		delete(t.calls, k)
		t.mu.Unlock()
		if err != nil {
			done(nil, err)
			return
		}
		// The body arrived whole, within maxBytes, which the client's
		// MaxResponseBodyBytes holds it to: reading it waits for nothing,
		// and cannot fail.
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		done(&reply{status: resp.StatusCode, header: resp.Header, body: data}, nil)
	})
	t.mu.Lock()
	k.fc = fc
	t.mu.Unlock()
}

// abandon abandons every call in flight; their done hear errClosed.
func (t *transport) abandon() {
	t.mu.Lock()
	calls := make([]*call, 0, len(t.calls))
	for k := range t.calls {
		calls = append(calls, k)
	}
	t.mu.Unlock()
	for _, k := range calls {
		t.mu.Lock()
		fc := k.fc
		t.mu.Unlock()
		fc.Cancel(errClosed)
	}
}

// close abandons the calls in flight and closes the connections.
func (t *transport) close() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.abandon()
	t.client.Close()
}

// The kinds of a Problem: what went wrong reaching a member, each told
// apart so that a person knows what to fix.
const (
	// KindUnreachable: no connection: refused, no route, no such host.
	KindUnreachable = "unreachable"
	// KindTimeout: the member did not answer in time.
	KindTimeout = "timeout"
	// KindTLSUntrusted: the member's certificate is not trusted by the
	// system's CAs or the member's ca; its fingerprint may be pinned.
	KindTLSUntrusted = "tls_untrusted"
	// KindTLSPin: the member's certificate is not the one pinned.
	KindTLSPin = "tls_pin_mismatch"
	// KindTLS: the TLS handshake failed otherwise.
	KindTLS = "tls"
	// KindAuth: the user name and password, or the token, were refused.
	KindAuth = "auth_failed"
	// KindRateLimited: the member refuses logins for a while, after too
	// many that failed.
	KindRateLimited = "rate_limited"
	// KindPasswordChange: the member's console asks for its temporary
	// password to be changed, which the manager does not do.
	KindPasswordChange = "password_change_required"
	// KindForbidden: the member refused the call (403).
	KindForbidden = "forbidden"
	// KindUnsupported: the member does not serve what the manager reads,
	// as a fit before GET /node.
	KindUnsupported = "unsupported"
	// KindStatus: the member answered an unexpected status.
	KindStatus = "http_status"
	// KindBadAnswer: the member's answer is not the JSON expected.
	KindBadAnswer = "bad_answer"
	// KindTooLarge: the member's answer is larger than max_response_bytes.
	KindTooLarge = "too_large"
)

// Problem is why a call to a member failed.
type Problem struct {
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	Status  int       `json:"status,omitempty"`
	At      time.Time `json:"at"`
	// RetryAfter is the seconds the member asks to wait, for
	// KindRateLimited.
	RetryAfter int `json:"retry_after,omitempty"`
	// Certificate is the certificate refused, for KindTLSUntrusted and
	// KindTLSPin.
	Certificate *CertInfo `json:"certificate,omitempty"`
}

// failure returns the Problem of err, a call's failure that started at
// start.
func (t *transport) failure(err error, start time.Time) *Problem {
	p := &Problem{Kind: KindUnreachable, Message: err.Error()}
	var ce *certError
	if seen := t.seen.Load(); seen != nil && seen.err != nil && !seen.at.Before(start) {
		ce = seen.err
		c := *seen
		c.err = nil
		p.Certificate = &c
	}
	if ce != nil || errors.As(err, &ce) {
		p.Kind, p.Message = ce.kind, ce.msg
		return p
	}
	var netErr net.Error
	var recordErr tls.RecordHeaderError
	var alert tls.AlertError
	switch {
	case errors.Is(err, fibhttp.ErrResponseBodyTooLarge) || errors.Is(err, fibhttp.ErrResponseHeaderTooLarge):
		p.Kind = KindTooLarge
		p.Message = "the member's answer is larger than max_response_bytes"
	case errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
		p.Kind = KindTimeout
	case errors.As(err, &recordErr) || errors.As(err, &alert) || strings.HasPrefix(err.Error(), "tls:"):
		p.Kind = KindTLS
	}
	return p
}
