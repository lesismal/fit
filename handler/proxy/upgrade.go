package proxy

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"math/rand/v2"
	"net"
	stdhttp "net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/upstream"
)

// A request that asks to switch protocols, as a WebSocket handshake does,
// is passed on to the upstream as an HTTP/1.1 upgrade. If the upstream
// switches, the client's request is switched too, with the upstream's
// answer, and the two tunnels are joined: what arrives on one is sent on
// the other, each side's reads held while the other has more than
// tunnelWatermark waiting to go to its peer. An HTTP/1.1 client asks with
// its Upgrade and Connection fields; an HTTP/2 or HTTP/3 one with an
// extended CONNECT (RFC 8441, RFC 9220) for websocket, which goes upstream
// as the HTTP/1.1 handshake it stands for. Any other answer the upstream
// gives is the client's, passed on as any response is (exchange.
// onResponse), its body streamed back with backpressure past
// upstream.StreamThreshold. A request asking to switch to h2c is an offer
// to the hop it came over, which a listener that speaks h2c takes up
// itself, and is proxied as the ordinary request it also is.

// tunnelWatermark is how much may wait to go to one side of a tunnel before
// the other side's reads are held.
const tunnelWatermark = 64 << 10

// DefaultTunnelIdleTimeout is how long a tunnel may carry nothing either
// way before it is closed, unless tunnel_idle_timeout says otherwise.
const DefaultTunnelIdleTimeout = 10 * time.Minute

var errTunnelIdle = errors.New("proxy: the tunnel was idle past its timeout")

// upgradeProtocol returns the protocol r asks to switch to, and whether it
// asks with an extended CONNECT, or "" for a request that asks for none.
func upgradeProtocol(r *stdhttp.Request) (protocol string, extended bool) {
	if r.Method == stdhttp.MethodConnect {
		if values := r.Header[":protocol"]; len(values) == 1 && values[0] != "" {
			return values[0], true
		}
		return "", false
	}
	if r.ProtoMajor != 1 || !headerHasToken(r.Header["Connection"], "upgrade") {
		return "", false
	}
	for _, value := range r.Header["Upgrade"] {
		name, _, _ := strings.Cut(value, ",")
		if name = strings.TrimSpace(name); name != "" && !strings.EqualFold(name, "h2c") {
			return name, false
		}
	}
	return "", false
}

// headerHasToken reports whether a comma-separated field's values include
// token, ignoring case.
func headerHasToken(values []string, token string) bool {
	for _, value := range values {
		for item := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(item), token) {
				return true
			}
		}
	}
	return false
}

// relay is a request that asked to switch protocols: its handshake with the
// upstream, and once both sides have switched, the tunnel between them.
type relay struct {
	p        *Proxy
	c        *fibhttp.Context
	pool     *upstream.Pool
	backend  *upstream.Backend
	protocol string
	extended bool
	idle     time.Duration
	// x is the exchange the upstream's request was built in. It holds the
	// handshake's Call and why it was abandoned, its cause, under its
	// callMu, and takes over an answer that does not switch (refused).
	x *exchange

	// client and up are the two tunnels, once each has switched; clientH1
	// says the client's is a connection of its own, whose output can be
	// waited for without a goroutine.
	client, up *fibhttp.Tunnel
	clientH1   bool

	holdingClient, holdingUp atomic.Bool
	// last is when data last went either way, in Unix nanoseconds.
	last  atomic.Int64
	timer *time.Timer
	shard *relaySet
	ended atomic.Int32 // sides that have closed
	// tunneled is 1 once the upstream's connection became a tunnel and the
	// pool was told, and 2 once the relay finished, whichever came first.
	tunneled atomic.Int32
	once     sync.Once
	mu       sync.Mutex
}

