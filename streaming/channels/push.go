package channels

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/hub"
	"github.com/lesismal/fib/media/rtmp"
	"github.com/lesismal/fib/media/srt"

	"github.com/lesismal/fit/config"
)

// The states of a push target.
const (
	// PushConnected is a target that took the stream and is sent it.
	PushConnected = "connected"
	// PushConnecting is a target being dialed.
	PushConnecting = "connecting"
	// PushRetrying is a target that refused, dropped or could not be
	// reached, waiting to be tried again.
	PushRetrying = "retrying"
	// PushWaiting is a target whose stream is not published, waiting for
	// it.
	PushWaiting = "waiting"
	// PushStopped is a target the Admin API stopped.
	PushStopped = "stopped"
)

// pushWait is how long a push keeps its connection while its stream has no
// publisher, as a channel stopped and started again or a publisher that
// reconnects.
const pushWait = 5 * time.Second

// errNotPublished is why a push waits; errNoEngine why a manager without
// an engine to dial from cannot push.
var (
	errNotPublished = errors.New("the stream is not published")
	errNoEngine     = errors.New("no engine to dial from")
)

// Push is a push target: a stream of the hub relayed to an outside RTMP or
// SRT server. Its methods never block.
type Push struct {
	m *Manager
	u *config.PushURL
	// name and stream are t's, which a reload does not change.
	name, stream string

	mu      sync.Mutex
	t       config.PushTarget
	running bool
	removed bool
	// gen counts attempts and stops: a callback of another gen is of an
	// attempt that is over.
	gen     uint64
	state   string
	err     string
	since   time.Time // of the state
	backoff time.Duration
	retryAt time.Time
	timer   Timer
	cc      *rtmp.ClientConn
	sub     *hub.Subscription
	sess    *srt.Session
	// srtSent is the payload bytes of the SRT sessions ended; bytes0 the
	// bytes when the connection began, for its bit rate.
	srtSent uint64
	bytes0  uint64

	sent               atomic.Uint64 // media bytes handed to RTMP
	connects, failures atomic.Uint64
}

func newPush(m *Manager, t config.PushTarget, u *config.PushURL) *Push {
	return &Push{m: m, t: t, u: u, name: t.Name, stream: t.Stream, state: PushStopped, backoff: t.RetryMinOr()}
}

// Name returns the target's name.
func (p *Push) Name() string { return p.name }

// same reports whether t is p's target as it connects: the same stream,
// url and timeouts.
func (p *Push) same(t config.PushTarget) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.t.Stream == t.Stream && p.t.URL == t.URL && p.t.Connect() == t.Connect()
}

// retarget takes t's retry bounds and channel, for what it connects to
// is the same.
func (p *Push) retarget(t config.PushTarget) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.t = t
	p.backoff = min(max(p.backoff, t.RetryMinOr()), t.RetryMaxOr())
}

// Start starts a target that is stopped.
func (p *Push) Start() {
	p.mu.Lock()
	if p.running || p.removed {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.backoff = p.t.RetryMinOr()
	p.gen++
	gen := p.gen
	p.mu.Unlock()
	p.attempt(gen)
}

// Stop stops the target: its connection is closed, and it is not tried
// again until Start.
func (p *Push) Stop() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.running = false
	p.gen++
	p.set(PushStopped, "")
	closers := p.release()
	p.mu.Unlock()
	for _, f := range closers {
		f()
	}
}

// Restart connects the target again at once.
func (p *Push) Restart() {
	p.Stop()
	p.Start()
}

// remove ends the target for good.
func (p *Push) remove() {
	p.Stop()
	p.mu.Lock()
	p.removed = true
	p.mu.Unlock()
}

// set sets the target's state. p.mu is held.
func (p *Push) set(state, err string) {
	if p.state != state {
		p.since = p.m.clock.Now()
	}
	p.state, p.err = state, err
}

