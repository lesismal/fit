package media

import (
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/webrtc/stun"
)

// portHandler serves the proxy's port: each peer address is a connection
// of fib's, which a relay takes once its first datagram has shown which
// session it is for.
type portHandler struct{ p *Proxy }

func (portHandler) OnOpen(*fib.Connection)                 {}
func (portHandler) OnPriorityData(*fib.Connection, []byte) {}

func (h portHandler) OnData(c *fib.Connection, b []byte) {
	if r, ok := c.Attachment().(*relay); ok {
		r.fromClient(b)
		return
	}
	h.p.open(c, b)
}

func (portHandler) OnClose(c *fib.Connection, _ error) {
	if r, ok := c.Attachment().(*relay); ok {
		r.close()
	}
}

// open takes the first datagram of a peer address: a STUN Binding request
// for a session's ufrag, signed with its ice-pwd, opens a relay toward the
// session's media server; anything else is dropped, and the address
// forgotten, so that its next datagram is looked at afresh.
func (p *Proxy) open(c *fib.Connection, b []byte) {
	ufrag := requestUfrag(b)
	if ufrag == "" {
		p.drop(c, dropNotSTUN)
		return
	}
	p.mu.Lock()
	rt := p.ufrags[ufrag]
	p.mu.Unlock()
	if rt == nil {
		p.drop(c, dropUnknown)
		return
	}
	// The request is checked in a copy: the check rewrites its header for
	// a moment.
	var m stun.Message
	if m.Decode(append([]byte(nil), b...)) != nil || m.CheckIntegrity(rt.pwd) != nil {
		p.drop(c, dropIntegrity)
		return
	}
	st := p.set.Load()
	r := &relay{p: p, s: rt.s, client: c, idle: st.idle, queue: [][]byte{append([]byte(nil), b...)}}
	r.last.Store(time.Now().UnixNano())
	r.timer = time.AfterFunc(r.idle, r.checkIdle)
	p.mu.Lock()
	if r.s.gone || p.relays >= st.maxRelays || len(r.s.relays) >= maxPaths {
		p.mu.Unlock()
		r.timer.Stop()
		p.drop(c, dropLimit)
		return
	}
	r.s.relays[r] = struct{}{}
	p.relays++
	p.mu.Unlock()
	p.m.relays.Add(1)
	p.m.opened.Add(1)
	c.SetUDPIdleTimeout(-1)
	c.SetAttachment(r)
	if err := p.key.DialWithHandler("udp", rt.media.String(), dialTimeout, (*relayUp)(r), r.dialed); err != nil {
		r.close()
	}
}

func (p *Proxy) drop(c *fib.Connection, why int) {
	p.m.dropped[why].Add(1)
	_ = c.Close()
}

// relay is a peer address and the socket fit opened for it toward its
// session's media server.
type relay struct {
	p      *Proxy
	s      *proxySession
	client *fib.Connection
	up     atomic.Pointer[fib.Connection]
	idle   time.Duration
	// last is when the peer last sent, in Unix nanoseconds: what the
	// media server sends does not keep a peer that is gone.
	last  atomic.Int64
	timer *time.Timer

	// mu orders the socket's opening with the peer's datagrams, which
	// queue waits for, and with close.
	mu     sync.Mutex
	queue  [][]byte
	closed bool
	// ended makes close's work happen once: closing a connection may call
	// its OnClose, and close, again before it returns.
	ended atomic.Bool
}

func (r *relay) fromClient(b []byte) {
	r.last.Store(time.Now().UnixNano())
	up := r.up.Load()
	if up == nil {
		r.mu.Lock()
		if up = r.up.Load(); up == nil {
			if !r.closed && len(r.queue) < maxQueued {
				r.queue = append(r.queue, append([]byte(nil), b...))
			}
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
	}
	// A datagram that cannot be sent is dropped, as the network would.
	if up.Send(b) == nil {
		r.p.m.toBackend.Add(uint64(len(b)))
	}
}

// dialed hears how the socket's opening went, after its OnOpen when it
// opened.
func (r *relay) dialed(_ *fib.Connection, err error) {
	if err != nil {
		r.close()
	}
}

// close ends the relay, both of its connections, and its count.
func (r *relay) close() {
	if !r.ended.CompareAndSwap(false, true) {
		return
	}
	r.mu.Lock()
	r.closed = true
	r.queue = nil
	r.mu.Unlock()
	r.timer.Stop()
	_ = r.client.Close()
	if up := r.up.Load(); up != nil {
		_ = up.Close()
	}
	p := r.p
	p.mu.Lock()
	p.relays--
	delete(r.s.relays, r)
	if len(r.s.relays) == 0 && !r.s.gone {
		r.s.timer.Reset(p.set.Load().sessionIdle)
	}
	p.mu.Unlock()
	p.m.relays.Add(-1)
}

// checkIdle closes a relay whose peer sent nothing for its idle timeout,
// and looks again when it last sent otherwise.
func (r *relay) checkIdle() {
	since := time.Since(time.Unix(0, r.last.Load()))
	if since >= r.idle {
		r.close()
		return
	}
	r.timer.Reset(r.idle - since)
}

// relayUp is the handler of a relay's socket toward the media server.
type relayUp relay

func (h *relayUp) OnOpen(up *fib.Connection) {
	r := (*relay)(h)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = up.Close()
		return
	}
	for _, b := range r.queue {
		if up.Send(b) == nil {
			r.p.m.toBackend.Add(uint64(len(b)))
		}
	}
	r.queue = nil
	r.up.Store(up)
	r.mu.Unlock()
}

func (h *relayUp) OnData(_ *fib.Connection, b []byte) {
	r := (*relay)(h)
	if r.client.Send(b) == nil {
		r.p.m.toClient.Add(uint64(len(b)))
	}
}

func (*relayUp) OnPriorityData(*fib.Connection, []byte) {}

// OnClose ends the relay: the media server refused the socket's datagrams,
// as a server gone does, or the relay closed it.
func (h *relayUp) OnClose(*fib.Connection, error) { (*relay)(h).close() }

// requestUfrag returns the ufrag of the agent a STUN Binding request is
// for, the first part of its USERNAME, "<ufrag>:<peer's ufrag>", or "" for
// any other datagram. It reads the attributes in place.
func requestUfrag(b []byte) string {
	const header = 20
	if len(b) < header || binary.BigEndian.Uint32(b[4:]) != stun.MagicCookie ||
		binary.BigEndian.Uint16(b) != 0x0001 {
		return ""
	}
	n := header + int(binary.BigEndian.Uint16(b[2:]))
	if n > len(b) {
		return ""
	}
	for i := header; i+4 <= n; {
		t := binary.BigEndian.Uint16(b[i:])
		l := int(binary.BigEndian.Uint16(b[i+2:]))
		v := i + 4
		if v+l > n {
			return ""
		}
		if t == uint16(stun.AttrUsername) {
			for j, c := range b[v : v+l] {
				if c == ':' {
					return string(b[v : v+j])
				}
			}
			return ""
		}
		i = v + (l+3)&^3
	}
	return ""
}
