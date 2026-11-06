package model

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	stdhttp "net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
)

// Limits bound a client's calls; a field left zero takes its default.
type Limits struct {
	// MaxStreamBytes bounds a streamed answer, which Request.MaxBytes may
	// lower or raise for a call; 8 MiB.
	MaxStreamBytes int64
	// MaxEventBytes bounds a line of an answer, and an SSE event's data;
	// 1 MiB.
	MaxEventBytes int
	// MaxToolCalls bounds the tool calls of an answer; 64.
	MaxToolCalls int
	// MaxErrorBytes is how much of a failed call's body is read for its
	// message; 8 KiB.
	MaxErrorBytes int
	// MaxModelsBytes bounds a model list; 4 MiB.
	MaxModelsBytes int64
	// Backoff is the wait before the first retry, doubled for each one
	// after it up to MaxBackoff, which also bounds the Retry-After an API
	// asks for; 500ms and 10s.
	Backoff, MaxBackoff time.Duration
}

func (l Limits) withDefaults() Limits {
	if l.MaxStreamBytes <= 0 {
		l.MaxStreamBytes = 8 << 20
	}
	if l.MaxEventBytes <= 0 {
		l.MaxEventBytes = 1 << 20
	}
	if l.MaxToolCalls <= 0 {
		l.MaxToolCalls = 64
	}
	if l.MaxErrorBytes <= 0 {
		l.MaxErrorBytes = 8 << 10
	}
	if l.MaxModelsBytes <= 0 {
		l.MaxModelsBytes = 4 << 20
	}
	if l.Backoff <= 0 {
		l.Backoff = 500 * time.Millisecond
	}
	if l.MaxBackoff <= 0 {
		l.MaxBackoff = 10 * time.Second
	}
	return l
}

// The bounds of a client's connections, for the few calls a console's
// users make at once.
const (
	maxConnsPerHost   = 8
	maxPendingPerHost = 32
	maxIdlePerHost    = 4
	idleConnTimeout   = time.Minute
	maxHeaderBytes    = 64 << 10
	// maxTransports bounds the transports a client keeps: one per connect
	// timeout and pinned certificate the providers ask for.
	maxTransports = 16
	// maxModelsWait bounds the wait for a model list, whatever the
	// provider's timeouts.
	maxModelsWait = 30 * time.Second
)

// Client calls model APIs on fib's asynchronous HTTP client, over the
// connections of an engine it is given, which must be running.
type Client struct {
	engine *fib.Engine
	lim    Limits

	mu         sync.Mutex
	closed     bool
	transports map[transportKey]*transport
	order      []transportKey // oldest first
}

// transportKey is what sets a transport apart: the connect timeout, and the
// SHA-256 of the pinned certificate.
type transportKey struct {
	connect time.Duration
	pin     [sha256.Size]byte
}

// transport is one fib client and the calls it carries; one retired, past
// maxTransports, is closed once its last call ends.
type transport struct {
	key      transportKey
	client   *fibhttp.Client
	inflight int
	retired  bool
}

// NewClient returns a client of engine's connections.
func NewClient(engine *fib.Engine, lim Limits) *Client {
	return &Client{engine: engine, lim: lim.withDefaults(), transports: map[transportKey]*transport{}}
}

// Close closes the client's connections; the calls in flight fail.
func (c *Client) Close() {
	c.mu.Lock()
	ts := c.transports
	c.closed, c.transports, c.order = true, nil, nil
	c.mu.Unlock()
	for _, t := range ts {
		t.client.Close()
	}
}

// acquire returns the transport for p, counting a call on it.
func (c *Client) acquire(p *Provider) (*transport, error) {
	key := transportKey{connect: orDur(p.ConnectTimeout, DefaultConnectTimeout)}
	if p.PinnedCert != nil {
		key.pin = sha256.Sum256(p.PinnedCert)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrClosed
	}
	t := c.transports[key]
	if t == nil {
		if len(c.order) >= maxTransports {
			old := c.transports[c.order[0]]
			delete(c.transports, c.order[0])
			c.order = c.order[1:]
			old.retired = true
			if old.inflight == 0 {
				old.client.Close()
			}
		}
		t = &transport{key: key, client: fibhttp.NewClient(c.engine, clientConfig(key.connect, p.PinnedCert))}
		c.transports[key] = t
		c.order = append(c.order, key)
	}
	t.inflight++
	return t, nil
}

