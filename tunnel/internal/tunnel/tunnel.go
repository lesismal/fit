// Package tunnel is the end of a tunnel that nodes and clients share: a TCP
// connection of their own, joined to the link to the server that carries
// the tunnel's frames.
//
// Each end sends no more than proto.Window bytes the other has not
// acknowledged, and stops reading its TCP connection when it has sent that
// many; the other end acknowledges what it has written to its own TCP
// connection, once that connection's queue is short again. So a slow reader
// at one end holds back the writer at the other, one tunnel at a time,
// rather than every tunnel the link carries. This is the backpressure of
// rule 8, end to end: what waits anywhere for a tunnel is bounded by its
// window, and what waits for a link by the windows of the tunnels it
// carries, which a Table bounds in number.
package tunnel

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"
	"github.com/lesismal/fit/tunnel/internal/link"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
)

// ackLimit is how much of what an end received may wait for its TCP
// connection's socket before the end stops acknowledging it.
const ackLimit = 1 << 20

// Link is what an end sends its frames through: a *link.Link.
type Link interface {
	// SendTunnel notifies m with the frame of tunnel id and payload.
	SendTunnel(m proto.Method, id uint64, payload []byte) error
}

// Endpoint is one end of a tunnel.
type Endpoint struct {
	ID uint64
	// Node and Service are where the tunnel goes, Addr the service's
	// address, and Peer the other party: the client's name at a node, the
	// listener's address at a client. Addr changes only through SetAddr
	// once the end is in a table.
	Node, Service, Addr, Peer string
	Created                   time.Time
	// ConnectRTT, at a node, is how long the connect to the service took,
	// in milliseconds: about a round trip between the node and the
	// service.
	ConnectRTT float64

	table *Table
	link  Link

	mu         sync.Mutex
	conn       *fib.Connection
	window     int64
	ready      bool
	held       bool
	unacked    int64
	ackWaiting bool
	closed     bool

	// in counts the bytes that arrived from the other end, out those sent
	// to it, and active is when either last moved, in Unix nanoseconds.
	in, out atomic.Int64
	active  atomic.Int64
}

// New is an end of tunnel id on link, in table once Add puts it there. An end
// that is not ready reads nothing from its connection until SetReady.
func New(table *Table, link Link, id uint64, ready bool) *Endpoint {
	e := &Endpoint{ID: id, Created: time.Now(), table: table, link: link, window: proto.Window, ready: ready}
	e.active.Store(e.Created.UnixNano())
	return e
}

// attach makes c the end's connection.
func (e *Endpoint) attach(c *fib.Connection) {
	// The end paces what it reads by the other end's acknowledgements, not
	// by its own backlog, which the other end's pace bounds already.
	c.SetWriteBackpressure(false)
	e.mu.Lock()
	e.conn = c
	e.updateHoldLocked()
	e.mu.Unlock()
}

// Link is what the end sends its frames through.
func (e *Endpoint) Link() Link { return e.link }

// SetAddr records the node's address.
func (e *Endpoint) SetAddr(addr string) {
	e.mu.Lock()
	e.Addr = addr
	e.mu.Unlock()
}

// Conn is the end's TCP connection.
func (e *Endpoint) Conn() *fib.Connection {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.conn
}

// SetReady lets the end read its connection.
func (e *Endpoint) SetReady() {
	e.mu.Lock()
	e.ready = true
	e.updateHoldLocked()
	e.mu.Unlock()
}

// updateHoldLocked holds the connection's reads while the end is not ready
// or has used up its window.
func (e *Endpoint) updateHoldLocked() {
	hold := !e.ready || e.window <= 0
	if e.conn != nil && hold != e.held {
		e.held = hold
		e.conn.HoldReads(hold)
	}
}

// local sends what the end's connection read to the other end.
func (e *Endpoint) local(data []byte) {
	total := int64(len(data))
	for len(data) > 0 {
		n := min(len(data), proto.MaxChunk)
		if err := e.link.SendTunnel(proto.MethodTunnelData, e.ID, data[:n]); err != nil {
			e.Close("link: " + err.Error())
			return
		}
		data = data[n:]
	}
	e.out.Add(total)
	e.active.Store(time.Now().UnixNano())
	e.mu.Lock()
	e.window -= total
	e.updateHoldLocked()
	e.mu.Unlock()
}

// remote writes what the other end sent to the end's connection, and
// acknowledges it once enough has arrived and the connection has taken it.
func (e *Endpoint) remote(payload []byte) {
	e.mu.Lock()
	c := e.conn
	e.mu.Unlock()
	if c == nil || c.Send(payload) != nil {
		return
	}
	e.in.Add(int64(len(payload)))
	e.active.Store(time.Now().UnixNano())
	e.mu.Lock()
	e.unacked += int64(len(payload))
	if e.unacked < proto.AckEvery || e.ackWaiting {
		e.mu.Unlock()
		return
	}
	if c.NotifyOutput(ackLimit, e.flushAck) {
		e.ackWaiting = true
		e.mu.Unlock()
		return
	}
	n := e.unacked
	e.unacked = 0
	e.mu.Unlock()
	e.sendAck(n)
}

