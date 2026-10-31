package stun

import (
	"sync"
	"time"

	"github.com/lesismal/fib"
)

// Defaults of ServerConfig.
const (
	DefaultUDPIdleTimeout = 10 * time.Second
	DefaultTCPIdleTimeout = 30 * time.Second
	DefaultMaxTCPMessage  = 4096
)

// ServerConfig configures a Server.
type ServerConfig struct {
	// Software, when set, is sent as SOFTWARE in every response.
	Software string
	// UDPIdleTimeout is how long a UDP peer is kept after its last request:
	// a client sends a few and is gone, and a peer kept costs a little
	// memory. Zero is DefaultUDPIdleTimeout.
	UDPIdleTimeout time.Duration
	// TCPIdleTimeout is how long a TCP connection may go without a whole
	// message before it is closed. Zero is DefaultTCPIdleTimeout.
	TCPIdleTimeout time.Duration
	// MaxTCPMessage is the largest message taken over TCP; a connection
	// that announces a larger one is closed. Zero is DefaultMaxTCPMessage.
	MaxTCPMessage int
}

// Server is a STUN server (RFC 8489 section 6.3) as a fib handler: it
// answers Binding requests over UDP and TCP with the address they came from,
// in XOR-MAPPED-ADDRESS, and refuses other requests. It keeps no state
// between requests but the bytes of a TCP message still arriving.
type Server struct {
	software string
	udpIdle  time.Duration
	tcpIdle  time.Duration
	maxTCP   int
}

// NewServer returns a server configured by c.
func NewServer(c ServerConfig) *Server {
	s := &Server{software: c.Software, udpIdle: c.UDPIdleTimeout, tcpIdle: c.TCPIdleTimeout, maxTCP: c.MaxTCPMessage}
	if s.udpIdle <= 0 {
		s.udpIdle = DefaultUDPIdleTimeout
	}
	if s.tcpIdle <= 0 {
		s.tcpIdle = DefaultTCPIdleTimeout
	}
	if s.maxTCP <= 0 {
		s.maxTCP = DefaultMaxTCPMessage
	}
	if s.maxTCP < headerSize {
		s.maxTCP = headerSize
	}
	return s
}

// tcpState is what a TCP connection keeps between OnData calls: the start of
// a message whose rest has not arrived yet.
type tcpState struct {
	pending []byte
}

// OnOpen sets a UDP peer's idle timeout, or a TCP connection's first
// deadline.
func (s *Server) OnOpen(c *fib.Connection) {
	if c.IsUDP() {
		c.SetUDPIdleTimeout(s.udpIdle)
		return
	}
	c.SetAttachment(&tcpState{})
	_ = c.SetReadDeadline(time.Now().Add(s.tcpIdle))
}

// OnData answers a UDP peer's datagram, or the whole messages a TCP
// connection's bytes complete.
func (s *Server) OnData(c *fib.Connection, data []byte) {
	if c.IsUDP() {
		// A datagram that is not STUN is dropped, as RFC 8489 section 6.3
		// asks: there is no one to tell.
		s.handle(c, data)
		return
	}
	st, _ := c.Attachment().(*tcpState)
	if st == nil {
		c.Close()
		return
	}
	if len(st.pending) > 0 {
		// Only the bytes the message lacks are copied; the rest of data is
		// read where it lies.
		n := MessageLen(st.pending)
		if n == 0 {
			take := min(headerSize-len(st.pending), len(data))
			st.pending, data = append(st.pending, data[:take]...), data[take:]
			if n = MessageLen(st.pending); n == 0 {
				return
			}
		}
		if n < 0 || n > s.maxTCP {
			c.Close()
			return
		}
		take := min(n-len(st.pending), len(data))
		st.pending, data = append(st.pending, data[:take]...), data[take:]
		if len(st.pending) < n {
			return
		}
		s.handle(c, st.pending)
		st.pending = st.pending[:0]
	}
	for len(data) > 0 {
		n := MessageLen(data)
		if n < 0 || n > s.maxTCP {
			// Not STUN, or more than this server takes: a stream has no
			// way to find the next message, so it ends.
			c.Close()
			return
		}
		if n == 0 || n > len(data) {
			st.pending = append(st.pending, data...)
			return
		}
		s.handle(c, data[:n])
		data = data[n:]
	}
	_ = c.SetReadDeadline(time.Now().Add(s.tcpIdle))
}

// OnPriorityData is OnData.
func (s *Server) OnPriorityData(c *fib.Connection, data []byte) { s.OnData(c, data) }

// OnClose drops a TCP connection's state.
func (s *Server) OnClose(c *fib.Connection, _ error) {
	if !c.IsUDP() {
		c.SetAttachment(nil)
	}
}

// messages are reused for parsing requests and building responses, each a
// pair, so that answering allocates nothing once warm.
var messages = sync.Pool{New: func() any { return new([2]Message) }}

// known reports the attributes this server understands in a request.
func known(t AttrType) bool {
	switch t {
	case AttrMessageIntegrity, AttrMessageIntegritySHA256, AttrUsername, AttrUserhash,
		AttrRealm, AttrNonce, AttrPriority, AttrUseCandidate, AttrPadding:
		return true
	}
	return false
}

// handle answers the message b, if it is a request.
func (s *Server) handle(c *fib.Connection, b []byte) {
	ms := messages.Get().(*[2]Message)
	req, resp := &ms[0], &ms[1]
	defer func() {
		// The request refers to b, which fib reuses once OnData returns.
		req.Raw = nil
		messages.Put(ms)
	}()
	if req.Parse(b) != nil || req.Type.Class != ClassRequest {
		// Indications need no answer, and responses are not this server's.
		return
	}
	fingerprint := req.Has(AttrFingerprint)
	if fingerprint && req.CheckFingerprint() != nil {
		return
	}
	var unknown [8]AttrType
	switch u := req.UnknownRequired(unknown[:0], known); {
	case len(u) > 0:
		resp.Build(Type{Method: req.Type.Method, Class: ClassError}, req.ID)
		resp.AddErrorCode(CodeUnknownAttribute, "Unknown Attribute")
		resp.AddUnknownAttributes(u)
	case req.Type.Method != MethodBinding:
		resp.Build(Type{Method: req.Type.Method, Class: ClassError}, req.ID)
		resp.AddErrorCode(CodeBadRequest, "Bad Request")
	default:
		resp.Build(Type{Method: MethodBinding, Class: ClassSuccess}, req.ID)
		resp.AddXORAddr(AttrXORMappedAddress, c.RemoteAddrPort())
	}
	// The request's integrity, if any, is not checked: a Binding request
	// gets the same answer from everyone, and the answer carries none.
	if s.software != "" {
		resp.AddString(AttrSoftware, s.software)
	}
	if fingerprint {
		resp.AddFingerprint()
	}
	_ = c.Send(resp.Raw)
}