// serveUpgrade passes a request that asks to switch protocols on to pool.
func (p *Proxy) serveUpgrade(c *fibhttp.Context, pool *upstream.Pool, protocol string, extended bool) {
	if extended && !strings.EqualFold(protocol, "websocket") {
		// Only a WebSocket has an HTTP/1.1 handshake to stand for it.
		_ = c.Respond(stdhttp.StatusNotImplemented, "text/plain; charset=utf-8", []byte("501 Not Implemented\n"))
		return
	}
	if pool.Saturated() {
		// Turned away before anything is built for it, as any request is.
		p.busy(c)
		return
	}
	x := &exchange{c: c, own: true}
	if !p.outgoing(c, x) {
		_ = c.Respond(stdhttp.StatusBadRequest, "text/plain; charset=utf-8", badRequest)
		return
	}
	out := &x.base
	out.Body, out.ContentLength, out.GetBody = nil, 0, nil
	out.Method = stdhttp.MethodGet
	h := out.Header
	if extended {
		// An HTTP/2 or HTTP/3 WebSocket has no key: the handshake upstream
		// needs one, and its accept goes no further.
		key := make([]byte, 16)
		_, _ = crand.Read(key)
		h["Sec-Websocket-Key"] = []string{base64.StdEncoding.EncodeToString(key)}
		protocol = "websocket"
	}
	h["Connection"] = []string{"Upgrade"}
	h["Upgrade"] = []string{protocol}
	r := &relay{p: p, c: c, pool: pool, protocol: protocol, extended: extended, idle: p.tunnelIdle,
		clientH1: c.Request.ProtoMajor == 1, x: x}
	x.start = time.Now()
	r.backend = pool.Pick(c.Request)
	// The request is held until both sides have switched, or the client has
	// its answer; the config until the tunnel is over.
	c.Retain()
	p.graph.Retain()
	p.addRelay(r)
	c.OnCancel(func(error) { r.abandon(errClientGone) })
	x.callMu.Lock()
	x.call = pool.SendUpgrade(r.backend, out, (*upSide)(r), r.answer)
	call, cause := x.call, x.cause.Load()
	x.callMu.Unlock()
	if cause != nil {
		call.Cancel(*cause)
	}
}

// abandon gives up the handshake for cause, or closes the tunnel once it
// is open.
func (r *relay) abandon(cause error) {
	r.x.cause.CompareAndSwap(nil, &cause)
	r.mu.Lock()
	client, up := r.client, r.up
	r.mu.Unlock()
	if client == nil || up == nil {
		// The handshake, or the body of a refusal, which the same Call
		// abandons.
		r.x.cancel(r.x.gen.Load(), cause)
		return
	}
	_ = client.Close()
	_ = up.Close()
}

// answer deals with the upstream's answer to the handshake. It runs in the
// upstream client's callback, before the upstream tunnel's handler opens.
func (r *relay) answer(resp *fibhttp.ClientResponse, err error) {
	c := r.c
	defer c.Release()
	if err != nil {
		cause := r.causeErr()
		// A handshake the client or a drain gave up on is no failure of the
		// backend's.
		r.finish(cause == nil)
		if !errors.Is(cause, errClientGone) {
			_ = c.Respond(upgradeStatus(err), "text/plain; charset=utf-8", nil)
		}
		return
	}
	up := resp.Tunnel()
	if up == nil {
		r.refused(resp)
		return
	}
	header := resp.Header.Clone()
	removeHopByHop(header)
	delete(header, "Content-Length")
	if r.extended {
		delete(header, "Sec-Websocket-Accept")
	}
	if r.tunneled.CompareAndSwap(0, 1) {
		// Out of the client's pool: its connection is the tunnel's now.
		r.pool.Tunneled()
	}
	r.mu.Lock()
	r.up = up
	r.mu.Unlock()
	r.last.Store(time.Now().UnixNano())
	// Given up before the upstream switched, as a client gone before the
	// handshake was sent, whose cancel came too late for the call.
	if cause := r.causeErr(); cause != nil {
		_ = up.Close()
		r.finish(false)
		if !errors.Is(cause, errClientGone) {
			_ = c.Respond(upgradeStatus(cause), "text/plain; charset=utf-8", nil)
		}
		return
	}
	client, err := c.Upgrade(r.protocol, header, (*clientSide)(r))
	if err != nil {
		_ = up.Close()
		r.finish(false)
		if !errors.Is(r.causeErr(), errClientGone) {
			_ = c.Respond(stdhttp.StatusBadGateway, "text/plain; charset=utf-8", nil)
		}
		return
	}
	r.mu.Lock()
	r.client = client
	r.mu.Unlock()
	// The relay paces each side by the other's backlog, with HoldReads; a
	// connection's own pacing of its reads by its backlog would stop it
	// reading the peer whose answers it waits to pass on (see fib's
	// Connection.SetWriteBackpressure). The upstream's connection is the
	// tunnel's alone, and so is an HTTP/1 client's.
	up.Conn().SetWriteBackpressure(false)
	if r.clientH1 {
		client.Conn().SetWriteBackpressure(false)
	}
	if cause := r.x.cause.Load(); cause != nil {
		r.abandon(*cause)
		return
	}
	if r.idle > 0 {
		r.timer = time.AfterFunc(r.idle, r.checkIdle)
	}
}