// release lets go of the target's connection and timer, returning what
// closes them, to be called without p.mu. p.mu is held.
func (p *Push) release() []func() {
	var out []func()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	if sub := p.sub; sub != nil {
		out = append(out, sub.Close)
	}
	if cc := p.cc; cc != nil {
		out = append(out, func() { _ = cc.Close() })
	}
	if s := p.sess; s != nil {
		p.srtSent += s.Stats().BytesSent
		out = append(out, s.Close)
	}
	p.cc, p.sub, p.sess = nil, nil, nil
	return out
}

// attempt dials the target, from a goroutine that is not the engine's: a
// timer, the Admin API or a reload.
func (p *Push) attempt(gen uint64) {
	p.mu.Lock()
	if gen != p.gen || !p.running {
		p.mu.Unlock()
		return
	}
	h := p.m.o.Hub
	if st, ok := h.Stream(p.t.Stream); !ok || !st.Publishing {
		p.retry(gen, PushWaiting, errNotPublished, false)
		p.mu.Unlock()
		return
	}
	if p.m.o.Dial == nil {
		p.retry(gen, PushRetrying, errNoEngine, true)
		p.mu.Unlock()
		return
	}
	p.set(PushConnecting, "")
	t, u := p.t, p.u
	p.mu.Unlock()
	switch u.Protocol {
	case config.PushProtocolSRT:
		p.dialSRT(gen, t, u)
	default:
		p.dialRTMP(gen, t)
	}
}