// release ends a call on t.
func (c *Client) release(t *transport) {
	c.mu.Lock()
	t.inflight--
	closeIt := t.retired && t.inflight == 0
	c.mu.Unlock()
	if closeIt {
		t.client.Close()
	}
}

func clientConfig(connect time.Duration, pin []byte) fibhttp.ClientConfig {
	cc := fibhttp.DefaultClientConfig()
	cc.Timeout = 0 // each call's own timers
	cc.DialTimeout = connect
	cc.MaxConnsPerHost, cc.MaxPendingPerHost, cc.MaxIdleConnsPerHost = maxConnsPerHost, maxPendingPerHost, maxIdlePerHost
	cc.IdleConnTimeout = idleConnTimeout
	cc.MaxResponseHeaderBytes = maxHeaderBytes
	// Every body streams as it arrives: a call's own bounds hold it.
	cc.StreamResponseBody = true
	cc.StreamResponseBodyThreshold = 0
	cc.StreamUnknownLengthAtHeader = true
	cc.MaxStreamedBodyBytes = 1 << 30
	if pin != nil {
		want := bytes.Clone(pin)
		cc.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			// The certificate is checked against the one pinned, below, in
			// place of a CA's chain: llm.proxy's own, on loopback.
			InsecureSkipVerify: true, //nolint:gosec
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 || !bytes.Equal(raw[0], want) {
					return errors.New("the server's certificate is not the one pinned")
				}
				return nil
			},
		}
	}
	return cc
}

// sink takes the body of a call's answer that succeeded.
type sink interface {
	data(p []byte) error
	end() error
}

// Call is a call in flight, which Cancel abandons.
type Call struct {
	c        *Client
	p        *Provider
	t        *transport
	method   string
	url      string
	body     []byte
	header   stdhttp.Header
	max      int64       // the bound on the answer's body
	newSink  func() sink // the answer's parse, for each attempt
	firstEnd time.Duration
	done     func(error)

	mu       sync.Mutex
	attempts int
	fc       fibhttp.Call
	resp     *fibhttp.ClientResponse
	cause    error // why the call is being abandoned
	ended    bool
	waiting  bool // between attempts
	timer    *time.Timer
	total    *time.Timer
	retry    *time.Timer
	bytes    int64
}

// Cancel abandons the call: its done callback hears err, or
// context.Canceled's text when err is nil, unless it has heard already.
// It may be called from any goroutine, any number of times.
func (k *Call) Cancel(err error) {
	if k == nil {
		return
	}
	if err == nil {
		err = errCanceled
	}
	k.abort(err)
}

var errCanceled = errors.New("the call was cancelled")

// Attempts returns how many times the call was sent so far.
func (k *Call) Attempts() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.attempts
}

// prepare returns a call of method to u with body, whose answer newSink
// parses, and which calls done once with its outcome; run starts it.
func (c *Client) prepare(p *Provider, method, u string, body []byte, max int64, firstByte time.Duration, newSink func() sink, done func(error)) *Call {
	return &Call{c: c, p: p, method: method, url: u, body: body, max: max, newSink: newSink, firstEnd: firstByte, done: done}
}

// run starts the call; done may run before it returns, for a call refused
// at once.
func (k *Call) run() *Call {
	p, c, done := k.p, k.c, k.done
	pu, err := url.Parse(k.url)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		k.ended = true
		done(fmt.Errorf("%s: %q is not an http or https URL", p.Name, p.BaseURL))
		return k
	}
	if p.Key != "" && pu.Scheme == "http" && !loopback(pu.Hostname()) {
		k.ended = true
		done(fmt.Errorf("%s: %w", p.Name, ErrKeyInClear))
		return k
	}
	k.header = headers(p, k.method)
	if k.t, err = c.acquire(p); err != nil {
		k.ended = true
		done(fmt.Errorf("%s: %w", p.Name, err))
		return k
	}
	k.mu.Lock()
	k.total = time.AfterFunc(orDur(p.Timeout, DefaultTimeout), func() { k.abort(ErrTimeout) })
	k.mu.Unlock()
	k.start()
	return k
}