// upgradeStatus is the status of a handshake that got no answer.
func upgradeStatus(err error) int {
	var netErr net.Error
	switch {
	case errors.Is(err, fibhttp.ErrResponseHeaderTimeout), errors.Is(err, os.ErrDeadlineExceeded),
		errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return stdhttp.StatusGatewayTimeout
	case errors.Is(err, errDrained), errors.Is(err, upstream.ErrBusy), errors.Is(err, upstream.ErrClosed):
		return stdhttp.StatusServiceUnavailable
	}
	return stdhttp.StatusBadGateway
}

// refused passes on an answer that did not switch, which is the client's:
// the relay's exchange takes it, and what the relay holds, the backend, the
// config and the request, and answers as it answers any request, its body,
// however large, streamed back as the client takes it, bounded by the
// route's response idle and write stall timeouts. It runs in the upstream
// client's callback, as answer does, whose hold on the request it keeps.
func (r *relay) refused(resp *fibhttp.ClientResponse) {
	if r.extended && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// A 2xx to an HTTP/2 or HTTP/3 extended CONNECT says the tunnel is
		// open (RFC 8441 section 5, RFC 9220), and what follows it would
		// reach the client as WebSocket frames, though the upstream never
		// switched: its body, any bytes it chose. Nor may a 2xx to a
		// CONNECT carry a length (RFC 9110 section 9.3.6). A backend that
		// answers a handshake with a success of HTTP/1.1's has no tunnel to
		// offer, which is the gateway's failure to the client; its 3xx, 4xx
		// and 5xx pass on as they are. Not counted against the backend,
		// whose plain answers serve other requests as well as ever.
		resp.Close()
		r.finish(false)
		if !errors.Is(r.causeErr(), errClientGone) {
			_ = r.c.Respond(stdhttp.StatusBadGateway, "text/plain; charset=utf-8", nil)
		}
		return
	}
	x, p := r.x, r.p
	x.pool, x.backend, x.graph, x.stall, x.timeouts = r.pool, r.backend, p.graph, p.stall, r.pool.Timeouts()
	// In the proxy's open exchanges before it leaves its relays, so that a
	// drain finds it in one or the other; abandon reaches it from either,
	// through the Call they share.
	x.open = &p.open[rand.Uint32()%openShards]
	x.open.add(x)
	r.once.Do(func() { p.removeRelay(r) })
	// The exchange's own hold, which its release gives back; answer's
	// deferred Release gives back the relay's.
	r.c.Retain()
	x.onResponse(resp, nil)
}

func (r *relay) causeErr() error { return r.x.causeErr() }

// checkIdle closes a tunnel that carried nothing for its idle timeout, and
// looks again when it last carried something otherwise.
func (r *relay) checkIdle() {
	since := time.Since(time.Unix(0, r.last.Load()))
	if since >= r.idle {
		r.abandon(errTunnelIdle)
		return
	}
	r.timer.Reset(r.idle - since)
}

// sideClosed counts a side's tunnel ending, and ends the other's: once the
// second has ended, the relay is over.
func (r *relay) sideClosed(other *fibhttp.Tunnel) {
	if other != nil {
		other.CloseAfterSend()
	}
	if r.ended.Add(1) == 2 {
		r.finish(false)
	}
}