func (p *Push) dialRTMP(gen uint64, t config.PushTarget) {
	cfg := rtmp.ClientConfig{
		DialTimeout:      t.Connect(),
		HandshakeTimeout: t.Connect(),
		OnClose:          func(_ *rtmp.ClientConn, err error) { p.fail(gen, err) },
	}
	cc, err := rtmp.Publish(p.m.o.Dial, t.URL, cfg, func(cc *rtmp.ClientConn, err error) { p.ready(gen, cc, err) })
	if err != nil {
		p.fail(gen, err)
		return
	}
	p.mu.Lock()
	if gen == p.gen {
		p.cc = cc
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	_ = cc.Close()
}

// ready hears that the RTMP server took the publish, or why not: on a fib
// worker. The push subscribes to its stream, which sends the stream's
// tracks and cached pictures at once, without blocking.
func (p *Push) ready(gen uint64, cc *rtmp.ClientConn, err error) {
	if err != nil {
		p.fail(gen, err)
		return
	}
	p.mu.Lock()
	if gen != p.gen {
		p.mu.Unlock()
		_ = cc.Close()
		return
	}
	p.cc = cc
	sub, err := p.m.o.Hub.Subscribe(p.t.Stream, &pushSink{p: p, next: cc}, hub.SubscribeOptions{
		Info: hub.SubscribeInfo{Protocol: "push"},
		Wait: pushWait,
	})
	if err != nil {
		p.mu.Unlock()
		p.fail(gen, err)
		return
	}
	p.sub = sub
	p.connected()
	p.mu.Unlock()
}

func (p *Push) dialSRT(gen uint64, t config.PushTarget, u *config.PushURL) {
	cfg := srt.ClientConfig{
		StreamID:         u.StreamID,
		Passphrase:       u.Passphrase,
		PBKeyLen:         u.PBKeyLen,
		Latency:          u.Latency,
		HandshakeTimeout: t.Connect(),
		Wait:             pushWait,
		OnConnect: func(*srt.Session) {
			p.mu.Lock()
			if gen == p.gen {
				p.connected()
			}
			p.mu.Unlock()
		},
		OnClose: func(err error) {
			if err == nil {
				err = errors.New("the session ended")
			}
			p.fail(gen, err)
		},
	}
	s, err := srt.Publish(p.m.o.Dial, u.Host, p.m.o.Hub, t.Stream, cfg)
	if err != nil {
		p.fail(gen, err)
		return
	}
	p.mu.Lock()
	if gen == p.gen {
		p.sess = s
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	s.Close()
}

// connected marks the target connected. p.mu is held.
func (p *Push) connected() {
	p.connects.Add(1)
	p.set(PushConnected, "")
	p.bytes0 = p.bytesLocked()
}

// fail ends an attempt of gen that failed, or a connection that dropped,
// and tries again after the backoff.
func (p *Push) fail(gen uint64, err error) {
	p.mu.Lock()
	if gen != p.gen || !p.running {
		p.mu.Unlock()
		return
	}
	up := p.state == PushConnected
	if up && p.m.clock.Now().Sub(p.since) >= p.t.RetryMaxOr() {
		// It stayed up: the next retry waits the least.
		p.backoff = p.t.RetryMinOr()
	}
	p.failures.Add(1)
	p.gen++
	msg := p.redact(err)
	closers := p.release()
	p.retry(p.gen, PushRetrying, err, true)
	p.mu.Unlock()
	for _, f := range closers {
		f()
	}
	p.m.o.Log.Warnf("push %s to %s: %s", p.name, p.u.Redacted(), msg)
}

// retry arms the timer that tries gen again: after the backoff, which then
// doubles, up to retry_max, with grow; after retry_min, for a stream not
// published, without. p.mu is held.
func (p *Push) retry(gen uint64, state string, err error, grow bool) {
	p.set(state, p.redact(err))
	d := p.t.RetryMinOr()
	if grow {
		d = p.backoff
		p.backoff = min(2*p.backoff, p.t.RetryMaxOr())
	}
	p.retryAt = p.m.clock.Now().Add(d)
	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = p.m.clock.AfterFunc(d, func() { p.attempt(gen) })
}

// redact is err's text without the target's URL, which is a secret.
func (p *Push) redact(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if p.t.URL != "" {
		s = strings.ReplaceAll(s, p.t.URL, p.u.Redacted())
	}
	return s
}

// bytesLocked is the bytes the target sent. p.mu is held.
func (p *Push) bytesLocked() uint64 {
	n := p.sent.Load() + p.srtSent
	if p.sess != nil {
		n += p.sess.Stats().BytesSent
	}
	return n
}

// PushStatus is what a push target is doing, as the Admin API shows it.
type PushStatus struct {
	Name   string `json:"name"`
	Stream string `json:"stream"`
	// Channel is the channel whose push it is, "" for streaming's.
	Channel  string `json:"channel,omitempty"`
	Protocol string `json:"protocol"`
	// URL is the target's scheme, host and app, never its stream key.
	URL string `json:"url"`
	// State is connected, connecting, retrying, waiting or stopped, since
	// Since; Error why it retries or waits.
	State string    `json:"state"`
	Error string    `json:"error,omitempty"`
	Since time.Time `json:"since,omitzero"`
	// RetryIn is how long until the next attempt, in seconds, while it
	// retries or waits.
	RetryIn float64 `json:"retry_in,omitempty"`
	// Connects and Failures count connections taken and attempts that
	// failed or dropped; Bytes is what it sent, and Bitrate the bits a
	// second of its connection.
	Connects uint64 `json:"connects"`
	Failures uint64 `json:"failures"`
	Bytes    uint64 `json:"bytes"`
	Bitrate  int64  `json:"bitrate"`
}

// Status returns what the target is doing.
func (p *Push) Status() PushStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.m.clock.Now()
	st := PushStatus{Name: p.t.Name, Stream: p.t.Stream, Channel: p.t.Channel, Protocol: p.u.Protocol, URL: p.u.Redacted(),
		State: p.state, Error: p.err, Since: p.since, Connects: p.connects.Load(), Failures: p.failures.Load(), Bytes: p.bytesLocked()}
	if p.state == PushRetrying || p.state == PushWaiting {
		st.RetryIn = max(p.retryAt.Sub(now), 0).Seconds()
	}
	if p.state == PushConnected {
		if d := now.Sub(p.since); d > 0 {
			st.Bitrate = int64(float64(st.Bytes-p.bytes0) * 8 / d.Seconds())
		}
	}
	return st
}

// pushSink is an RTMP push's subscription: it counts what it hands on.
type pushSink struct {
	p    *Push
	next hub.Sink
}

func (s *pushSink) OnTracks(tracks []*av.Track) { s.next.OnTracks(tracks) }

func (s *pushSink) OnPacket(pkt *av.Packet) {
	s.p.sent.Add(uint64(len(pkt.Data)))
	s.next.OnPacket(pkt)
}

func (s *pushSink) OnClose(err error) { s.next.OnClose(err) }
