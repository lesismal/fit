package l4

import (
	"net/netip"
	"strings"
	"time"

	fib "github.com/lesismal/fib"
)

// OpeningTimeout bounds how long a connection may take to send what a
// listener reads before a service takes it: its PROXY header and its TLS
// ClientHello.
const OpeningTimeout = 10 * time.Second

// Entry is what a tcp or udp listener hands its connections to: its
// services, picked by the server name a TLS client asks for (SNI) when any
// has server_names, and the addresses it takes PROXY headers from.
type Entry struct {
	fallback *Service
	exact    map[string]*Service
	// wild holds the services of names "*.d", by d.
	wild    map[string]*Service
	trusted Trusted
}

// NewEntry returns an entry that takes PROXY headers from trusted, or from
// no one when it is empty.
func NewEntry(trusted Trusted) *Entry { return &Entry{trusted: trusted} }

// Add has s take the connections that ask for one of names, or those no
// other service's names take when names is empty.
func (e *Entry) Add(s *Service, names []string) {
	if len(names) == 0 {
		e.fallback = s
		return
	}
	for _, name := range names {
		name = strings.ToLower(name)
		if d, ok := strings.CutPrefix(name, "*."); ok {
			if e.wild == nil {
				e.wild = map[string]*Service{}
			}
			e.wild[d] = s
			continue
		}
		if e.exact == nil {
			e.exact = map[string]*Service{}
		}
		e.exact[name] = s
	}
}

// routes reports whether the entry picks services by SNI.
func (e *Entry) routes() bool { return e.exact != nil || e.wild != nil }

// named returns the service whose server_names hold name, or nil.
func (e *Entry) named(name string) *Service {
	if s := e.exact[name]; s != nil {
		return s
	}
	if _, d, ok := strings.Cut(name, "."); ok {
		if s := e.wild[d]; s != nil {
			return s
		}
	}
	return nil
}

// Open hands c, a connection one of the entry's listeners has just
// accepted, or a udp listener's new session, to its service, from the
// listener's OnOpen; Clients serves its callbacks from then on. First, when
// it comes from a trusted address, its PROXY header is read, and when the
// entry routes by SNI, its ClientHello. release is called once, when the
// connection is over.
//
// term, when not nil, is the TLS handler of a listener that tells its
// protocols apart and has tls, and c is a TLS connection: one whose server
// name no service's server_names hold is handed to term, which terminates
// it, rather than to the service without server_names.
func (e *Entry) Open(c *fib.Connection, release func(), term fib.Handler) {
	proxy := !c.IsUDP() && len(e.trusted) > 0 && e.trusted.Contains(c.RemoteAddrPort().Addr())
	if term != nil && !proxy && !e.routes() {
		release()
		HandOff(c, term)
		return
	}
	if !proxy && (!e.routes() || c.IsUDP()) {
		if e.fallback == nil {
			_ = c.Close()
			release()
			return
		}
		e.fallback.start(c, release, nil, netip.AddrPort{})
		return
	}
	c.SetAttachment(&opening{e: e, release: release, proxy: proxy, term: term})
	_ = c.SetReadDeadline(time.Now().Add(OpeningTimeout))
}

// opening is a connection whose PROXY header or ClientHello is still being
// read.
type opening struct {
	e       *Entry
	release func()
	proxy   bool
	term    fib.Handler
	buf     []byte
	// dst is where the PROXY header says the client connected to.
	dst netip.AddrPort
}

// data takes what the client sent, until the service is known.
func (o *opening) data(c *fib.Connection, data []byte) {
	o.buf = append(o.buf, data...)
	if o.proxy {
		n, src, dst, err := parseProxy(o.buf)
		switch {
		case err != nil || n == 0 && len(o.buf) >= MaxProxyHeader:
			_ = c.Close()
			return
		case n == 0:
			return
		}
		if src.IsValid() {
			c.SetRemoteAddrPort(src)
		}
		o.dst, o.proxy = dst, false
		o.buf = o.buf[n:]
	}
	name := ""
	if o.e.routes() {
		if len(o.buf) == 0 {
			return
		}
		var done bool
		var err error
		name, done, err = readSNI(o.buf)
		switch {
		case err != nil:
			// Not TLS, or not a hello fit can read: the fallback's.
			name = ""
		case !done && len(o.buf) < MaxClientHello:
			return
		}
	}
	s := o.e.named(name)
	if s == nil && o.term != nil {
		_ = c.SetReadDeadline(time.Time{})
		o.release()
		c.SetAttachment(nil)
		handOff(c, o.term, o.buf)
		return
	}
	if s == nil {
		s = o.e.fallback
	}
	if s == nil {
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	s.start(c, o.release, o.buf, o.dst)
}

// HandOff has h serve c, from the OnOpen of the handler that serves it now,
// whose OnData must be Clients': h takes over with the data that comes
// next, as if it had accepted c itself.
func HandOff(c *fib.Connection, h fib.Handler) { c.SetAttachment(handoff{h}) }

// handoff is a connection HandOff gives to h at its first data.
type handoff struct{ h fib.Handler }

// handOff has h serve c, which has sent data so far.
func handOff(c *fib.Connection, h fib.Handler, data []byte) {
	c.SetHandler(h)
	h.OnOpen(c)
	if len(data) > 0 {
		h.OnData(c, data)
	}
}

// Conns returns how many connections and udp sessions the entry's services
// proxy now, theirs from other listeners too.
func (e *Entry) Conns() int64 {
	seen := map[*Service]bool{}
	var n int64
	add := func(s *Service) {
		if s != nil && !seen[s] {
			seen[s] = true
			n += s.Conns()
		}
	}
	add(e.fallback)
	for _, s := range e.exact {
		add(s)
	}
	for _, s := range e.wild {
		add(s)
	}
	return n
}
