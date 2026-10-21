// Package l4 proxies layer 4: each connection a tcp listener takes goes to
// an address of its service's upstream, and what either side sends is
// passed to the other as it comes, untouched.
//
// A udp listener's sessions go the same way. fib makes each address that
// sends the listener a datagram a connection of its own; its session dials
// a UDP socket of its own to an upstream address, and each datagram either
// side sends is sent on as one datagram. UDP has no backpressure: what
// cannot be sent is dropped, as the network would drop it, and so are the
// datagrams past Watermark that arrive before the upstream's socket is
// open. A session ends only by its idle timeout, a drain, or an upstream
// that refuses it, after which the client's next datagram starts another.
//
// Nothing waits. The upstream's connection is dialed through fib's engine,
// asynchronously, while the client's reads are held; each side's data is
// sent on from the callback that received it; and a side's reads are held
// while the other has more than Watermark of it unsent, until
// Connection.NotifyOutput says that has drained. Both connections' own
// write backpressure is off, as fib's relays have it, since the relay paces
// each by the other.
//
// A side that ends ends the other once what it sent has gone. A connection
// that carries nothing either way for the service's idle timeout is closed,
// and so is one still open when its graph is drained.
package l4

import (
	"errors"
	"math/rand/v2"
	"net"
	stdhttp "net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	fib "github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/upstream"
)

// Watermark is how much may wait to go to one side before the other side's
// reads are held.
const Watermark = 64 << 10

// maxAttempts bounds the addresses a connection's dial is tried on.
const maxAttempts = 3

const shards = 16

// Service proxies the connections of its tcp listeners, and the sessions of
// its udp listeners, to its upstream.
type Service struct {
	name     string
	pool     *upstream.Pool
	engine   *fib.Engine
	idle     time.Duration
	udpIdle  time.Duration
	maxConns int64
	attempts int
	// proxy is the PROXY protocol version sent to the upstream first, or 0.
	proxy int
	conns atomic.Int64
	open  [shards]relaySet
}

// NewService returns the service c, whose upstream is pool, dialing through
// engine, the engine pool dials through. A nil engine makes a service that
// closes every connection, for a config that is only checked.
func NewService(c *config.Service, pool *upstream.Pool, engine *fib.Engine) *Service {
	s := &Service{name: c.Name, pool: pool, engine: engine, idle: c.Idle(), udpIdle: c.UDPIdle(), maxConns: int64(c.Conns()),
		attempts: min(maxAttempts, len(pool.Backends()))}
	switch c.ProxyProtocol {
	case config.ProxyProtocolV1:
		s.proxy = 1
	case config.ProxyProtocolV2:
		s.proxy = 2
	}
	return s
}

// Name returns the service's name.
func (s *Service) Name() string { return s.name }

// Conns returns how many connections and udp sessions the service proxies
// now.
func (s *Service) Conns() int64 { return s.conns.Load() }

// start proxies c, a connection one of the service's listeners has accepted,
// or a udp listener's new session, for Entry.Open; Clients serves its
// callbacks from then on. initial is what the client sent before, and dst
// where its PROXY header said it connected to, if it sent one. release is
// called once, when the connection is over, as it is at once for one past
// max_conns, which is closed.
func (s *Service) start(c *fib.Connection, release func(), initial []byte, dst netip.AddrPort) {
	if s.engine == nil || s.conns.Add(1) > s.maxConns {
		s.conns.Add(-1)
		_ = c.Close()
		release()
		return
	}
	r := &relay{s: s, client: c, release: release, udp: c.IsUDP(), idle: s.idle, dst: dst,
		pick: stdhttp.Request{RemoteAddr: c.RemoteAddr().String()}}
	if len(initial) > 0 {
		r.pending = append([]byte(nil), initial...)
	}
	r.toUp.resume = func(error) { r.toUp.lift() }
	r.toClient.resume = func(error) { r.toClient.lift() }
	r.last.Store(time.Now().UnixNano())
	if r.udp {
		// The session's idle timeout is the relay's, not the engine's.
		r.idle = s.udpIdle
		c.SetUDPIdleTimeout(-1)
	} else {
		// Nothing is read from the client until there is somewhere to send
		// it.
		c.HoldReads(true)
		c.SetWriteBackpressure(false)
	}
	c.SetAttachment(r)
	s.add(r)
	r.timer = time.AfterFunc(r.idle, r.checkIdle)
	r.dial()
}