// headers returns the headers of a call to p: its own, then the key's and
// the body's, which they cannot replace.
func headers(p *Provider, method string) stdhttp.Header {
	h := stdhttp.Header{}
	for name, v := range p.Headers {
		h.Set(name, v)
	}
	key := string(p.Key)
	switch p.Kind {
	case KindAnthropic:
		if key != "" {
			h.Set("x-api-key", key)
		}
		h.Set("anthropic-version", AnthropicVersion)
	case KindGemini:
		if key != "" {
			h.Set("x-goog-api-key", key)
		}
	case KindAzureOpenAI:
		if key != "" {
			h.Set("api-key", key)
		}
	default:
		if key != "" {
			h.Set("Authorization", "Bearer "+key)
		}
	}
	if method == stdhttp.MethodPost {
		h.Set("Content-Type", "application/json")
		if p.Kind == KindOllama {
			h.Set("Accept", "application/x-ndjson")
		} else {
			h.Set("Accept", "text/event-stream")
		}
	} else {
		h.Set("Accept", "application/json")
	}
	return h
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// start sends the call once more.
func (k *Call) start() {
	k.mu.Lock()
	if k.ended {
		k.mu.Unlock()
		return
	}
	if cause := k.cause; cause != nil {
		k.mu.Unlock()
		k.finish(cause)
		return
	}
	k.attempts++
	k.waiting, k.resp, k.bytes = false, nil, 0
	k.armLocked(k.firstEnd, ErrFirstByte)
	k.mu.Unlock()
	req, _ := stdhttp.NewRequest(k.method, k.url, nil) // the URL parsed in begin
	req.Header = k.header.Clone()
	if k.body != nil {
		req.Body, req.ContentLength = fibhttp.NewBufferedBody(k.body), int64(len(k.body))
	}
	fc := k.t.client.Start(req, k.onResponse)
	k.mu.Lock()
	k.fc = fc
	cause := k.cause
	k.mu.Unlock()
	if cause != nil {
		fc.Cancel(cause)
	}
}

// armLocked (re)starts the timer that abandons the call with cause after d.
func (k *Call) armLocked(d time.Duration, cause error) {
	if k.timer != nil {
		k.timer.Stop()
	}
	k.timer = time.AfterFunc(d, func() { k.abort(cause) })
}

// abort abandons the call for cause: the attempt in flight is cancelled,
// and its callbacks end the call; one waiting to be tried again ends now.
func (k *Call) abort(cause error) {
	k.mu.Lock()
	if k.ended || k.cause != nil {
		k.mu.Unlock()
		return
	}
	k.cause = cause
	fc, resp, waiting, retry := k.fc, k.resp, k.waiting, k.retry
	k.mu.Unlock()
	switch {
	case waiting:
		// A retry that already fired finds the cause in start.
		if retry.Stop() {
			k.finish(cause)
		}
	case resp != nil:
		resp.Close()
	default:
		fc.Cancel(cause)
	}
}

// finish ends the call once with err, or with its success.
func (k *Call) finish(err error) {
	k.mu.Lock()
	if k.ended {
		k.mu.Unlock()
		return
	}
	k.ended = true
	for _, t := range []*time.Timer{k.timer, k.total, k.retry} {
		if t != nil {
			t.Stop()
		}
	}
	k.mu.Unlock()
	k.c.release(k.t)
	if err != nil {
		err = k.describe(err)
	}
	k.done(err)
}

// describe names the provider in err and takes the key out of it.
func (k *Call) describe(err error) error {
	var ae *APIError
	if errors.As(err, &ae) {
		ae.Provider, ae.Message = k.p.Name, redact(ae.Message, k.p.Key)
		return ae
	}
	msg := redact(err.Error(), k.p.Key)
	if msg != err.Error() {
		return fmt.Errorf("%s: %s", k.p.Name, msg)
	}
	return fmt.Errorf("%s: %w", k.p.Name, err)
}

// again tries the call once more after wait, when it may be: it has been
// tried at most Retries times and is not being abandoned. It reports
// whether it will.
func (k *Call) again(wait time.Duration) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ended || k.cause != nil || k.attempts > k.p.Retries {
		return false
	}
	if k.timer != nil {
		k.timer.Stop()
	}
	k.waiting = true
	k.retry = time.AfterFunc(wait, k.start)
	return true
}