// flushAck acknowledges what arrived once the connection's queue is short
// again.
func (e *Endpoint) flushAck(err error) {
	e.mu.Lock()
	e.ackWaiting = false
	n := e.unacked
	e.unacked = 0
	e.mu.Unlock()
	if err == nil && n > 0 {
		e.sendAck(n)
	}
}

func (e *Endpoint) sendAck(n int64) {
	_ = e.link.SendTunnel(proto.MethodTunnelAck, e.ID, proto.AckPayload(n))
}

// acked gives back window the other end acknowledged.
func (e *Endpoint) acked(n int64) {
	e.mu.Lock()
	e.window += n
	e.updateHoldLocked()
	e.mu.Unlock()
}

// markClosed records that the tunnel is closing, reporting whether it was
// open.
func (e *Endpoint) markClosed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return false
	}
	e.closed = true
	return true
}

// Close ends the tunnel now: it tells the other end, unless the other end
// closed it, and closes the connection.
func (e *Endpoint) Close(reason string) {
	if e.markClosed() {
		_ = e.link.SendTunnel(proto.MethodTunnelClose, e.ID, []byte(reason))
	}
	e.table.remove(e)
	if c := e.Conn(); c != nil {
		_ = c.Close()
	}
}

// remoteClose ends the tunnel the other end closed, once the connection has
// written what the other end sent before it.
func (e *Endpoint) remoteClose() {
	e.markClosed()
	e.table.remove(e)
	if c := e.Conn(); c != nil {
		c.CloseAfterSend()
	}
}

// localClosed ends the tunnel whose connection closed.
func (e *Endpoint) localClosed(err error) {
	if e.markClosed() {
		reason := "closed"
		if err != nil {
			reason = err.Error()
		}
		_ = e.link.SendTunnel(proto.MethodTunnelClose, e.ID, []byte(reason))
	}
	e.table.remove(e)
}

// Snapshot is an end as a status page shows it.
type Snapshot struct {
	ID      uint64    `json:"id,string"`
	Node    string    `json:"node"`
	Service string    `json:"service"`
	Addr    string    `json:"addr"`
	Peer    string    `json:"peer"`
	Local   string    `json:"local"`
	Remote  string    `json:"remote"`
	Created time.Time `json:"created"`
	In      int64     `json:"in"`
	Out     int64     `json:"out"`
	// ConnectRTT is the end's ConnectRTT.
	ConnectRTT float64 `json:"connect_rtt,omitempty"`
}

// Snapshot is the end's state.
func (e *Endpoint) Snapshot() Snapshot {
	e.mu.Lock()
	addr, c := e.Addr, e.conn
	e.mu.Unlock()
	s := Snapshot{ID: e.ID, Node: e.Node, Service: e.Service, Addr: addr, Peer: e.Peer, Created: e.Created, In: e.in.Load(), Out: e.out.Load(), ConnectRTT: e.ConnectRTT}
	if c != nil {
		if a := c.LocalAddr(); a != nil {
			s.Local = a.String()
		}
		if a := c.RemoteAddr(); a != nil {
			s.Remote = a.String()
		}
	}
	return s
}

// Handler is the fib.Handler of tunnels' TCP connections. Open makes the end
// of a connection as it opens, or returns nil to have it closed. Opened, if
// set, runs once the end has the connection, and the end can receive.
type Handler struct {
	Open   func(*fib.Connection) *Endpoint
	Opened func(*Endpoint)
}

func (h *Handler) OnOpen(c *fib.Connection) {
	e := h.Open(c)
	if e == nil {
		_ = c.Close()
		return
	}
	c.SetAttachment(e)
	_ = c.SetKeepAliveConfig(transport.KeepAlive)
	e.attach(c)
	if h.Opened != nil {
		h.Opened(e)
	}
}

func (h *Handler) OnData(c *fib.Connection, data []byte) {
	if e, ok := c.Attachment().(*Endpoint); ok {
		e.local(data)
	}
}

func (h *Handler) OnPriorityData(*fib.Connection, []byte) {}

func (h *Handler) OnClose(c *fib.Connection, err error) {
	if e, ok := c.Attachment().(*Endpoint); ok {
		e.localClosed(err)
	}
}

// Table is the ends of the tunnels the links to one server carry.
type Table struct {
	// Limit, when set, bounds the tunnels open: those of every table that
	// shares it.
	Limit *Limit

	mu sync.Mutex
	m  map[uint64]*Endpoint
}