// Drain closes the connections the service still proxies, once the graph
// they started on has been swapped out for the drain timeout.
func (s *Service) Drain() {
	var open []*relay
	for i := range s.open {
		set := &s.open[i]
		set.mu.Lock()
		for r := range set.relays {
			open = append(open, r)
		}
		set.mu.Unlock()
	}
	for _, r := range open {
		r.abandon()
	}
}

// relaySet is a shard of a service's open connections, for Drain to find.
type relaySet struct {
	mu     sync.Mutex
	relays map[*relay]struct{}
}

func (s *Service) add(r *relay) {
	set := &s.open[rand.Uint32()%shards]
	r.set = set
	set.mu.Lock()
	if set.relays == nil {
		set.relays = map[*relay]struct{}{}
	}
	set.relays[r] = struct{}{}
	set.mu.Unlock()
}

func (s *Service) remove(r *relay) {
	r.set.mu.Lock()
	delete(r.set.relays, r)
	r.set.mu.Unlock()
}

// relay is a client's connection, or udp session, and the upstream's it is
// joined to.
type relay struct {
	s       *Service
	client  *fib.Connection
	release func()
	udp     bool
	idle    time.Duration
	// dst is where the client's PROXY header said it connected to.
	dst netip.AddrPort
	// pick is what the upstream's balancer picks an address by: the
	// client's address, for ip_hash.
	pick    stdhttp.Request
	backend *upstream.Backend
	attempt int
	set     *relaySet

	// up is the upstream's connection, once it is open.
	up atomic.Pointer[fib.Connection]
	// mu orders the dial's outcome with the client's data and close: what
	// the client sent before the upstream opened waits in pending, or for
	// UDP in datagrams, and clientGone says the client closed first.
	mu         sync.Mutex
	pending    []byte
	datagrams  [][]byte
	clientGone bool

	toUp, toClient direction
	// last is when data last went either way, in Unix nanoseconds.
	last   atomic.Int64
	timer  *time.Timer
	ended  atomic.Int32 // sides that are over
	failed bool         // the last dial failed, which counts against the backend
	once   sync.Once
}

// direction is one way through a relay, and whether the side it reads from
// is held for the other's backlog.
type direction struct {
	mu   sync.Mutex
	held atomic.Bool
	from *fib.Connection
	// resume is lift, made once, for NotifyOutput.
	resume func(error)
}

// lift lets the held side read again, once the other's backlog has drained
// or it is closing.
func (d *direction) lift() {
	d.mu.Lock()
	d.held.Store(false)
	d.from.HoldReads(false)
	d.mu.Unlock()
}

// pass sends data, which arrived on from, to to, and holds from's reads
// while to has more than the watermark unsent.
func (r *relay) pass(d *direction, from, to *fib.Connection, data []byte) {
	r.last.Store(time.Now().UnixNano())
	// A datagram that cannot be sent is dropped.
	if to.Send(data) != nil || r.udp || d.held.Load() {
		return
	}
	d.mu.Lock()
	// Held under the lock, so that a lift that comes before HoldReads(true)
	// has returned cannot be undone by it.
	if !d.held.Load() && to.NotifyOutput(Watermark, d.resume) {
		d.held.Store(true)
		d.from = from
		from.HoldReads(true)
	}
	d.mu.Unlock()
}

// dial picks an address and connects to it.
func (r *relay) dial() {
	s := r.s
	r.attempt++
	r.backend = s.pool.Pick(&r.pick)
	network := "tcp"
	if r.udp {
		network = "udp"
	}
	if err := s.engine.DialWithHandler(network, r.backend.Address, s.pool.ConnectTimeout(), (*upSide)(r), r.dialed); err != nil {
		r.dialed(nil, err)
	}
}

// dialed hears how the dial went, after the upstream side's OnOpen when it
// opened. A failed one is tried on the next address, up to maxAttempts, while
// the client is still there.
func (r *relay) dialed(up *fib.Connection, err error) {
	r.mu.Lock()
	gone := r.clientGone
	r.mu.Unlock()
	if err == nil {
		if gone {
			_ = up.Close()
			return
		}
		if !r.udp {
			r.client.HoldReads(false)
		}
		return
	}
	if !gone && r.attempt < r.s.attempts {
		r.s.pool.Done(r.backend, true)
		r.dial()
		return
	}
	// There is no upstream side to wait for.
	r.failed = true
	r.sideEnded()
	_ = r.client.Close()
}