// finish lets go of what the relay holds, once: the backend, the config,
// its place among the proxy's relays.
func (r *relay) finish(failed bool) {
	r.once.Do(func() {
		if r.timer != nil {
			r.timer.Stop()
		}
		r.p.removeRelay(r)
		if r.tunneled.Swap(2) == 1 {
			r.pool.Untunneled()
		}
		r.pool.Done(r.backend, failed)
		r.p.graph.Release()
	})
}

// pass sends data, which arrived on from, to to, and holds from's reads
// while to has more than the watermark waiting.
func (r *relay) pass(from, to *fibhttp.Tunnel, toH1 bool, holding *atomic.Bool, data []byte) {
	r.last.Store(time.Now().UnixNano())
	if to.Send(data) != nil || to.Unsent() <= tunnelWatermark || !holding.CompareAndSwap(false, true) {
		return
	}
	from.HoldReads(true)
	resume := func(error) {
		from.HoldReads(false)
		holding.Store(false)
	}
	if toH1 {
		// The connection says when its output has drained; nothing waits.
		if !to.Conn().NotifyOutput(tunnelWatermark, resume) {
			resume(nil)
		}
		return
	}
	// An HTTP/2 or HTTP/3 stream can only be waited for (fib-api-requests,
	// A30): a goroutine waits, one at most a held side.
	go func() { resume(to.WaitSendable(tunnelWatermark / 2)) }()
}

// clientSide is the client tunnel's handler.
type clientSide relay

func (s *clientSide) OnTunnelOpen(*fibhttp.Tunnel) {}

func (s *clientSide) OnTunnelData(t *fibhttp.Tunnel, data []byte) {
	r := (*relay)(s)
	r.mu.Lock()
	up := r.up
	r.mu.Unlock()
	// The upstream's tunnel is an HTTP/1.1 connection of its own.
	r.pass(t, up, true, &r.holdingClient, data)
}

func (s *clientSide) OnTunnelClose(*fibhttp.Tunnel, error) {
	r := (*relay)(s)
	r.mu.Lock()
	up := r.up
	r.mu.Unlock()
	r.sideClosed(up)
}

// upSide is the upstream tunnel's handler. It opens once answer has
// returned, by when the client's tunnel is open, or the handshake failed
// and it is closed.
type upSide relay

func (s *upSide) OnTunnelOpen(t *fibhttp.Tunnel) {
	r := (*relay)(s)
	r.mu.Lock()
	client := r.client
	r.mu.Unlock()
	if client == nil {
		_ = t.Close()
	}
}

func (s *upSide) OnTunnelData(t *fibhttp.Tunnel, data []byte) {
	r := (*relay)(s)
	r.mu.Lock()
	client := r.client
	r.mu.Unlock()
	if client == nil {
		return
	}
	r.pass(t, client, r.clientH1, &r.holdingUp, data)
}

func (s *upSide) OnTunnelClose(*fibhttp.Tunnel, error) {
	r := (*relay)(s)
	r.mu.Lock()
	client := r.client
	r.mu.Unlock()
	if client == nil {
		return // the handshake failed, and finished the relay
	}
	r.sideClosed(client)
}

// relaySet is a proxy's relays, for a drain to close.
type relaySet struct {
	mu     sync.Mutex
	relays map[*relay]struct{}
}

func (p *Proxy) addRelay(r *relay) {
	s := &p.relays[rand.Uint32()%openShards]
	r.shard = s
	s.mu.Lock()
	if s.relays == nil {
		s.relays = map[*relay]struct{}{}
	}
	s.relays[r] = struct{}{}
	s.mu.Unlock()
}

func (p *Proxy) removeRelay(r *relay) {
	s := r.shard
	s.mu.Lock()
	delete(s.relays, r)
	s.mu.Unlock()
}

// drainRelays closes the tunnels, and gives up the handshakes, still open.
func (p *Proxy) drainRelays() {
	var open []*relay
	for i := range p.relays {
		s := &p.relays[i]
		s.mu.Lock()
		for r := range s.relays {
			open = append(open, r)
		}
		s.mu.Unlock()
	}
	for _, r := range open {
		r.abandon(errDrained)
	}
}