// backoff is the wait before the next attempt: what the API asked for in
// h, or the client's, doubled for each attempt made.
func (k *Call) backoff(h stdhttp.Header) time.Duration {
	lim := k.c.lim
	if h != nil {
		if ms, err := strconv.Atoi(h.Get("Retry-After-Ms")); err == nil && ms >= 0 {
			return min(time.Duration(ms)*time.Millisecond, lim.MaxBackoff)
		}
		if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil && s >= 0 {
			return min(time.Duration(s)*time.Second, lim.MaxBackoff)
		}
	}
	k.mu.Lock()
	n := k.attempts
	k.mu.Unlock()
	d := lim.Backoff
	for i := 1; i < n && d < lim.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, lim.MaxBackoff)
}

func (k *Call) onResponse(resp *fibhttp.ClientResponse, err error) {
	if err != nil {
		k.mu.Lock()
		cause := k.cause
		k.mu.Unlock()
		if cause != nil {
			k.finish(cause)
			return
		}
		if k.again(k.backoff(nil)) {
			return
		}
		k.finish(err)
		return
	}
	k.mu.Lock()
	if k.ended {
		k.mu.Unlock()
		resp.Close()
		return
	}
	k.resp = resp
	k.mu.Unlock()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		k.failed(resp)
	} else {
		k.succeeded(resp)
	}
	k.mu.Lock()
	cause := k.cause
	k.mu.Unlock()
	if cause != nil {
		// Abandoned while the body's function was being set: it may not
		// hear of it.
		k.finish(cause)
	}
}

// failed reads a little of a failed answer's body for its message, then
// tries the call again or ends it.
func (k *Call) failed(resp *fibhttp.ClientResponse) {
	var msg []byte
	status, header := resp.StatusCode, resp.Header
	var once sync.Once
	settle := func() {
		once.Do(func() {
			ae := &APIError{Status: status, Message: apiMessage(msg)}
			if retryable(status) && k.again(k.backoff(header)) {
				return
			}
			k.finish(ae)
		})
	}
	resp.OnBody(func(data []byte, fin bool, err error) {
		k.mu.Lock()
		cause := k.cause
		k.mu.Unlock()
		if cause != nil {
			k.finish(cause)
			return
		}
		if room := k.c.lim.MaxErrorBytes - len(msg); room > 0 {
			msg = append(msg, data[:min(room, len(data))]...)
		}
		if err != nil || fin {
			settle()
			return
		}
		if len(msg) >= k.c.lim.MaxErrorBytes {
			// Enough to say why: the rest is not read.
			settle()
			resp.Close()
		}
	})
}

// succeeded hands the answer's body to the call's parse as it arrives.
func (k *Call) succeeded(resp *fibhttp.ClientResponse) {
	s := k.newSink()
	resp.OnBody(func(data []byte, fin bool, err error) {
		k.mu.Lock()
		cause, ended := k.cause, k.ended
		k.mu.Unlock()
		switch {
		case ended:
			return
		case cause != nil:
			k.finish(cause)
			return
		case err != nil:
			k.finish(fmt.Errorf("the answer broke off: %w", err))
			return
		}
		if len(data) > 0 {
			k.mu.Lock()
			k.bytes += int64(len(data))
			over := k.bytes > k.max
			if !fin {
				k.armLocked(orDur(k.p.IdleTimeout, DefaultIdleTimeout), ErrIdle)
			}
			k.mu.Unlock()
			if over {
				k.stop(resp, ErrTooLarge)
				return
			}
			if perr := s.data(data); perr != nil {
				k.stop(resp, perr)
				return
			}
		}
		if fin {
			k.finish(s.end())
		}
	})
}

// stop ends the call with err, and the rest of its answer is not read.
func (k *Call) stop(resp *fibhttp.ClientResponse, err error) {
	if err == errStopped {
		// The call was abandoned while it was parsed: its cause ends it.
		k.mu.Lock()
		err = k.cause
		k.mu.Unlock()
	}
	k.finish(err)
	resp.Close()
}