// fromClient passes what the client sent to the upstream, or keeps it until
// the upstream has opened.
func (r *relay) fromClient(data []byte) {
	up := r.up.Load()
	if up == nil {
		r.mu.Lock()
		if up = r.up.Load(); up == nil {
			switch {
			case !r.udp:
				r.pending = append(r.pending, data...)
			case len(r.pending)+len(data) <= Watermark:
				// Each is a slice of what pending has gathered.
				r.pending = append(r.pending, data...)
				r.datagrams = append(r.datagrams, r.pending[len(r.pending)-len(data):])
			}
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
	}
	r.pass(&r.toUp, r.client, up, data)
}

// clientClosed ends the upstream's side once what the client sent has gone.
func (r *relay) clientClosed() {
	r.mu.Lock()
	r.clientGone = true
	up := r.up.Load()
	r.mu.Unlock()
	if up != nil {
		if r.udp {
			_ = up.Close()
		} else {
			up.CloseAfterSend()
		}
	}
	r.sideEnded()
}

// abandon closes both sides at once, for the idle timeout or a drain.
func (r *relay) abandon() {
	_ = r.client.Close()
	if up := r.up.Load(); up != nil {
		_ = up.Close()
	}
}

// checkIdle closes a relay that carried nothing for the idle timeout, and
// looks again when it last carried something otherwise.
func (r *relay) checkIdle() {
	since := time.Since(time.Unix(0, r.last.Load()))
	if since >= r.idle {
		r.abandon()
		return
	}
	r.timer.Reset(r.idle - since)
}

// sideEnded counts a side's end; once both are over, the relay lets go of
// what it holds.
func (r *relay) sideEnded() {
	if r.ended.Add(1) < 2 {
		return
	}
	r.once.Do(func() {
		r.timer.Stop()
		r.s.remove(r)
		r.s.pool.Done(r.backend, r.failed)
		r.s.conns.Add(-1)
		r.release()
	})
}

// upSide is the upstream connection's handler.
type upSide relay

// OnOpen joins the upstream's connection to the client's, and sends it what
// the client sent meanwhile.
func (h *upSide) OnOpen(up *fib.Connection) {
	r := (*relay)(h)
	up.SetWriteBackpressure(false)
	if v := r.s.proxy; v != 0 {
		dst := r.dst
		if !dst.IsValid() {
			if a, ok := r.client.LocalAddr().(*net.TCPAddr); ok {
				dst = a.AddrPort()
			}
		}
		_ = up.Send(appendProxy(nil, v, r.client.RemoteAddrPort(), dst))
	}
	r.mu.Lock()
	switch {
	case r.udp:
		for _, d := range r.datagrams {
			_ = up.Send(d)
		}
		r.datagrams, r.pending = nil, nil
	case len(r.pending) > 0:
		_ = up.Send(r.pending)
		r.pending = nil
	}
	r.up.Store(up)
	r.mu.Unlock()
}

func (h *upSide) OnData(up *fib.Connection, data []byte) {
	r := (*relay)(h)
	r.pass(&r.toClient, up, r.client, data)
}

func (h *upSide) OnPriorityData(*fib.Connection, []byte) {}

// OnClose ends the client's side once what the upstream sent has gone. A
// UDP socket closes when the upstream refuses its datagrams, which counts
// against the backend, and ends the session: the client's next datagram
// starts another.
func (h *upSide) OnClose(_ *fib.Connection, err error) {
	r := (*relay)(h)
	if r.udp {
		if errors.Is(err, syscall.ECONNREFUSED) {
			r.failed = true
		}
		_ = r.client.Close()
	} else {
		r.client.CloseAfterSend()
	}
	r.sideEnded()
}

// Clients serves the callbacks of a tcp or udp listener's connections once
// Entry.Open has taken them; a listener's handler embeds it beside the
// OnOpen that finds the entry.
type Clients struct{}

func (Clients) OnData(c *fib.Connection, data []byte) {
	switch a := c.Attachment().(type) {
	case *relay:
		a.fromClient(data)
	case *opening:
		a.data(c, data)
	}
}

func (Clients) OnPriorityData(*fib.Connection, []byte) {}

func (Clients) OnClose(c *fib.Connection, _ error) {
	switch a := c.Attachment().(type) {
	case *relay:
		a.clientClosed()
	case *opening:
		a.release()
	}
}