// Limit bounds the tunnels open across tables.
type Limit struct {
	n   atomic.Int64
	max atomic.Int64
}

// NewLimit is a Limit of max tunnels.
func NewLimit(max int) *Limit {
	l := &Limit{}
	l.max.Store(int64(max))
	return l
}

// SetMax changes the bound; the tunnels open past it stay open.
func (l *Limit) SetMax(max int) { l.max.Store(int64(max)) }

// Open is how many tunnels are open.
func (l *Limit) Open() int { return int(l.n.Load()) }

func (l *Limit) take() bool {
	if l == nil {
		return true
	}
	if l.n.Add(1) > l.max.Load() {
		l.n.Add(-1)
		return false
	}
	return true
}

func (l *Limit) give() {
	if l != nil {
		l.n.Add(-1)
	}
}

// ErrFull is why a table refused an end: its Limit.
var ErrFull = errors.New("tunnel: too many tunnels open")

// NewTable is an empty Table, bounded by limit, which may be nil.
func NewTable(limit *Limit) *Table { return &Table{Limit: limit, m: map[uint64]*Endpoint{}} }

// Add puts e in the table, reporting why not: its ID is taken, or the
// table's Limit is reached.
func (t *Table) Add(e *Endpoint) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.m[e.ID]; ok {
		return ErrTaken
	}
	if !t.Limit.take() {
		return ErrFull
	}
	t.m[e.ID] = e
	return nil
}

// ErrTaken is why a table refused an end: its ID is in use.
var ErrTaken = errors.New("tunnel: tunnel ID in use")

// Get is the end of tunnel id.
func (t *Table) Get(id uint64) *Endpoint {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[id]
}

func (t *Table) remove(e *Endpoint) {
	t.mu.Lock()
	if t.m[e.ID] == e {
		delete(t.m, e.ID)
		t.Limit.give()
	}
	t.mu.Unlock()
}

// CloseIdle closes the tunnels that have carried nothing for idle.
func (t *Table) CloseIdle(idle time.Duration) {
	before := time.Now().Add(-idle).UnixNano()
	t.mu.Lock()
	var ends []*Endpoint
	for _, e := range t.m {
		if e.active.Load() < before {
			ends = append(ends, e)
		}
	}
	t.mu.Unlock()
	for _, e := range ends {
		e.Close("idle for " + idle.String())
	}
}

// Len is how many tunnels are open.
func (t *Table) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.m)
}

// List is the ends in the order they were made.
func (t *Table) List() []Snapshot {
	t.mu.Lock()
	ends := make([]*Endpoint, 0, len(t.m))
	for _, e := range t.m {
		ends = append(ends, e)
	}
	t.mu.Unlock()
	out := make([]Snapshot, len(ends))
	for i, e := range ends {
		out[i] = e.Snapshot()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CloseAll closes every tunnel.
func (t *Table) CloseAll(reason string) {
	t.mu.Lock()
	ends := make([]*Endpoint, 0, len(t.m))
	for _, e := range t.m {
		ends = append(ends, e)
	}
	t.mu.Unlock()
	for _, e := range ends {
		e.Close(reason)
	}
}

// CloseLink closes every tunnel link carries.
func (t *Table) CloseLink(link Link, reason string) {
	t.mu.Lock()
	var ends []*Endpoint
	for _, e := range t.m {
		if e.link == link {
			ends = append(ends, e)
		}
	}
	t.mu.Unlock()
	for _, e := range ends {
		e.Close(reason)
	}
}

// Load is how many tunnels each link carries; a link that carries none is
// missing.
func (t *Table) Load() map[Link]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[Link]int{}
	for _, e := range t.m {
		out[e.link]++
	}
	return out
}

// Register has r serve the tunnel frames for t's ends. They are handled
// where the link is read, in the order they arrive.
func (t *Table) Register(r *link.Router) {
	r.HandleNotify(proto.MethodTunnelData, func(_ *link.Link, body []byte) {
		if id, payload, err := proto.ParseFrame(body); err == nil {
			if e := t.Get(id); e != nil {
				e.remote(payload)
			}
		}
	}, true)
	r.HandleNotify(proto.MethodTunnelAck, func(_ *link.Link, body []byte) {
		if id, payload, err := proto.ParseFrame(body); err == nil {
			if n, err := proto.ParseAck(payload); err == nil {
				if e := t.Get(id); e != nil {
					e.acked(n)
				}
			}
		}
	}, true)
	r.HandleNotify(proto.MethodTunnelClose, func(_ *link.Link, body []byte) {
		if id, _, err := proto.ParseFrame(body); err == nil {
			if e := t.Get(id); e != nil {
				e.remoteClose()
			}
		}
	}, true)
}
